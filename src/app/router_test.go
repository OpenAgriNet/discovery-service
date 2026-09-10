package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
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
	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/middlewares"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
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
//
// Telemetry is left nil, and that is the point for every test but the two that
// set it: a nil provider is what most of this file exercises, so `chain` calling
// Tracer() on one has to be safe. Provider.Tracer() answers with a no-op tracer
// rather than dereferencing, and this is what would catch a change that stopped
// it doing so — as a panic in eleven tests rather than at 3am in a deployment
// that left OTEL_EXPORTER unset.
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
	cfg.Geo.ResolutionCells = geo.DefaultTestResolution
	cfg.Geo.MaxGeometriesPerCatalog = 256
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
				cfg.App.Network, time.UTC, cfg.Geo.MaxGeometriesPerCatalog),
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

// The chain order, proved by the span rather than by the X-Beckn-Chain header
// it used to be proved by.
//
// Trace stamped a `trace` entry there only so this assertion had something to
// observe at that slot. 23c gave it a real side effect — the span — and a marker
// kept beside it would be a second thing to keep true. What is asserted is the
// same claim in stronger terms: not that Trace ran before Recover, but that the
// 500 Recover wrote is ON the span, which is what an operator opening the trace
// for a failed request actually needs and what the ordering was for.
//
// Recover stamps its own entry still — nothing else places it — and it is
// checked here so the two links are named in one place.
func TestOnAPanickingRouteTheFiveHundredIsInsideTheSpan(t *testing.T) {
	provider, recorder := telemetry.NewRecorder()

	app := testApp(t, livePool{}, zap.NewNop())
	app.Telemetry = provider

	panics := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("the handler fell over")
	})

	response := request(t, chain(app)(panics), http.MethodPost, "/discover", validDiscover)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — the panic was not recovered", response.Code)
	}
	if got := response.Header().Values(middlewares.HeaderChain); len(got) != 1 || got[0] != "recover" {
		t.Errorf("%s = %v, want [recover] — Trace no longer stamps one", middlewares.HeaderChain, got)
	}

	spans := recorder.Spans()
	if len(spans) != 1 {
		t.Fatalf("%d spans exported for one request, want 1", len(spans))
	}
	if got := spans[0].Attributes["http.status_code"]; got != int64(http.StatusInternalServerError) {
		t.Errorf("http.status_code = %v, want 500 — Recover is not inside the span, so the "+
			"one request an operator opens the trace for is the one with nothing on it", got)
	}
	if spans[0].Status != "Error" {
		t.Errorf("span status = %q on a recovered panic, want Error", spans[0].Status)
	}
}

// TestTheAssembledChainNamesTheSpanByTheAction is the whole chain answering the
// question each of its links answers a part of: Trace starts the span, Envelope
// four links below parses the action, and Trace's deferred block renames it.
//
// It is here rather than in middlewares because the middleware tests drive
// Trace over a stub handler that writes the record itself; this is the only
// place the real Envelope does it, over the real chain, through the real
// container wiring. A chain that dropped Trace, or a Trace handed a tracer that
// was not the App's, would leave every span named for its route and nothing in
// the middleware tests would notice.
//
// It does not pin the ORDER — the record is allocated above both links and
// Envelope writes into it wherever it sits, so this still passes with Trace
// below Envelope. TestOnAPanickingRouteTheFiveHundredIsInsideTheSpan is the one
// that fails on that, and it is checked to fail on it.
func TestTheAssembledChainNamesTheSpanByTheAction(t *testing.T) {
	provider, recorder := telemetry.NewRecorder()

	app := testApp(t, livePool{}, zap.NewNop())
	app.Telemetry = provider

	served := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	request(t, chain(app)(served), http.MethodPost, "/discover", validDiscover)

	spans := recorder.Spans()
	if len(spans) != 1 {
		t.Fatalf("%d spans exported for one request, want 1", len(spans))
	}
	if spans[0].Name != "discover" {
		t.Errorf("span name = %q, want the action; Envelope is below Trace in the assembled "+
			"chain and its correlators have to reach the span", spans[0].Name)
	}
}

// traced serves one request through the whole router with a recording exporter
// and gives back the single span it produced.
//
// NewRouter rather than chain over a stub, which is the difference that makes
// the two tests below worth having: the facts come from the real controllers
// and the real service, over the real store, and the record they write into is
// the one Trace allocated at the top of the chain.
func tracedSpan(t *testing.T, path, body string) telemetry.Span {
	t.Helper()

	provider, recorder := telemetry.NewRecorder()
	app := testApp(t, livePool{}, zap.NewNop())
	app.Telemetry = provider

	request(t, NewRouter(app), http.MethodPost, path, body)

	spans := recorder.Spans()
	if len(spans) != 1 {
		t.Fatalf("%d spans exported for one request, want 1", len(spans))
	}
	return spans[0]
}

func eventNames(span telemetry.Span) []string {
	spelled := make([]string, 0, len(span.Events))
	for _, event := range span.Events {
		spelled = append(spelled, event.Name)
	}
	return spelled
}

// TestTheEventsOfOneRequestLandOnOneSpanInOrder — 23d end to end.
//
// Every other test of the events works on one layer: the projection is pinned
// in telemetry/traces_test.go, the timestamping in middlewares, and each
// call site against its own record. None of them can show that the three
// point-in-time facts of a discover — written by the controller, by the service
// below it and by the controller again — reach the SAME span, in the order they
// happened. A record allocated per handler rather than per request, or a
// service given a context that had lost it, would leave every one of those
// tests green and this one with one event, or none.
//
// Non-decreasing rather than strictly increasing, deliberately. Strictness at
// this level would be an assertion about the clock's resolution between three
// calls with no work between them; the acceptance criterion's real content is
// that the stamps come from where the facts were observed, and
// TestTheEventsAreStampedWhereTheyHappened pins that with sleeps and was
// checked to fail without WithTimestamp. What is worth pinning here is order
// and containment.
func TestTheEventsOfOneRequestLandOnOneSpanInOrder(t *testing.T) {
	span := tracedSpan(t, "/discover", validDiscover)

	want := []string{
		fact.RequestInfo.EventName(),
		fact.RetrievalInfo.EventName(),
		fact.ResponseInfo.EventName(),
	}
	if got := eventNames(span); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}

	for index, event := range span.Events {
		if event.Time.Before(span.Start) || !event.Time.Before(span.End) {
			t.Errorf("%s at %s is outside the span [%s, %s)",
				event.Name, event.Time, span.Start, span.End)
		}
		if index > 0 && event.Time.Before(span.Events[index-1].Time) {
			t.Errorf("%s is stamped before %s, which ran first",
				event.Name, span.Events[index-1].Name)
		}
	}
}

// TestARefusedRequestCarriesTheErrorEventAndNothingElse.
//
// The error event is the one of the four no controller writes — it comes from
// WriteNack, which this body reaches through Envelope, four links above any
// handler. So this is the only place the assembled chain shows that a rejection
// too early for a controller to see is still on the span.
//
// And nothing else: a request that was refused before it was parsed has no
// intent to describe and no retrieval to report, so an empty request_info here
// would read as a discover that arrived asking for nothing.
func TestARefusedRequestCarriesTheErrorEventAndNothingElse(t *testing.T) {
	span := tracedSpan(t, "/discover", `{"context":{`)

	want := []string{fact.ErrorEvent.EventName()}
	if got := eventNames(span); !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
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
