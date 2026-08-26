// Package dbtest runs the suite against a real PostgreSQL rather than a
// double.
//
// Everything this schema is built out of — GIN opclasses, the H3 cell algebra's
// array operators, HNSW, the inlining rules that decide whether geo_haversine_m
// costs a call per row — is PostgreSQL behaviour, not application behaviour. A
// fake would assert only that this package's own author understood it, which is
// exactly the thing in doubt.
package dbtest

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // the pgx5:// target
	_ "github.com/golang-migrate/migrate/v4/source/file"     // the file:// source
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Pool is what a test holds. Named here so a test reads as a test rather than
// as three lines of pgx types, and so the driver stays one import to change.
type Pool = *pgxpool.Pool

// image is pinned to the same tag docker-compose.yml runs, so a behaviour that
// holds in the suite holds in development. pgvector rather than plain postgres
// because migration 001 creates the extension, and a missing extension fails at
// migrate time with a message that reads as a broken migration.
const image = "pgvector/pgvector:0.8.0-pg16"

const (
	database = "discovery"
	username = "discovery"
	password = "discovery"
)

// One container per package, built once. Starting a container per test would
// dominate the suite's runtime; migrating per test would dominate what is left.
// Each test truncates instead, which is why the shared pool is safe to hand out
// repeatedly.
var (
	once      sync.Once
	shared    Pool
	adminDSN  string
	startErr  error
	tableList string
)

// NewPostgres hands out the package's pool against a migrated, empty database.
//
// Tests using it must not call t.Parallel: the reset below truncates every
// table, and a parallel sibling's rows are indistinguishable from a previous
// test's leftovers.
func NewPostgres(t *testing.T) Pool {
	t.Helper()
	skipIfShort(t)

	once.Do(func() { startErr = start() })
	if startErr != nil {
		t.Fatalf("start PostgreSQL: %v", startErr)
	}

	reset(t, shared)
	return shared
}

// skipIfShort is the only escape hatch, and it is explicit. A suite that
// silently skipped when Docker was missing would report green on a machine that
// had verified none of this — so no Docker and no -short is a failure, by
// design.
func skipIfShort(t *testing.T) {
	t.Helper()

	if testing.Short() {
		t.Skip("needs PostgreSQL; -short was requested")
	}
}

func start() error {
	ctx := context.Background()

	container, err := postgres.Run(ctx, image,
		postgres.WithDatabase(database),
		postgres.WithUsername(username),
		postgres.WithPassword(password),
		// The module's own strategy waits for the log line TWICE, because the
		// entrypoint starts the server once for initialisation and again for
		// real. Waiting on the first one connects to a server that is about to
		// be shut down, which surfaces as an unexplained connection reset in
		// whichever test happened to run first.
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return fmt.Errorf("run %s: %w", image, err)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return fmt.Errorf("read the connection string: %w", err)
	}
	adminDSN = dsn

	if migrateErr := applyMigrations(dsn); migrateErr != nil {
		return migrateErr
	}

	shared, err = open(dsn)
	if err != nil {
		return err
	}
	tableList, err = readTables(ctx, shared)
	return err
}

// open configures the pool the index assertions in this package depend on.
//
// plan_cache_mode is a correctness setting here, not a tuning knob. Once a
// cached statement stops being re-planned per call, the plan is built without
// the parameter values, and a plan built that way cannot fold
// `$1 IS NULL OR <indexable predicate>` — it falls to a sequential scan, as
// TestAGenericPlanCannotReachTheCellIndex shows. Production wants it for the
// same reason, which is why it sits on the pool rather than in a test helper.
//
// PostgreSQL's own `auto` heuristic already declines the generic plan for this
// predicate shape, because it costs it and finds it worse. Forcing the custom
// plan removes the dependence on that costing going the same way at every size
// the corpus reaches.
func open(dsn string) (Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse the connection string: %w", err)
	}
	config.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_custom_plan"

	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, fmt.Errorf("open a pool: %w", err)
	}
	return pool, nil
}

// migrationsDir finds the repository's migrations by walking up from the test's
// working directory, which is the package directory and not the repository
// root. Anchored on go.mod rather than on a relative hop count, so moving this
// package does not silently start migrating nothing.
func migrationsDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("read the working directory: %w", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "migrations"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above the working directory")
		}
		dir = parent
	}
}

