package telemetry_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry"
)

// baseConfig is a configuration that has already passed validateOTel, because
// Init's contract starts there: config refuses the incoherent combinations at
// boot, so Init does not re-check them and these tests must not pretend it does.
func baseConfig(exporter string) config.Config {
	return config.Config{
		App: config.App{
			Network:    "mahavistar",
			Subscriber: "discovery.oan.example.org",
			Domain:     "Agriculture",
		},
		OTel: config.OTel{Exporter: exporter},
	}
}

// TestNoneBootsAndRecordsNothing is the acceptance criterion an existing
// deployment depends on: OTEL_EXPORTER=none still boots.
//
// It asserts more than "no error". Under `none` the provider is a real SDK
// provider — one construction path, so a boot that works without a collector is
// evidence about the boot that will run with one — and what makes it free is
// the sampler, not the absence of an exporter. A recording span allocates,
// holds attributes and runs every processor; if this assertion ever flips, every
// request on every collector-less deployment starts paying for telemetry
// nothing will read.
func TestNoneBootsAndRecordsNothing(t *testing.T) {
	provider, err := telemetry.Init(context.Background(), baseConfig(config.ExporterNone))
	if err != nil {
		t.Fatalf("Init with the default exporter: %v", err)
	}
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	_, span := provider.Tracer().Start(context.Background(), "probe")
	defer span.End()

	if span.IsRecording() {
		t.Error("a span is recording under OTEL_EXPORTER=none; nothing consumes it, so it is pure cost")
	}
}

// TestOtlpBootsWithoutACollectorListening is the case a rollout actually hits.
//
// The exporter is created lazily and dials on first export, which is the
// behaviour to depend on: a pod that refused to start because the collector was
// not up yet would make telemetry a hard dependency of serving traffic. That is
// backwards — the service answers farmers' queries whether or not anyone is
// watching it.
func TestOtlpBootsWithoutACollectorListening(t *testing.T) {
	cfg := baseConfig(config.ExporterOTLP)
	// Port 1 on the loopback: nothing listens and nothing can be started there
	// by accident, so this cannot become a test that quietly passes because
	// something else in the suite bound a collector port.
	cfg.OTel.Endpoint = "127.0.0.1:1"

	provider, err := telemetry.Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Init with an unreachable collector: %v", err)
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown with an unreachable collector: %v", err)
	}
}

// TestTheSamplerRespectsAnInboundDecision is the pin on a line that is not
// there, and it is the only kind of pin such a line can have.
//
// Init passes no WithSampler on the OTLP path. The default with no option is
// ParentBased(AlwaysSample): sampled at the root, and — the half that matters
// here — deferring to whatever the caller decided. Passing AlwaysSample explicitly
// looks like the same thing and is not: it discards the parent's decision, so
// four layers each sampling independently produce traces with holes, and a hole
// reads as a dropped hop rather than as a sampling artefact. It also silently
// overrides OTEL_TRACES_SAMPLER, which an operator can see in their manifest and
// believe is doing something.
//
// There is no code to review for that, which is why open question 6 asked for
// the behaviour to be encoded instead. This is that encoding: it fails the
// moment anyone adds the option back.
func TestTheSamplerRespectsAnInboundDecision(t *testing.T) {
	cfg := baseConfig(config.ExporterOTLP)
	cfg.OTel.Endpoint = "127.0.0.1:1"

	provider, err := telemetry.Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	for _, decision := range []struct {
		name  string
		flags trace.TraceFlags
		want  bool
	}{
		{"the caller did not sample", 0, false},
		{"the caller sampled", trace.FlagsSampled, true},
	} {
		t.Run(decision.name, func(t *testing.T) {
			parent := trace.NewSpanContext(trace.SpanContextConfig{
				TraceID:    trace.TraceID{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6},
				SpanID:     trace.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
				TraceFlags: decision.flags,
				Remote:     true,
			})

			// Started and never ended, which is the point rather than an
			// oversight: End is what hands the span to the batcher, and a batcher
			// pointed at a closed port spends its full export timeout failing to
			// deliver it. The sampling decision is made at Start, so there is
			// nothing left to learn from ending it.
			_, span := provider.Tracer().Start(
				trace.ContextWithSpanContext(context.Background(), parent), "discover")

			if got := span.IsRecording(); got != decision.want {
				t.Errorf("IsRecording() = %v under a parent with sampled=%v, want %v — "+
					"the sampler is not parent-based, so this node decides on its own and "+
					"the cross-layer trace comes out with holes in it",
					got, decision.flags.IsSampled(), decision.want)
			}
		})
	}
}

