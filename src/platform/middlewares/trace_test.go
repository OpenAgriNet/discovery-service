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
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// recipient is the subscriber id these tests run as. It is not the caller's
// receiverId and the two must not be conflated — see the test at the bottom.
const recipient = "discovery.oan.example.org"

// traced serves one request through the given links and returns the response.
// The spans are read back off the Recorder that tracing handed out, not from
// here, because most tests want only one of the two.
//
// The links are given outermost first, the way router.go assembles them, and
// Trace is not implied: the tests about ordering mount it themselves so the
// nesting under test is visible in the test rather than in this helper.
func traced(t *testing.T, request *http.Request, links []func(http.Handler) http.Handler,
	below http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()

	var handler http.Handler = below
	for index := len(links) - 1; index >= 0; index-- {
		handler = links[index](handler)
	}

	core, _ := observer.New(zapcore.DebugLevel)
	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded,
		request.WithContext(logger.NewContext(request.Context(), zap.New(core))))

	return recorded
}

// only returns the single span a test expected to be exported, failing rather
// than indexing when there is not exactly one — a nil panic three lines later
// says nothing about what actually went wrong.
func only(t *testing.T, spans []telemetry.Span) telemetry.Span {
	t.Helper()
	if len(spans) != 1 {
		t.Fatalf("%d spans exported, want exactly 1", len(spans))
	}
	return spans[0]
}

// tracing builds a recorder-backed Trace link and the recorder that reads it
// back, so each test states the chain it is about in one place.
func tracing(t *testing.T) (func(http.Handler) http.Handler, *telemetry.Recorder) {
	t.Helper()
	provider, recorder := telemetry.NewRecorder()
	return Trace(provider.Tracer(), recipient), recorder
}

// TestTheSpanIsNamedByTheActionAndNotTheRoute is the shape the worked example
// pins (telemetry-examples.md:83-110): "name": "discover".
//
// The route would have been the easier answer and it is the wrong one across
// repos. A facilitator reading spans from this service and from beckn-onix has
// to group them by what was asked, and onix — which has no HTTP route to speak
// of on some hops — names them by the action. Two conventions for one concept
// makes every cross-layer query a union.
//
// The name is set at the END, in the deferred block, not at Start. The action
// lives inside the body, and the body has not been parsed when the span starts:
// Envelope is four links below.
func TestTheSpanIsNamedByTheActionAndNotTheRoute(t *testing.T) {
	trace, recorder := tracing(t)

	const discovering = `{"context":{"action":"discover","transactionId":"a3f0",` +
		`"messageId":"2f6b"},"message":{"intent":{}}}`

	request := httptest.NewRequest(http.MethodPost, "/discover", strings.NewReader(discovering))
	traced(t, request, []func(http.Handler) http.Handler{
		trace, RequestLogger, Envelope(config.Errors{}, roomy),
	}, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	span := only(t, recorder.Spans())
	if span.Name != "discover" {
		t.Errorf("span name is %q, want the action %q — a facilitator grouping this "+
			"service's spans with onix's has to union two conventions otherwise",
			span.Name, "discover")
	}
}

// TestAPublishSpanIsNamedForTheNormalisedAction is where the two spellings of
// one action stop being two.
//
// context.action accepts both `publish` and `catalog/publish` (beckn/actions.go)
// because the field arrives in a body this service did not write. beckn.action
// is Bounded over ["discover","publish"], and its registry Note says why: two
// spellings split every publish query in two, and the person writing the query
// has no way to know they needed a union.
func TestAPublishSpanIsNamedForTheNormalisedAction(t *testing.T) {
	trace, recorder := tracing(t)

	const publishing = `{"context":{"action":"catalog/publish","transactionId":"a3f0",` +
		`"messageId":"2f6b"},"message":{"catalogs":[]}}`

	request := httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader(publishing))
	traced(t, request, []func(http.Handler) http.Handler{
		trace, RequestLogger, Envelope(config.Errors{}, roomy),
	}, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	span := only(t, recorder.Spans())
	if span.Name != "publish" {
		t.Errorf("span name is %q, want %q; catalog/publish is the same action under a "+
			"second spelling and beckn.action is Bounded over the normalised one",
			span.Name, "publish")
	}
	if got := span.Attributes[fact.Of(fact.BecknAction).SpanKey]; got != "publish" {
		t.Errorf("beckn.action = %v, want publish", got)
	}
}

// TestASpanWhoseEnvelopeNeverParsedKeepsTheRouteAsItsName is the failure path,
// and it is the reason the span is named at Start and RE-named at the end rather
// than only at the end.
//
// A request rejected by the schema validator, or one whose body is not JSON at
// all, never reaches an action. A span named "" is what a backend shows as a
// blank row, which is the least useful thing to hand somebody looking at exactly
// the requests that failed.
func TestASpanWhoseEnvelopeNeverParsedKeepsTheRouteAsItsName(t *testing.T) {
	trace, recorder := tracing(t)

	request := httptest.NewRequest(http.MethodPost, "/discover", strings.NewReader("not json"))
	traced(t, request, []func(http.Handler) http.Handler{trace},
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) })

	span := only(t, recorder.Spans())
	if span.Name != "/discover" {
		t.Errorf("span name is %q, want the route %q — the requests that never parsed are "+
			"the ones somebody is looking for, and a blank name loses them", span.Name, "/discover")
	}
}

