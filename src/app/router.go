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
// A pool whose backend has gone away answers Ping by blocking until its own
// dial timeout, and a readiness probe that hangs is read by a kubelet as a
// failure only after ITS timeout — so the pod is marked unready several seconds
// later than the truth, on the exact path that exists to report the truth
// promptly. Two seconds is longer than a healthy ping by three orders of
// magnitude and shorter than every default probe timeout.
const readinessTimeout = 2 * time.Second

// readinessCacheTTL is how long one ping's answer stands for every probe that
// arrives behind it.
//
// /readyz carries no rate limit, deliberately — shedding a kubelet's probe is
// how a healthy pod gets restarted by the mechanism meant to notice it was
// healthy. That leaves the pool as the thing an anonymous caller can amplify
// into: one acquire per GET, from the same bounded pool that serves real
// traffic, until there is nothing left for it. Sharing the answer caps the cost
// of a flood at one ping per window instead of one per request.
//
// A second is invisible to the mechanism this endpoint exists for — a kubelet
// probes every few seconds and requires several consecutive answers before it
// acts — and it is the whole of the staleness admitted.
const readinessCacheTTL = time.Second

// readiness is the shared answer. Its zero value is ready to use, and it must
// not be copied, which is why App holds it by value and App is only ever passed
// as a pointer.
type readiness struct {
	mu      sync.Mutex
	checked time.Time
	err     error
}

// check returns the current answer, asking the database for a new one only when
// the last has expired.
//
// The lock is held across the ping on purpose. It means at most one probe is
// ever inside the pool, so a flood queues on a mutex — bounded, local, and
// costing no connection — rather than on the pool itself. now is a parameter
// because an expiry that cannot be advanced by a test is an expiry no test can
// observe.
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

// probe is what /healthz and /readyz answer with. A body rather than a bare
// status because an operator reading a curl by hand should not have to look up
// what 503 meant here.
type probe struct {
	Status string `json:"status"`
}

// NewRouter is the route table, and it is deliberately the whole of it.
//
// Four routes and no wildcard. A wildcard mount would send every unmatched path
// through the chain to be refused by a validator, which turns `/catalog/publish`
// — the alias C2 says does not exist — into a 400 about its body rather than a
// 404 about its path. The alias must read as absent, because a caller told
// "your envelope is wrong" will go on believing the route is there.
func NewRouter(a *App) http.Handler {
	protocol := chain(a)(protocolRoutes(a))

	mux := http.NewServeMux()

	// Named here as well as inside protocolRoutes, and that is the point: this
	// mux decides what is a route at all, the inner one decides which handler
	// serves it. The inner table is the controllers' own — the same Register
	// calls their tests drive — so neither this file nor a controller is the
	// single place a route can be added by accident.
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
// `Signature` is NOT mounted. It is parked with Task 6 and does not exist, and
// nothing stands in its slot — not even a pass-through. A link that stamps a
// marker while doing nothing makes an absent security control observable as a
// present one, which is a worse failure than the absence it papers over.
//
// `Recover` is inside `RequestLogger` rather than outside it (A11): the 500 a
// recovered panic produces has to leave through RequestLogger's response
// wrapper, or the one request an operator most needs timed is the one that logs
// nothing and every count of requests by status under-reports exactly the
// failures. The cost is that a panic in RequestID, Trace or RequestLogger
// itself is uncaught — the right cost, because a panic in this repository's own
// logging is a bug in this repository, and net/http's own recovery is where
// that belongs.
//
// Written as a slice applied in reverse rather than as nested calls because the
// slice reads in the order the plan states it, and this list is a contract that
// tests assert against by observing side effects.
func chain(a *App) func(http.Handler) http.Handler {
	links := []func(http.Handler) http.Handler{
		middlewares.RequestID(a.Log),
		middlewares.Trace,
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
// No Envelope, RateLimit or SchemaValidator: a probe carries no Beckn envelope,
// so every one of them would refuse it. Rate limiting is the pointed omission —
// a kubelet probing on a fixed interval shares one source address with nothing,
// and shedding its probe is how a healthy pod gets restarted by the mechanism
// meant to notice it was healthy.
//
// No RequestLogger either. A probe every few seconds for the life of the pod is
// the noise an operator has to read past to find the requests, and neither
// route does anything a log line would explain.
//
// Recover stays, because a panic in a probe is still a panic, and RequestID
// stays because Recover logs a stack that is worth correlating.
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
// It must not ping the database. A liveness probe that fails when the datastore
// does gets the container killed and restarted, which cannot fix a datastore
// and removes the one process that could still serve a cached answer or report
// the outage. The question it answers is "is this process still able to run a
// handler", and returning is the whole of the answer.
func healthz(w http.ResponseWriter, r *http.Request) {
	write(w, r, http.StatusOK, "ok")
}

// readyz is readiness, and it is the one that asks.
//
// A process whose pool cannot reach PostgreSQL can answer no request this
// service serves, so it should be taken out of the load balancer — and put back
// when the ping succeeds again, without a restart. That is exactly the
// difference between the two probes.
func (a *App) readyz(w http.ResponseWriter, r *http.Request) {
	// WithoutCancel, because the answer is shared with every probe in this
	// window: a caller that hangs up mid-ping says nothing about the database,
	// and letting its cancellation become the cached result would report a
	// healthy service unready until the window closed. readinessTimeout is what
	// bounds this instead.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), readinessTimeout)
	defer cancel()

	if err := a.ready.check(ctx, a.DB, time.Now()); err != nil {
		logger.FromContext(r.Context()).Warn("readiness probe failed", zap.Error(err))
		write(w, r, http.StatusServiceUnavailable, "unready")
		return
	}
	write(w, r, http.StatusOK, "ready")
}

// write answers a probe. The encode cannot fail — probe is one string field —
// so the error is dropped here rather than turned into a second error body that
// would itself have to be written to a response already committed.
func write(w http.ResponseWriter, r *http.Request, status int, state string) {
	if err := httpx.WriteJSON(r.Context(), w, status, probe{Status: state}); err != nil {
		logger.FromContext(r.Context()).Error("write the probe response", zap.Error(err))
	}
}
