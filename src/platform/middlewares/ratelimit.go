package middlewares

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
)

// rateLimitedMessage names the remedy and nothing else: how much allowance this
// deployment grants, or how much of it the caller has spent, is not something a
// refusal should teach an unauthenticated caller to probe for.
const rateLimitedMessage = "too many requests; retry after the interval in Retry-After"

// bucket is one caller's allowance, and when that was last true. Tokens are a
// float because the refill is continuous: rounding a third of a second's worth
// to zero would hand every sub-second caller a lower limit than the configured
// one.
type bucket struct {
	tokens float64
	last   time.Time
}

// limiter is the token-bucket state behind RateLimit.
//
// Keyed on the REMOTE ADDRESS and never on context.senderId, which is what A4
// says: until a signature is verified that field is a string the caller chose,
// so keying on it would let any caller exhaust a named third party's bucket by
// claiming their id. The departure and the task that ends it are
// implementation-plan.md Task 8.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	// Requests per second, and the ceiling the bucket refills to.
	rps   float64
	burst float64

	// How long a bucket may sit untouched before it is dropped, and when the
	// last sweep ran.
	horizon time.Duration
	swept   time.Time

	// The clock, injected so eviction is testable without waiting for it.
	now func() time.Time
}

// newLimiter builds the bucket map. Takes the clock RateLimit does not, which is
// the seam the eviction test drives.
func newLimiter(cfg config.RateLimit, now func() time.Time) *limiter {
	rps := float64(cfg.RPS)
	burst := float64(cfg.Burst)

	return &limiter{
		buckets: map[string]*bucket{},
		rps:     rps,
		burst:   burst,
		// The time an empty bucket takes to refill to full, past which a bucket
		// holds exactly what a new one would — which is what makes eviction
		// unobservable rather than a second, hidden allowance. Shortening it
		// breaks that.
		horizon: time.Duration(burst / rps * float64(time.Second)),
		swept:   now(),
		now:     now,
	}
}

// allow spends one token from key's bucket, reporting whether there was one and,
// when there was not, how long until there is.
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	at := l.now()
	l.sweep(at)

	held, found := l.buckets[key]
	if !found {
		held = &bucket{tokens: l.burst, last: at}
		l.buckets[key] = held
	} else {
		held.tokens = min(l.burst, held.tokens+at.Sub(held.last).Seconds()*l.rps)
		held.last = at
	}

	if held.tokens < 1 {
		return false, time.Duration((1 - held.tokens) / l.rps * float64(time.Second))
	}
	held.tokens--
	return true, 0
}

// sweep drops the buckets nothing has touched for a horizon, at most once per
// horizon. Called under the lock.
func (l *limiter) sweep(at time.Time) {
	if at.Sub(l.swept) < l.horizon {
		return
	}
	l.swept = at

	for key, held := range l.buckets {
		if at.Sub(held.last) >= l.horizon {
			delete(l.buckets, key)
		}
	}
}

// RateLimit refuses a caller who is over their allowance with 429, Retry-After
// and AUT_RATE_LIMITED (A4).
//
// It sits below Envelope, so the id it echoes is the parsed one rather than
// C13's salvage, and the bytes it refuses are already bounded by Envelope's
// ceiling — which is why there is no second bound here (C14).
func RateLimit(cfg config.RateLimit, errs config.Errors) func(http.Handler) http.Handler {
	limiter := newLimiter(cfg, time.Now)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			allowed, retryAfter := limiter.allow(callerKey(r.RemoteAddr))
			if !allowed {
				// apperrors.RateLimited rather than a bare Auth fault: the
				// constructor carries the back-off, so the interval cannot be
				// forgotten between building the fault and writing it.
				httpx.WriteNack(r.Context(), w, errs, parsedMessageID(r),
					apperrors.RateLimited(retryAfter, rateLimitedMessage))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// callerKey reduces a remote address to the peer it identifies.
//
// The port is dropped: every connection brings a fresh ephemeral one, so a key
// including it would hand each request its own full bucket. X-Forwarded-For is
// deliberately NOT consulted — a header the caller controls mints buckets on
// demand, and there is no trusted-proxy list here that would make it safe.
//
// An address with no port is used as it stands, rather than refusing to limit a
// caller whose address surprised us.
func callerKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// parsedMessageID returns the id Envelope parsed, or empty where it did not run.
// Empty rather than minted: C13 refuses an id the caller never sent, which would
// look like an answer and correlate to nothing.
func parsedMessageID(r *http.Request) string {
	envelope, ok := EnvelopeFromContext(r.Context())
	if !ok {
		return ""
	}
	return envelope.Context.MessageID
}