// migrator builds a migrator over the repository's own migrations directory,
// through golang-migrate — the same tool the Makefile runs — rather than by
// executing the .sql files directly. The version bookkeeping and the file
// naming are part of what the suite is proving.
func migrator(dsn string) (*migrate.Migrate, error) {
	dir, err := migrationsDir()
	if err != nil {
		return nil, err
	}

	target, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse the connection string: %w", err)
	}
	target.Scheme = "pgx5"

	instance, err := migrate.New("file://"+dir, target.String())
	if err != nil {
		return nil, fmt.Errorf("open the migrations in %s: %w", dir, err)
	}
	return instance, nil
}

// closeMigrator reports the migrator's own errors, which are two: golang-migrate
// returns a source error and a database error separately, and discarding either
// hides a migration that ran against a connection it then failed to release.
func closeMigrator(instance *migrate.Migrate) error {
	sourceErr, databaseErr := instance.Close()
	if sourceErr != nil {
		return fmt.Errorf("close the migration source: %w", sourceErr)
	}
	if databaseErr != nil {
		return fmt.Errorf("close the migration database: %w", databaseErr)
	}
	return nil
}

func applyMigrations(dsn string) error {
	instance, err := migrator(dsn)
	if err != nil {
		return err
	}

	upErr := instance.Up()
	closeErr := closeMigrator(instance)
	if upErr != nil && upErr != migrate.ErrNoChange {
		return fmt.Errorf("migrate up: %w", upErr)
	}
	return closeErr
}

// readTables asks the catalog which tables exist rather than listing them here,
// so a table added in a later migration is truncated between tests without
// anyone remembering to add it. schema_migrations is excluded: it is
// golang-migrate's bookkeeping, and truncating it would tell the next migrator
// the database is empty when it is fully migrated.
func readTables(ctx context.Context, pool Pool) (string, error) {
	rows, err := pool.Query(ctx,
		`SELECT tablename FROM pg_tables
		  WHERE schemaname = 'public' AND tablename <> 'schema_migrations'
		  ORDER BY tablename`)
	if err != nil {
		return "", fmt.Errorf("read the table list: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", fmt.Errorf("scan a table name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("read the table list: %w", err)
	}
	if len(names) == 0 {
		return "", fmt.Errorf("the migrations built no tables")
	}
	return strings.Join(names, ", "), nil
}

// reset empties the database BEFORE the test rather than after, so a test that
// panicked or was killed mid-way cannot leave its rows to be read as the next
// test's own.
//
// One TRUNCATE over every table, so the foreign keys between them do not
// dictate an order that a later migration would silently invalidate.
func reset(t *testing.T, pool Pool) {
	t.Helper()

	if _, err := pool.Exec(context.Background(), "TRUNCATE "+tableList+" CASCADE"); err != nil {
		t.Fatalf("truncate %s: %v", tableList, err)
	}
}

// NewMigrationTarget returns a DSN for a fresh, UNMIGRATED database inside the
// package's container.
//
// It exists for the up-then-down-then-up assertion, which has to roll the
// schema away entirely. Run against the shared database, that test's failure
// mode would be every other test in the package.
func NewMigrationTarget(t *testing.T) string {
	t.Helper()
	skipIfShort(t)

	once.Do(func() { startErr = start() })
	if startErr != nil {
		t.Fatalf("start PostgreSQL: %v", startErr)
	}

	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, strings.ToLower(t.Name()))

	// CREATE DATABASE takes no parameters, so the name is built here rather
	// than taken from anywhere a caller could reach, and quoted as an
	// identifier — the one shape of statement this repository's
	// always-parameterised rule cannot cover.
	admin, err := pgxpool.New(context.Background(), adminDSN)
	if err != nil {
		t.Fatalf("connect to create %s: %v", name, err)
	}
	defer admin.Close()

	if _, createErr := admin.Exec(context.Background(), `CREATE DATABASE "`+name+`"`); createErr != nil {
		t.Fatalf("create the database %s: %v", name, createErr)
	}

	target, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parse the connection string: %v", err)
	}
	target.Path = "/" + name
	return target.String()
}

