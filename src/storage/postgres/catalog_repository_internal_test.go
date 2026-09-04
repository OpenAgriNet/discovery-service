package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres/gen"
	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// testResolution mirrors postgres_test's own `resolution` constant, which
// this file cannot see — it lives in package postgres_test.
const testResolution = 8

// UpsertCatalog wraps pool.Begin's own failure — reached by closing a pool of
// this test's OWN, never the package's shared one: dbtest.NewPostgres hands
// out the same pool to every test in this binary, and closing it would break
// every test that runs after this one. Its Commit-failure and its "rollback
// failed with something other than ErrTxClosed" branches are NOT covered
// here: both need the underlying connection to die at a specific instant —
// after write() finishes but before Commit runs, or after Commit's own
// attempt — and there is no seam in the production code to arrange that
// without either adding a test-only hook to UpsertCatalog (a real code change
// to buy a test) or a genuinely racy pg_terminate_backend timed against a
// goroutine, which pins a race, not a behaviour.
func TestUpsertCatalogWrapsAFailureToBeginTheTransaction(t *testing.T) {
	dsn := dbtest.NewMigrationTarget(t)
	dbtest.MigrateUp(t, dsn)

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open a pool: %v", err)
	}
	pool.Close()

	repo := NewCatalogRepository(pool, testResolution)
	_, err = repo.UpsertCatalog(context.Background(), republishPatch("cat-closed-pool"), domain.UpdateModeFull, noopDerive)
	if err == nil || !strings.Contains(err.Error(), "begin the publish transaction") {
		t.Errorf("err = %v, want it naming the begin", err)
	}
}

// This file pins catalog_repository.go's error-wrap branches — every SQL call
// in write()/persist()/loadForMerge() and the three standalone reads wraps its
// own failure with a distinct message naming what it was doing, and none of
// those wraps had a test forcing the query underneath it to fail.
//
// package postgres, not postgres_test: write, persist, loadForMerge,
// deleteOmitted, pruneOrphanedOffers, clearGeometries, coverGeometries and
// geometryFault are all unexported, and the struct literals below reach past
// NewCatalogRepository/gen.New entirely.

// errRow is a pgx.Row whose Scan always fails — QueryRow's failure shape.
type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// errBatchResults is a pgx.BatchResults whose every operation fails —
// SendBatch's failure shape. Close is what a batch of ZERO items reaches,
// since Exec's per-item loop never runs; Exec is what a non-empty one does.
type errBatchResults struct{ err error }

func (b errBatchResults) Exec() (pgconn.CommandTag, error) { return pgconn.CommandTag{}, b.err }
func (b errBatchResults) Query() (pgx.Rows, error)         { return nil, b.err }
func (b errBatchResults) QueryRow() pgx.Row                { return errRow(b) }
func (b errBatchResults) Close() error                     { return b.err }

// txFailsAt wraps a real pgx.Tx and fails the Nth call across all four
// statement methods, counted together — write()'s own calls run in one fixed
// order per mode, so a single global count identifies exactly which one broke
// without needing a separate counter per method.
type txFailsAt struct {
	pgx.Tx
	calls int
	n     int
}

var errBoom = errors.New("boom")

func (f *txFailsAt) hit() bool {
	f.calls++
	return f.calls == f.n
}

func (f *txFailsAt) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if f.hit() {
		return pgconn.CommandTag{}, errBoom
	}
	return f.Tx.Exec(ctx, sql, args...)
}

func (f *txFailsAt) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if f.hit() {
		return nil, errBoom
	}
	return f.Tx.Query(ctx, sql, args...)
}

func (f *txFailsAt) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if f.hit() {
		return errRow{errBoom}
	}
	return f.Tx.QueryRow(ctx, sql, args...)
}

func (f *txFailsAt) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	if f.hit() {
		return errBatchResults{errBoom}
	}
	return f.Tx.SendBatch(ctx, b)
}