// TestTheSpanIsAServerSpan is one line of code and a cross-repo contract. A
// backend's service map is built from SPAN_KIND_SERVER edges; an internal span
// makes this service invisible on it.
func TestTheSpanIsAServerSpan(t *testing.T) {
	trace, recorder := tracing(t)

	traced(t, httptest.NewRequest(http.MethodPost, "/discover", nil),
		[]func(http.Handler) http.Handler{trace},
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	if got := only(t, recorder.Spans()).Kind; got != "server" {
		t.Errorf("span kind is %q, want server; the service map is built from server edges", got)
	}
}

// TestTheSpanCarriesOurScopeAndNotSomebodyElses is A23 stated as an assertion.
//
// The instrumentation scope is fixed when the tracer is obtained, so a wrapper
// that obtains its own — otelhttp — stamps its package name here, and every
// other property of the span still looks right. scope.name is Required in the
// network telemetry spec, so that is a batch a facilitator rejects.
func TestTheSpanCarriesOurScopeAndNotSomebodyElses(t *testing.T) {
	trace, recorder := tracing(t)

	traced(t, httptest.NewRequest(http.MethodPost, "/discover", nil),
		[]func(http.Handler) http.Handler{trace},
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	span := only(t, recorder.Spans())
	if span.Scope.Name != telemetry.ScopeName || span.Scope.Version != telemetry.ScopeVersion {
		t.Errorf("scope is %s/%s, want %s/%s", span.Scope.Name, span.Scope.Version,
			telemetry.ScopeName, telemetry.ScopeVersion)
	}
}

// TestAnInboundTraceparentIsJoinedRatherThanReplaced is the whole point of
// propagating.
//
// "The seeker asked and nobody served it" is currently unanswerable because each
// hop starts its own trace, so the two halves live in different traces and
// nothing joins them. Starting a root span here regardless of the header would
// leave it that way while looking, on this service's own dashboards, entirely
// correct.
func TestAnInboundTraceparentIsJoinedRatherThanReplaced(t *testing.T) {
	trace, recorder := tracing(t)

	const (
		callersTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
		callersSpan  = "00f067aa0ba902b7"
	)

	request := httptest.NewRequest(http.MethodPost, "/discover", nil)
	request.Header.Set("traceparent", "00-"+callersTrace+"-"+callersSpan+"-01")

	traced(t, request, []func(http.Handler) http.Handler{trace},
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	span := only(t, recorder.Spans())
	if span.TraceID != callersTrace {
		t.Errorf("trace id is %s, want the caller's %s; this hop started a second trace and "+
			"the request now has no end-to-end view", span.TraceID, callersTrace)
	}
	if span.ParentSpanID != callersSpan {
		t.Errorf("parent span id is %s, want the caller's %s", span.ParentSpanID, callersSpan)
	}
}

// TestARequestWithNoTraceparentStartsARoot is the other side, and it is a
// deliberate non-refusal: a caller with no instrumentation gets served. Making
// this service's availability depend on its callers' telemetry would invert what
// the telemetry is for.
func TestARequestWithNoTraceparentStartsARoot(t *testing.T) {
	trace, recorder := tracing(t)

	traced(t, httptest.NewRequest(http.MethodPost, "/discover", nil),
		[]func(http.Handler) http.Handler{trace},
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	span := only(t, recorder.Spans())
	if span.ParentSpanID != "0000000000000000" {
		t.Errorf("parent span id is %s on a request with no traceparent, want the zero id",
			span.ParentSpanID)
	}
	if span.TraceID == "00000000000000000000000000000000" {
		t.Error("no trace id at all; the span is not recording")
	}
}

// TestAMalformedTraceparentIsIgnoredRatherThanRefused is the same argument
// applied to a header this service cannot fix. A 400 here would reject a valid
// Beckn request over a header the Beckn spec does not mention.
func TestAMalformedTraceparentIsIgnoredRatherThanRefused(t *testing.T) {
	trace, recorder := tracing(t)

	request := httptest.NewRequest(http.MethodPost, "/discover", nil)
	request.Header.Set("traceparent", "this-is-not-a-traceparent")

	recorded := traced(t, request, []func(http.Handler) http.Handler{trace},
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	if recorded.Code != http.StatusOK {
		t.Errorf("status %d on a malformed traceparent, want 200; the header is not part of "+
			"the Beckn contract and cannot be grounds for refusing a valid request", recorded.Code)
	}
	if got := len(recorder.Spans()); got != 1 {
		t.Errorf("%d spans on a malformed traceparent, want 1", got)
	}
}

// TestTheSpanCarriesTheStatusThatWasActuallyWritten is the ordering constraint
// inside Trace itself.
//
// RequestLogger observes the status from inside its own recorder, which is BELOW
// Trace — so it lands on the record during next.ServeHTTP, and Trace's
// projection has to run after that returns. Projecting at the top would put 200
// on every span, including the ones that were rejected, and the span would be
// wrong exactly where somebody is looking.
func TestTheSpanCarriesTheStatusThatWasActuallyWritten(t *testing.T) {
	trace, recorder := tracing(t)

	traced(t, httptest.NewRequest(http.MethodPost, "/discover", nil),
		[]func(http.Handler) http.Handler{trace, RequestLogger},
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })

	span := only(t, recorder.Spans())
	if got := span.Attributes[fact.Of(fact.HTTPStatusCode).SpanKey]; got != int64(http.StatusTeapot) {
		t.Errorf("http.status_code = %v, want %d; the projection ran before the handler "+
			"answered and every span says 200", got, http.StatusTeapot)
	}
}

// TestAFiveHundredMarksTheSpanAsAnError is what a backend's error rate is
// computed from. An unset status on a 500 makes the span green while the request
// failed.
func TestAFiveHundredMarksTheSpanAsAnError(t *testing.T) {
	trace, recorder := tracing(t)

	traced(t, httptest.NewRequest(http.MethodPost, "/discover", nil),
		[]func(http.Handler) http.Handler{trace, RequestLogger},
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })

	if got := only(t, recorder.Spans()).Status; got != "Error" {
		t.Errorf("span status is %q on a 500, want Error", got)
	}
}

// TestAFourHundredDoesNotMarkTheSpanAsAnError is the semantic-convention rule
// for a SERVER span and it is not an oversight: a 400 is the caller's fault, and
// counting it as this service's error makes the error-rate panel track how many
// malformed requests arrived rather than how often the service broke.
func TestAFourHundredDoesNotMarkTheSpanAsAnError(t *testing.T) {
	trace, recorder := tracing(t)

	traced(t, httptest.NewRequest(http.MethodPost, "/discover", nil),
		[]func(http.Handler) http.Handler{trace, RequestLogger},
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) })

	if got := only(t, recorder.Spans()).Status; got == "Error" {
		t.Error("a 400 marks the span as an error; the error-rate panel then measures how " +
			"many bad requests arrived, not how often this service failed")
	}
}

// TestTheRecoveredPanicsFiveHundredIsInsideTheSpan is Task 20's chain-order
// assertion, moved off the header it used to be made on.
//
// It is a real hazard rather than a bookkeeping one. Recover's abort path
// RE-PANICS with http.ErrAbortHandler, which unwinds straight through Trace — so
// a projection and an End written as straight-line code after next.ServeHTTP
// never run, and the span leaks on precisely the path an operator most needs to
// see. Deferring is what makes this pass.
func TestTheRecoveredPanicsFiveHundredIsInsideTheSpan(t *testing.T) {
	trace, recorder := tracing(t)

	traced(t, httptest.NewRequest(http.MethodPost, "/discover", nil),
		[]func(http.Handler) http.Handler{trace, RequestLogger, Recover(config.Errors{})},
		func(http.ResponseWriter, *http.Request) { panic("the handler fell over") })

	span := only(t, recorder.Spans())
	if got := span.Attributes[fact.Of(fact.HTTPStatusCode).SpanKey]; got != int64(http.StatusInternalServerError) {
		t.Errorf("http.status_code = %v, want 500; Recover is not inside the span, so the "+
			"one request an operator opens the trace for is the one with nothing on it", got)
	}
	if span.Status != "Error" {
		t.Errorf("span status is %q on a recovered panic, want Error", span.Status)
	}
}

// TestASpanIsEndedEvenWhenTheHandlerAborts is the same hazard at its worst: the
// committed-response path, where Recover re-panics rather than answering. If the
// span is not ended by a defer it is never exported at all, and the trace for the
// request whose connection was dropped simply is not there.
func TestASpanIsEndedEvenWhenTheHandlerAborts(t *testing.T) {
	trace, recorder := tracing(t)

	defer func() {
		if panicked := recover(); panicked != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler — this test is not exercising "+
				"the abort path any more", panicked)
		}
		if got := len(recorder.Spans()); got != 1 {
			t.Errorf("%d spans after an aborted request, want 1; the span leaked on the "+
				"path that most needs a trace", got)
		}
	}()

	traced(t, httptest.NewRequest(http.MethodPost, "/discover", nil),
		[]func(http.Handler) http.Handler{trace, RequestLogger, Recover(config.Errors{})},
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte(`{"partial":`)); err != nil {
				t.Errorf("write: %v", err)
			}
			panic("fell over after committing")
		})
}

