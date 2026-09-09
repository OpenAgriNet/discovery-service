package telemetry

import (
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// This file is test support in a non-test file, the way src/indexing/geo does
// it, and the reason is the import boundary rather than convenience.
//
// A23 keeps go.opentelemetry.io out of everything but this package plus the
// handful of files boundary_test.go names. Tests are the largest population that
// would otherwise need it: middlewares, app, and every controller 23d touches
// all need to see what a span came out as. Exporting the answer as plain structs
// here means one place imports the SDK instead of a dozen, and it means a test
// asserts on "the span's name" rather than on a tracetest.SpanStub whose shape
// is the SDK's to change.

// Recorder collects the spans a Provider exported, for tests to read back.
type Recorder struct {
	exporter *tracetest.InMemoryExporter
}

// NewRecorder builds a Provider that exports into memory, and the Recorder that
// reads it.
//
// It is deliberately not Init with an extra option. Init's subject is
// configuration — which exporter, which endpoint, which Resource — and a test
// asserting on span shape has no opinion about any of that. What it does share
// is the two things a span's shape depends on: the spanUUID processor, so a test
// sees the same stamped attribute production does, and the scoped tracer, so
// scope.name and scope.version are asserted rather than assumed.
//
// WithSyncer, not WithBatcher: a batching exporter would make every assertion
// depend on a flush the test has to remember, and a forgotten flush reads as a
// span that was never started.
func NewRecorder() (*Provider, *Recorder) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithSpanProcessor(spanUUID{}),
	)
	return &Provider{
			provider: provider,
			tracer:   provider.Tracer(ScopeName, trace.WithInstrumentationVersion(ScopeVersion)),
		}, &Recorder{
			exporter: exporter,
		}
}

// Scope is the instrumentation scope a span was created under. Its whole reason
// for being assertable is A23: otelhttp would have stamped its own here, and no
// other property of the span would have looked wrong.
type Scope struct {
	Name    string
	Version string
}

// Span is one exported span, flattened to what a test asks about.
type Span struct {
	Name   string
	Kind   string
	Status string
	Scope  Scope

	TraceID      string
	SpanID       string
	ParentSpanID string

	Start time.Time
	End   time.Time

	// Attributes by key. A map because every assertion is "what is X", and the
	// order attributes come out in is the exporter's business, not a test's.
	Attributes map[string]any

	// Events in the order they were added, which for 23d is the assertion: the
	// event times have to be strictly increasing and none of them may equal the
	// span's end.
	Events []Event
}

// Event is one span event.
type Event struct {
	Name       string
	Time       time.Time
	Attributes map[string]any
}

// Spans returns what has been exported so far, oldest first.
func (r *Recorder) Spans() []Span {
	stubs := r.exporter.GetSpans()

	spans := make([]Span, 0, len(stubs))
	for _, stub := range stubs {
		span := Span{
			Name:         stub.Name,
			Kind:         stub.SpanKind.String(),
			Status:       stub.Status.Code.String(),
			Scope:        Scope{Name: stub.InstrumentationScope.Name, Version: stub.InstrumentationScope.Version},
			TraceID:      stub.SpanContext.TraceID().String(),
			SpanID:       stub.SpanContext.SpanID().String(),
			ParentSpanID: stub.Parent.SpanID().String(),
			Start:        stub.StartTime,
			End:          stub.EndTime,
			Attributes:   flatten(stub.Attributes),
		}
		for _, event := range stub.Events {
			span.Events = append(span.Events, Event{
				Name:       event.Name,
				Time:       event.Time,
				Attributes: flatten(event.Attributes),
			})
		}
		spans = append(spans, span)
	}
	return spans
}

// Reset drops what has been recorded, for a test that serves several requests
// through one provider and wants to talk about one of them.
func (r *Recorder) Reset() { r.exporter.Reset() }

// flatten turns the SDK's attribute list into the map every assertion wants.
//
// AsInterface rather than a switch on Type, so an attribute Kind nobody has used
// yet arrives readable instead of arriving as the zero value of whichever branch
// a switch fell through to.
func flatten(attributes []attribute.KeyValue) map[string]any {
	if len(attributes) == 0 {
		return map[string]any{}
	}
	flat := make(map[string]any, len(attributes))
	for _, kv := range attributes {
		flat[string(kv.Key)] = kv.Value.AsInterface()
	}
	return flat
}
