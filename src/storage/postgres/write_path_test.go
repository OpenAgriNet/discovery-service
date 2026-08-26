package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres"
	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// The assertions in this file are the ones the conformance suite CANNOT make.
// The suite speaks the two ports, so it can say what a catalog holds; it cannot
// say how many row versions holding it cost, how many round trips it took, or
// what two concurrent publishes do to each other. Those are the properties this
// adapter exists to get right, and the ones a refactor silently breaks.

// resourcePatch is the smallest patch that names a resource: an id and some
// attributes. The conformance fixtures build the same thing, but they are
// unexported to their own package, and importing a test fixture across a
// package boundary would make one file's convenience the other file's
// constraint.
func resourcePatch(id, attributes string) domain.ResourcePatch {
	return domain.ResourcePatch{ID: id, Attributes: json.RawMessage(attributes)}
}

// stampHash is a derive that gives every resource the SAME new hash on every
// publish, so a resource whose hash changed is a resource that was WRITTEN.
// A derive that skipped the untouched ones would make the untouched-rows
// assertion pass by not testing anything.
func stampHash(hash string) domain.DeriveFunc {
	return func(merged *domain.Catalog, _ []string) []domain.Fault {
		for index := range merged.Resources {
			merged.Resources[index].EmbeddingSourceHash = []byte(hash)
		}
		return nil
	}
}

func noDerive(*domain.Catalog, []string) []domain.Fault { return nil }

// dedicatedPool opens a pool of ONE connection against a database of this
// test's own.
//
// One connection because both callers below read per-backend state —
// pg_stat_force_next_flush flushes the calling backend's counters and nobody
// else's, so a pool free to answer the read on a second connection would report
// zero and read as "nothing was written". A private database because the
// counters it reads are cumulative for the whole database and a sibling test's
// writes would land in them.
func dedicatedPool(t *testing.T, tracer any) *pgxpool.Pool {
	t.Helper()

	dsn := dbtest.NewMigrationTarget(t)
	dbtest.MigrateUp(t, dsn)

	settings, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse the connection string: %v", err)
	}
	settings.MaxConns = 1
	settings.MinConns = 1
	settings.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_custom_plan"
	if queryTracer, ok := tracer.(pgx.QueryTracer); ok {
		settings.ConnConfig.Tracer = queryTracer
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), settings)
	if err != nil {
		t.Fatalf("open a pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// tupleUpdates reads how many resource tuples this database has updated in its
// lifetime. Only the DELTA across a publish means anything.
func tupleUpdates(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()

	ctx := context.Background()
	// Counters are accumulated per backend and flushed on a timer, so a read
	// taken straight after a write sees whatever happened to have been flushed
	// already — usually nothing.
	if _, err := pool.Exec(ctx, "SELECT pg_stat_force_next_flush()"); err != nil {
		t.Fatalf("flush the statistics counters: %v", err)
	}

	var updates int64
	err := pool.QueryRow(ctx,
		`SELECT coalesce(n_tup_upd, 0) FROM pg_stat_user_tables WHERE relname = 'resources'`).Scan(&updates)
	if err != nil {
		t.Fatalf("read the update counter for resources: %v", err)
	}
	return updates
}

func resourceXmin(t *testing.T, pool *pgxpool.Pool, catalogID, resourceID string) string {
	t.Helper()

	var version string
	err := pool.QueryRow(context.Background(),
		`SELECT xmin::TEXT FROM resources WHERE catalog_id = $1 AND id = $2`,
		catalogID, resourceID).Scan(&version)
	if err != nil {
		t.Fatalf("read xmin of %s/%s: %v", catalogID, resourceID, err)
	}
	return version
}

// A touched resource is written ONCE, not twice.
//
// Two resources, and a republish that names one of them while moving the gate.
// The touched one is written by the upsert; the untouched one is written by the
// gate propagate; neither is written by both. Three tuple updates instead of two
// is the regression — a second row version, a second WAL record, a dead tuple
// and a second insertion into a GIN index for a value that did not change,
// invisible in every functional test and visible in bloat a quarter later.
func TestATouchedResourceIsWrittenOnceNotTwice(t *testing.T) {
	pool := dedicatedPool(t, nil)
	repository := postgres.NewCatalogRepository(pool, resolution)
	ctx := context.Background()

	first := domain.CatalogPatch{
		ID: "c1", NetworkID: "n1", Active: true, VisibleTo: []string{"n1"},
		Resources: []domain.ResourcePatch{
			resourcePatch("r1", `{"grade":"A"}`),
			resourcePatch("r2", `{"grade":"B"}`),
		},
	}
	if _, err := repository.UpsertCatalog(ctx, first, domain.UpdateModeMerge, noDerive); err != nil {
		t.Fatalf("the first publish: %v", err)
	}

	before := tupleUpdates(t, pool)
	versionBefore := resourceXmin(t, pool, "c1", "r1")

	// Names r1 only, and moves the gate — so the propagate has real work to do
	// on r2 and would have apparent work to do on r1.
	second := domain.CatalogPatch{
		ID: "c1", NetworkID: "n1", Active: true, VisibleTo: []string{"n1", "n2"},
		Resources: []domain.ResourcePatch{resourcePatch("r1", `{"grade":"A+"}`)},
	}
	if _, err := repository.UpsertCatalog(ctx, second, domain.UpdateModeMerge, noDerive); err != nil {
		t.Fatalf("the republish: %v", err)
	}

	if updates := tupleUpdates(t, pool) - before; updates != 2 {
		t.Errorf("the republish updated %d resource tuples, want 2 — one for the touched "+
			"resource and one for the untouched one the gate propagate reaches", updates)
	}
	if versionAfter := resourceXmin(t, pool, "c1", "r1"); versionAfter == versionBefore {
		t.Errorf("r1 still carries xmin %s; the republish named it and did not write it", versionBefore)
	}
}

// roundTrips counts what the pool actually sends. A batch counts ONE however
// many statements it carries, which is the whole property being asserted: a
// pgx.Batch is one round trip, and a loop of Exec calls is one per entity.
type roundTrips struct {
	mutex sync.Mutex
	count int
}

func (r *roundTrips) TraceQueryStart(
	ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData,
) context.Context {
	r.add()
	return ctx
}

func (r *roundTrips) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *roundTrips) TraceBatchStart(
	ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData,
) context.Context {
	r.add()
	return ctx
}

func (r *roundTrips) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}

