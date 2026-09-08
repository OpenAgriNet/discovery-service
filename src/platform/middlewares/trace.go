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

// Trace is the tracing slot in the chain. It allocates the request's fact
// record, and 23c starts the span here.
//
// The record reaches the chain HERE rather than one link down in RequestLogger,
// which is where it used to be allocated as middlewares.correlation. The reason
// is ordering: Trace is above RequestLogger, so a span started here cannot read
// attributes off a record that link below it allocates. Doing it here costs
// nothing — RequestLogger adopts what it finds, see recordFor — and it means the
// span, the completion line and 23e's metrics read one record per request
// instead of one each.
//
// Unconditionally with respect to configuration, including under
// OTEL_EXPORTER=none. Trace takes none today and recordFor asks about none
// deliberately: a record whose lifetime depended on an environment variable
// would make 23b's whole invariant untestable in the configuration `make test`
// runs in, and would fail only where nobody is looking. The record is one struct
// and a slice that grows to about twenty entries — cheaper than the span the
// same request may not start.
//
// The chain entry exists so Task 20's order test has something to observe at
// this slot; 23c drops it and moves the order assertion to the span. NOT
// otelhttp: the network telemetry spec requires scope.name/scope.version on
// every exported batch, and the instrumentation scope is fixed when the span is
// created, so a span otelhttp started would carry that package's scope for ever
// (A23, ADR-0011). The exported signature does not change, so the chain Task 20
// wires does not move when that lands.
func Trace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Before next, not after: Recover writes its 500 from a deferred
		// function, so an entry stamped on the way back out would be stamped
		// after the response had already gone.
		w.Header().Add(HeaderChain, chainTrace)

		ctx, _ := recordFor(r.Context())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
