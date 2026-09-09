package middlewares

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// capturing is a probe link that reports the record it found in context at the
// slot it is mounted at. It exists so a test can compare the record two
// different links see — the one thing an assertion made in the handler alone
// cannot tell apart from two records that happen to hold the same facts.
func capturing(found **fact.Record) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*found = fact.From(r.Context())
			next.ServeHTTP(w, r)
		})
	}
}

// serveChained runs a handler under the given links, outermost first, with an
// observed logger installed above them the way RequestID installs one in the
// assembled chain.
func serveChained(t *testing.T, body string, links []func(http.Handler) http.Handler,
	below http.HandlerFunc) (*httptest.ResponseRecorder, *observer.ObservedLogs) {
	t.Helper()

	core, logged := observer.New(zapcore.DebugLevel)

	var handler http.Handler = below
	for index := len(links) - 1; index >= 0; index-- {
		handler = links[index](handler)
	}

	request := httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader(body))
	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded,
		request.WithContext(logger.NewContext(request.Context(), zap.New(core))))
	return recorded, logged
}

// TestTraceAllocatesTheRecord is the reason the allocation moved up a slot.
//
// The record used to be allocated by RequestLogger, which is one link BELOW
// Trace. That was fine while the log was its only reader; from 23c the span
// starts here, and a span cannot read attributes from a record allocated after
// it. So Trace allocates and RequestLogger adopts.
//
// Unconditionally, including under OTEL_EXPORTER=none. Making the allocation
// depend on a live tracer would make the record's lifetime vary by environment
// variable, which is exactly the kind of difference that turns a green test
// suite into a production-only failure. 23c did give Trace configuration — a
// tracer and a subscriber id — and this is what says neither of them gates the
// allocation.
func TestTraceAllocatesTheRecord(t *testing.T) {
	var found *fact.Record
	trace, _ := tracing(t)

	serveChained(t, "", []func(http.Handler) http.Handler{trace, capturing(&found)},
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	if found == nil {
		t.Fatal("no record below Trace; 23c's span would have nothing to read attributes off")
	}
}

// TestTraceAdoptsARecordAlreadyInContext is the other half of Trace's
// adopt-or-allocate, and it is here because nothing in the assembled chain
// exercises it — Trace runs first, so it always allocates.
//
// That is exactly why it needs a test. An unconditional fact.New here would be
// invisible today and would SHADOW an inherited record the moment a chain change
// puts something above Trace: two records holding the same facts, because
// Envelope writes whichever is nearer, and every assertion about the log line
// still passing while the span read the other one.
func TestTraceAdoptsARecordAlreadyInContext(t *testing.T) {
	var found *fact.Record
	trace, _ := tracing(t)

	core, _ := observer.New(zapcore.DebugLevel)
	handler := trace(capturing(&found)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })))

	request := httptest.NewRequest(http.MethodPost, "/publish", nil)
	ctx, allocated := fact.New(logger.NewContext(request.Context(), zap.New(core)))
	handler.ServeHTTP(httptest.NewRecorder(), request.WithContext(ctx))

	if found != allocated {
		t.Error("Trace replaced the record it was handed rather than adopting it; a link " +
			"above it would write facts the span never sees")
	}
}

// TestRequestLoggerAdoptsTheRecordTraceAllocated is the half that bites, and it
// asserts identity rather than contents on purpose.
//
// If RequestLogger allocated its own, the two records would hold the same facts
// — Envelope runs below both and would observe onto whichever is nearer — and
// every assertion about the completion line would still pass. What would break
// is the span: Trace would project Trace's record, Envelope would write
// RequestLogger's, and the span would lose the correlators while the log kept
// them. A pointer comparison is the only thing that sees that coming.
func TestRequestLoggerAdoptsTheRecordTraceAllocated(t *testing.T) {
	var atTrace, atHandler *fact.Record
	trace, _ := tracing(t)

	serveChained(t, "", []func(http.Handler) http.Handler{
		trace, capturing(&atTrace), RequestLogger, capturing(&atHandler),
	}, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	if atTrace == nil {
		t.Fatal("Trace allocated no record")
	}
	if atHandler != atTrace {
		t.Error("RequestLogger replaced the record Trace allocated rather than adopting it; " +
			"the span and the log line would observe onto different records")
	}
}

// TestRequestLoggerAllocatesWhenItIsMountedAlone is the other side of
// adopt-or-allocate, and it is not hypothetical: request_logger_test.go mounts
// RequestLogger with no Trace above it, and so does every test that wants a
// completion line without a chain.
//
// Adopt-only would leave those with a nil record, no facts and a completion line
// carrying neither status nor duration — passing tests that assert on the
// header and silently empty ones that assert on the line.
func TestRequestLoggerAllocatesWhenItIsMountedAlone(t *testing.T) {
	var found *fact.Record

	serveChained(t, "", []func(http.Handler) http.Handler{RequestLogger, capturing(&found)},
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	if found == nil {
		t.Fatal("RequestLogger mounted alone allocated no record, so it has no facts to log")
	}
}

// TestTheCompletionLineKeepsItsFieldOrder is 23b's acceptance criterion stated
// as an assertion, and it is the one the rest of the suite cannot make.
//
// Every other test on the completion line reads ContextMap, which is a map and
// has no order. The line's order comes from the order the facts were observed —
// Envelope's three correlators, then the status and duration complete() adds,
// then the category — and it has to survive the projection unchanged. Reordering
// every log line in the service is a diff nobody reviews and every dashboard
// notices.
func TestTheCompletionLineKeepsItsFieldOrder(t *testing.T) {
	const correlating = `{"context":{"action":"catalog/publish","transactionId":"a3f0",` +
		`"messageId":"2f6b"},"message":{"catalogs":[]}}`

	trace, _ := tracing(t)

	_, logged := serveChained(t, correlating, []func(http.Handler) http.Handler{
		trace, RequestLogger, Envelope(config.Errors{}, roomy),
	}, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	want := []string{"transaction_id", "message_id", "action", "status", "duration_ms"}

	var got []string
	for _, field := range completionLine(t, logged).Context {
		got = append(got, field.Key)
	}

	if len(got) != len(want) {
		t.Fatalf("the completion line carries %v, want %v", got, want)
	}
	for index, key := range want {
		if got[index] != key {
			t.Errorf("field %d is %q, want %q — the line's order changed", index, got[index], key)
		}
	}
}