func (r *roundTrips) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}

func (r *roundTrips) add() {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.count++
}

func (r *roundTrips) read() int {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.count
}

// The statement count does not grow with the catalog.
//
// It is the only assertion that keeps a pgx.Batch from decaying into a loop the
// next time someone edits this file. A loop is not a correctness bug, so nothing
// else in the suite has any opinion about it — it is a bug that shows up as lock
// hold time under a large catalog, which is precisely where it is hardest to
// diagnose.
func TestTheStatementCountDoesNotGrowWithTheCatalog(t *testing.T) {
	counter := &roundTrips{}
	pool := dedicatedPool(t, counter)
	repository := postgres.NewCatalogRepository(pool, resolution)
	ctx := context.Background()

	publish := func(catalogID string, resources int) int {
		patch := domain.CatalogPatch{ID: catalogID, NetworkID: "n1", Active: true}
		for index := range resources {
			patch.Resources = append(patch.Resources,
				resourcePatch(fmt.Sprintf("r%d", index), `{"grade":"A"}`))
		}

		before := counter.read()
		if _, err := repository.UpsertCatalog(ctx, patch, domain.UpdateModeMerge, noDerive); err != nil {
			t.Fatalf("publishing %d resources: %v", resources, err)
		}
		return counter.read() - before
	}

	small := publish("c-small", 5)
	large := publish("c-large", 50)

	if large != small {
		t.Errorf("a 50-resource publish cost %d round trips and a 5-resource one cost %d; "+
			"they must be equal — every per-entity loop goes out as one batch", large, small)
	}
}

// A mid-transaction failure leaves no partial catalog.
//
// The catalog row is written first and the resources after it, so a resource
// the database refuses is exactly the case where a missing rollback shows: the
// catalog would exist, empty, and read as a publisher who published nothing
// rather than as a publisher whose publish failed.
func TestAMidTransactionFailureLeavesNoPartialCatalog(t *testing.T) {
	pool := dbtest.NewPostgres(t)
	repository := postgres.NewCatalogRepository(pool, resolution)
	ctx := context.Background()

	// `CHECK (id <> '')` on resources. The check is the schema's, not this
	// package's, which is what makes it a genuine mid-transaction failure
	// rather than a fault this code raised on purpose.
	patch := domain.CatalogPatch{
		ID: "c1", NetworkID: "n1", Active: true,
		Resources: []domain.ResourcePatch{resourcePatch("", `{"grade":"A"}`)},
	}
	if _, err := repository.UpsertCatalog(ctx, patch, domain.UpdateModeMerge, noDerive); err == nil {
		t.Fatal("a resource with an empty id was accepted; the schema's CHECK should have refused it")
	}

	if _, err := repository.GetCatalog(ctx, "c1"); !errors.Is(err, domain.ErrCatalogNotFound) {
		t.Fatalf("GetCatalog after the failed publish = %v, want ErrCatalogNotFound — "+
			"the catalog row survived a transaction that did not commit", err)
	}
}

