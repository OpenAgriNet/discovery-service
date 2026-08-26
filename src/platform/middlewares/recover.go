package middlewares

import (
	"fmt"
	"net/http"
	"runtime/debug"

	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
)

// committer answers the one question Recover cannot answer for itself: has the
// response already gone? RequestLogger's recorder implements it and sits
// directly above, so in the assembled chain the answer is always available; a
// Recover mounted alone gets no answer and writes, which is right, because
// alone it is the only thing that could have written.
type committer interface{ committed() bool }

// Recover turns a panic below it into a 500 and keeps the stack trace out of the
// response.
//
// The trace is not discarded, it is moved: it rides on the error handed to
// httpx.WriteNack, which logs the original and puts only the coerced fault's
// fixed message in the body. So the operator gets the trace, the caller gets
// "internal error", and there is still exactly one place that decides what a
// fault looks like on the wire. A second log line here would be a second
// account of one fault, and the useful half of it is already in the first —
// the one exception being the committed-response path, where WriteNack is never
// reached and so never makes the first account. See abort.
//
// It takes config.Errors because it answers the request itself, like every other
// middleware that rejects: the body goes out through WriteNack, which shapes it
// from that config (C1). A 500 assembled here instead would be the one error
// body in the service that ignores ERROR_INCLUDE_LEGACY_TYPE.
//
// No message id is echoed. Recover sits above Envelope, so the context it holds
// is the one from before the body was parsed and there is no id in it to echo —
// and C13 refuses to mint one, because an id the caller never sent looks like an
// answer and correlates to nothing.
func Recover(cfg config.Errors) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Before next, and on every request rather than only on the ones
			// caught: this entry is what places Recover against Trace in the
			// chain, and a marker that appeared only on a panicking route would
			// leave the ordinary route's order unobservable. See HeaderChain.
			w.Header().Add(HeaderChain, chainRecover)

			// answer is the deferred function itself rather than a closure
			// around one, because recover() reports a panic only to a function
			// deferred directly — one frame further in and it returns nil.
			defer answer(w, r, cfg)

			next.ServeHTTP(w, r)
		})
	}
}

// answer turns a panic into the response. Deferred by Recover, never called
// directly.
//
// debug.Stack is captured here, while the panicking goroutine's stack is still
// unwinding. Captured anywhere else it is this middleware's own stack and names
// nothing that failed.
//
// %v, never %w: a panic is a bug in this service, so it is a 500 whatever it
// was carrying. Wrapping would let a panicked *AppError be found by errors.As
// and answer with its own status, handing the panicking value a say in the
// response.
func answer(w http.ResponseWriter, r *http.Request, cfg config.Errors) {
	panicked := recover()
	if panicked == nil {
		return
	}
	fault := fmt.Errorf("recovered panic: %v\n%s", panicked, debug.Stack())

	// Written as if/else rather than a guard because abort does not return —
	// it re-panics — and a bare call followed by a fall-through reads like one
	// that does, which is exactly the misreading that would put two response
	// bodies on one connection.
	if recorder, ok := w.(committer); ok && recorder.committed() {
		abort(r, fault)
	} else {
		httpx.WriteNack(r.Context(), w, cfg, "", fault)
	}
}

// abort gives up on a response that has already partly gone.
//
// The status line is sent and bytes are on the wire, so there is no second
// response to write: a WriteNack here would append a NACK document to a
// half-written body and leave it under whatever status was already claimed — a
// 200 carrying two documents, neither of them valid, and a caller with no way
// to tell. What is true at this point is that the response is incomplete, and
// http.ErrAbortHandler is how net/http is told to drop the connection and say
// so without printing a stack of its own. A truncated body served as a clean
// 200 is the worse failure, because it is the one the caller cannot detect.
//
// This is the one path that logs here rather than through WriteNack, and it is
// not a second account of the fault: it is the only account, because the writer
// that would have made one is never reached.
func abort(r *http.Request, fault error) {
	logger.FromContext(r.Context()).Error("recovered panic after the response was committed", zap.Error(fault))
	panic(http.ErrAbortHandler)
}
