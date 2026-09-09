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

// NewPool opens the service's connection pool.
//
// The DSN arrives from config, which reads it from DATABASE_URL and from
// nowhere else — it is a secret, so it appears in neither YAML file. This
// function never logs it and never returns it inside an error: pgx's parse
// errors quote the string they failed on, so the wrapping below states what
// failed without restating what it was given.
func NewPool(ctx context.Context, database config.Database) (*pgxpool.Pool, error) {
	settings, err := pgxpool.ParseConfig(database.URL)
	if err != nil {
		return nil, fmt.Errorf("parse the database connection string: %w", errWithoutDSN(err, database.URL))
	}

	settings.MaxConns = database.MaxConns
	settings.MinConns = database.MinConns

	// plan_cache_mode is a CORRECTNESS setting on this service, not a tuning
	// knob, and it is set as a RuntimeParam so it travels in the startup packet
	// rather than as an extra round trip per acquire.
	//
	// Every nullable predicate in the read path has the shape
	// `$1 IS NULL OR <indexable predicate>`. With the value in hand the planner
	// folds the first arm away and is left with something sargable; without it
	// — which is what a GENERIC plan is — it must keep both arms, and an OR
	// whose first arm does not mention the column cannot be answered by an
	// index on that column. pgx speaks the extended protocol, so PostgreSQL
	// builds a custom plan for the first five executions and may switch to a
	// generic one after: the fast plan is what a cold connection gets and the
	// slow one is what a warm connection settles into, which is the opposite of
	// how a performance problem is usually shaped and invisible to any EXPLAIN
	// run once.
	settings.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_custom_plan"

	// pgvector's `vector` is an extension type, so its OID is assigned per
	// database and cannot be compiled in. Registering on every connection is
	// what lets a resource's embedding be sent as a value rather than as a
	// string this package would have to format itself.
	settings.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvector.RegisterTypes(ctx, conn)
	}

	pool, err := pgxpool.NewWithConfig(ctx, settings)
	if err != nil {
		return nil, fmt.Errorf("open the database pool: %w", errWithoutDSN(err, database.URL))
	}
	return pool, nil
}

// errWithoutDSN keeps a connection string out of an error string.
//
// pgx quotes what it was given when a DSN will not parse, and an error travels
// to a log field, which is the one place a password must never reach. A
// redaction rather than a dropped cause: the parse error names WHICH part of
// the string it choked on, and that is the whole diagnostic value.
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
