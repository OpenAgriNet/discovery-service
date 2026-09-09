package telemetry

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

func testIdentity() Identity {
	return Identity{
		Producer:  "discovery.oan.example.org",
		Domain:    "Agriculture",
		NetworkID: "mahavistar",
	}
}

// attributesOf indexes a Resource by key, so an assertion names the attribute
// rather than a position in a slice whose order the SDK is free to change.
func attributesOf(t *testing.T, keyValues []attribute.KeyValue) map[string]string {
	t.Helper()

	indexed := make(map[string]string, len(keyValues))
	for _, keyValue := range keyValues {
		indexed[string(keyValue.Key)] = keyValue.Value.String()
	}
	return indexed
}

// TestTheResourceCarriesTheFiveSpecAttributes is 23a's central acceptance
// criterion.
//
// The five are asserted through the registry rather than against literal
// strings, which is the whole claim telemetry-seam.md makes: renaming `producer`
// is one edit in registry.go and this test follows it. A literal "producer" here
// would let the Resource and the registry drift, and the drift would be
// invisible because both halves would still pass their own tests.
func TestTheResourceCarriesTheFiveSpecAttributes(t *testing.T) {
	res, err := projectResource(context.Background(), testIdentity(), readBuild())
	if err != nil {
		t.Fatalf("projectResource: %v", err)
	}
	present := attributesOf(t, res.Attributes())

	want := map[fact.Key]string{
		fact.ResourceEID:         eidAPI,
		fact.ResourceProducer:    "discovery.oan.example.org",
		fact.ResourceDomain:      "Agriculture",
		fact.ResourceServiceName: serviceName,
		fact.ResourceNetworkID:   "mahavistar",
	}
	for key, wantValue := range want {
		def := fact.Of(key)
		got, ok := present[def.SpanKey]
		if !ok {
			t.Errorf("the Resource carries no %s (%s)", def.SpanKey, def.Name)
			continue
		}
		if got != wantValue {
			t.Errorf("%s = %q, want %q", def.SpanKey, got, wantValue)
		}
	}
}

// TestServiceNameIsNotProducer pins the correction opentelemetry.md:437 records.
//
// The two rows said the same thing once, and collapsing them means either every
// deployment reports a different service.name and ClickStack cannot group the
// service, or producer reports a service name and the network cannot identify
// the participant. The bug is one assignment away and it reads as a
// simplification, so it gets its own test.
func TestServiceNameIsNotProducer(t *testing.T) {
	res, err := projectResource(context.Background(), testIdentity(), readBuild())
	if err != nil {
		t.Fatalf("projectResource: %v", err)
	}
	present := attributesOf(t, res.Attributes())

	producer := present[fact.Of(fact.ResourceProducer).SpanKey]
	service := present[fact.Of(fact.ResourceServiceName).SpanKey]

	if producer == service {
		t.Errorf("producer and service.name are both %q; they answer different questions — "+
			"which participant this is, and what software this is", producer)
	}
	if service != serviceName {
		t.Errorf("service.name = %q, want the constant %q: it is ClickStack's grouping column "+
			"and must not vary per deployment", service, serviceName)
	}
}

// TestAnUnstampedBuildReportsDev is OP5's explicit acceptance.
//
// `go test` never passes -ldflags, so this test IS an unstamped build and needs
// no fixture to be one. An empty service.version is indistinguishable from an
// unset Resource field — a facilitator seeing nothing cannot tell whether the
// participant declined to say or the attribute was dropped in transit — so the
// unstamped case has to answer something, and `dev` is a value nobody will
// mistake for a release.
func TestAnUnstampedBuildReportsDev(t *testing.T) {
	if version != "dev" {
		t.Fatalf("version = %q in a test binary; the -ldflags default is what this asserts", version)
	}

	res, err := projectResource(context.Background(), testIdentity(), readBuild())
	if err != nil {
		t.Fatalf("projectResource: %v", err)
	}
	present := attributesOf(t, res.Attributes())

	key := fact.Of(fact.ResourceServiceVersion).SpanKey
	if present[key] != "dev" {
		t.Errorf("%s = %q, want dev", key, present[key])
	}
}

// TestNoBuildAttributeIsEmpty covers the other three, and for the same reason:
// each has a named absence — `unknown` for the commit and the tree state — so
// none of them may reach a Resource as "".
//
// A `go test` binary carries no VCS stamp at all, which makes this test the
// exact case the fallbacks exist for. It asserts they fired, not what they
// found: asserting a commit here would assert against the checkout.
func TestNoBuildAttributeIsEmpty(t *testing.T) {
	res, err := projectResource(context.Background(), testIdentity(), readBuild())
	if err != nil {
		t.Fatalf("projectResource: %v", err)
	}
	present := attributesOf(t, res.Attributes())

	buildKeys := []fact.Key{
		fact.ResourceServiceVersion,
		fact.ResourceBuildCommit,
		fact.ResourceBuildTreeState,
		fact.ResourceBuildDate,
	}
	for _, key := range buildKeys {
		def := fact.Of(key)
		value, ok := present[def.SpanKey]
		if !ok {
			t.Errorf("the Resource carries no %s (%s)", def.SpanKey, def.Name)
			continue
		}
		if value == "" {
			t.Errorf("%s is empty; an empty attribute is indistinguishable from an unset one", def.SpanKey)
		}
	}
}

