package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/OpenAgriNet/discovery-service/src/discover"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/embeddings"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/middlewares"
	"github.com/OpenAgriNet/discovery-service/src/platform/validation"
	"github.com/OpenAgriNet/discovery-service/src/publish"
	"github.com/OpenAgriNet/discovery-service/src/storage/memory"
)

// specFixture is the pinned protocol document, read rather than fetched: a
// router test that reached the network would be testing the network.
const specFixture = "../../tests/testdata/beckn-v2.0.0.yaml"

// A discover request that satisfies both gates the chain puts in front of a
// handler: the C6 envelope rules, which require all five context fields, and
// the L1 pass against the pinned document. The chain tests are about what
// happens INSIDE the handler, so a body either gate would refuse would never
// reach one and the test would pass for the wrong reason.
const validDiscover = `{"context":{"action":"discover","version":"2.0.0",` +
	`"messageId":"2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11",` +
	`"transactionId":"6d1f0d2a-7c11-4a5e-9d3f-2f6b3f7e4c1a",` +
	`"timestamp":"2026-08-26T10:00:00Z"},` +
	`"message":{"intent":{"textSearch":"wheat"}}}`

// deadPool answers every ping with an error, which is what "the database is
// down" means to /readyz.
type deadPool struct{}

func (deadPool) Ping(context.Context) error { return errors.New("no route to host") }

// livePool is the other half. Both exist so the health tests assert a
// difference rather than a constant.
type livePool struct{}

func (livePool) Ping(context.Context) error { return nil }

// testApp wires the same collaborators Build wires, over the in-memory backend
// and a pinned spec, so a router test needs no Postgres and no network.
//
// It builds the App by hand rather than calling Build, because Build's job is
// to open a pool and this file's subject is what sits above one.
func testApp(t *testing.T, db Pinger, log *zap.Logger) *App {
	t.Helper()

	document, err := os.ReadFile(specFixture)
	if err != nil {
		t.Fatalf("read %s: %v", specFixture, err)
	}
	index, err := validation.NewSpecIndex(document)
	if err != nil {
		t.Fatalf("compile the pinned spec: %v", err)
	}

	cfg := config.Config{}
	cfg.App.Network = "mahavistar"
	cfg.Geo.ResolutionCells = 8
	cfg.Search.DefaultPageSize = 20
	cfg.Search.MaxPageSize = 100
	cfg.Search.MaxCandidatesPerMode = 500
	cfg.Search.MaxRadiusMeters = 200000
	cfg.Validation.EnableL1Schema = true
	cfg.Server.MaxRequestBodyBytes = 1 << 20
	cfg.RateLimit.RPS = 1000
	cfg.RateLimit.Burst = 1000

	store := memory.New(cfg.Geo.ResolutionCells)

	return &App{
		Config: cfg,
		Log:    log,
		DB:     db,
		Spec:   index,
		Publish: publish.NewController(
			publish.NewService(store, NoopReplicator{}, embeddings.NewNoop(768),
				cfg.App.Network, time.UTC),
			cfg.Errors),
		Discover: discover.NewController(discover.NewService(store, cfg), cfg.Errors),
	}
}

func request(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	return recorder
}

// The four routes, and the C2 alias that must not exist.
//
// Asserted as "not 404" rather than "200": what this pins is the route table,
// and a body strict enough to satisfy every validator below would make this
// test fail for reasons that are not routing.
func TestTheRouteTableIsTheFourAndTheAliasIsNotAmongThem(t *testing.T) {
	router := NewRouter(testApp(t, livePool{}, zap.NewNop()))

	mounted := []struct {
		method, path string
	}{
		{http.MethodPost, "/publish"},
		{http.MethodPost, "/discover"},
		{http.MethodGet, "/healthz"},
		{http.MethodGet, "/readyz"},
	}
	for _, route := range mounted {
		if got := request(t, router, route.method, route.path, "{}").Code; got == http.StatusNotFound {
			t.Errorf("%s %s = 404, want it mounted", route.method, route.path)
		}
	}

	// C2: the action lives in the body, so there is no second path for it.
	if got := request(t, router, http.MethodPost, "/catalog/publish", "{}").Code; got != http.StatusNotFound {
		t.Errorf("POST /catalog/publish = %d, want 404 — the alias must not reappear", got)
	}
}

// The chain order, proved by the ORDER of the two entries and not by the
// presence of either.
//
// Both are appended before Recover writes its 500, so a test asserting only
// that a marker survived the panic passes under either nesting. Reading
// Values() gives insertion order, which does not.
func TestOnAPanickingRouteTheChainReadsTraceThenRecover(t *testing.T) {
	panics := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("the handler fell over")
	})

	recorder := request(t, chain(testApp(t, livePool{}, zap.NewNop()))(panics), http.MethodPost, "/discover", validDiscover)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — the panic was not recovered", recorder.Code)
	}

	got := recorder.Header().Values(middlewares.HeaderChain)
	want := []string{"trace", "recover"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("%s = %v, want %v — Trace is outside Recover", middlewares.HeaderChain, got, want)
	}
}

