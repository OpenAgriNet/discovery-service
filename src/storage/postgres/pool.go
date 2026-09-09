// Package postgres is the PostgreSQL adapter, and the only package in this
// service that speaks SQL.
//
// The import-graph guard in tests/architecture holds that boundary: nothing
// outside this package and the composition root may import it, and nothing
// outside it and the database test harness may import pgx. That is what makes
// the store interface a seam rather than a convention.
package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvector "github.com/pgvector/pgvector-go/pgx"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

// applicationName is what this service calls itself to Postgres. It is why
// Task 25 emits no pool-utilisation gauge: grouped by this, pg_stat_activity
// already reports connections per service (opentelemetry.md OP3).
//
// It matches telemetry's service.name and is duplicated rather than shared,
// because the import guard puts that constant in a package this one may not
// import — see telemetry/traces.go's serviceName.
const applicationName = "discovery-service"

// NewPool opens the service's connection pool.
//
// The DSN is a secret: it arrives from DATABASE_URL and from neither YAML file,
// and it must never reach a log field or an error string. pgx quotes what it was
// given when a parse fails, hence errWithoutDSN below.
func NewPool(ctx context.Context, database config.Database) (*pgxpool.Pool, error) {
	settings, err := pgxpool.ParseConfig(database.URL)
	if err != nil {
		return nil, fmt.Errorf("parse the database connection string: %w", errWithoutDSN(err, database.URL))
	}

	settings.MaxConns = database.MaxConns
	settings.MinConns = database.MinConns

	// A CORRECTNESS setting, not a tuning knob: every nullable predicate in the
	// read path has the shape `$1 IS NULL OR <indexable predicate>`, which a
	// generic plan cannot answer from an index. The reasoning, and why the slow
	// plan is the one a WARM connection settles into, is
	// discover-and-publish.md:1273. A RuntimeParam, so it travels in the startup
	// packet rather than as a round trip per acquire.
	settings.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_custom_plan"

	// See the constant: this is what makes pg_stat_activity legible per service.
	settings.ConnConfig.RuntimeParams["application_name"] = applicationName

	// pgvector's `vector` is an extension type, so its OID is assigned per
	// database and cannot be compiled in. Registering per connection is what
	// lets an embedding be sent as a value rather than as formatted text.
	settings.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvector.RegisterTypes(ctx, conn)
	}

	pool, err := pgxpool.NewWithConfig(ctx, settings)
	if err != nil {
		return nil, fmt.Errorf("open the database pool: %w", errWithoutDSN(err, database.URL))
	}
	return pool, nil
}

// errWithoutDSN keeps a connection string out of an error string. A redaction
// rather than a dropped cause: the parse error names which part of the string it
// choked on, and that is the whole diagnostic value.
func errWithoutDSN(err error, dsn string) error {
	if dsn == "" {
		return err
	}
	return redacted{err: err, dsn: dsn}
}

type redacted struct {
	err error
	dsn string
}

func (r redacted) Error() string {
	return strings.ReplaceAll(r.err.Error(), r.dsn, "[DATABASE_URL]")
}

func (r redacted) Unwrap() error { return r.err }