// beginTx opens a real transaction against a real Postgres.
//
// Rolled back by the CALLER immediately after use, not deferred to t.Cleanup:
// LockAndLoadCatalog takes a row lock, and a table-driven test opening a new
// one per case while the previous one is still open (only released at the
// whole test's Cleanup) would have every case after the first block forever
// on the one before it.
func beginTx(t *testing.T, pool dbtest.Pool) pgx.Tx {
	t.Helper()

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return tx
}

// seedCatalog publishes a catalog with one resource and one offer for real, so
// a MERGE republish afterwards has something to load — the load-branch calls
// (ListStoredResources/ListStoredOffers) only run in MERGE mode against an
// existing row.
func seedCatalog(t *testing.T, pool dbtest.Pool, id string) {
	t.Helper()

	repo := NewCatalogRepository(pool, testResolution)
	patch := domain.CatalogPatch{
		ID: id, NetworkID: "n1", Active: true, ProtocolVersion: beckn.Version, VisibleTo: []string{"n1"},
		Resources: []domain.ResourcePatch{{ID: "r1", Document: []byte(`{"id":"r1"}`)}},
		Offers:    []domain.OfferPatch{{ID: "o1", ResourceIDs: []string{"r1"}, Document: []byte(`{"id":"o1"}`)}},
	}
	if _, err := repo.UpsertCatalog(context.Background(), patch, domain.UpdateModeFull, noopDerive); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func noopDerive(*domain.Catalog, []string) []domain.Fault { return nil }

// republishPatch is a MERGE/FULL republish naming the same resource and
// offer, so every one of write()'s statements has real work: the resource and
// offer batches are non-empty, and the geometry batches run (even empty ones
// reach SendBatch — sqlc queues zero items and sends the batch anyway).
func republishPatch(id string) domain.CatalogPatch {
	return domain.CatalogPatch{
		ID: id, NetworkID: "n1", Active: true, ProtocolVersion: beckn.Version, VisibleTo: []string{"n1"},
		Resources: []domain.ResourcePatch{{ID: "r1", Document: []byte(`{"id":"r1"}`)}},
		Offers:    []domain.OfferPatch{{ID: "o1", ResourceIDs: []string{"r1"}, Document: []byte(`{"id":"o1"}`)}},
	}
}

// write's own call sequence, one failure point at a time, MERGE mode against
// an already-published catalog — the mode that exercises loadForMerge's two
// extra reads.
func TestWriteNamesWhicheverOfItsMergeModeQueriesFailed(t *testing.T) {
	pool := dbtest.NewPostgres(t)
	seedCatalog(t, pool, "cat-merge-fail")

	cases := []struct {
		call    int
		wantsay string
	}{
		{1, "lock the catalog row"},
		{2, "load the stored resources"},
		{3, "load the stored offers"},
		{4, "write the catalog row"},
		{5, "clear the catalog-level geometries"},
		{6, "clear the resource geometries"},
		{7, "write the resources"},
		{8, "write the geometries"},
		{9, "propagate the scope gate"},
		{10, "write the offers"},
		{11, "rebuild the filter composites"},
	}
	for _, testCase := range cases {
		tx := beginTx(t, pool)
		repo := &CatalogRepository{queries: gen.New(nil), resolution: testResolution}

		_, err := repo.write(context.Background(), &txFailsAt{Tx: tx, n: testCase.call},
			republishPatch("cat-merge-fail"), domain.UpdateModeMerge, noopDerive)
		if rollbackErr := tx.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			t.Fatalf("rollback: %v", rollbackErr)
		}
		if err == nil || !strings.Contains(err.Error(), testCase.wantsay) {
			t.Errorf("call %d failing: err = %v, want it naming %q", testCase.call, err, testCase.wantsay)
		}
	}
}

