// Package app is the composition root: the one place that knows which concrete
// thing satisfies each seam, so no package below it has to.
package app

import (
	"context"
	"errors"
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
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry"
	"github.com/OpenAgriNet/discovery-service/src/platform/validation"
	"github.com/OpenAgriNet/discovery-service/src/publish"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres"
)

// Pinger is the one question /readyz asks of the datastore.
//
// An interface rather than *pgxpool.Pool so readiness is testable without a
// database: the probe's whole behaviour is what it does when the ping fails.
type Pinger interface {
	Ping(ctx context.Context) error
}

// App is everything Build resolved, held by the thing that owns it.
type App struct {
	Config config.Config
	Log    *zap.Logger

	// DB is what /readyz pings. Separate from pool because a test supplies one
	// without the other.
	DB   Pinger
	Spec *validation.SpecIndex

	// Telemetry is the tracer provider, held here so Close can flush it and so the
	// Trace middleware can take a tracer off it. Nil is a working value — every
	// method on it tolerates one — which is what lets the acceptance and dbtest
	// suites build an App without starting an exporter.
	Telemetry *telemetry.Provider

	Publish  *publish.Controller
	Discover *discover.Controller

	// ready caches the readiness answer so an unauthenticated flood of probes
	// cannot amplify into one pool acquire each. Zero value is usable, and it holds
	// a mutex — App is a pointer everywhere for this reason.
	ready readiness

	// pool is owned here and closed by Close. Unexported: the layers above take
	// their dependency through the seams, and a pool reachable from the router is a
	// pool the router will eventually be tempted to use.
	pool *pgxpool.Pool
}

// NoopReplicator is the Phase 1 write fan-out (A7). Injected like everything
// else, so the day a queue arrives it replaces one constructor call rather than
// being retrofitted through the publish service.
type NoopReplicator struct{}

var _ domain.CatalogReplicator = NoopReplicator{}

// Replicate fans out to nothing, successfully. Not an error: a deployment with one
// store has replicated correctly by doing nothing.
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
	// loader's fallback warning reaches an operator at all — without this an
	// air-gapped boot on a stale cached document says nothing about it.
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
	// return — three pool.Close() calls is three places for the fourth failure
	// path to be added without one.
	built, err := wire(ctx, cfg, log, pool, zone)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return built, nil
}

// wire builds everything downstream of an open pool.
//
// Split out of Build so the pool has exactly one owner on the failure path: Build
// acquires what has to be released, wire assembles what does not.
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

	// Nil when this deployment has no semantic mode, and that nil is what the
	// search repository reads to leave the capability undeclared — putting
	// `semantic` in X-Beckn-Degraded instead of running a query that can only
	// return nothing (A5, C11).
	embedder, err := newEmbedder(cfg.Embeddings)
	if err != nil {
		return nil, fmt.Errorf("build the embedder: %w", err)
	}

	catalogs := postgres.NewCatalogRepository(pool, cfg.Geo.ResolutionCells)
	search := postgres.NewSearchRepository(pool, cfg.Search, embedder)

	tracing, err := startTelemetry(ctx, cfg, pool)
	if err != nil {
		return nil, err
	}

	return &App{
		Config:    cfg,
		Log:       log,
		DB:        pool,
		Spec:      spec,
		Telemetry: tracing,
		Publish: publish.NewController(
			publish.NewService(catalogs, NoopReplicator{}, writeEmbedder(cfg.Embeddings),
				cfg.App.Network, zone, cfg.Geo.MaxGeometriesPerCatalog),
			cfg.Errors),
		Discover: discover.NewController(discover.NewService(search, cfg), cfg.Errors),
		pool:     pool,
	}, nil
}

