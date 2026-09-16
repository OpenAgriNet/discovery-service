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

// HeaderChain records which links of the chain ran, in the order they ran
// (implementation-plan.md Task 8). Recover is the only link that stamps it:
// every other middleware is placed by a side effect it already has, and a
// marker for one of those would be a second thing to keep true.
const HeaderChain = "X-Beckn-Chain"

// chainRecover is the one entry. Spelled once, because recover_test.go reads the
// header by name and a second spelling is an entry that silently never matches.
const chainRecover = "recover"

// committer answers the one question Recover cannot answer for itself: has the
// response already gone? RequestLogger's recorder implements it and sits
// directly above. A Recover mounted alone gets no answer and writes, which is
// right — alone it is the only thing that could have written.
type committer interface{ committed() bool }

// Recover turns a panic below it into a 500 and keeps the stack trace out of the
// response.
//
// The trace is not discarded, it is moved: it rides on the error handed to
// httpx.WriteNack, which logs the original and puts only the coerced fault's
// fixed message in the body. Nothing is logged here as well, which would be a
// second account of one fault — except on the committed-response path, where
// WriteNack is never reached. See abort.
//
// No message id is echoed: Recover sits above Envelope, so nothing has parsed
// one yet, and C13 refuses to mint one.
func Recover(cfg config.Errors) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Before next, and on every request rather than only the ones
			// caught: a marker that appeared only on a panicking route would
			// leave the ordinary route's order unobservable.
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
// debug.Stack is captured HERE, while the panicking goroutine's stack is still
// unwinding; anywhere else it is this middleware's own stack and names nothing
// that failed. And %v, never %w: wrapping would let a panicked *AppError be
// found by errors.As and answer with its own status, handing the panicking value
// a say in the response.
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

// abort gives up on a response that has already partly gone, by re-panicking
// with http.ErrAbortHandler: net/http drops the connection, which is the honest
// answer once bytes are on the wire (A11, implementation-plan.md Task 8).
//
// The one path that logs here rather than through WriteNack, and it is the only
// account of the fault rather than a second one — the writer that would have
// made the first is never reached.
func abort(r *http.Request, fault error) {
	logger.FromContext(r.Context()).Error("recovered panic after the response was committed", zap.Error(fault))
	panic(http.ErrAbortHandler)
}