// The same, FULL mode — loadForMerge takes the short-circuit (no load reads),
// and persist gains the four FULL-only statements.
func TestWriteNamesWhicheverOfItsFullModeQueriesFailed(t *testing.T) {
	pool := dbtest.NewPostgres(t)
	seedCatalog(t, pool, "cat-full-fail")

	cases := []struct {
		call    int
		wantsay string
	}{
		{1, "lock the catalog row"},
		{2, "write the catalog row"},
		{3, "delete the resources this FULL republish omitted"},
		{4, "delete the offers this FULL republish omitted"},
		{5, "clear the catalog-level geometries"},
		{6, "clear the resource geometries"},
		{7, "write the resources"},
		{8, "write the geometries"},
		{9, "propagate the scope gate"},
		{10, "write the offers"},
		{11, "prune the orphaned offer references"},
		{12, "delete the offers the prune emptied"},
		{13, "rebuild the filter composites"},
	}
	for _, testCase := range cases {
		tx := beginTx(t, pool)
		repo := &CatalogRepository{queries: gen.New(nil), resolution: testResolution}

		_, err := repo.write(context.Background(), &txFailsAt{Tx: tx, n: testCase.call},
			republishPatch("cat-full-fail"), domain.UpdateModeFull, noopDerive)
		if rollbackErr := tx.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			t.Fatalf("rollback: %v", rollbackErr)
		}
		if err == nil || !strings.Contains(err.Error(), testCase.wantsay) {
			t.Errorf("call %d failing: err = %v, want it naming %q", testCase.call, err, testCase.wantsay)
		}
	}
}

// dbtxFailsAt is the gen.DBTX-level twin of txFailsAt, for the three reads
// that run outside any transaction: DeleteCatalog, GetCatalog and
// ListCatalogResources build their *gen.Queries straight from the repository's
// own pool rather than from WithTx.
type dbtxFailsAt struct {
	gen.DBTX
	calls int
	n     int
}

func (f *dbtxFailsAt) hit() bool {
	f.calls++
	return f.calls == f.n
}

func (f *dbtxFailsAt) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if f.hit() {
		return pgconn.CommandTag{}, errBoom
	}
	return f.DBTX.Exec(ctx, sql, args...)
}

func (f *dbtxFailsAt) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if f.hit() {
		return nil, errBoom
	}
	return f.DBTX.Query(ctx, sql, args...)
}

func (f *dbtxFailsAt) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if f.hit() {
		return errRow{errBoom}
	}
	return f.DBTX.QueryRow(ctx, sql, args...)
}

// DeleteCatalog wraps Exec's own failure.
func TestDeleteCatalogNamesItsOwnQueryFailure(t *testing.T) {
	pool := dbtest.NewPostgres(t)
	repo := &CatalogRepository{queries: gen.New(&dbtxFailsAt{DBTX: pool, n: 1})}

	err := repo.DeleteCatalog(context.Background(), "whatever")
	if err == nil || !strings.Contains(err.Error(), "delete catalog") {
		t.Errorf("err = %v, want it naming the delete", err)
	}
}

// GetCatalog reads four things in sequence — its own row, then the three
// ListX calls ListCatalogResources wraps as its own delegate — each isolated.
func TestGetCatalogNamesWhicheverOfItsFourReadsFailed(t *testing.T) {
	pool := dbtest.NewPostgres(t)
	seedCatalog(t, pool, "cat-get-fail")

	cases := []struct {
		call    int
		wantsay string
	}{
		{1, "read catalog"},
		{2, "list the resources"},
		{3, "read the offers"},
		{4, "read the geometries"},
	}
	for _, testCase := range cases {
		repo := &CatalogRepository{queries: gen.New(&dbtxFailsAt{DBTX: pool, n: testCase.call})}

		_, err := repo.GetCatalog(context.Background(), "cat-get-fail")
		if err == nil || !strings.Contains(err.Error(), testCase.wantsay) {
			t.Errorf("call %d failing: err = %v, want it naming %q", testCase.call, err, testCase.wantsay)
		}
	}
}

// ListCatalogResources's own failure, isolated from GetCatalog's use of it.
func TestListCatalogResourcesNamesItsOwnQueryFailure(t *testing.T) {
	pool := dbtest.NewPostgres(t)
	repo := &CatalogRepository{queries: gen.New(&dbtxFailsAt{DBTX: pool, n: 1})}

	_, err := repo.ListCatalogResources(context.Background(), "whatever")
	if err == nil || !strings.Contains(err.Error(), "list the resources") {
		t.Errorf("err = %v, want it naming the list", err)
	}
}