// startTelemetry builds the providers and registers Task 25's one instrument.
//
// Called last from wire: under OTEL_EXPORTER=none it starts no exporter and
// reaches no network, and under otlp it creates a lazy client that dials on first
// export, so a collector that is not up does not hold up the boot.
//
// A function rather than four lines inline because the registration has a failure
// path that must shut the provider down — wire's caller closes the pool and
// nothing else, so a bare `return nil, err` after Init would leak a tracer
// provider and, under otlp, its gRPC client.
//
// The only place in the service that knows both names: the import guard keeps pgx
// inside src/storage/postgres and the SDK inside src/platform/telemetry, so
// postgres.PoolStats satisfies telemetry.PoolStatsSource without either package
// naming the other.
func startTelemetry(ctx context.Context, cfg config.Config, pool *pgxpool.Pool) (*telemetry.Provider, error) {
	tracing, err := telemetry.Init(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("start telemetry: %w", err)
	}

	// Registers a callback and starts nothing: under OTEL_EXPORTER=none the meter
	// provider has no reader, so the callback is never invoked.
	if registerErr := telemetry.RegisterPoolStats(tracing.MeterProvider(), postgres.NewPoolStats(pool)); registerErr != nil {
		if shutdownErr := tracing.Shutdown(ctx); shutdownErr != nil {
			return nil, errors.Join(
				fmt.Errorf("register the pool metrics: %w", registerErr), shutdownErr)
		}
		return nil, fmt.Errorf("register the pool metrics: %w", registerErr)
	}
	return tracing, nil
}

// EmptyAcquireCount reports how many pool acquires found no idle connection and
// had to wait for one.
//
// It exists for the acceptance suite's latency scenario rather than for any caller
// here: without it an undersized pool fails that scenario as a slow QUERY and the
// fix goes looking in the SQL. A count rather than the whole pgxpool.Stat, which
// would put the pool's full surface one dot away.
func (a *App) EmptyAcquireCount() int64 {
	if a.pool == nil {
		return 0
	}
	return a.pool.Stat().EmptyAcquireCount()
}

// telemetryFlushTimeout bounds the export of whatever the batcher is still
// holding when the process is going away.
//
// Its own constant rather than Server.ShutdownTimeout: the two run in sequence,
// so sharing the value would make the worst-case shutdown twice the number an
// operator configured and push a default 30s terminationGracePeriodSeconds over
// the edge. Losing the last batch beats being SIGKILLed mid-drain.
const telemetryFlushTimeout = 5 * time.Second

// Close releases what Build opened. Safe on a partially built App, because
// Build closes the pool itself on every path that fails after opening it.
//
// Telemetry goes first and the logger last: the exporter is the only thing here
// that talks to the network, so the only one that can fail in a way worth reading,
// and it has to be able to say so through a logger that is still open.
//
// Also why the flush lives here rather than in Run's drain — Run does not return
// until no handler is running, so flushing inside the drain would race the last
// handler's span.End.
func (a *App) Close() {
	if a.Telemetry != nil {
		// Background, not a request context: everything that had one has already
		// finished. Bounded, because a collector that has gone away takes as long
		// as the deadline allows to say so.
		ctx, cancel := context.WithTimeout(context.Background(), telemetryFlushTimeout)
		defer cancel()

		if err := a.Telemetry.Shutdown(ctx); err != nil && a.Log != nil {
			// Warn, not Error: the spans are lost and the process is exiting
			// anyway. It must not look like the shutdown itself failed.
			a.Log.Warn("flush telemetry", zap.Error(err))
		}
	}
	if a.pool != nil {
		a.pool.Close()
	}
	if a.Log != nil {
		// Sync on a terminal returns ENOTTY on some platforms, so this cannot be
		// fatal. It goes to stderr rather than to the logger being flushed: the
		// one thing certainly not working is the logger.
		if err := a.Log.Sync(); err != nil {
			fmt.Fprintf(os.Stderr, "discovery-service: flush the log: %v\n", err)
		}
	}
}

// newEmbedder is the EMBEDDING_PROVIDER selector for the READ side (Q4 in the
// implementation prompts).
//
// `noop` returns NIL rather than a Noop, and that is the whole contract with
// NewSearchRepository: a Noop declares the capability, embeds to nothing, and
// reports a query that can only match zero rows as a successful search. Nil says
// the mode is absent, which reaches the caller as a named degradation.
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
// The two sides differ deliberately: a nil read-side embedder removes a retrieval
// mode and says so, while a nil write-side one would be a nil call on every
// publish. So `noop` here is the real Noop, leaving the `embedding` column NULL —
// A5's backfill queue rather than an absence.
func writeEmbedder(cfg config.Embeddings) embeddings.Embedder {
	embedder, err := newEmbedder(cfg)
	if err != nil || embedder == nil {
		return embeddings.NewNoop(cfg.Dimensions)
	}
	return embedder
}
