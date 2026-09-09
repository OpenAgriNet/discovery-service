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

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

// The instrumentation scope, stamped on every exported batch. `scope` is
// Optional in the spec and `name` and `version` are Required inside it; we emit
// it.
const (
	// ScopeName is the spec's own example value, underscored. Deliberately not
	// the same spelling as service.name: that names the software for
	// ClickStack's grouping, this names the instrumentation a facilitator may
	// key on, and a second implementation reading the spec's example gets this
	// one.
	ScopeName = "discovery_service"

	// ScopeVersion is the network-telemetry-spec version — except the spec repo
	// carries no version tags, so `1.0` is example-derived rather than
	// released (decision 1). Recorded as borrowed so nobody bumps it to match a
	// release that does not exist.
	ScopeVersion = "1.0"
)

// Provider is the tracer provider and its shutdown, held by the composition
// root.
//
// A wrapper rather than the SDK type itself, for the same reason App keeps its
// pool unexported: handing out *sdktrace.TracerProvider puts RegisterSpanProcessor
// one dot away from every caller, and a span processor registered from a
// request path is a leak the type system would not mention.
//
// Every method below is nil-tolerant, for one population rather than three:
// router_test.go builds an App by hand, the acceptance suite calls controllers
// with no Provider at all, and app.Close runs on paths where Init never ran.
// Each hands back a noop rather than nil, so the alternative — a nil check at
// every call site, or a nil panic during boot — never arises. A noop tracer's
// spans are not recording and a noop meter provider's instruments observe
// nothing, so callers run unchanged and pay nothing.
type Provider struct {
	provider *sdktrace.TracerProvider
	tracer   trace.Tracer

	// meters is Task 25's half, and it is a second SDK provider rather than a
	// second signal on the first: OpenTelemetry has no combined type, and the
	// two have genuinely different exports — spans batch, metrics are collected
	// on a period.
	meters *sdkmetric.MeterProvider

	// metersOnce is ours, not the SDK's. sdktrace.TracerProvider.Shutdown is
	// idempotent; sdkmetric.MeterProvider.Shutdown is not.
	metersOnce sync.Once
}

// Init builds the Resource, the exporter and the tracer provider.
//
// It starts no spans and registers nothing globally. No otel.SetTracerProvider:
// a global would let any package in the service reach the SDK through
// otel.Tracer(), which is precisely the coupling A23 and the import guard exist
// to prevent — the provider travels as a value to the one middleware that needs
// it, which reaches it through App.Telemetry.Tracer().
//
// The incoherent configurations are already gone: validateOTel refuses at boot
// an exporter this build does not have, an otlp with no endpoint, and an otlp
// with no producer or an undeclared domain. So this function has no error path
// for bad configuration, only for a Resource or an exporter that will not build.
func Init(ctx context.Context, cfg config.Config) (*Provider, error) {
	res, err := projectResource(ctx, NewIdentity(cfg), readBuild())
	if err != nil {
		return nil, fmt.Errorf("build the telemetry resource: %w", err)
	}

	options := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),

		// telemetry-seam.md:770. Registering it starts no spans, which is why
		// it lands in 23a rather than waiting for the middleware that will
		// create the spans it stamps.
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

