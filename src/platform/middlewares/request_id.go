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
// First in the chain, because until it has run logger.FromContext returns the
// no-op logger and a request refused by Envelope is a request with no record
// (implementation-plan.md Task 8).
//
// It MINTS rather than trusts: an inbound X-Request-Id is a value an
// unauthenticated caller chose, and honouring it lets one caller collide two
// requests' log lines. Propagating a gateway's id needs a trusted-proxy list
// first — the same reason RateLimit does not read X-Forwarded-For.
//
// It takes the service logger rather than reading one from the context: this is
// the middleware that puts one there.
func RequestID(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 26 base32 characters from the same source a uuid4 would come
			// from, and no dependency. The id never crosses the protocol, so
			// the spec requires no shape of it — only that it is unguessable
			// and not derived from anything the caller sent.
			id := rand.Text()

			w.Header().Set(HeaderRequestID, id)
			ctx := logger.NewContext(r.Context(), log.With(logger.RequestID(id)))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