// MigrateUp applies every migration to the named database.
func MigrateUp(t *testing.T, dsn string) {
	t.Helper()

	if err := applyMigrations(dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
}

// MigrateDown rolls every migration back.
func MigrateDown(t *testing.T, dsn string) {
	t.Helper()

	instance, err := migrator(dsn)
	if err != nil {
		t.Fatalf("open the migrations: %v", err)
	}

	downErr := instance.Down()
	closeErr := closeMigrator(instance)
	if downErr != nil && downErr != migrate.ErrNoChange {
		t.Fatalf("migrate down: %v", downErr)
	}
	if closeErr != nil {
		t.Fatalf("migrate down: %v", closeErr)
	}
}

// SchemaObjects lists everything the migrations put in the public schema —
// tables, indexes and functions, each tagged with its kind so a name reused
// across two of them still reads as two objects.
//
// schema_migrations and its primary key are excluded: golang-migrate creates
// them and never drops them, so a residue assertion that counted them could
// never pass.
func SchemaObjects(t *testing.T, dsn string) []string {
	t.Helper()

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect to %s: %v", dsn, err)
	}
	defer pool.Close()

	rows, err := pool.Query(context.Background(),
		`SELECT 'table:' || tablename FROM pg_tables
		  WHERE schemaname = 'public' AND tablename <> 'schema_migrations'
		 UNION ALL
		 SELECT 'index:' || indexname FROM pg_indexes
		  WHERE schemaname = 'public' AND tablename <> 'schema_migrations'
		 UNION ALL
		 SELECT 'function:' || p.proname FROM pg_proc p
		   JOIN pg_namespace n ON n.oid = p.pronamespace
		  WHERE n.nspname = 'public'
		 ORDER BY 1`)
	if err != nil {
		t.Fatalf("read the schema objects: %v", err)
	}
	defer rows.Close()

	var objects []string
	for rows.Next() {
		var object string
		if err := rows.Scan(&object); err != nil {
			t.Fatalf("scan a schema object: %v", err)
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the schema objects: %v", err)
	}
	return objects
}

// ExplainVerbose returns the VERBOSE plan for a query, which is where inlining
// shows: an inlined SQL function is replaced by its body, so its name is gone
// from the Output line.
func ExplainVerbose(t *testing.T, pool Pool, sql string) string {
	t.Helper()

	return explain(t, pool, "EXPLAIN (VERBOSE) "+sql)
}

// GenericPlan returns the plan PostgreSQL builds WITHOUT the parameter values —
// the state a cached statement lands in once the server stops re-planning per
// call, and the state the pool's force_custom_plan exists to keep it out of.
//
// EXPLAIN (GENERIC_PLAN) is PostgreSQL 16's own way to ask for this; the older
// route, PREPARE followed by EXPLAIN EXECUTE under force_generic_plan, needs
// the argument values spelled into the statement text.
//
// Sent down the raw simple-query protocol rather than through pgx's ordinary
// path, because a generic plan is precisely a plan for UNBOUND parameters:
// binding them is the thing being withheld, and pgx substitutes $1 from the
// argument list in every mode it offers. The statement text is this package's
// own, never a caller's data.
func GenericPlan(t *testing.T, pool Pool, sql string) string {
	t.Helper()

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a connection: %v", err)
	}
	defer conn.Release()

	// One batch, so the connection cannot go back to the pool with sequential
	// scans still disabled — the RESET travels with the statement that needed
	// the SET even if the assertion between them fails.
	results, err := conn.Conn().PgConn().Exec(ctx,
		"SET enable_seqscan = off; EXPLAIN (GENERIC_PLAN) "+sql+"; RESET enable_seqscan").ReadAll()
	if err != nil {
		t.Fatalf("explain generically %q: %v", sql, err)
	}
	if len(results) != 3 {
		t.Fatalf("expected three results from the batch, got %d", len(results))
	}

	var plan strings.Builder
	for _, row := range results[1].Rows {
		plan.Write(row[0])
		plan.WriteString("\n")
	}
	return plan.String()
}

