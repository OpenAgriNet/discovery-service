package telemetry

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel/propagation"
)

// propagator is the one W3C Trace Context propagator this service has.
//
// A package value rather than otel.SetTextMapPropagator, for the same reason
// 23a declined otel.SetTracerProvider: a global is set by whoever imports the
// package and is visible to every test in the binary, so a test that needs a
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
