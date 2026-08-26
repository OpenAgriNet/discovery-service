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

// rateLimitedMessage is what the refused caller is told. It names the remedy and
// nothing else: the interval is in Retry-After, and how much allowance this
// deployment grants, or how much of it the caller has spent, is not something a
// refusal should teach an unauthenticated caller to probe for.
const rateLimitedMessage = "too many requests; retry after the interval in Retry-After"

// bucket is one caller's allowance: how many requests they may make now, and
// when that was last true.
//
// The tokens are a float because the refill is continuous — a caller who waited
// a third of a second at 20 rps has earned two thirds of a request, and rounding
// that to zero would hand every sub-second caller a limit lower than the one
// configured.
type bucket struct {
	tokens float64
	last   time.Time
}

// limiter is the token-bucket state behind RateLimit.
//
// It is keyed on the caller's remote address. **Not on `context.bapId`**, which
// is what A4 says, and the departure is deliberate: until a signature is
// verified that field is a string the caller chose. Keying on it would let any
// caller shed their own limit by rotating the field, and — the worse half — let
// any caller exhaust a *named third party's* bucket by claiming their id, which
// turns the protection into the attack. The key moves to the subscriber id in
// the task that verifies the signature, because that is the point at which the
// id stops being a claim and becomes an identity. It is recorded in the plan's
// Deferred section, not here, so someone deciding scope finds it.
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

	// The clock, injected so eviction is testable without waiting for it. The
	// exported constructor passes time.Now; nothing reads a package-level clock.
	now func() time.Time
}

// newLimiter builds the bucket map. Unexported, and takes the clock RateLimit
// does not, which is the seam the eviction test drives.
func newLimiter(cfg config.RateLimit, now func() time.Time) *limiter {
	rps := float64(cfg.RPS)
	burst := float64(cfg.Burst)

	return &limiter{
		buckets: map[string]*bucket{},
		rps:     rps,
		burst:   burst,
		// The time it takes an empty bucket to refill to full. Past that a
		// bucket holds exactly what a new one would, so dropping it changes
		// nothing a caller can observe — which is what makes eviction safe
		// rather than a second, hidden allowance. Derived rather than
		// configured: a knob no scenario sets is not shipped, and this one has
		// exactly one correct value given the other two.
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
//
// Amortised rather than on every request: a walk of the whole map per request is
// O(callers) on the hot path, and the thing being prevented is unbounded growth
// over hours, not a transient. A background goroutine would be the other answer
// and is worse — it is a second lifetime to manage and something to shut down,
// for a map that is only ever read here.
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

// RateLimit refuses a caller who is over their allowance with 429,
// Retry-After and AUT_RATE_LIMITED (A4).
//
// It takes config.Errors beside its own knobs like every other middleware that
// rejects: the refusal goes out through httpx.WriteNack, which shapes the body
// from that config (C1).
//
// It sits below Envelope, so the id it echoes is the one Envelope parsed rather
// than C13's salvage out of a body that would not parse — and the bytes it is
// refusing have already been bounded by Envelope's ceiling, which is why there
// is no second bound here (C14).
func RateLimit(cfg config.RateLimit, errs config.Errors) func(http.Handler) http.Handler {
	limiter := newLimiter(cfg, time.Now)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			allowed, retryAfter := limiter.allow(callerKey(r.RemoteAddr))
			if !allowed {
				// apperrors.RateLimited rather than a bare Auth fault: it is
				// the constructor that carries the back-off, so the interval
				// cannot be forgotten between building the fault and writing
				// it.
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
// The port is dropped, and that is the whole of the function: every connection
// brings a fresh ephemeral port, so a key including it would hand each request
// its own full bucket and the limiter would refuse nothing. X-Forwarded-For is
// not consulted — there is no trusted-proxy list that would make it safe, and a
// header the caller controls is a header that mints buckets on demand. A
// deployment behind a proxy must therefore hand this service the real peer
// address.
//
// An address with no port is used as it stands: net.SplitHostPort fails on one,
// and refusing to limit a caller because their address surprised us is the wrong
// way to fail.
func callerKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// parsedMessageID returns the id Envelope parsed, or empty where it did not run.
// Empty rather than minted: C13 refuses an id the caller never sent, because it
// looks like an answer and correlates to nothing.
func parsedMessageID(r *http.Request) string {
	envelope, ok := EnvelopeFromContext(r.Context())
	if !ok {
		return ""
	}
	return envelope.Context.MessageID
}
