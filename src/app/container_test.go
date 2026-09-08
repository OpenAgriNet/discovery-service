package app

import (
	"context"
	"os"
	"strings"
	"testing"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// repoCommonYAML is config/common.yaml from this package's directory — the
// file this whole file's hardcoded values are meant to mirror.
const repoCommonYAML = "../../config/common.yaml"

// TestBuildableConfigMatchesCommonYAML pins the subset of buildableConfig's
// values that common.yaml itself sets a review-and-commit answer for. Without
// this, a change to common.yaml keeps this whole file silently testing the
// OLD shape — buildableConfig has no other tie back to the file it claims to
// mirror. Fields common.yaml leaves to instance.yaml/the environment (the
// network id, the DSN, geo resolution, rate limits, request-body ceiling,
// embedding dimensions) are deployment-specific and are not checked here.
func TestBuildableConfigMatchesCommonYAML(t *testing.T) {
	document, err := os.ReadFile(repoCommonYAML)
	if err != nil {
		t.Fatalf("read %s: %v", repoCommonYAML, err)
	}

	var common struct {
		App struct {
			DefaultTimezone string `yaml:"defaultTimezone"`
		} `yaml:"app"`
		Search struct {
			MaxRadiusMeters      int `yaml:"maxRadiusMeters"`
			DefaultPageSize      int `yaml:"defaultPageSize"`
			MaxPageSize          int `yaml:"maxPageSize"`
			MaxCandidatesPerMode int `yaml:"maxCandidatesPerMode"`
		} `yaml:"search"`
		Embeddings struct {
			Provider string `yaml:"provider"`
		} `yaml:"embeddings"`
		Validation struct {
			EnableL1Schema bool `yaml:"enableL1Schema"`
		} `yaml:"validation"`
	}
	if err := yaml.Unmarshal(document, &common); err != nil {
		t.Fatalf("parse %s: %v", repoCommonYAML, err)
	}

	cfg := buildableConfig(t)
	if cfg.App.DefaultTimezone != common.App.DefaultTimezone {
		t.Errorf("App.DefaultTimezone = %q, common.yaml says %q", cfg.App.DefaultTimezone, common.App.DefaultTimezone)
	}
	if cfg.Search.MaxRadiusMeters != common.Search.MaxRadiusMeters {
		t.Errorf("Search.MaxRadiusMeters = %d, common.yaml says %d", cfg.Search.MaxRadiusMeters, common.Search.MaxRadiusMeters)
	}
	if cfg.Search.DefaultPageSize != common.Search.DefaultPageSize {
		t.Errorf("Search.DefaultPageSize = %d, common.yaml says %d", cfg.Search.DefaultPageSize, common.Search.DefaultPageSize)
	}
	if cfg.Search.MaxPageSize != common.Search.MaxPageSize {
		t.Errorf("Search.MaxPageSize = %d, common.yaml says %d", cfg.Search.MaxPageSize, common.Search.MaxPageSize)
	}
	if cfg.Search.MaxCandidatesPerMode != common.Search.MaxCandidatesPerMode {
		t.Errorf("Search.MaxCandidatesPerMode = %d, common.yaml says %d", cfg.Search.MaxCandidatesPerMode, common.Search.MaxCandidatesPerMode)
	}
	if cfg.Embeddings.Provider != common.Embeddings.Provider {
		t.Errorf("Embeddings.Provider = %q, common.yaml says %q", cfg.Embeddings.Provider, common.Embeddings.Provider)
	}
	if cfg.Validation.EnableL1Schema != common.Validation.EnableL1Schema {
		t.Errorf("Validation.EnableL1Schema = %v, common.yaml says %v", cfg.Validation.EnableL1Schema, common.Validation.EnableL1Schema)
	}
}

// buildableConfig is the smallest config Build accepts, pointed at a real
// (migrated) Postgres and the pinned spec fixture rather than the network —
// the same fixture router_test.go's testApp reads, since Build and testApp
// have to agree on what "a working App" means.
//
// The values below are not read from config/common.yaml (Load reads it
// relative to the process working directory, which a go test binary does not
// share with a deployed container) — TestBuildableConfigMatchesCommonYAML
// pins the subset common.yaml itself answers, so a change there fails loudly
// here instead of leaving this file silently testing the old shape.
func buildableConfig(t *testing.T) config.Config {
	t.Helper()

	var cfg config.Config
	cfg.Log.Level = "info"
	cfg.App.Network = "mahavistar"
	cfg.App.DefaultTimezone = "Asia/Kolkata"
	cfg.Database.URL = dbtest.DSN(t)
	cfg.Database.MaxConns = 4
	cfg.Database.MinConns = 1
	cfg.Geo.ResolutionCells = geo.DefaultTestResolution
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

// selectionDimensions is deliberately not the suite-wide 768 (embeddings_test.go
// and elsewhere): these cases pin WHICH constructor newEmbedder/writeEmbedder
// picks, never a vector's contents, so any positive width is equivalent —
// named to say so rather than reading as an unexplained departure from 768.
const selectionDimensions = 8

// newEmbedder is the EMBEDDING_PROVIDER selector (Q4): noop answers nil
// rather than a Noop value, which is the whole contract with
// NewSearchRepository — a Noop there would declare a capability that can only
// return zero rows.
func TestNewEmbedderSelectsByProvider(t *testing.T) {
	cases := []struct {
		name       string
		cfg        config.Embeddings
		wantErr    bool
		wantNonNil bool
	}{
		{name: "noop", cfg: config.Embeddings{Provider: "noop"}, wantNonNil: false},
		{name: "hashing", cfg: config.Embeddings{Provider: "hashing", Dimensions: selectionDimensions}, wantNonNil: true},
		{name: "ollama", cfg: config.Embeddings{
			Provider: "ollama", Model: "m", Endpoint: "http://x", Dimensions: selectionDimensions,
		}, wantNonNil: true},
		{name: "unknown provider", cfg: config.Embeddings{Provider: "not-a-provider"}, wantErr: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			embedder, err := newEmbedder(testCase.cfg)
			if testCase.wantErr {
				if err == nil {
					t.Error("an unknown provider was accepted; want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("newEmbedder: %v", err)
			}
			if got := embedder != nil; got != testCase.wantNonNil {
				t.Errorf("newEmbedder = %v, want non-nil %v", embedder, testCase.wantNonNil)
			}
		})
	}
}

// writeEmbedder never answers nil, unlike the read side: a nil embedder
// handed to the publish path would be a nil call on every write, so both an
// explicit noop and an unknown (therefore erroring) provider fall back to the
// real Noop rather than propagating nil or the error.
func TestWriteEmbedderIsNeverNil(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Embeddings
	}{
		{name: "noop", cfg: config.Embeddings{Provider: "noop", Dimensions: selectionDimensions}},
		{name: "unknown provider", cfg: config.Embeddings{Provider: "not-a-provider", Dimensions: selectionDimensions}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if embedder := writeEmbedder(testCase.cfg); embedder == nil {
				t.Error("writeEmbedder = nil, want the real Noop")
			}
		})
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
