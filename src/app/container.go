// Package app is the composition root: the one place that knows which concrete
// thing satisfies each seam, so no package below it has to.
package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/discover"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/embeddings"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
	"github.com/OpenAgriNet/discovery-service/src/platform/validation"
	"github.com/OpenAgriNet/discovery-service/src/publish"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres"
)

// Pinger is the one question /readyz asks of the datastore.
//
// An interface rather than *pgxpool.Pool so readiness is testable without a
// database — the probe's whole behaviour is what it does when the ping fails,
// and a test that cannot make it fail tests the other branch twice.
type Pinger interface {
	Ping(ctx context.Context) error
}

// App is everything Build resolved, held by the thing that owns it.
//
// The fields are exported because the router and the server read them and
// nothing else does; accessors over a struct in the same package would be
// ceremony around a field.
type App struct {
	Config config.Config
	Log    *zap.Logger

	// DB is what /readyz pings. Separate from pool because a test supplies one
	// without the other.
	DB   Pinger
	Spec *validation.SpecIndex

	Publish  *publish.Controller
	Discover *discover.Controller

	// ready caches the readiness answer so an unauthenticated flood of probes
	// cannot amplify into one pool acquire each. Zero value is usable, and it
	// holds a mutex — App is a pointer everywhere for this reason.
	ready readiness

	// pool is owned here and closed by Close. Unexported: the layers above take
	// their dependency through the seams, and a pool reachable from the router
	// is a pool the router will eventually be tempted to use.
	pool *pgxpool.Pool
}

// NoopReplicator is the Phase 1 write fan-out (A7).
//
// It is constructed at the composition root and injected like everything else,
// which is the whole point of the seam existing before there is a second store:
// a seam nothing builds is not a seam, and the day a queue arrives it replaces
// one constructor call rather than being retrofitted through the publish
// service.
type NoopReplicator struct{}

var _ domain.CatalogReplicator = NoopReplicator{}

// Replicate fans out to nothing, successfully. Not an error: a deployment with
// one store has replicated correctly by doing nothing, and returning an error
// would make the publish path log a failure on every write.
func (NoopReplicator) Replicate(context.Context, string) error { return nil }

// Build resolves every seam and returns the wired application.
//
// Explicit constructors, no reflection (D3): a missing collaborator has to fail
// at compile time, because a container that resolves by name fails at startup
// on the one deployment nobody tested.
func Build(ctx context.Context, cfg config.Config) (*App, error) {
	log, err := logger.New(cfg.Log)
	if err != nil {
		return nil, fmt.Errorf("build the logger: %w", err)
	}

	// Everything below here reports through the context, which is how the spec
	// loader's fallback warning reaches an operator at all — without this it
	// logs to a no-op and an air-gapped boot running on a stale cached document
	// says nothing about it.
	ctx = logger.NewContext(ctx, log)

	zone, err := time.LoadLocation(cfg.App.DefaultTimezone)
	if err != nil {
		return nil, fmt.Errorf("load timezone %q: %w", cfg.App.DefaultTimezone, err)
	}

	pool, err := postgres.NewPool(ctx, cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("open the database pool: %w", err)
	}

	// One close for every failure past this point, rather than one beside each
	// return. Three separate pool.Close() calls is three places for the fourth
	// failure path to be added without one.
	built, err := wire(ctx, cfg, log, pool, zone)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return built, nil
}

// wire builds everything downstream of an open pool.
//
// Split out of Build so the pool has exactly one owner on the failure path, and
// so the two halves read as what they are: Build acquires the resources that
// have to be released, wire assembles the things that do not.
func wire(
	ctx context.Context, cfg config.Config, log *zap.Logger, pool *pgxpool.Pool, zone *time.Location,
) (*App, error) {
	// Before anything reads a table. Off by default (D10, and the config field
	// says why): a process that rewrites the schema as it starts is a decision
	// an operator makes, and `make migrate` is the other way to make it.
	if cfg.Database.AutoMigrate {
		if err := postgres.Migrate(cfg.Database.URL); err != nil {
			return nil, fmt.Errorf("apply migrations: %w", err)
		}
	}

	spec, err := validation.LoadSpecIndex(ctx, cfg.Validation, validation.HTTPFetcher())
	if err != nil {
		return nil, fmt.Errorf("load the protocol spec: %w", err)
	}

	// Nil when this deployment has no semantic mode, and that nil is the
	// signal the search repository reads to leave the capability undeclared —
	// which is what puts `semantic` in X-Beckn-Degraded instead of running a
	// query that can only return nothing (A5, C11).
	embedder, err := newEmbedder(cfg.Embeddings)
	if err != nil {
		return nil, fmt.Errorf("build the embedder: %w", err)
	}

	catalogs := postgres.NewCatalogRepository(pool, cfg.Geo.ResolutionCells)
	search := postgres.NewSearchRepository(pool, cfg.Search, embedder)

	return &App{
		Config: cfg,
		Log:    log,
		DB:     pool,
		Spec:   spec,
		Publish: publish.NewController(
			publish.NewService(catalogs, NoopReplicator{}, writeEmbedder(cfg.Embeddings),
				cfg.App.Network, zone),
			cfg.Errors),
		Discover: discover.NewController(discover.NewService(search, cfg), cfg.Errors),
		pool:     pool,
	}, nil
}

// Close releases what Build opened. Safe on a partially built App, because
// Build closes the pool itself on every path that fails after opening it.
func (a *App) Close() {
	if a.pool != nil {
		a.pool.Close()
	}
	if a.Log != nil {
		// Sync on a terminal returns ENOTTY on some platforms and on a closed
		// stderr it returns nothing useful either, so this cannot be fatal. It
		// goes to stderr rather than to the logger being flushed: the one thing
		// that is certainly not working is the logger.
		if err := a.Log.Sync(); err != nil {
			fmt.Fprintf(os.Stderr, "discovery-service: flush the log: %v\n", err)
		}
	}
}

// newEmbedder is the EMBEDDING_PROVIDER selector, and it answers the read side
// (Q4 in the implementation prompts).
//
// `noop` returns NIL rather than a Noop, and the difference is the whole
// contract with NewSearchRepository: a Noop handed to the semantic retriever
// declares a capability, embeds to nothing, and reports a query that can only
// match zero rows as a successful search. Nil says the mode is absent, which is
// what reaches the caller as a named degradation instead of silence.
func newEmbedder(cfg config.Embeddings) (embeddings.Embedder, error) {
	switch cfg.Provider {
	case "noop":
		return nil, nil
	case "hashing":
		return embeddings.NewHashing(cfg.Dimensions), nil
	case "ollama":
		return embeddings.NewOllama(cfg.Endpoint, cfg.Model, cfg.Dimensions, cfg.WriteDeadline), nil
	default:
		return nil, fmt.Errorf("unknown provider %q: want noop, hashing or ollama", cfg.Provider)
	}
}

// writeEmbedder is the publish path's, and it is never nil.
//
// The two sides differ deliberately. A nil read-side embedder removes a
// retrieval mode and says so; a nil write-side one would be a nil call on every
// publish. So `noop` here is the real Noop, whose Embed returns nothing and
// leaves the `embedding` column NULL — which is exactly the backfill queue A5
// describes, rather than an absence.
func writeEmbedder(cfg config.Embeddings) embeddings.Embedder {
	embedder, err := newEmbedder(cfg)
	if err != nil || embedder == nil {
		return embeddings.NewNoop(cfg.Dimensions)
	}
	return embedder
}
