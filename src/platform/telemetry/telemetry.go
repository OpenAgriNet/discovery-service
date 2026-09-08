// Package telemetry owns the OpenTelemetry SDK, and it is the only package in
// the service that links it.
//
// Everything below it records facts against src/platform/telemetry/fact, which
// imports nothing but the standard library, and this package projects those
// facts onto spans (A23). The split is enforced by tests/architecture rather
// than by convention: a controller that links an exporter is a controller whose
// build breaks on an SDK release, and whose test binary starts a batch
// processor nobody asked for.
//
// 23a builds the package, the Resource and the exporter. It starts no spans —
// the Trace middleware that will is 23c.
package telemetry

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

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

// Identity is who this deployment says it is, read off config once at boot.
//
// A struct rather than three parameters because the Resource is the one place
// all three appear and they are all strings: projectResource(id, build) cannot
// be called with domain and network the wrong way round, and
// projectResource(a, b, c) can.
type Identity struct {
	// Producer is the registered subscriber id, an FQDN. Never service.name.
	Producer string

	// Domain is the sector. Already checked against the registry's declared
	// values by validateOTel, which is why nothing re-checks it here.
	Domain string

	// NetworkID is the network this deployment serves — our key, not the spec's.
	NetworkID string
}

// NewIdentity reads the three off config. Exported so the composition root can
// build one without this package importing anything of app's.
func NewIdentity(cfg config.Config) Identity {
	return Identity{
		Producer:  cfg.App.Subscriber,
		Domain:    cfg.App.Domain,
		NetworkID: cfg.App.Network,
	}
}

// Provider is the tracer provider and its shutdown, held by the composition
// root.
//
// A wrapper rather than the SDK type itself, for the same reason App keeps its
// pool unexported: handing out *sdktrace.TracerProvider puts RegisterSpanProcessor
// one dot away from every caller, and a span processor registered from a
// request path is a leak the type system would not mention.
type Provider struct {
	provider *sdktrace.TracerProvider
	tracer   trace.Tracer
}

// Init builds the Resource, the exporter and the tracer provider.
//
// It starts no spans and registers nothing globally. No otel.SetTracerProvider:
// a global would let any package in the service reach the SDK through
// otel.Tracer(), which is precisely the coupling A23 and the import guard exist
// to prevent — the provider travels as a value to the one middleware that needs
// it (23c).
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

	if cfg.OTel.Exporter == config.ExporterOTLP {
		exporter, err := newExporter(ctx, cfg.OTel.Endpoint)
		if err != nil {
			return nil, err
		}
		options = append(options,
			sdktrace.WithBatcher(exporter),

			// Unsampled, deliberately. This service answers a query rate a
			// human network produces, not a machine one, and the questions the
			// verdict table asks — did anyone ask and nobody serve it, which
			// participants talked to which — are counting questions that a
			// sample answers wrongly rather than approximately.
			sdktrace.WithSampler(sdktrace.AlwaysSample()),
		)
	} else {
		// Under `none` the provider is otherwise identical, so a collector-less
		// boot exercises the same construction as a real one. What makes it
		// free is this: a non-recording span allocates nothing, holds no
		// attributes and runs no processor, so 23c's middleware costs a
		// comparison per request on a deployment nobody is watching.
		options = append(options, sdktrace.WithSampler(sdktrace.NeverSample()))
	}

	provider := sdktrace.NewTracerProvider(options...)
	return &Provider{
		provider: provider,
		tracer:   provider.Tracer(ScopeName, trace.WithInstrumentationVersion(ScopeVersion)),
	}, nil
}

// Tracer is the one tracer this service has.
//
// Obtained once here rather than per request, because the instrumentation scope
// is fixed when the tracer is obtained and cannot be set at span creation —
// which is the finding that ruled out otelhttp (A23) and would equally rule out
// any caller doing provider.Tracer("") for itself.
func (p *Provider) Tracer() trace.Tracer {
	return p.tracer
}

// Shutdown flushes what the batcher is holding and closes the connection.
//
// Nil-tolerant and idempotent, because app.Close runs on paths where Init never
// ran and on paths where this already has. The SDK's own Shutdown is idempotent
// after the first call; the nil check is ours.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.provider == nil {
		return nil
	}
	if err := p.provider.Shutdown(ctx); err != nil {
		return fmt.Errorf("shut down the tracer provider: %w", err)
	}
	return nil
}

// newExporter builds the OTLP/gRPC exporter (decision 2: gRPC is the OTel
// default for OTEL_EXPORTER_OTLP_ENDPOINT, which config reads under that exact
// name, and it is what ClickStack's collector accepts).
//
// The endpoint is passed explicitly rather than left to the SDK's own env
// lookup: config layers YAML underneath the environment, so an endpoint set in
// instance.yaml is a value the SDK would never see.
//
// It does not dial. A pod that refused to start because the collector was not
// up yet would make telemetry a hard dependency of serving traffic, and the
// service answers queries whether or not anyone is watching it.
func newExporter(ctx context.Context, endpoint string) (*otlptrace.Exporter, error) {
	var options []otlptracegrpc.Option
	if hasScheme(endpoint) {
		// Carries its own transport security: http:// is insecure, https:// is
		// not.
		options = append(options, otlptracegrpc.WithEndpointURL(endpoint))
	} else {
		// A bare host:port. url.Parse reads "localhost:4317" as scheme
		// "localhost" with an empty host, so this form must not reach
		// WithEndpointURL — it would yield an exporter pointed at nothing that
		// fails by never delivering rather than by refusing.
		options = append(options, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	}

	exporter, err := otlptracegrpc.New(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("build the otlp exporter for %q: %w", endpoint, err)
	}
	return exporter, nil
}

// hasScheme distinguishes the two spellings an operator will write. Not
// url.Parse: every host:port parses successfully as a URL, which is the trap.
func hasScheme(endpoint string) bool {
	return strings.Contains(endpoint, "://")
}
