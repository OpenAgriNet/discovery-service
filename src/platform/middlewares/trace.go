package middlewares

import "net/http"

// HeaderChain records which links of the chain ran, in the order they ran.
//
// Header().Add appends, so Values(HeaderChain) reads back as insertion order —
// which is what makes the chain's *order* assertable rather than merely its
// membership. Only the links whose ordering nothing else observes stamp here:
// every other middleware is placed by a side effect it already has (a header, a
// context value, a status), and a marker for one of those would be a second
// thing to keep true.
const HeaderChain = "X-Beckn-Chain"

// The entries. Spelled once, because the header is read by name in Task 20's
// order assertion and a second spelling is an entry that silently never matches.
const (
	chainTrace   = "trace"
	chainRecover = "recover"
)

// Trace is the tracing slot in the chain: a pass-through today, and the place
// Task 23 starts the span.
//
// It is a pass-through with a side effect rather than a bare pass-through. The
// chain entry exists purely so Task 20's order test has something to observe at
// this slot — a link with no side effect is the one link no order test can
// place. The request itself goes through untouched, which is what a test
// asserting on the request the handler below receives pins.
//
// Task 23 replaces this body with a hand-rolled span and drops the entry,
// moving the order assertion to the span. NOT otelhttp: the network telemetry
// spec requires scope.name/scope.version on every exported batch, and the
// instrumentation scope is fixed when the span is created, so a span otelhttp
// started would carry that package's scope for ever (A23, ADR-0011). The
// exported signature does not change, so the chain Task 20 wires does not move
// when that lands.
func Trace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Before next, not after: Recover writes its 500 from a deferred
		// function, so an entry stamped on the way back out would be stamped
		// after the response had already gone.
		w.Header().Add(HeaderChain, chainTrace)
		next.ServeHTTP(w, r)
	})
}
