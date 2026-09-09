// Package telemetry owns the OpenTelemetry SDK and is the only package in the
// service that links it. It starts no span of its own: Provider hands out a
// tracer, traces.go hands back projected attributes, and the Trace middleware is
// the one caller that puts the two together.
//
// One file per signal, each owning that signal whole:
//
//	provider.go   the SDK's lifecycle — Init, Provider, Tracer, MeterProvider,
//	              Shutdown, the two OTLP exporters and the W3C propagator
//	traces.go     everything about spans — Identity and the Resource they all
//	              carry, one request's facts projected onto span attributes and
//	              span events, and the spanUUID processor
//	metrics.go    everything about metrics — the pool's counters and their labels
//	fact/         the attribute table every projection above reads
//
// testsupport.go is the one file that map leaves out: it is test scaffolding
// deliberately in a non-test file, and says at its top why.
//
// fact/ is a separate package because it links no SDK, so a controller can name
// an attribute key without pulling an exporter into its build graph (A23,
// opentelemetry.md §The import boundary); tests/architecture/boundary_test.go enforces that
// rather than convention. There is no logs.go for the same reason: the log
// projection is src/platform/logger/fields.go and moving it here would need an
// exemption for package logger — see that file's doc comment before trying.
//
// One test file per source file, with one forced exception:
// provider_internal_test.go is `package telemetry` and provider_test.go is
// `package telemetry_test`, so they cannot merge.
package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/OpenAgriNet/discovery-service/src/platform/buildinfo"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

// The instrumentation scope, stamped on every exported batch (`scope` is
// Optional in the spec; `name` and `version` are Required inside it).
const (
	// ScopeName is the spec's own example value, underscored. Deliberately NOT
	// service.name's spelling: that names the software, this names the
	// instrumentation a facilitator may key on (opentelemetry.md §Scope).
	ScopeName = "discovery_service"

	// ScopeVersion is the network-telemetry-spec version. `1.0` is
	// example-derived rather than released — the spec repo carries no version
	// tags — so do not bump it to match a release that does not exist
	// (decision 1, opentelemetry.md §The spec is prose only).
	ScopeVersion = "1.0"
)

// Provider is the tracer provider and its shutdown, held by the composition
// root.
//
// A wrapper rather than the SDK type itself: handing out *sdktrace.TracerProvider
// would put RegisterSpanProcessor one dot away from every caller, and a span
// processor registered from a request path is a leak the type system would not
// mention.
//
// Every method is NIL-TOLERANT and hands back a noop, because three populations
// have no Provider — router_test.go's hand-built App, the acceptance suite's
// controllers, and app.Close on a path where Init never ran. A noop tracer's
// spans are not recording, so those callers run unchanged and pay nothing.
type Provider struct {
	provider *sdktrace.TracerProvider
	tracer   trace.Tracer

	// A second SDK provider rather than a second signal on the first:
	// OpenTelemetry has no combined type, and the two export differently —
	// spans batch, metrics are collected on a period.
	meters *sdkmetric.MeterProvider

	// Ours, not the SDK's: sdktrace.TracerProvider.Shutdown is idempotent and
	// sdkmetric.MeterProvider.Shutdown is not.
	metersOnce sync.Once
}

// Init builds the Resource, the exporter and the tracer provider.
//
// It registers nothing GLOBALLY — no otel.SetTracerProvider — because a global
// would let any package reach the SDK through otel.Tracer(), which is the
// coupling A23 and the import guard exist to prevent. The provider travels as a
// value to the one middleware that needs it.
//
// There is no error path for bad configuration, only for a Resource or an
// exporter that will not build: config.validateOTel has already refused the
// incoherent combinations at boot.
func Init(ctx context.Context, cfg config.Config) (*Provider, error) {
	res, err := projectResource(ctx, NewIdentity(cfg), buildinfo.Read())
	if err != nil {
		return nil, fmt.Errorf("build the telemetry resource: %w", err)
	}

	options := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),

		// Registering it starts no spans, so 23a's "boots only" acceptance holds.
		sdktrace.WithSpanProcessor(spanUUID{}),
	}

	options, err = withExport(ctx, cfg, options)
	if err != nil {
		return nil, err
	}

	provider := sdktrace.NewTracerProvider(options...)

	meters, err := newMeterProvider(ctx, cfg, res)
	if err != nil {
		return nil, err
	}

	return &Provider{
		provider: provider,
		tracer:   provider.Tracer(ScopeName, trace.WithInstrumentationVersion(ScopeVersion)),
		meters:   meters,
	}, nil
}