// TestTheTreeStateStaysInsideItsDeclaredValues is the Bounded row's guard.
//
// vcs.modified arrives as the strings "true" and "false", and the registry
// declares clean/dirty/unknown, so there is a mapping here that can be wrong in
// a way nothing else notices — a Resource reporting `false` for a clean tree
// would satisfy every other test on this page.
func TestTheTreeStateStaysInsideItsDeclaredValues(t *testing.T) {
	def := fact.Of(fact.ResourceBuildTreeState)

	res, err := projectResource(context.Background(), testIdentity(), readBuild())
	if err != nil {
		t.Fatalf("projectResource: %v", err)
	}
	got := attributesOf(t, res.Attributes())[def.SpanKey]

	if !strings.Contains(strings.Join(def.Values, "|"), got) {
		t.Errorf("%s = %q, which the registry does not declare: %v", def.SpanKey, got, def.Values)
	}
}

// TestTheOperatorsResourceAttributesMergeWithoutOverridingIdentity pins the
// decision at opentelemetry.md:479.
//
// parent_id is not emitted; pod identity goes on the Resource through
// OTEL_RESOURCE_ATTRIBUTES from the chart's downward API, needing no code here.
// That only works if the env detector runs — and it must run BENEATH our
// attributes, not above them, or an operator can quietly replace the `producer`
// that validateOTel just refused to boot without.
func TestTheOperatorsResourceAttributesMergeWithoutOverridingIdentity(t *testing.T) {
	// t.Setenv rather than a parameter: resource.WithFromEnv reads the process
	// environment by construction, and this test's subject IS that read. The
	// rule it looks like it breaks — never assert against os.Environ — is about
	// inheriting a value the Makefile set; this sets its own and restores it.
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES",
		"k8s.pod.name=discovery-7d9f,k8s.namespace.name=oan,"+fact.Of(fact.ResourceProducer).SpanKey+"=impostor")

	res, err := projectResource(context.Background(), testIdentity(), readBuild())
	if err != nil {
		t.Fatalf("projectResource: %v", err)
	}
	present := attributesOf(t, res.Attributes())

	if present["k8s.pod.name"] != "discovery-7d9f" {
		t.Errorf("k8s.pod.name = %q; the env detector did not run, so pod identity needs code after all",
			present["k8s.pod.name"])
	}
	if present["k8s.namespace.name"] != "oan" {
		t.Errorf("k8s.namespace.name = %q", present["k8s.namespace.name"])
	}

	producer := fact.Of(fact.ResourceProducer).SpanKey
	if present[producer] != "discovery.oan.example.org" {
		t.Errorf("%s = %q; the environment overrode the validated identity", producer, present[producer])
	}
}

// TestTheSpanUUIDProcessorStampsEverySpan covers the one thing 23a registers
// that will ever touch a span.
//
// telemetry-seam.md:770 puts the processor here rather than in 23c because
// registering it starts no spans. It is nonetheless the only 23a code that runs
// per request, and the attribute it sets is a Required one, so a test drives a
// real provider rather than calling OnStart directly — OnStart against a
// hand-made span would not prove the processor was wired.
func TestTheSpanUUIDProcessorStampsEverySpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(spanUUID{}),
		sdktrace.WithSpanProcessor(recorder),
	)

	tracer := provider.Tracer(ScopeName, trace.WithInstrumentationVersion(ScopeVersion))
	for range 2 {
		_, span := tracer.Start(context.Background(), "probe")
		span.End()
	}

	key := fact.Of(fact.SpanUUID).SpanKey
	seen := map[string]bool{}
	for _, span := range recorder.Ended() {
		stamped := attributesOf(t, span.Attributes())[key]
		if stamped == "" {
			t.Fatalf("a span carries no %s", key)
		}
		if seen[stamped] {
			t.Errorf("%s %q appeared on two spans; it identifies the span, so it cannot repeat", key, stamped)
		}
		seen[stamped] = true
	}
	if len(seen) != 2 {
		t.Fatalf("recorded %d spans, want 2", len(seen))
	}
}

// TestTheEndpointAcceptsBothFormsAnOperatorWillWrite is a small thing with a
// silent failure mode.
//
// otlptracegrpc.WithEndpoint wants host:port and url.Parse turns "localhost:4317"
// into scheme "localhost" with an empty host, so handing a bare authority to
// WithEndpointURL yields an exporter pointed at nothing — and it fails by never
// delivering, not by refusing. OTEL_EXPORTER_OTLP_ENDPOINT is the SDK's own
// variable and the SDK accepts the URL form, so an operator who has set it
// before will write a scheme.
func TestTheEndpointAcceptsBothFormsAnOperatorWillWrite(t *testing.T) {
	cases := map[string]bool{
		"localhost:4317":            false,
		"collector.oan.svc:4317":    false,
		"http://localhost:4317":     true,
		"https://collector.oan:443": true,
	}
	for endpoint, wantURL := range cases {
		if hasScheme(endpoint) != wantURL {
			t.Errorf("hasScheme(%q) = %v, want %v", endpoint, hasScheme(endpoint), wantURL)
		}
	}
}