// TestTheRecipientIsOursAndTheReceiverIdIsTheCallers is two attributes that read
// as the same thing and are not.
//
// recipient.id is who this service IS, from APP_SUBSCRIBER_ID. beckn.receiverId
// is who the caller ADDRESSED, from the envelope. They agree on every correct
// request, which is exactly why one field holding both would be undetectably
// wrong — and disagreeing is a caller talking to a participant that is not us,
// which is worth a query and impossible to ask if the two were merged.
func TestTheRecipientIsOursAndTheReceiverIdIsTheCallers(t *testing.T) {
	trace, recorder := tracing(t)

	const misaddressed = `{"context":{"action":"discover","receiverId":"somebody.else.example.org",` +
		`"transactionId":"a3f0","messageId":"2f6b"},"message":{"intent":{}}}`

	request := httptest.NewRequest(http.MethodPost, "/discover", strings.NewReader(misaddressed))
	traced(t, request, []func(http.Handler) http.Handler{
		trace, RequestLogger, Envelope(config.Errors{}, roomy),
	}, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	span := only(t, recorder.Spans())
	if got := span.Attributes[fact.Of(fact.RecipientID).SpanKey]; got != recipient {
		t.Errorf("recipient.id = %v, want this service's own subscriber id %q", got, recipient)
	}
	if got := span.Attributes[fact.Of(fact.BecknReceiverID).SpanKey]; got != "somebody.else.example.org" {
		t.Errorf("beckn.receiverId = %v, want the id the caller addressed", got)
	}
}

// TestAnUnconfiguredSubscriberIsAbsentRatherThanEmpty is the case every
// deployment is in today: nothing sets APP_SUBSCRIBER_ID and it has no default.
//
// An empty recipient.id would say this service is a participant whose id is the
// empty string. recipient.unidentified says it was never told, which is the true
// statement and the one an operator can act on.
func TestAnUnconfiguredSubscriberIsAbsentRatherThanEmpty(t *testing.T) {
	provider, recorder := telemetry.NewRecorder()

	traced(t, httptest.NewRequest(http.MethodPost, "/discover", nil),
		[]func(http.Handler) http.Handler{Trace(provider.Tracer(), "")},
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	span := only(t, recorder.Spans())
	if _, present := span.Attributes[fact.Of(fact.RecipientID).SpanKey]; present {
		t.Error("recipient.id is on the span with no subscriber configured; an empty id " +
			"reads as a participant whose id is the empty string")
	}
	if got := span.Attributes[fact.Of(fact.RecipientID).AbsentFlag]; got != true {
		t.Errorf("%s = %v, want true", fact.Of(fact.RecipientID).AbsentFlag, got)
	}
}

// TestTheHTTPAttributesDescribeTheRequestThatArrived. Nothing subtle, but they
// are the attributes a route-level latency panel groups by, and http.route
// carrying the raw path with ids in it would make every request its own group.
func TestTheHTTPAttributesDescribeTheRequestThatArrived(t *testing.T) {
	trace, recorder := tracing(t)

	traced(t, httptest.NewRequest(http.MethodPost, "/discover", nil),
		[]func(http.Handler) http.Handler{trace},
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	span := only(t, recorder.Spans())
	for key, want := range map[fact.Key]string{
		fact.HTTPMethod: http.MethodPost,
		fact.HTTPRoute:  "/discover",
		fact.HTTPHost:   "example.com",
		fact.HTTPScheme: "http",
		fact.HTTPFlavor: "1.1",
	} {
		def := fact.Of(key)
		if got := span.Attributes[def.SpanKey]; got != want {
			t.Errorf("%s = %v, want %q", def.SpanKey, got, want)
		}
	}
}

// TestTraceNoLongerStampsTheChainHeader is the deletion, asserted.
//
// The entry existed so Task 20's order test had something to observe at this
// slot; the span is now that observation, and a marker kept beside it would be a
// second thing to keep true. Recover keeps its own — nothing else places it.
func TestTraceNoLongerStampsTheChainHeader(t *testing.T) {
	trace, _ := tracing(t)

	recorded := traced(t, httptest.NewRequest(http.MethodPost, "/discover", nil),
		[]func(http.Handler) http.Handler{trace},
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	if got := recorded.Header().Values(HeaderChain); len(got) != 0 {
		t.Errorf("Trace stamped %v; the span is the order assertion now", got)
	}
}

// TestTraceStillPassesTheRequestThroughUnmodified. It observes, it does not
// interfere: no status of its own, no body, and no response header.
func TestTraceStillPassesTheRequestThroughUnmodified(t *testing.T) {
	trace, _ := tracing(t)

	recorded := traced(t, httptest.NewRequest(http.MethodPost, "/discover", nil),
		[]func(http.Handler) http.Handler{trace},
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Handler", "reached")
			w.WriteHeader(http.StatusTeapot)
		})

	if recorded.Code != http.StatusTeapot {
		t.Errorf("status %d, want the handler's %d", recorded.Code, http.StatusTeapot)
	}
	if len(recorded.Header()) != 1 || recorded.Header().Get("X-Handler") != "reached" {
		t.Errorf("response headers are %v, want only the handler's own", recorded.Header())
	}
}

// TestTheCrossLayerAliasesCarryTheSameValueAsTheBecknPair is I1, asserted as an
// EQUALITY rather than as two literals.
//
// beckn-onix keys its collectors on `transaction_id` and `message_id`; this
// service's own spelling is `beckn.transactionId` and `beckn.messageId`. Both go
// out so a facilitator's query finds either, and the whole risk of that is the
// two drifting — a join that silently returns nothing because one hop wrote a
// value the other did not.
//
// Two assertions against two hard-coded strings would pass with the projection
// reading a different field for each. Comparing them to one another is what
// cannot: they come from one value at one call site, and this says so.
func TestTheCrossLayerAliasesCarryTheSameValueAsTheBecknPair(t *testing.T) {
	trace, recorder := tracing(t)

	const discovering = `{"context":{"action":"discover","transactionId":"6d1f0d2a-7c11",` +
		`"messageId":"2f6b3f7e-4c1a"},"message":{"intent":{}}}`

	request := httptest.NewRequest(http.MethodPost, "/discover", strings.NewReader(discovering))
	traced(t, request, []func(http.Handler) http.Handler{
		trace, RequestLogger, Envelope(config.Errors{}, roomy),
	}, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	span := only(t, recorder.Spans())
	for _, pair := range []struct{ ours, theirs string }{
		{fact.Of(fact.BecknTransactionID).SpanKey, "transaction_id"},
		{fact.Of(fact.BecknMessageID).SpanKey, "message_id"},
	} {
		ours, present := span.Attributes[pair.ours]
		if !present || ours == "" {
			t.Fatalf("%s is absent; there is nothing for %s to agree with", pair.ours, pair.theirs)
		}
		if theirs := span.Attributes[pair.theirs]; theirs != ours {
			t.Errorf("%s = %v but %s = %v; the alias has drifted from the value it aliases",
				pair.theirs, theirs, pair.ours, ours)
		}
	}
}

// TestNoReceiverIdAliasIsEmitted is the third alias that must not exist.
//
// There are two, and the temptation to add a `receiver.id` beside them is the
// symmetry argument: transactionId and messageId got one, so receiverId should.
// It should not. onix has no such attribute for a collector to key on, so the
// alias would join to nothing — and it would sit one underscore away from
// `recipient.id`, which Trace writes from APP_SUBSCRIBER_ID and which means
// something else entirely: who this service IS, not who the caller addressed.
// Two keys that differ by a synonym and disagree on every misrouted request is
// exactly the pair somebody debugging a misroute would read the wrong one of.
//
// Asserted over the whole exported key set rather than by naming one spelling,
// because the mistake is a plausible spelling and not a specific one.
func TestNoReceiverIdAliasIsEmitted(t *testing.T) {
	trace, recorder := tracing(t)

	const addressed = `{"context":{"action":"discover","transactionId":"a3f0","messageId":"2f6b",` +
		`"receiverId":"somebody.else.example.org"},"message":{"intent":{}}}`

	request := httptest.NewRequest(http.MethodPost, "/discover", strings.NewReader(addressed))
	traced(t, request, []func(http.Handler) http.Handler{
		trace, RequestLogger, Envelope(config.Errors{}, roomy),
	}, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	span := only(t, recorder.Spans())
	if _, present := span.Attributes[fact.Of(fact.BecknReceiverID).SpanKey]; !present {
		t.Fatalf("beckn.receiverId itself is absent, so this test would pass vacuously")
	}
	for key := range span.Attributes {
		if strings.Contains(key, "receiver") && key != fact.Of(fact.BecknReceiverID).SpanKey {
			t.Errorf("the span carries %q; beckn.receiverId is the only receiver attribute", key)
		}
	}
}