// newMeterProvider builds the metrics half, over the trace Resource with its
// `eid` overridden to METRIC.
//
// It takes the API Resource and derives, rather than being handed a finished
// one, so that Init has no way to wire the wrong Resource in: there is no
// argument here that could carry eid=API. That matters because the mistake it
// replaces was exactly that — one Resource, both providers, and every metric
// this service exported labelled itself an API event until 2026-09-10.
//
// `producer` and `domain` are spec-Required on both signals and must agree.
// That used to hold because the two shared one object; it now holds because
// withEID copies everything it does not override, and
// TestTheMetricResourceSaysMETRICAndAgreesOnEverythingElse fails if that stops
// being true.
//
// Under any exporter but otlp it gets no reader, which collects nothing and
// invokes no callback — the metrics equivalent of NeverSample, spelled as an
// absent reader because there is no metrics sampler.
func newMeterProvider(ctx context.Context, cfg config.Config, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	metricRes, err := withEID(res, eidMetric)
	if err != nil {
		return nil, err
	}

	options := []sdkmetric.Option{sdkmetric.WithResource(metricRes)}

	if cfg.OTel.Exporter != config.ExporterOTLP {
		return sdkmetric.NewMeterProvider(options...), nil
	}

	exporter, err := newMetricExporter(ctx, cfg.OTel.Endpoint)
	if err != nil {
		return nil, err
	}
	return sdkmetric.NewMeterProvider(append(options, sdkmetric.WithReader(
		sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithTimeout(metricExportTimeout)),
	))...), nil
}

// MeterProvider hands out the metrics provider for the composition root to
// register instruments against.
func (p *Provider) MeterProvider() metric.MeterProvider {
	if p == nil || p.meters == nil {
		return metricnoop.NewMeterProvider()
	}
	return p.meters
}

// withExport appends the option that decides where spans go.
//
// Under otlp it passes NO sampler, which is the decision rather than an
// omission: the SDK's default is ParentBased(AlwaysSample), which keeps
// everything and still defers to a caller who already decided, where an
// explicit AlwaysSample would discard that decision and leave holes that read
// as dropped hops (opentelemetry.md §Decisions needed before 23a). A line that is not here cannot
// be reviewed, so TestTheSamplerRespectsAnInboundDecision fails the moment
// anyone adds it back.
func withExport(ctx context.Context, cfg config.Config, options []sdktrace.TracerProviderOption) ([]sdktrace.TracerProviderOption, error) {
	if cfg.OTel.Exporter != config.ExporterOTLP {
		// Otherwise identical under `none`, so a collector-less boot exercises
		// the same construction as a real one — and costs a comparison per
		// request, because a non-recording span allocates nothing and runs no
		// processor.
		return append(options, sdktrace.WithSampler(sdktrace.NeverSample())), nil
	}

	exporter, err := newExporter(ctx, cfg.OTel.Endpoint)
	if err != nil {
		return nil, err
	}
	return append(options, sdktrace.WithBatcher(exporter)), nil
}

// Tracer is the one tracer this service has.
//
// Obtained ONCE, at Init, because the instrumentation scope is fixed when the
// tracer is obtained and cannot be set at span creation — the finding that ruled
// out otelhttp (A23) and equally rules out a caller doing provider.Tracer("")
// for itself.
func (p *Provider) Tracer() trace.Tracer {
	if p == nil || p.tracer == nil {
		return noop.NewTracerProvider().Tracer(ScopeName)
	}
	return p.tracer
}

// Shutdown flushes what the batcher is holding and closes the connection. Both
// providers go down on one call.
//
// Idempotent by requirement, not by nicety: main defers this and app.Close calls
// it. The tracer provider's own Shutdown already is; the meter provider's is not
// — a second call returns "reader is shutdown" — hence the sync.Once.
//
// The meter provider's error is deliberately NOT returned; see the nolint below.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}

	if p.meters != nil {
		//nolint:errcheck,gosec // A PeriodicReader flushes on shutdown, so with
		// no collector listening it fails — which must not read as a failed
		// shutdown, and which the tracer's batcher already drops internally.
		// Returning it for metrics alone would make the two signals disagree
		// about one event. TestOtlpBootsWithoutACollectorListening pins it.
		p.metersOnce.Do(func() { p.meters.Shutdown(ctx) })
	}

	if p.provider == nil {
		return nil
	}
	if err := p.provider.Shutdown(ctx); err != nil {
		return fmt.Errorf("shut down the tracer provider: %w", err)
	}
	return nil
}

