package middlewares

import (
	"crypto/rand"
	"net/http"

	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
)

// HeaderRequestID carries this service's own per-request identifier, minted by
// RequestID and echoed so a caller reporting a failure can name the request in
// the log without a timestamp search.
const HeaderRequestID = "X-Request-Id"

// RequestID mints a request id, installs the request-scoped logger carrying it
// and echoes the id as X-Request-Id.
//
// It is first in the chain because it is what makes everything below it
// loggable: until it has run, logger.FromContext returns the no-op logger, so
// httpx.WriteNack's one log line goes nowhere and a request refused by
// Envelope is a request with no record. That is why the chain starts here
// rather than at Trace.
//
// It mints rather than trusts. Phase 1 is unauthenticated, so an inbound
// X-Request-Id is a value the caller chose: honouring it lets one caller
// collide two requests' log lines, or write control characters into a log
// field. Propagating a gateway's id is a Phase 2 decision and it needs a
// trusted-proxy list before it is one — the same reason RateLimit does not read
// X-Forwarded-For.
//
// It takes the service logger rather than reading one from the context: this is
// the middleware that puts one there, so there is nothing to read yet.
func RequestID(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// rand.Text is 26 base32 characters from the same source a uuid4
			// would come from, and no dependency. The id is this service's
			// correlation handle and never crosses the protocol, so it has no
			// shape the spec requires — what it must be is unguessable and
			// unique, and what it must not be is derived from anything the
			// caller sent.
			id := rand.Text()

			w.Header().Set(HeaderRequestID, id)
			ctx := logger.NewContext(r.Context(), log.With(logger.RequestID(id)))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