// A11, which the chain header cannot show because RequestLogger stamps no entry
// in it: the 500 a recovered panic produces has to go out through
// RequestLogger's wrapper, or the one request an operator most needs timed is
// the one that logs nothing.
//
// Exactly one line, not at least one: two would mean the request is
// double-counted in any tally by status.
func TestAPanickingRouteIsStillLoggedOnceAtFiveHundred(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	panics := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("the handler fell over")
	})

	recorder := request(t,
		chain(testApp(t, livePool{}, zap.New(core)))(panics), http.MethodPost, "/discover", validDiscover)

	completed := logs.FilterMessage("request completed").All()
	if len(completed) != 1 {
		t.Fatalf("completion lines = %d, want exactly 1 — RequestLogger sits above Recover (A11)", len(completed))
	}
	if status, ok := completed[0].ContextMap()["status"]; !ok || status != int64(http.StatusInternalServerError) {
		t.Errorf("logged status = %v, want 500", status)
	}
	if recorder.Header().Get(middlewares.HeaderResponseTime) == "" {
		t.Errorf("%s is absent — the 500 did not go out through RequestLogger's wrapper",
			middlewares.HeaderResponseTime)
	}
}

// Liveness and readiness answer different questions, and the difference is the
// whole point: a liveness probe that fails when the database does gets the
// container killed and restarted, which fixes nothing and removes the one
// process that could have served a cached answer or reported the outage.
func TestHealthzAnswersWithTheDatabaseDownAndReadyzDoesNot(t *testing.T) {
	down := NewRouter(testApp(t, deadPool{}, zap.NewNop()))

	if got := request(t, down, http.MethodGet, "/healthz", "").Code; got != http.StatusOK {
		t.Errorf("/healthz = %d with the database down, want 200 — liveness has no dependencies", got)
	}
	if got := request(t, down, http.MethodGet, "/readyz", "").Code; got != http.StatusServiceUnavailable {
		t.Errorf("/readyz = %d with the database down, want 503", got)
	}

	up := NewRouter(testApp(t, livePool{}, zap.NewNop()))
	if got := request(t, up, http.MethodGet, "/readyz", "").Code; got != http.StatusOK {
		t.Errorf("/readyz = %d with the database up, want 200", got)
	}
}

// A7's seam, asserted rather than assumed: the publish service takes a
// replicator as a required collaborator, so the no-op is a decision visible
// here and not a nil check inside the service.
func TestTheNoopReplicatorIsSatisfiedByNothing(t *testing.T) {
	var replicator domain.CatalogReplicator = NoopReplicator{}

	if err := replicator.Replicate(t.Context(), "c1"); err != nil {
		t.Errorf("Replicate: %v, want the no-op to succeed", err)
	}
}

// countingPool answers every ping successfully and says how many it was asked.
//
// It returns ctx.Err() rather than a bare nil because that is what acquiring
// from a real pool does on a cancelled context, and a fake that ignores its
// context cannot show the difference between asking the database and asking the
// caller — which is the whole of what the hang-up test is about.
type countingPool struct{ pings atomic.Int64 }

func (c *countingPool) Ping(ctx context.Context) error {
	c.pings.Add(1)
	return ctx.Err()
}

// /readyz is unauthenticated and carries no rate limit — deliberately, because
// shedding a kubelet's probe is how a healthy pod gets restarted. That leaves
// the pool as the thing an anonymous caller can amplify into: one acquire per
// GET, against the same bounded pool that serves real traffic, until nothing is
// left for it.
//
// So the answer is shared for a moment rather than asked per request. A flood
// costs one ping, not one each.
func TestAFloodOfReadinessProbesCostsOnePing(t *testing.T) {
	pool := &countingPool{}
	router := NewRouter(testApp(t, pool, zap.NewNop()))

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := request(t, router, http.MethodGet, "/readyz", "").Code; got != http.StatusOK {
				t.Errorf("/readyz = %d, want 200", got)
			}
		}()
	}
	wg.Wait()

	if got := pool.pings.Load(); got != 1 {
		t.Errorf("pings = %d for 50 probes, want 1 — the readiness answer is not shared", got)
	}
}

// The other half: shared for a moment, not forever. A pod whose database came
// back has to leave the unready state without a restart, so the cached answer
// must expire — and it must expire on the clock, which is why check takes one.
func TestTheReadinessAnswerExpires(t *testing.T) {
	pool := &countingPool{}
	application := testApp(t, pool, zap.NewNop())

	start := time.Now()
	for _, at := range []time.Time{
		start,
		start.Add(readinessCacheTTL / 2), // inside the window: still the first answer
		start.Add(readinessCacheTTL),     // the window has closed
		start.Add(readinessCacheTTL * 2),
	} {
		if err := application.ready.check(t.Context(), application.DB, at); err != nil {
			t.Fatalf("check at %s: %v", at.Sub(start), err)
		}
	}

	if got := pool.pings.Load(); got != 3 {
		t.Errorf("pings = %d, want 3 — one per elapsed %s window", got, readinessCacheTTL)
	}
}

// A caller that hangs up mid-probe says nothing about the database, and the
// answer is shared, so its cancellation must not become everyone else's
// "unready" for the rest of the window.
func TestAProbeThatHangsUpDoesNotPoisonTheSharedAnswer(t *testing.T) {
	pool := &countingPool{}
	application := testApp(t, pool, zap.NewNop())

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(cancelled)
	recorder := httptest.NewRecorder()
	NewRouter(application).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Errorf("/readyz = %d for a caller that hung up, want 200 — the database was reachable", recorder.Code)
	}
}
