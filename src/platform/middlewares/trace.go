package middlewares

import (
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// Trace is the tracing slot in the chain: it joins the caller's trace, allocates
// the request's fact record, starts the server span and — at the end, from a
// deferred function — projects the record onto it.
//
// It takes the tracer rather than obtaining one, and that is the whole of A23.
// The instrumentation scope is fixed when the tracer is obtained and cannot be
// set at span creation, so a span started by otelhttp would carry that package's
// scope for ever; scope.name and scope.version are Required on every exported
// batch in the network telemetry spec, so that is a batch a facilitator rejects
// and nothing else about the span looks wrong (ADR-0011). One tracer, obtained
// once in telemetry.Init, passed here.
//
// recipient is this service's own subscriber id, from APP_SUBSCRIBER_ID. It is
// NOT the caller's receiverId, which Envelope observes separately: the two agree
// on every correctly addressed request, which is exactly why merging them would
// be undetectably wrong, and disagreeing means a caller talking to a participant
// that is not us — worth a query, and unaskable if one field held both.
//
// The record reaches the chain HERE rather than one link down in RequestLogger,
// which is where it used to be allocated as middlewares.correlation. The reason
// is ordering: Trace is above RequestLogger, so a span started here cannot read
// attributes off a record that link below it allocates. Doing it here costs
// nothing — RequestLogger adopts what it finds, see recordFor — and it means the
// span, the completion line and everything projected from either read one
// record per request instead of one each.
//
// Unconditionally with respect to configuration, including under
// OTEL_EXPORTER=none. recordFor asks about none deliberately: a record whose
// lifetime depended on an environment variable would make 23b's whole invariant
// untestable in the configuration `make test` runs in, and would fail only where
// nobody is looking. Under `none` the tracer is real and its spans are
// non-recording, so the cost of everything below is a comparison per request.
func Trace(tracer oteltrace.Tracer, recipient string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Join, do not replace. The context that comes back carries the
			// caller's trace id and their span as this one's parent, so the span
			// below lands INSIDE their trace. A hop that starts its own trace is
			// why "the seeker asked and nobody served it" is unanswerable today —
			// the two halves are in different traces and nothing joins them.
			ctx := telemetry.Extract(r.Context(), r.Header)

			ctx, record := recordFor(ctx)

			// Named for the route at Start and renamed for the action at End. The
			// action lives inside the body and Envelope is four links below, so
			// there is nothing better to say yet — and a request whose body never
			// parsed keeps a name a human can read instead of a blank one, which
			// is the population somebody is most often looking at.
			ctx, span := tracer.Start(ctx, r.URL.Path, oteltrace.WithSpanKind(oteltrace.SpanKindServer))

			observeRequest(record, r, recipient)

			// Deferred, and this is not tidiness. Recover's abort path re-panics
			// with http.ErrAbortHandler, which unwinds straight through here: as
			// straight-line code after next.ServeHTTP, none of this would run and
			// the span would leak on exactly the request an operator opens the
			// trace to understand.
			defer complete(span, record)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// observeRequest puts what is knowable before the handler runs onto the record.
//
// Through the record rather than onto the span directly, so there is one place
// the span, the completion line and Task 25's metrics read these from. Setting them
// on the span here as well would be a second write of the same value that is
// free to disagree with the first.
func observeRequest(record *fact.Record, r *http.Request, recipient string) {
	record.ObserveString(fact.HTTPMethod, r.Method)
	record.ObserveString(fact.HTTPHost, r.Host)
	record.ObserveString(fact.HTTPRoute, r.URL.Path)
	record.ObserveString(fact.HTTPScheme, scheme(r))
	record.ObserveString(fact.HTTPFlavor, flavor(r))

	// Omitted rather than written empty when nothing is configured, which is
	// every deployment today: APP_SUBSCRIBER_ID has no default. An empty
	// recipient.id says this service is a participant whose id is the empty
	// string; the absent flag the projection then emits says it was never told,
	// which is true and is the one an operator can act on.
	if recipient != "" {
		record.ObserveString(fact.RecipientID, recipient)
	}
}

// complete finishes the span from Trace's deferred function.
//
// The order is fixed: observe the end time, name it, set the status, project,
// end. Everything before End, because the SDK ignores mutations to an ended
// span — silently, so the attributes simply would not be there.
func complete(span oteltrace.Span, record *fact.Record) {
	// The one fact that cannot be observed anywhere else: the moment the request
	// finished, in unix nanos, which is what the network telemetry spec asks for
	// on every signal and what a facilitator aligns this service's spans with
	// onix's on.
	record.ObserveString(fact.ObservedTimeUnixNano, strconv.FormatInt(time.Now().UnixNano(), 10))

	// By the action, not the route (telemetry-examples.md:83-110). onix names
	// spans by action and some of its hops have no route to name, so two
	// conventions for one concept would make every cross-layer query a union.
	// Envelope has run by now, or the request never parsed and the route set at
	// Start stands.
	if action, found := record.Lookup(fact.BecknAction); found && action.Text != "" {
		span.SetName(action.Text)
	}

	setStatus(span, record)

	// Once, here, rather than as the facts arrive. Attributes set at Start would
	// be set before the body was parsed and there would be nothing to say; set as
	// each link observes, they would be spread across four files and the seam's
	// one-place property would be gone.
	span.SetAttributes(telemetry.SpanAttributes(record)...)

	span.End()
}

// setStatus marks the span an error, or leaves it unset.
//
// 5xx only. The semantic conventions say a SERVER span is an error when the
// SERVER failed, and a 4xx is the caller's fault — counting those would make the
// error-rate panel measure how many malformed requests arrived rather than how
// often this service broke, which is the opposite of what somebody looks at it
// for. The description stays empty: httpx.WriteNack already put the category on
// the record as error_type, and 23d puts the message on the error event.
//
// A request that never reached RequestLogger has no status on the record at all
// — the probes chain, and anything mounting Trace alone — and Unset is the right
// answer there rather than a guess.
func setStatus(span oteltrace.Span, record *fact.Record) {
	status, found := record.Lookup(fact.HTTPStatusCode)
	if !found || status.Int < http.StatusInternalServerError {
		return
	}
	span.SetStatus(codes.Error, "")
}

// scheme reports http or https for the request as it arrived.
//
// TLS terminates at the ingress in every deployment this service has, so
// r.TLS is nil and the honest answer is http — what this process actually
// served. X-Forwarded-Proto is deliberately not read: it is a caller-settable
// header, and a span attribute an unauthenticated caller chooses is one a
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