// GetCatalog's own NotFound translation — pgx.ErrNoRows becomes
// domain.ErrCatalogNotFound rather than a generic wrapped error, which is what
// lets a caller tell "absent" from "the read broke".
func TestGetCatalogOfAnAbsentCatalogIsErrCatalogNotFound(t *testing.T) {
	pool := dbtest.NewPostgres(t)
	repo := NewCatalogRepository(pool, testResolution)

	_, err := repo.GetCatalog(context.Background(), "does-not-exist")
	if !errors.Is(err, domain.ErrCatalogNotFound) {
		t.Errorf("err = %v, want domain.ErrCatalogNotFound", err)
	}
}

// geometryFault names the SourcePath, not the wildcard TargetPath — a
// publisher fixing a bad polygon needs to know WHICH availableAt entry it was.
func TestGeometryFaultNamesTheSourcePath(t *testing.T) {
	shape := domain.Geometry{SourcePath: "$.provider.availableAt[2].geo", TargetPath: "$.provider.availableAt[*].geo"}
	fault := geometryFault(shape, errBoom)

	if fault.Path != shape.SourcePath {
		t.Errorf("Path = %q, want the SourcePath %q", fault.Path, shape.SourcePath)
	}
	if fault.Code != string(beckn.CodeSchemaInvalidFormat) {
		t.Errorf("Code = %q, want %q", fault.Code, beckn.CodeSchemaInvalidFormat)
	}
	if !strings.Contains(fault.Message, "boom") {
		t.Errorf("Message = %q, want the underlying error included", fault.Message)
	}
}

// A shape whose GeoJSON will not even decode is a PARTIAL naming it, not an
// abort — coverGeometries is a pure function over an already-merged catalog,
// so this is reached directly rather than through a real publish, which the
// mapper's own validation would refuse before storage ever sees it.
func TestCoverGeometriesFaultsAShapeThatWillNotDecode(t *testing.T) {
	repo := &CatalogRepository{resolution: testResolution}
	merged := domain.Catalog{
		ID:         "cat-bad-geo",
		Geometries: []domain.Geometry{{SourcePath: "$.provider.availableAt[0].geo", GeoJSON: []byte("not json")}},
	}

	inserts, faults := repo.coverGeometries(merged, nil)
	if len(inserts) != 0 {
		t.Errorf("inserts = %v, want none — the shape never covered", inserts)
	}
	if len(faults) != 1 || faults[0].Path != "$.provider.availableAt[0].geo" {
		t.Fatalf("faults = %+v, want one naming the shape's SourcePath", faults)
	}
}

// A shape whose cover computes but whose bounds do not is errUnboundedGeometry
// — the box columns are NOT NULL, and for a shape at the pole the box IS the
// whole predicate (boundsOf refuses a box touching the pole, A15's own
// documented boundary), so a row with no box at all is undiscoverable rather
// than degraded. A Point exactly at the pole is what produces this: geo covers
// it (a single H3 cell) but boundsOf's own guard against MaxLat >= 90 refuses
// the box.
func TestCoverGeometriesFaultsAShapeAtThePoleAsUnbounded(t *testing.T) {
	repo := &CatalogRepository{resolution: testResolution}
	merged := domain.Catalog{
		ID: "cat-pole-geo",
		Geometries: []domain.Geometry{{
			SourcePath: "$.provider.availableAt[0].geo",
			GeoJSON:    []byte(`{"type":"Point","coordinates":[0,90]}`),
		}},
	}

	inserts, faults := repo.coverGeometries(merged, nil)
	if len(inserts) != 0 {
		t.Errorf("inserts = %v, want none — a shape with no bounding box is undiscoverable, not degraded", inserts)
	}
	if len(faults) != 1 || !strings.Contains(faults[0].Message, "bounding box") {
		t.Fatalf("faults = %+v, want one naming the missing bounding box", faults)
	}
}
