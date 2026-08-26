package dbtest_test

import (
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// Each predicate is phrased the way the discover query phrases it —
// `$1 IS NULL OR <indexable predicate>` — because that shape is what makes the
// six executions worth running: a plan built without the parameter values
// cannot fold the `IS NULL` arm away and so cannot reach the index behind it,
// as TestAGenericPlanCannotReachTheCellIndex below demonstrates.
//
// Counted from the index's own scan counter, not read off an EXPLAIN. Six
// ordinary executions move the counter by six; an EXPLAIN would have been
// re-planned with the values in hand and could not have told us anything about
// the sixth.
func TestTheDiscoverPredicatesReachTheirIndexes(t *testing.T) {
	pool := dbtest.NewPostgres(t)

	cases := []struct {
		name  string
		sql   string
		arg   any
		index string
		why   string
	}{
		{
			// S_INTERSECTS, S_DWITHIN and S_OVERLAPS all read the cover.
			name:  "the overlap arm of the cell algebra",
			sql:   `SELECT 1 FROM resource_geometries g WHERE ($1::bigint[] IS NULL OR g.cells_cover && $1::bigint[])`,
			arg:   []int64{1, 2, 3},
			index: "idx_rg_cells_cover",
			why:   "S_INTERSECTS, S_DWITHIN and S_OVERLAPS are all phrased over cells_cover",
		},
		{
			// S_WITHIN and S_DISJOINT read the full cover, and `<@` is the half
			// whose selectivity the planner estimates worst — so an EXPLAIN
			// asserting only the overlap case leaves half the operator set
			// unproven.
			name:  "the containment arm of the cell algebra",
			sql:   `SELECT 1 FROM resource_geometries g WHERE ($1::bigint[] IS NULL OR $1::bigint[] <@ g.cells_full)`,
			arg:   []int64{1},
			index: "idx_rg_cells_full",
			why: "S_WITHIN and S_DISJOINT are phrased over cells_full, and <@ is the " +
				"half whose selectivity the planner estimates worst",
		},
		{
			name:  "the scope gate",
			sql:   `SELECT 1 FROM resources r WHERE ($1::text[] IS NULL OR r.visible_to && $1::text[])`,
			arg:   []string{"bap.example.com"},
			index: "idx_resources_visible_to",
			why:   "every read carries the scope gate, including count(*)",
		},
		{
			name: "the schema filter",
			sql: `SELECT 1 FROM resources r
			      WHERE r.active AND ($1::text IS NULL OR r.schema_context = $1::text)`,
			arg:   "https://beckn.org/Agri",
			index: "idx_resources_schema",
			why:   "C4 makes @context and @type filter columns, matched exactly",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			scans, plan := dbtest.IndexScansOverSixExecutions(t, pool, testCase.index, testCase.sql, testCase.arg)
			if scans != 6 {
				t.Errorf("six executions produced %d scans of %s, want 6 (%s):\n%s",
					scans, testCase.index, testCase.why, plan)
			}
		})
	}
}

// The pool's own setting, asserted directly — and it has to be asserted
// directly, which is not what the plan expected.
//
// The plan's reasoning was that six executions would distinguish
// force_custom_plan from its absence, because the fifth is where PostgreSQL
// switches to a generic plan. Measured, it does not: `auto` COSTS the generic
// plan against the average custom one and keeps the custom plan when the
// generic is worse, which for `$1 IS NULL OR <indexable>` it always is. So the
// tests above pass identically under `auto`, and only this assertion pins the
// setting. The six executions stay — they cost nothing and they still catch a
// future PostgreSQL whose costing goes the other way — but they are not the
// thing doing the pinning.
func TestThePoolForcesACustomPlan(t *testing.T) {
	pool := dbtest.NewPostgres(t)

	if mode := dbtest.PlanCacheMode(t, pool); mode != "force_custom_plan" {
		t.Errorf("plan_cache_mode is %q, want force_custom_plan", mode)
	}
}

// Why the setting is there at all, demonstrated rather than asserted from the
// outside: planned WITHOUT the parameter values, the cell predicate cannot fold
// its `$1 IS NULL` arm away, cannot reach the GIN index behind it, and falls to
// a sequential scan even with sequential scans disabled.
//
// This is the failure the pool's force_custom_plan prevents, and it is the one
// nobody would attribute — the query is correct, the index exists, and the
// service is simply slow. If a later PostgreSQL learns to reach the index from
// a generic plan, this test reports the good news.
func TestAGenericPlanCannotReachTheCellIndex(t *testing.T) {
	pool := dbtest.NewPostgres(t)

	plan := dbtest.GenericPlan(t, pool,
		`SELECT 1 FROM resource_geometries g WHERE ($1::bigint[] IS NULL OR g.cells_cover && $1::bigint[])`)

	if strings.Contains(plan, "idx_rg_cells_cover") {
		t.Errorf("a generic plan reached idx_rg_cells_cover after all — good news, and the "+
			"pool's force_custom_plan may no longer be load-bearing:\n%s", plan)
	}
	if !strings.Contains(plan, "Seq Scan") {
		t.Errorf("expected a generic plan to fall to a sequential scan:\n%s", plan)
	}
}
