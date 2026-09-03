package app

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// buildableConfig is the smallest config Build accepts, pointed at a real
// (migrated) Postgres and the pinned spec fixture rather than the network —
// the same fixture router_test.go's testApp reads, since Build and testApp
// have to agree on what "a working App" means.
func buildableConfig(t *testing.T) config.Config {
	t.Helper()

	var cfg config.Config
	cfg.Log.Level = "info"
	cfg.App.Network = "mahavistar"
	cfg.App.DefaultTimezone = "Asia/Kolkata"
	cfg.Database.URL = dbtest.DSN(t)
	cfg.Database.MaxConns = 4
	cfg.Database.MinConns = 1
	cfg.Geo.ResolutionCells = 8
	cfg.Search.DefaultPageSize = 20
	cfg.Search.MaxPageSize = 100
	cfg.Search.MaxCandidatesPerMode = 500
	cfg.Search.MaxRadiusMeters = 200000
	cfg.Validation.EnableL1Schema = true
	cfg.Validation.SpecCachePath = specFixture
	cfg.Server.MaxRequestBodyBytes = 1 << 20
	cfg.RateLimit.RPS = 1000
	cfg.RateLimit.Burst = 1000
	cfg.Embeddings.Provider = "noop"
	cfg.Embeddings.Dimensions = 768
	return cfg
}

// Build's own happy path — never exercised until now; every other test in
// this package builds an App by hand (testApp) specifically to avoid opening
// a pool. This is the real production entrypoint.
func TestBuildWiresARealPoolAndBothControllers(t *testing.T) {
	application, err := Build(context.Background(), buildableConfig(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(application.Close)

	if application.Publish == nil || application.Discover == nil {
		t.Errorf("Publish = %v, Discover = %v, want both wired", application.Publish, application.Discover)
	}
	if application.DB == nil {
		t.Error("DB is nil, want the pool Build opened")
	}
	if err := application.DB.Ping(context.Background()); err != nil {
		t.Errorf("the pool Build handed back cannot be pinged: %v", err)
	}
}

// A bad timezone fails the boot rather than silently shifting every daily
// validity window (A6's neighbour).
func TestBuildWrapsAnInvalidTimezone(t *testing.T) {
	cfg := buildableConfig(t)
	cfg.App.DefaultTimezone = "Nowhere/Imaginary"

	if _, err := Build(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "timezone") {
		t.Errorf("Build with a bad timezone = %v, want an error naming it", err)
	}
}

// A DSN that will not even parse is a boot failure at the pool, not a panic
// three calls later inside pgx.
func TestBuildWrapsAnUnparseableDatabaseURL(t *testing.T) {
	cfg := buildableConfig(t)
	cfg.Database.URL = "not a dsn"

	if _, err := Build(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "database pool") {
		t.Errorf("Build with an unparseable DSN = %v, want an error naming the pool", err)
	}
}

// A bad log level is Build's very first failure, before anything has opened.
func TestBuildWrapsAnInvalidLogLevel(t *testing.T) {
	cfg := buildableConfig(t)
	cfg.Log.Level = "not-a-level"

	if _, err := Build(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "logger") {
		t.Errorf("Build with a bad log level = %v, want an error naming the logger", err)
	}
}

// wire's own failure, reached only once the pool is open: an unknown
// EMBEDDING_PROVIDER value fails the boot rather than defaulting to noop —
// Build must still close the pool it opened, which this test cannot observe
// directly but which is what the neighbouring "one close for every failure
// past this point" comment promises.
func TestBuildWrapsAnUnknownEmbeddingProvider(t *testing.T) {
	cfg := buildableConfig(t)
	cfg.Embeddings.Provider = "not-a-real-provider"

	if _, err := Build(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "embedder") {
		t.Errorf("Build with an unknown embedder = %v, want an error naming it", err)
	}
}

// A spec that can neither be fetched (no URL) nor read from the cache (a path
// naming nothing) fails the boot rather than serving requests against no
// schema at all.
func TestBuildWrapsASpecThatCanBeNeitherFetchedNorCached(t *testing.T) {
	cfg := buildableConfig(t)
	cfg.Validation.SpecCachePath = "/no/such/file.yaml"

	if _, err := Build(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "protocol spec") {
		t.Errorf("Build with an unreachable spec = %v, want an error naming it", err)
	}
}

// newEmbedder is the EMBEDDING_PROVIDER selector (Q4): noop answers nil
// rather than a Noop value, which is the whole contract with
// NewSearchRepository — a Noop there would declare a capability that can only
// return zero rows.
func TestNewEmbedderSelectsByProvider(t *testing.T) {
	if embedder, err := newEmbedder(config.Embeddings{Provider: "noop"}); err != nil || embedder != nil {
		t.Errorf("noop: newEmbedder = %v, %v, want nil, nil", embedder, err)
	}
	if embedder, err := newEmbedder(config.Embeddings{Provider: "hashing", Dimensions: 8}); err != nil || embedder == nil {
		t.Errorf("hashing: newEmbedder = %v, %v, want a non-nil Hashing", embedder, err)
	}
	if embedder, err := newEmbedder(
		config.Embeddings{Provider: "ollama", Model: "m", Endpoint: "http://x", Dimensions: 8},
	); err != nil || embedder == nil {
		t.Errorf("ollama: newEmbedder = %v, %v, want a non-nil Ollama", embedder, err)
	}
	if _, err := newEmbedder(config.Embeddings{Provider: "not-a-provider"}); err == nil {
		t.Error("an unknown provider was accepted; want an error")
	}
}

// writeEmbedder never answers nil, unlike the read side: a nil embedder
// handed to the publish path would be a nil call on every write, so both an
// explicit noop and an unknown (therefore erroring) provider fall back to the
// real Noop rather than propagating nil or the error.
func TestWriteEmbedderIsNeverNil(t *testing.T) {
	if embedder := writeEmbedder(config.Embeddings{Provider: "noop", Dimensions: 8}); embedder == nil {
		t.Error("noop: writeEmbedder = nil, want the real Noop")
	}
	if embedder := writeEmbedder(config.Embeddings{Provider: "not-a-provider", Dimensions: 8}); embedder == nil {
		t.Error("unknown provider: writeEmbedder = nil, want it to fall back to Noop rather than propagate nil")
	}
}

// A Noop write-side embedder still has to answer Embed cleanly, since it is
// what a Phase 1 deployment (EMBEDDING_PROVIDER=noop, A5) hands every
// publish.
func TestWriteEmbedderOfNoopEmbedsToNothing(t *testing.T) {
	embedder := writeEmbedder(config.Embeddings{Provider: "noop", Dimensions: 8})
	vector, err := embedder.Embed(context.Background(), "wheat")
	if err != nil || vector != nil {
		t.Errorf("Embed = %v, %v, want nil, nil", vector, err)
	}
}

// EmptyAcquireCount on an App built by hand (testApp, no pool) answers zero
// rather than dereferencing a nil pool — the one branch its own doc comment
// says exists for a test that asks about latency without a database.
func TestEmptyAcquireCountOfAHandBuiltAppIsZero(t *testing.T) {
	application := testApp(t, livePool{}, zap.NewNop())
	if got := application.EmptyAcquireCount(); got != 0 {
		t.Errorf("EmptyAcquireCount = %d, want 0 — this App holds no pool", got)
	}
}