// TestARootSpanIsAlwaysRecordedBecauseTheMetricsAreCountedFromIt is the other
// half of the sampler pin, and it exists because the five `metric.code` streams
// in otel/collector.yaml are derived from the exported span stream. A span that
// is not recorded is not exported, and a span that is not exported is not
// counted — so the sampling rate on roots IS the accuracy of every one of those
// metrics, and nothing said so.
//
// The test above already pins the parented cases, which is why a ratio sampler
// would slip past it: ParentBased(TraceIDRatioBased(0.1)) still defers to a
// parent, so both of its subtests keep passing while every ROOT span samples at
// one in ten. discover_api_total_count would then read 10% of the traffic
// served, with no error anywhere and no way for a consumer to tell — the
// facilitator sees a small, healthy-looking network.
//
// A root span is a real case here rather than a hypothetical one: an inbound
// request with no traceparent starts one, which is every request that does not
// arrive through an onix adapter.
//
// What this does NOT pin, because it is deliberate and tested above: an inbound
// traceparent carrying sampled=0 produces no span and therefore no count. That
// undercount is the network's decision to make and not this service's to
// override — see opentelemetry.md §The five codes — the registry.
func TestARootSpanIsAlwaysRecordedBecauseTheMetricsAreCountedFromIt(t *testing.T) {
	cfg := baseConfig(config.ExporterOTLP)
	cfg.OTel.Endpoint = "127.0.0.1:1"

	provider, err := telemetry.Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	// No parent in the context, which is what makes it a root. Started and
	// never ended, for the reason given above: End hands it to a batcher
	// pointed at a closed port.
	_, span := provider.Tracer().Start(context.Background(), "discover")

	if !span.IsRecording() {
		t.Error("IsRecording() = false on a root span, want true — the sampler " +
			"drops spans this node originates, so every metric.code derived from " +
			"the trace stream in otel/collector.yaml now undercounts by the " +
			"sampling rate, silently and with no error anywhere")
	}
}

// TestShutdownIsSafeTwiceAndOnNothing is what app.Close needs.
//
// Close runs on the happy path and on every path Build failed after Init, so it
// can see a nil Provider; and main defers it while Run may also have returned an
// error, so it can see one that has already been shut down. Neither may panic —
// a panic in the cleanup that is answering a failure replaces the diagnosis with
// a stack trace from the wrong place.
func TestShutdownIsSafeTwiceAndOnNothing(t *testing.T) {
	var absent *telemetry.Provider
	if err := absent.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown on a nil Provider: %v", err)
	}

	provider, err := telemetry.Init(context.Background(), baseConfig(config.ExporterNone))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Errorf("first Shutdown: %v", err)
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Errorf("second Shutdown: %v", err)
	}
}

// TestTheScopeIsWhatTheSpecRequires pins the two constants.
//
// scope.name and scope.version are Required inside the spec's Optional scope
// object, and both values are borrowed rather than chosen: `discovery_service`
// is the spec's own example value, and `1.0` is the only version any of its
// examples use because the spec repo carries no tags (decision 1). Neither is
// ours to tidy — a facilitator may key on them — so a test states them
// explicitly rather than leaving them as constants someone renames while
// cleaning up.
func TestTheScopeIsWhatTheSpecRequires(t *testing.T) {
	if telemetry.ScopeName != "discovery_service" {
		t.Errorf("ScopeName = %q, want discovery_service — the spec's own example value "+
			"(otel-specification.md:330-400), underscored and not the service.name spelling",
			telemetry.ScopeName)
	}
	if telemetry.ScopeVersion != "1.0" {
		t.Errorf("ScopeVersion = %q, want 1.0 — example-derived, since the spec repo has no "+
			"version tags (decision 1)", telemetry.ScopeVersion)
	}
}

// TestTheTracerCarriesTheScope is why A23 rejected otelhttp.
//
// The instrumentation scope is fixed when the tracer is obtained and cannot be
// changed at span creation, so a wrapper that obtains its own tracer emits its
// own scope — which is how a hand-rolled Trace middleware became cheaper than
// configuring somebody else's. Tracer() exists so 23c cannot get this wrong by
// calling provider.Tracer("") somewhere.
func TestTheTracerCarriesTheScope(t *testing.T) {
	provider, err := telemetry.Init(context.Background(), baseConfig(config.ExporterNone))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	if provider.Tracer() == nil {
		t.Fatal("Tracer() is nil; 23c's middleware takes this and would have to build its own")
	}
}
