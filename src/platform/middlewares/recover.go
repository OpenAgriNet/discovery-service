package middlewares

import (
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
)

// Recover turns a panic below it into a 500 and keeps the stack trace out of the
// response.
//
// The trace is not discarded, it is moved: it rides on the error handed to
// httpx.WriteNack, which logs the original and puts only the coerced fault's
// fixed message in the body. So the operator gets the trace, the caller gets
// "internal error", and there is still exactly one place that decides what a
// fault looks like on the wire. A second log line here would be a second
// account of one fault, and the useful half of it is already in the first.
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

			defer func() {
				panicked := recover()
				if panicked == nil {
					return
				}
				// debug.Stack is captured here, inside the deferred function,
				// which is the last frame that still has the panicking
				// goroutine's stack. Captured anywhere else it is this
				// middleware's own stack and names nothing that failed.
				httpx.WriteNack(r.Context(), w, cfg, "",
					fmt.Errorf("recovered panic: %v\n%s", panicked, debug.Stack()))
			}()

			next.ServeHTTP(w, r)
		})
	}
}