// Only `touched` resources are rewritten.
//
// Forty resources, one named. This is the test that catches a re-embed of the
// whole catalog on a one-field patch: a cost bug, not a correctness bug, and
// therefore invisible everywhere else. Both columns, because `updated_at` alone
// would pass a repository that rewrote the row without recomputing the
// embedding, and the hash alone would pass one that recomputed it and wrote
// nothing.
func TestOnlyTouchedResourcesAreRewritten(t *testing.T) {
	pool := dbtest.NewPostgres(t)
	repository := postgres.NewCatalogRepository(pool, resolution)
	ctx := context.Background()

	patch := domain.CatalogPatch{ID: "c1", NetworkID: "n1", Active: true}
	for index := range 40 {
		patch.Resources = append(patch.Resources,
			resourcePatch(fmt.Sprintf("r%02d", index), `{"grade":"A"}`))
	}
	if _, err := repository.UpsertCatalog(ctx, patch, domain.UpdateModeMerge, stampHash("first")); err != nil {
		t.Fatalf("the first publish: %v", err)
	}

	type row struct {
		updated string
		hash    string
	}
	read := func() map[string]row {
		rows, err := pool.Query(ctx,
			`SELECT id, updated_at::TEXT, encode(embedding_source_hash, 'escape')
			   FROM resources WHERE catalog_id = $1`, "c1")
		if err != nil {
			t.Fatalf("read the resources: %v", err)
		}
		defer rows.Close()

		state := map[string]row{}
		for rows.Next() {
			var id string
			var stored row
			if err := rows.Scan(&id, &stored.updated, &stored.hash); err != nil {
				t.Fatalf("scan a resource: %v", err)
			}
			state[id] = stored
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read the resources: %v", err)
		}
		return state
	}
	before := read()

	// The gate does not move, so the propagate has nothing to do and the only
	// writer left is the upsert. A republish that also moved the gate would
	// rewrite all forty for a reason this test is not about.
	republish := domain.CatalogPatch{
		ID: "c1", NetworkID: "n1", Active: true,
		Resources: []domain.ResourcePatch{resourcePatch("r07", `{"grade":"A+"}`)},
	}
	if _, err := repository.UpsertCatalog(ctx, republish, domain.UpdateModeMerge, stampHash("second")); err != nil {
		t.Fatalf("the republish: %v", err)
	}
	after := read()

	if after["r07"].hash != "second" {
		t.Errorf("the touched resource carries hash %q, want \"second\"", after["r07"].hash)
	}
	for id, stored := range before {
		if id == "r07" {
			continue
		}
		if after[id].updated != stored.updated {
			t.Fatalf("%s moved its updated_at from %s to %s; the patch never named it",
				id, stored.updated, after[id].updated)
		}
		if after[id].hash != stored.hash {
			t.Fatalf("%s was re-embedded (hash %q became %q); the patch never named it",
				id, stored.hash, after[id].hash)
		}
	}
}