// newMeterProvider builds the metrics half, over the same Resource.
//
// The same Resource object, not an equal one: `producer` and `domain` are
// spec-Required on all three signals, and constructing it once is what makes
// "the same Resource everywhere" true by construction rather than by Task 24
// remembering to match it. `eid` is the single attribute that differs per
// signal, and it is a registry row with a per-signal projection rule rather
// than a literal here.
//
// Under any exporter but otlp it gets no reader at all. A MeterProvider with no
// reader collects nothing and never invokes a callback, so a collector-less boot
// pays nothing for the registration — the metrics equivalent of NeverSample,
// and expressed as an absent reader because there is no metrics sampler.
func newMeterProvider(ctx context.Context, cfg config.Config, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	options := []sdkmetric.Option{sdkmetric.WithResource(res)}

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
// register instruments against. Nil-tolerant, per the type's comment, so
// RegisterPoolStats needs no nil check of its own.
func (p *Provider) MeterProvider() metric.MeterProvider {
	if p == nil || p.meters == nil {
		return metricnoop.NewMeterProvider()
	}
	return p.meters
}

// withExport appends the option that decides where spans go, and it is one
// function because the two branches are one decision.
//
// Unsampled, deliberately, and by NOT passing WithSampler rather than by passing
// AlwaysSample. This service answers a query rate a human network produces, not
// a machine one, and the questions the verdict table asks — did anyone ask and
// nobody serve it, which participants talked to which — are counting questions
// that a sample answers wrongly rather than approximately. So the answer is
// "keep everything", and there are two ways to say it that are not the same
// thing.
//
// The SDK's default with no option is ParentBased(AlwaysSample): it keeps every
// trace this service starts, and it DEFERS to a caller who already decided.
// AlwaysSample discards that decision, so with four layers each sampling for
// itself a trace arrives with holes in it — and a hole reads as a dropped hop,
// which is the one diagnosis this telemetry exists to make. It would also
// silently override OTEL_TRACES_SAMPLER, which an operator can see in their
// manifest and reasonably believe is doing something.
//
// A line that is not here cannot be reviewed, so the behaviour is pinned
// instead: TestTheSamplerRespectsAnInboundDecision fails the moment anyone adds
// the option back (opentelemetry.md open question 6).
func withExport(ctx context.Context, cfg config.Config, options []sdktrace.TracerProviderOption) ([]sdktrace.TracerProviderOption, error) {
	if cfg.OTel.Exporter != config.ExporterOTLP {
		// Under `none` the provider is otherwise identical, so a collector-less
		// boot exercises the same construction as a real one. What makes it free
		// is this: a non-recording span allocates nothing, holds no attributes
		// and runs no processor, so the Trace middleware costs a comparison per
		// request on a deployment nobody is watching.
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
// Obtained once here rather than per request, because the instrumentation scope
// is fixed when the tracer is obtained and cannot be set at span creation —
// which is the finding that ruled out otelhttp (A23) and would equally rule out
// any caller doing provider.Tracer("") for itself.
//
// Nil-tolerant, per the type's comment: chain() reads this while assembling the
// router, and a router can be assembled without a Provider.
func (p *Provider) Tracer() trace.Tracer {
	if p == nil || p.tracer == nil {
		return noop.NewTracerProvider().Tracer(ScopeName)
	}
	return p.tracer
}

// Shutdown flushes what the batcher is holding and closes the connection.
//
// Both providers go down on one call, and idempotency is a requirement rather
// than a nicety because main defers this and app.Close calls it. The tracer
// provider's own Shutdown is already idempotent; the meter provider's is not — a
// second call returns "reader is shutdown" — so that one is guarded by a
// sync.Once. TestShutdownIsSafeTwiceAndOnNothing is what found it.
//
// The meter provider's error is deliberately NOT returned, and this is the one
// asymmetry between the two signals here. A PeriodicReader flushes on shutdown,
// so with no collector listening it fails — the condition newExporter below says
// must never be a service failure. The tracer's batcher already drops the same
// failure internally, so returning it for metrics only would make the two
// signals disagree about one event, and would make every clean shutdown on a
// cluster with a down collector log an error that names nothing the operator can
// act on from this side. TestOtlpBootsWithoutACollectorListening pins it.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}

	if p.meters != nil {
		//nolint:errcheck,gosec // deliberate, and the doc comment above is the
		// reason: a flush into a collector that is not there must not read as a
		// failed shutdown. TestOtlpBootsWithoutACollectorListening pins it.
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

// metricExportTimeout bounds one metric export, and therefore also bounds how
// long a shutdown waits on a collector that is not there.
//
// Well under the SDK's 30s default because of that second role: left at the
// default, a pod whose collector is down takes half a minute to exit and gets
// SIGKILLed by a terminationGracePeriodSeconds nobody connected to a telemetry
// setting.
const metricExportTimeout = 5 * time.Second

// newExporter builds the OTLP/gRPC span exporter (decision 2: gRPC is the OTel
// default for OTEL_EXPORTER_OTLP_ENDPOINT, which config reads under that exact
// name, and it is what ClickStack's collector accepts).
//
// The endpoint is passed explicitly rather than left to the SDK's own env
// lookup: config layers YAML underneath the environment, so an endpoint set in
// instance.yaml is a value the SDK would never see.
//
// Neither exporter here dials. A pod that refused to start because the collector
// was not up yet would make telemetry a hard dependency of serving traffic, and
// the service answers queries whether or not anyone is watching it. That one
// choice is also why Shutdown discards the meter provider's error.
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

// hasScheme distinguishes the two spellings an operator will write, and it is
// the reason both exporters above branch at all.
//
// Not url.Parse: every host:port parses successfully as a URL, which is the
// trap. It reads "localhost:4317" as scheme "localhost" with an empty host, so a
// bare host:port reaching WithEndpointURL yields an exporter pointed at nothing
// — one that fails by never delivering rather than by refusing.
func hasScheme(endpoint string) bool {
	return strings.Contains(endpoint, "://")
}

// --- How a trace crosses a network hop ------------------------------------

// propagator is the one W3C Trace Context propagator this service has.
//
// A package value rather than otel.SetTextMapPropagator, for the same reason
// Init registers no global tracer provider: a global is set by whoever imports
// the package and is visible to every test in the binary, so a test that needs a
// different one either cannot have it or takes it from everybody else. A value
// passed explicitly has neither problem, and there is exactly one caller shape —
// Extract at the inbound edge, Inject at each outbound one.
//
// TraceContext alone, no Baggage. Baggage travels key-value pairs of the
// caller's choosing across every hop, which on a network of participants who do
// not share a trust boundary is an unbounded field an unauthenticated caller
// fills. The correlators this service needs — transaction and message id —
// already travel in the Beckn envelope, which is signed.
var propagator propagation.TextMapPropagator = propagation.TraceContext{}

// Extract joins the caller's trace, returning a context whose span context is
// the inbound traceparent's.
//
// Joined and not replaced: the returned context carries the caller's trace id
// and their span as the parent, so the span Trace starts next lands INSIDE the
// caller's trace rather than starting a second one. A network hop that starts
// its own trace is why "the seeker sent it and nobody served it" is currently
// unanswerable — the two halves are in different traces and nothing joins them.
//
// A request with no traceparent, or with a malformed one, comes back unchanged
// and the span becomes a root. That is the right failure: refusing the request
// would make this service's availability depend on its callers' instrumentation.
func Extract(ctx context.Context, header http.Header) context.Context {
	return propagator.Extract(ctx, propagation.HeaderCarrier(header))
}

// Inject writes the current span's context onto an outbound request's headers,
// so the far side can join this trace the way Extract joined the caller's.
//
// It is a no-op when no span is in flight, which is what makes it safe to call
// unconditionally at every outbound edge rather than guarding each one.
func Inject(ctx context.Context, header http.Header) {
	propagator.Inject(ctx, propagation.HeaderCarrier(header))
}