// --- Where the signals go -------------------------------------------------

// metricExportTimeout bounds one metric export, and through that also bounds how
// long a shutdown waits on a collector that is not there. Well under the SDK's
// 30s default because of that second role: at the default, a pod whose collector
// is down takes half a minute to exit and is SIGKILLed by a
// terminationGracePeriodSeconds nobody connected to a telemetry setting.
const metricExportTimeout = 5 * time.Second

// newExporter builds the OTLP/gRPC span exporter (decision 2,
// opentelemetry.md §Build order).
//
// The endpoint is passed explicitly rather than left to the SDK's env lookup:
// config layers YAML underneath the environment, so an endpoint set in
// instance.yaml is a value the SDK would never see.
//
// Neither exporter here DIALS. A pod that refused to start because the collector
// was not up would make telemetry a hard dependency of serving traffic.
func newExporter(ctx context.Context, endpoint string) (*otlptrace.Exporter, error) {
	var options []otlptracegrpc.Option
	if hasScheme(endpoint) {
		// A full URL carries its own transport security: http:// is insecure,
		// https:// is not.
		options = append(options, otlptracegrpc.WithEndpointURL(endpoint))
	} else {
		options = append(options, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	}

	exporter, err := otlptracegrpc.New(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("build the otlp exporter for %q: %w", endpoint, err)
	}
	return exporter, nil
}

// newMetricExporter is newExporter's metrics twin: same two endpoint spellings,
// same reasons, and it does not dial either.
func newMetricExporter(ctx context.Context, endpoint string) (sdkmetric.Exporter, error) {
	var options []otlpmetricgrpc.Option
	if hasScheme(endpoint) {
		options = append(options, otlpmetricgrpc.WithEndpointURL(endpoint))
	} else {
		options = append(options, otlpmetricgrpc.WithEndpoint(endpoint), otlpmetricgrpc.WithInsecure())
	}

	exporter, err := otlpmetricgrpc.New(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("build the otlp metric exporter for %q: %w", endpoint, err)
	}
	return exporter, nil
}

// hasScheme distinguishes the two spellings an operator will write, which is
// why both exporters above branch.
//
// NOT url.Parse: it reads "localhost:4317" as scheme "localhost" with an empty
// host, so a bare host:port reaching WithEndpointURL yields an exporter pointed
// at nothing — one that fails by never delivering rather than by refusing.
func hasScheme(endpoint string) bool {
	return strings.Contains(endpoint, "://")
}

// --- How a trace crosses a network hop ------------------------------------

// propagator is the one W3C Trace Context propagator this service has. A package
// value rather than otel.SetTextMapPropagator, for the reason Init registers no
// global tracer provider.
//
// TraceContext alone, NO Baggage: baggage carries key-value pairs of the
// caller's choosing across every hop, which on a network of participants who do
// not share a trust boundary is an unbounded field an unauthenticated caller
// fills. The correlators this service needs already travel in the envelope.
var propagator propagation.TextMapPropagator = propagation.TraceContext{}

// Extract joins the caller's trace, returning a context whose span context is
// the inbound traceparent's — so the span Trace starts next lands INSIDE the
// caller's trace rather than starting a second one.
//
// A request with no traceparent, or a malformed one, comes back unchanged and
// the span becomes a root. Refusing it instead would make this service's
// availability depend on its callers' instrumentation.
func Extract(ctx context.Context, header http.Header) context.Context {
	return propagator.Extract(ctx, propagation.HeaderCarrier(header))
}

// Inject writes the current span's context onto an outbound request's headers,
// so the far side can join this trace. A no-op when no span is in flight, which
// is what makes it safe to call unconditionally at every outbound edge.
func Inject(ctx context.Context, header http.Header) {
	propagator.Inject(ctx, propagation.HeaderCarrier(header))
}