// IndexScansOverSixExecutions runs a query six times on one connection through
// the extended protocol and reports how many scans the named index actually
// served, alongside the plan for diagnosis.
//
// Counted from pg_stat_user_indexes rather than read off an EXPLAIN, and that
// distinction is the whole point. Preparing `EXPLAIN <query>` does not exercise
// the plan cache at all: EXPLAIN is a utility statement, so its target is
// re-planned at every execution with the argument values in hand, and the
// resulting plan says nothing about what a sixth ordinary execution would do.
// These are ordinary executions, and the counter is what they moved.
//
// Six, not one. Five executions in is where a cached statement may stop being
// re-planned per call, and a generic plan cannot fold `$1 IS NULL OR
// <predicate>` — the shape every discover filter has. An assertion that stopped
// at the fifth could not see a sixth execution that had quietly started
// sequentially scanning.
func IndexScansOverSixExecutions(t *testing.T, pool Pool, index, sql string, args ...any) (int64, string) {
	t.Helper()

	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a connection: %v", err)
	}
	defer conn.Release()

	// Session-level rather than SET LOCAL: the six executions below must share
	// one cached statement, and wrapping them in a transaction that is rolled
	// back would be a different shape from the one production runs. Reset on
	// the way out so the connection returns to the pool as it left it.
	if _, err := conn.Exec(ctx, "SET enable_seqscan = off"); err != nil {
		t.Fatalf("disable sequential scans: %v", err)
	}
	defer func() {
		if _, err := conn.Exec(ctx, "RESET enable_seqscan"); err != nil {
			t.Errorf("reset enable_seqscan: %v", err)
		}
	}()

	before := indexScans(t, conn, index)
	for execution := range 6 {
		rows, err := conn.Query(ctx, sql, args...)
		if err != nil {
			t.Fatalf("execution %d of %q: %v", execution+1, sql, err)
		}
		for rows.Next() { //nolint:revive // draining is the point; the rows are not read
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("execution %d of %q: %v", execution+1, sql, err)
		}
	}

	// Index counters are accumulated per backend and flushed on a timer, so a
	// read taken straight after the loop would see whatever happened to have
	// been flushed already — usually nothing, which reads as "the index was
	// never used".
	if _, err := conn.Exec(ctx, "SELECT pg_stat_force_next_flush()"); err != nil {
		t.Fatalf("flush the statistics counters: %v", err)
	}

	return indexScans(t, conn, index) - before, explainOn(t, conn, "EXPLAIN "+sql, args)
}

// indexScans reads one index's scan counter, failing rather than returning zero
// when the index does not exist — a misspelled name would otherwise report as
// "the index served no scans", which is the same thing a dropped index reports
// and the opposite of what it means.
func indexScans(t *testing.T, conn *pgxpool.Conn, index string) int64 {
	t.Helper()

	var scans int64
	err := conn.QueryRow(context.Background(),
		`SELECT coalesce(idx_scan, 0) FROM pg_stat_user_indexes WHERE indexrelname = $1`,
		index).Scan(&scans)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("there is no index named %s", index)
	}
	if err != nil {
		t.Fatalf("read the scan counter for %s: %v", index, err)
	}
	return scans
}

// explain runs one EXPLAIN on a connection of the pool's choosing.
//
// enable_seqscan is disabled for the duration, and that is the honest scope of
// these assertions: at any size a test corpus reaches, a sequential scan is
// genuinely cheaper, so the planner would decline every index here for the
// right reason. What is being asserted is that the predicate CAN reach the
// index — that the index exists and its opclass covers the operator — which is
// the regression a dropped index or a mis-specified opclass actually produces.
//
// SET LOCAL inside a transaction that is always rolled back, so the setting
// goes back to the pool with the connection.
func explain(t *testing.T, pool Pool, sql string, args ...any) string {
	t.Helper()

	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil {
			t.Errorf("roll back the EXPLAIN transaction: %v", err)
		}
	}()

	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("disable sequential scans: %v", err)
	}
	return readPlan(t, tx, sql, args)
}

// explainOn runs one EXPLAIN on a connection the caller already holds, so the
// plan reported alongside a scan count comes from the same session that
// produced it.
func explainOn(t *testing.T, conn *pgxpool.Conn, sql string, args []any) string {
	t.Helper()

	return readPlan(t, conn, sql, args)
}

// querier is the shared surface of a pool, a connection and a transaction, so
// the plan reader does not care which one it was handed.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func readPlan(t *testing.T, from querier, sql string, args []any) string {
	t.Helper()

	rows, err := from.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("explain %q: %v", sql, err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan a plan line: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("explain %q: %v", sql, err)
	}
	return plan.String()
}

// PlanCacheMode reports what the server thinks the setting is, rather than what
// the pool was configured with — a runtime parameter the server did not accept
// is silently absent, not an error.
func PlanCacheMode(t *testing.T, pool Pool) string {
	t.Helper()

	var mode string
	if err := pool.QueryRow(context.Background(), "SHOW plan_cache_mode").Scan(&mode); err != nil {
		t.Fatalf("read plan_cache_mode: %v", err)
	}
	return mode
}