// A FULL republish deletes an offer whose resources are gone rather than
// leaving it holding an empty `resource_ids`.
//
// An empty `resource_ids` is not "no resources", it is EVERY resource: an offer
// emptied rather than deleted would silently attach to the provider's entire
// inventory, and there is no foreign key on the column to catch it. That
// OUTCOME is what this pins, deliberately and not the statement order that
// produces it — see the note in `pruneOrphanedOffers` for why the order is not
// observable from here.
func TestAFullRepublishDeletesAnOfferWhoseResourcesAreGone(t *testing.T) {
	pool := dbtest.NewPostgres(t)
	repository := postgres.NewCatalogRepository(pool, resolution)
	ctx := context.Background()

	first := domain.CatalogPatch{
		ID: "c1", NetworkID: "n1", Active: true,
		Resources: []domain.ResourcePatch{
			resourcePatch("r1", `{"grade":"A"}`),
			resourcePatch("r2", `{"grade":"B"}`),
		},
		Offers: []domain.OfferPatch{
			{ID: "o1", Document: json.RawMessage(`{"price":"10"}`), ResourceIDs: []string{"r1"}},
		},
	}
	if _, err := repository.UpsertCatalog(ctx, first, domain.UpdateModeMerge, noDerive); err != nil {
		t.Fatalf("the first publish: %v", err)
	}

	// FULL, dropping r1. o1 named it and nothing else.
	second := domain.CatalogPatch{
		ID: "c1", NetworkID: "n1", Active: true,
		Resources: []domain.ResourcePatch{resourcePatch("r2", `{"grade":"B"}`)},
		Offers: []domain.OfferPatch{
			{ID: "o1", Document: json.RawMessage(`{"price":"10"}`), ResourceIDs: []string{"r1"}},
		},
	}
	faults, err := repository.UpsertCatalog(ctx, second, domain.UpdateModeFull, noDerive)
	if err != nil {
		t.Fatalf("the FULL republish: %v", err)
	}
	if len(faults) != 1 || faults[0].Code != string(beckn.CodeBusinessItemNotFound) {
		t.Fatalf("the FULL republish returned %v, want one %s — the offer named a resource "+
			"the republish dropped", faults, beckn.CodeBusinessItemNotFound)
	}

	var offers int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM offers WHERE catalog_id = $1`, "c1").Scan(&offers); err != nil {
		t.Fatalf("count the offers: %v", err)
	}
	if offers != 0 {
		var wide int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM offers
			  WHERE catalog_id = $1 AND cardinality(resource_ids) = 0`, "c1").Scan(&wide); err != nil {
			t.Fatalf("count the catalog-wide offers: %v", err)
		}
		t.Fatalf("the catalog still holds %d offers, %d of them catalog-wide; an offer whose "+
			"every resource is gone must be deleted, not emptied", offers, wide)
	}
}

// Two concurrent republishes, each patching a different attribute, end with
// BOTH attributes present.
//
// The catalog's row lock is what makes that true. Without it the two overlap as
// read-modify-writes and the second one commits a document that never contained
// the first one's field — a lost update that no single-threaded test can see and
// that production produces the first time two publishers share a catalog.
func TestTwoConcurrentRepublishesBothSurvive(t *testing.T) {
	pool := dbtest.NewPostgres(t)
	repository := postgres.NewCatalogRepository(pool, resolution)
	ctx := context.Background()

	seed := domain.CatalogPatch{
		ID: "c1", NetworkID: "n1", Active: true,
		Resources: []domain.ResourcePatch{resourcePatch("r1", `{"grade":"A"}`)},
	}
	if _, err := repository.UpsertCatalog(ctx, seed, domain.UpdateModeMerge, noDerive); err != nil {
		t.Fatalf("the seed publish: %v", err)
	}

	patches := []string{`{"moisture":"12%"}`, `{"origin":"Kolar"}`}
	errs := make([]error, len(patches))

	var start, done sync.WaitGroup
	start.Add(1)
	for index, attributes := range patches {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait() // both goroutines enter the write at once, not in turn
			_, errs[index] = repository.UpsertCatalog(ctx, domain.CatalogPatch{
				ID: "c1", NetworkID: "n1", Active: true,
				Resources: []domain.ResourcePatch{resourcePatch("r1", attributes)},
			}, domain.UpdateModeMerge, noDerive)
		}()
	}
	start.Done()
	done.Wait()

	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent republish %d: %v", index, err)
		}
	}

	stored, err := repository.GetCatalog(ctx, "c1")
	if err != nil {
		t.Fatalf("GetCatalog after the concurrent republishes: %v", err)
	}
	if len(stored.Resources) != 1 {
		t.Fatalf("the catalog holds %d resources, want 1", len(stored.Resources))
	}

	var attributes map[string]any
	if err := json.Unmarshal(stored.Resources[0].Attributes, &attributes); err != nil {
		t.Fatalf("the stored attributes are not an object: %v (%s)", err, stored.Resources[0].Attributes)
	}
	if attributes["moisture"] != "12%" || attributes["origin"] != "Kolar" {
		t.Errorf("the resource holds %v, want both moisture and origin — one republish "+
			"overwrote the other's field with a document that never contained it", attributes)
	}
	if attributes["grade"] != "A" {
		t.Errorf("grade is %v, want A — neither republish named it", attributes["grade"])
	}
}
