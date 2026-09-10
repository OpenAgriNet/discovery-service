package dbtest_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// indexesOn reads the index names PostgreSQL actually built, which is the only
// authority here: a migration that names an index it fails to create, or an
// opclass the operator does not match, is invisible in the .sql file and
// visible only in the catalog.
func indexesOn(t *testing.T, pool dbtest.Pool, table string) []string {
	t.Helper()

	rows, err := pool.Query(context.Background(),
		`SELECT indexname FROM pg_indexes WHERE schemaname = 'public' AND tablename = $1`, table)
	if err != nil {
		t.Fatalf("read the indexes on %s: %v", table, err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan an index name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the indexes on %s: %v", table, err)
	}
	sort.Strings(names)
	return names
}

// The exact set, by name, on every table. An index silently dropped in a
// migration is the kind of regression that shows up as a latency page rather
// than a test failure — nothing about a missing index is wrong, only slow, and
// slow at a size no test corpus reaches.
//
// Asserted as an EXACT set rather than a subset, so an index added without a
// justification also fails here. Every one of these costs write throughput on
// the publish path, which is the side of the trade nobody feels while adding it.
func TestTheIndexInventoryIsExactlyWhatTheMigrationsBuild(t *testing.T) {
	pool := dbtest.NewPostgres(t)

	want := map[string][]string{
		// The primary key's own btree, which PostgreSQL builds whether or not
		// this plan wanted it, and nothing else. Catalogs are reached by id or
		// by a join from a resource that was already found — and since A18 a
		// catalog-level filter is answered from the resource's own composite,
		// so there is no read that enters through catalogs.document.
		"catalogs": {"catalogs_pkey"},

		"resources": {
			"idx_resources_embedding",
			// The ONE index the whole attribute filter resolves through (A18),
			// at every level: catalog, resource and offer members all live in
			// the composite it covers.
			"idx_resources_filter_doc",
			"idx_resources_name_trgm",
			"idx_resources_schema",
			"idx_resources_search_tsv",
			"idx_resources_visible_to",
			"resources_pkey",
		},

		"resource_geometries": {
			"idx_rg_catalog_resource",
			"idx_rg_catalog_target_path",
			"idx_rg_cells_cover",
			"idx_rg_cells_full",
			"uq_resource_geometries",
		},

		"offers": {
			"idx_offers_resource_ids",
			"offers_pkey",
		},
	}

	for table, expected := range want {
		got := indexesOn(t, pool, table)
		if strings.Join(got, ",") != strings.Join(expected, ",") {
			t.Errorf("indexes on %s:\n got  %v\n want %v", table, got, expected)
		}
	}
}

// The absence list, named one at a time so a failure carries the reason the
// index is missing rather than just the fact. The exact-set test above already
// fails if any of these appears; this one fails with an explanation, which is
// what stops the next person re-adding it.
func TestTheIndexesThisPlanDeliberatelyDoesNotBuildAreAbsent(t *testing.T) {
	pool := dbtest.NewPostgres(t)

	absent := []struct {
		table, index, because string
	}{
		{
			"resource_geometries", "idx_rg_bbox",
			"the bounding box is a REFINEMENT applied to rows the cell indexes already " +
				"selected, never a way in. Indexing four independent DOUBLE PRECISION " +
				"columns buys a scan the cell predicate has already narrowed, and costs " +
				"every publish the write",
		},
		{
			"resources", "idx_resources_catalog_id",
			"resources is keyed (catalog_id, id), so resources_pkey's own btree already " +
				"leads with catalog_id and serves every prefix scan this would have",
		},
		{
			"offers", "idx_offers_catalog_id",
			"offers is keyed (catalog_id, id) for the same reason; offers_pkey already " +
				"leads with catalog_id",
		},
		{
			"catalogs", "idx_catalogs_document",
			"the attribute filter runs against ONE column (A18) and it is not this one. A " +
				"jsonpath is evaluated against a single jsonb value, so a predicate " +
				"crossing catalog and offer members could never have been split across " +
				"per-table indexes; catalogs.document is copied into every resource's " +
				"filter_doc instead, and this index would be paid for on every publish " +
				"to serve a read that no longer arrives",
		},
		{
			"resources", "idx_resources_document",
			"superseded by idx_resources_filter_doc, which covers a SUPERSET of it: the " +
				"composite carries this resource's document verbatim alongside its " +
				"catalog's and its offers'. Two GIN indexes over the same bytes is the " +
				"write cost of one of them for nothing",
		},
		{
			"offers", "idx_offers_document",
			"same as the other two: an offer's members reach the filter through the " +
				"composite of every resource it names, which is where A18 pays its " +
				"write amplification and collects the read",
		},
	}

	for _, entry := range absent {
		for _, built := range indexesOn(t, pool, entry.table) {
			if built == entry.index {
				t.Errorf("%s exists on %s, and should not: %s", entry.index, entry.table, entry.because)
			}
		}
	}
}

// seedCatalog gives the geometry rows below something to reference. The three
// CHECK tests insert with resource_id NULL, which is a catalog-level geometry
// and, under MATCH SIMPLE, leaves the composite foreign key to resources
// satisfied without a resource row existing.
func seedCatalog(t *testing.T, pool dbtest.Pool, id string) {
	t.Helper()

	if _, err := pool.Exec(context.Background(),
		`INSERT INTO catalogs (id) VALUES ($1)`, id); err != nil {
		t.Fatalf("seed catalog %q: %v", id, err)
	}
}

// insertGeometry attempts one resource_geometries row and returns the error
// rather than failing, because every caller below is asserting that it fails.
func insertGeometry(pool dbtest.Pool, catalogID string, cellsFull, cellsCover []int64,
	minLat, maxLat, minLon, maxLon float64,
) error {
	_, err := pool.Exec(context.Background(),
		`INSERT INTO resource_geometries
		   (catalog_id, resource_id, target_path, source_path, geojson,
		    cells_full, cells_cover, min_lat, max_lat, min_lon, max_lon)
		 VALUES ($1, NULL, '$.provider.locations[*].gps', '$.provider.locations[0].gps',
		         '{"type":"Point","coordinates":[77.64,12.97]}',
		         $2, $3, $4, $5, $6, $7)`,
		catalogID, cellsFull, cellsCover, minLat, maxLat, minLon, maxLon)
	return err
}

// A cell pair is written by one H3 fill or by neither. Half of one means the
// row was built by a path that stopped mid-way, and the operator CASE tests
// `cells_cover IS NULL` alone and never re-tests `cells_full` — so a row with a
// cover and no full would be read as fully indexed and answer S_WITHIN from a
// column that was never filled.
func TestAHalfNullCellPairIsRejected(t *testing.T) {
	pool := dbtest.NewPostgres(t)
	seedCatalog(t, pool, "half-null")

	err := insertGeometry(pool, "half-null", nil, []int64{1, 2}, 12.9, 13.0, 77.5, 77.7)
	if err == nil {
		t.Fatal("a row with cells_cover but no cells_full was accepted")
	}
	if !strings.Contains(err.Error(), "resource_geometries_check") {
		t.Errorf("rejected by %v, not by the cell-pair CHECK", err)
	}

	if err := insertGeometry(pool, "half-null", []int64{1}, nil, 12.9, 13.0, 77.5, 77.7); err == nil {
		t.Fatal("a row with cells_full but no cells_cover was accepted")
	}
}

// The one that matters most. `'{}' <@ anything` is TRUE, so an empty cover
// would silently answer S_WITHIN — and every other refutation phrased over
// `cells_cover <@ …` — with "cannot refute", turning the cell algebra's
// guaranteed-superset half into a constant TRUE. cells_full is legitimately
// empty (a Point contains no cell); a cover never is, because every geometry
// touches at least the cell it sits in.
func TestAnEmptyCellCoverIsRejected(t *testing.T) {
	pool := dbtest.NewPostgres(t)
	seedCatalog(t, pool, "empty-cover")

	err := insertGeometry(pool, "empty-cover", []int64{}, []int64{}, 12.9, 13.0, 77.5, 77.7)
	if err == nil {
		t.Fatal("a row with an empty cells_cover was accepted; '{}' <@ anything is TRUE, " +
			"so it would answer every cover refutation with 'cannot refute'")
	}
	if !strings.Contains(err.Error(), "cells_cover_check") {
		t.Errorf("rejected by %v, not by the empty-cover CHECK", err)
	}
}

// An inverted box matches nothing, which is the failure mode that never
// reports itself: the geometry is stored, the publish succeeds, and the
// resource is simply absent from every spatial search forever.
func TestAnInvertedBoundingBoxIsRejected(t *testing.T) {
	pool := dbtest.NewPostgres(t)
	seedCatalog(t, pool, "inverted-box")

	err := insertGeometry(pool, "inverted-box", []int64{}, []int64{1}, 12.9, 13.0, 77.7, 77.5)
	if err == nil {
		t.Fatal("a row with min_lon > max_lon was accepted")
	}
	if !strings.Contains(err.Error(), "resource_geometries_check1") {
		t.Errorf("rejected by %v, not by the bounding-box CHECK", err)
	}

	if err := insertGeometry(pool, "inverted-box", []int64{}, []int64{1}, 13.0, 12.9, 77.5, 77.7); err == nil {
		t.Fatal("a row with min_lat > max_lat was accepted")
	}
}

// Down is asserted to be the reverse of up, not merely to run. A down migration
// that drops a table but forgets a function leaves the next up failing on
// "already exists" — in a rollback, which is the one moment nobody has time to
// read a migration.
//
// Run against a database of its own: the shared one is migrated once for the
// whole package, and rolling it back underneath the other tests would make this
// test's failure mode "every other test in the package".
func TestUpThenDownThenUpLeavesNoResidue(t *testing.T) {
	target := dbtest.NewMigrationTarget(t)

	dbtest.MigrateUp(t, target)
	before := dbtest.SchemaObjects(t, target)
	if len(before) == 0 {
		t.Fatal("the first up built nothing")
	}

	dbtest.MigrateDown(t, target)
	if residue := dbtest.SchemaObjects(t, target); len(residue) != 0 {
		t.Errorf("down left %v behind", residue)
	}

	dbtest.MigrateUp(t, target)
	after := dbtest.SchemaObjects(t, target)
	if strings.Join(before, ",") != strings.Join(after, ",") {
		t.Errorf("the second up built a different schema:\n first  %v\n second %v", before, after)
	}
}
