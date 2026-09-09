package middlewares

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// Trace is the tracing slot in the chain: it joins the caller's trace, allocates
// the request's fact record, starts the server span and — at the end, from a
// deferred function — projects the record onto it.
//
// The tracer is PASSED and not obtained here, which is A23: a scope is fixed
// when the tracer is obtained, so a span started by otelhttp would carry that
// package's scope and report its version in scope.version, where the network
// telemetry spec wants the spec's own (ADR-0011, opentelemetry.md §Build identity).
//
// recipient is this service's own subscriber id from APP_SUBSCRIBER_ID, never
// the caller's receiverId, which Envelope observes separately — see correlators.
//
// The record is allocated HERE and not in RequestLogger one link below: Trace
// runs above it, so a span started here could not read a record allocated
// there. RequestLogger adopts what it finds; see recordFor. It runs regardless
// of configuration, including under OTEL_EXPORTER=none, where the tracer is real
// and its spans are non-recording — a record whose lifetime depended on an
// environment variable would make 23b's invariant untestable in the
// configuration `make test` runs in.
func Trace(tracer oteltrace.Tracer, recipient string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Join, do not replace: the caller's span becomes this one's parent,
			// so the span below lands INSIDE their trace. A hop that starts its
			// own leaves the two halves of a transaction unjoinable.
			ctx := telemetry.Extract(r.Context(), r.Header)

			ctx, record := recordFor(ctx)

			// Named for the route now and for the action at End — the action is
			// in a body Envelope has not parsed yet, four links below. A request
			// that never parses keeps this name rather than a blank one.
			ctx, span := tracer.Start(ctx, r.URL.Path, oteltrace.WithSpanKind(oteltrace.SpanKindServer))

			ctx = correlateLog(ctx, span)

			observeRequest(record, r, recipient)

			// Deferred, and not for tidiness: Recover's abort path re-panics
			// with http.ErrAbortHandler, which unwinds straight through here. As
			// straight-line code after next.ServeHTTP this would not run, and
			// the span would leak on exactly the request an operator opened the
			// trace to understand.
			defer complete(span, record)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// correlateLog puts trace_id and span_id on the request-scoped logger, so every
// line written below this point can be reached from the span (23e).
//
// On the LOGGER rather than the record: a record fact would reach the completion
// line and nothing else, and the line an operator following a failed span wants
// is httpx.WriteNack's, written before RequestLogger's deferred block runs.
//
// IsSampled and NOT IsValid. Under OTEL_EXPORTER=none the SDK still mints ids
// for the non-recording span it hands back, so a validity check would put a pair
// of ids on every line that reach no backend — and an id present but unfindable
// reads as a dropped span. Absent, not empty, when the answer is no: an empty
// trace_id is a value, and a query on the field's presence would match
// everything.
func correlateLog(ctx context.Context, span oteltrace.Span) context.Context {
	spanContext := span.SpanContext()
	if !spanContext.IsSampled() {
		return ctx
	}

	return logger.With(ctx,
		logger.TraceID(spanContext.TraceID().String()),
		logger.SpanID(spanContext.SpanID().String()))
}

// observeRequest puts what is knowable before the handler runs onto the record —
// never onto the span directly, which would be a second write of the same value,
// free to disagree with the first.
func observeRequest(record *fact.Record, r *http.Request, recipient string) {
	record.ObserveString(fact.HTTPMethod, r.Method)
	record.ObserveString(fact.HTTPHost, r.Host)
	record.ObserveString(fact.HTTPRoute, r.URL.Path)
	record.ObserveString(fact.HTTPScheme, scheme(r))
	record.ObserveString(fact.HTTPFlavor, flavor(r))

	// Omitted rather than written empty, which is every deployment today —
	// APP_SUBSCRIBER_ID has no default. An empty recipient.id claims an identity;
	// the absent flag the projection emits instead says we were never told.
	if recipient != "" {
		record.ObserveString(fact.RecipientID, recipient)
	}
}

// complete finishes the span from Trace's deferred function. The order is fixed
// — end time, name, status, project, End — and everything must precede End,
// because the SDK ignores mutations to an ended span silently.
func complete(span oteltrace.Span, record *fact.Record) {
	// The one fact observable nowhere else: when the request finished, in unix
	// nanos, which is what a facilitator aligns our spans with onix's on.
	record.ObserveString(fact.ObservedTimeUnixNano, strconv.FormatInt(time.Now().UnixNano(), 10))

	// By the ACTION, not the route (telemetry-examples.md §3. Span): onix names
	// spans by action and some of its hops have no route, so two conventions
	// would make every cross-layer query a union. If Envelope never parsed a
	// body, the route set at Start stands.
	if action, found := record.Lookup(fact.BecknAction); found && action.Text != "" {
		span.SetName(action.Text)
	}

	setStatus(span, record)

	// Once, here, rather than as the facts arrive — which would spread the seam
	// across four files and lose its one-place property.
	span.SetAttributes(telemetry.SpanAttributes(record)...)

	addEvents(span, record)

	span.End()
}

// addEvents puts the point-in-time facts on the span as timestamped events.
//
// WithTimestamp is the whole of this function and the line somebody deletes as
// redundant. Without it the SDK stamps every event at the moment AddEvent is
// called — the span's end — and the phase breakdown these events exist for
// collapses to zeros while everything still renders (opentelemetry.md §Events).
// fact.Observation.Time is stamped where each fact happened for this call.
func addEvents(span oteltrace.Span, record *fact.Record) {
	for _, event := range telemetry.SpanEvents(record) {
		span.AddEvent(event.Name,
			oteltrace.WithTimestamp(event.Time),
			oteltrace.WithAttributes(event.Attributes...))
	}
}

// setStatus marks the span an error, or leaves it unset.
//
// 5xx ONLY (opentelemetry.md §How the derivation happens): a SERVER span is an
// error when the server
// failed, and counting 4xx would make the error-rate panel measure how many
// malformed requests arrived. The description stays empty — WriteNack already put
// the category on the record, and 23d puts the message on the error event.
//
// A request that never reached RequestLogger has no status at all, and Unset is
// the right answer there rather than a guess.
func setStatus(span oteltrace.Span, record *fact.Record) {
	status, found := record.Lookup(fact.HTTPStatusCode)
	if !found || status.Int < http.StatusInternalServerError {
		return
	}
	span.SetStatus(codes.Error, "")
}

// scheme reports http or https for the request as this process served it. TLS
// terminates at the ingress in every deployment here, so r.TLS is nil and http
// is the honest answer. X-Forwarded-Proto is deliberately NOT read: it is
// caller-settable, and an attribute an unauthenticated caller chooses is one a
// dashboard cannot trust.
func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// flavor is the HTTP version, spelled the way the semantic conventions do —
// "1.1", not "HTTP/1.1", which is what r.Proto holds.
func flavor(r *http.Request) string {
	return strconv.Itoa(r.ProtoMajor) + "." + strconv.Itoa(r.ProtoMinor)
}
