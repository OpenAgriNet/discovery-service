package app

import (
	"context"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
	"github.com/OpenAgriNet/discovery-service/src/platform/middlewares"
)

// readinessTimeout bounds the one question /readyz asks.
//
// A pool whose backend has gone away answers Ping by blocking until its own dial
// timeout, so an unbounded probe is read as a failure only after the KUBELET's
// timeout — marking the pod unready seconds later than the truth, on the path
// that exists to report it promptly.
const readinessTimeout = 2 * time.Second

// readinessCacheTTL is how long one ping's answer stands for every probe that
// arrives behind it.
//
// /readyz carries no rate limit (see probes), which leaves the pool as the thing
// an anonymous caller can amplify into: one acquire per GET, from the same bounded
// pool that serves real traffic. Sharing the answer caps a flood at one ping per
// window. A second is invisible to a kubelet, and it is the whole of the staleness
// admitted.
const readinessCacheTTL = time.Second

// readiness is the shared answer. Its zero value is ready to use, and it must not
// be copied — which is why App holds it by value and App is only ever passed as a
// pointer.
type readiness struct {
	mu      sync.Mutex
	checked time.Time
	err     error
}

// check returns the current answer, asking the database for a new one only when
// the last has expired.
//
// The lock is held across the ping on purpose: at most one probe is ever inside
// the pool, so a flood queues on a mutex rather than on the pool. now is a
// parameter because an expiry a test cannot advance is one no test can observe.
func (r *readiness) check(ctx context.Context, db Pinger, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.checked.IsZero() && now.Sub(r.checked) < readinessCacheTTL {
		return r.err
	}
	r.err = db.Ping(ctx)
	r.checked = now
	return r.err
}

// probe is what /healthz and /readyz answer with. A body rather than a bare status
// because an operator reading a curl by hand should not have to look up what 503
// meant here.
type probe struct {
	Status string `json:"status"`
}

// NewRouter is the route table, and it is deliberately the whole of it.
//
// Four routes and no wildcard. A wildcard mount would send every unmatched path
// through the chain to be refused by a validator, turning `/catalog/publish` — the
// alias C2 says does not exist — into a 400 about its body rather than a 404 about
// its path.
func NewRouter(a *App) http.Handler {
	protocol := chain(a)(protocolRoutes(a))

	mux := http.NewServeMux()

	// Named here as well as inside protocolRoutes, and that is the point: this mux
	// decides what is a route at all, the inner one which handler serves it. The
	// inner table is the controllers' own Register calls, so neither this file nor
	// a controller alone can add a route by accident.
	mux.Handle("POST /publish", protocol)
	mux.Handle("POST /discover", protocol)

	mux.Handle("GET /healthz", probes(a)(http.HandlerFunc(healthz)))
	mux.Handle("GET /readyz", probes(a)(http.HandlerFunc(a.readyz)))

	return mux
}

// protocolRoutes is the inner table, built by the controllers themselves.
func protocolRoutes(a *App) http.Handler {
	mux := http.NewServeMux()
	a.Publish.Register(mux)
	a.Discover.Register(mux)
	return mux
}

// chain is the middleware order fixed by the plan, outermost first:
//
//	RequestID → Trace → RequestLogger → Recover → Envelope
//	          → RateLimit → [Signature] → SchemaValidator → controller
//
// `Signature` is NOT mounted: it is parked with Task 6, and nothing stands in its
// slot — not even a pass-through. A link that stamps a marker while doing nothing
// makes an absent security control observable as a present one.
//
// `Recover` is inside `RequestLogger` rather than outside it (A11): the 500 a
// recovered panic produces has to leave through RequestLogger's response wrapper,
// or every count of requests by status under-reports exactly the failures. The
// cost — an uncaught panic in RequestID, Trace or RequestLogger itself — is the
// right one, since net/http's own recovery is where that belongs.
//
// A slice applied in reverse rather than nested calls, so it reads in the order
// the plan states it.
func chain(a *App) func(http.Handler) http.Handler {
	links := []func(http.Handler) http.Handler{
		middlewares.RequestID(a.Log),
		middlewares.Trace(a.Telemetry.Tracer(), a.Config.App.Subscriber),
		middlewares.RequestLogger,
		middlewares.Recover(a.Config.Errors),
		middlewares.Envelope(a.Config.Errors, a.Config.Server.MaxRequestBodyBytes),
		middlewares.RateLimit(a.Config.RateLimit, a.Config.Errors),
		middlewares.SchemaValidator(a.Config.Errors, a.Config.Validation, a.Spec),
	}
	return apply(links)
}

// probes is the chain the two health routes get, and it is short on purpose.
//
// No Envelope, RateLimit or SchemaValidator: a probe carries no Beckn envelope, so
// every one of them would refuse it. Rate limiting is the pointed omission —
// shedding a kubelet's probe is how a healthy pod gets restarted by the mechanism
// meant to notice it was healthy.
//
// No RequestLogger either: a probe every few seconds for the life of the pod is
// noise an operator has to read past to find the requests.
//
// Recover stays, because a panic in a probe is still a panic, and RequestID stays
// because Recover logs a stack worth correlating.
func probes(a *App) func(http.Handler) http.Handler {
	return apply([]func(http.Handler) http.Handler{
		middlewares.RequestID(a.Log),
		middlewares.Recover(a.Config.Errors),
	})
}

// apply nests links so that links[0] is outermost.
func apply(links []func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		for i := len(links) - 1; i >= 0; i-- {
			next = links[i](next)
		}
		return next
	}
}

// healthz is liveness, and it depends on nothing.
//
// It must not ping the database: a liveness probe that fails when the datastore
// does gets the container restarted, which cannot fix a datastore. The question is
// "can this process still run a handler", and returning is the whole answer.
func healthz(w http.ResponseWriter, r *http.Request) {
	write(w, r, http.StatusOK, "ok")
}

// readyz is readiness, and it is the one that asks.
//
// A process whose pool cannot reach PostgreSQL can answer no request this service
// serves, so it leaves the load balancer — and returns when the ping succeeds,
// without a restart. That is the difference between the two probes.
func (a *App) readyz(w http.ResponseWriter, r *http.Request) {
	// WithoutCancel, because the answer is shared with every probe in this window:
	// a caller that hangs up mid-ping says nothing about the database, and letting
	// its cancellation become the cached result would report a healthy service
	// unready. readinessTimeout bounds this instead.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), readinessTimeout)
	defer cancel()

	if err := a.ready.check(ctx, a.DB, time.Now()); err != nil {
		logger.FromContext(r.Context()).Warn("readiness probe failed", zap.Error(err))
		write(w, r, http.StatusServiceUnavailable, "unready")
		return
	}
	write(w, r, http.StatusOK, "ready")
}

// write answers a probe. The encode cannot fail — probe is one string field — so
// a failure here is logged rather than turned into a second error body written to
// a response already committed.
func write(w http.ResponseWriter, r *http.Request, status int, state string) {
	if err := httpx.WriteJSON(r.Context(), w, status, probe{Status: state}); err != nil {
		logger.FromContext(r.Context()).Error("write the probe response", zap.Error(err))
	}
}
