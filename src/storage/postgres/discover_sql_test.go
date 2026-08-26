package postgres_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// This file asserts against the SQL SOURCE rather than against query results,
// and that is the point of it. A retriever that drops the scope gate still
// answers every functional test correctly on a corpus where nothing is out of
// scope — and the corpus where something is out of scope is the one nobody
// thought to write, because the leak it produces is invisible from inside the
// query. So the gate is pinned as a property of the text: present in every
// query that reads `resources`, whatever that query is for.

// namedQuery is one `-- name: X :kind` block of a .sql file, with its body.
//
// The kind is matched but not kept: nothing here asserts on `:many` vs `:one`,
// and a field no test reads is a field that can go stale without anything
// noticing.
type namedQuery struct {
	name string
	body string
}

// queryMarker matches sqlc's own annotation, which is the only thing in these
// files that reliably separates one statement from the next: statements may
// carry semicolons inside a string or a `$$` body, so splitting on `;` would
// cut a query in half and assert against the pieces.
var queryMarker = regexp.MustCompile(`(?m)^-- name: (\w+) :(\w+)\s*$`)

// namedQueries splits a query file into its statements.
func namedQueries(t *testing.T, path string) []namedQuery {
	t.Helper()

	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	markers := queryMarker.FindAllStringSubmatchIndex(string(source), -1)
	if len(markers) == 0 {
		t.Fatalf("%s holds no named query — the marker format must have changed", path)
	}

	queries := make([]namedQuery, 0, len(markers))
	for index, marker := range markers {
		end := len(source)
		if index+1 < len(markers) {
			end = markers[index+1][0]
		}
		queries = append(queries, namedQuery{
			name: string(source[marker[2]:marker[3]]),
			body: string(source[marker[1]:end]),
		})
	}
	return queries
}

// discoverQueries is the file this whole test file is about.
const discoverQueries = "queries/discover.sql"

// The five clauses of the scope gate, spelled as the plan spells them.
//
// `network_id` is listed in its NULLABLE form on purpose. A query that
// hard-codes `visible_to @> ARRAY[$1]` still gates every row it returns, so no
// leak test catches it — it silently makes single-network scoping the default
// and drops every unscoped caller's results to one network. Matching the
// `sqlc.narg` spelling is the only way to tell the two apart from here.
var scopeGate = []string{
	"sqlc.narg('network_id')::text IS NULL",
	"r.visible_to @> ARRAY[sqlc.narg('network_id')::text]",
	"r.active",
	"r.valid_from IS NULL OR r.valid_from <= now()",
	"r.valid_to   IS NULL OR r.valid_to   >= now()",
	"within_daily_window(r.valid_time_from, r.valid_time_to",
}

// A query that reads `resources` is a query that can leak one.
func readsResources(query namedQuery) bool {
	return strings.Contains(query.body, "FROM resources r")
}

// retrieves reports whether this is one of the queries the page is decided by —
// the three retrievers and the counter. They are the queries that must ALSO
// carry the spatial and schema predicates, because a candidate the retriever
// admits is a candidate the page can contain, and the hydrator is keyed by
// primary key and narrows nothing.
func retrieves(query namedQuery) bool {
	return strings.HasSuffix(query.name, "Candidates")
}

func TestEveryDiscoverQueryOverResourcesCarriesTheScopeGate(t *testing.T) {
	gated := 0
	for _, query := range namedQueries(t, discoverQueries) {
		if !readsResources(query) {
			continue
		}
		gated++

		for _, clause := range scopeGate {
			if !strings.Contains(query.body, clause) {
				t.Errorf("%s reads resources but does not carry %q", query.name, clause)
			}
		}
	}

	// Without this the test passes on a file with no queries in it at all, and
	// would keep passing if `FROM resources r` were re-aliased to dodge it.
	if gated < 5 {
		t.Fatalf("only %d discover queries read `FROM resources r`; the three retrievers, "+
			"the counter and the hydrator all must", gated)
	}
}

func TestEveryRetrievalQueryCarriesTheSpatialAndSchemaPredicates(t *testing.T) {
	required := []string{
		// The spatial EXISTS, and the two halves of it a mistake removes
		// separately: the operator switch, and the exact refinement.
		"sqlc.narg('spatial_op')::text IS NULL",
		"@geo_negate::boolean <> EXISTS",
		"@match_negate::boolean <> (",
		"geo_distance_m(",
		// The schema pair. Two independent IN lists return the cross-match
		// this paired form exists to refuse: {(beckn, Item), (fsm, Service)}
		// must not admit an fsm Item.
		//
		// The pairing is spelled as two ordinality-numbered unnests joined on
		// that number, because sqlc v1.30's analyzer has no 2-argument
		// `unnest` and rejects `unnest(a, b) AS s(ctx, typ)` outright.
		// `USING (n)` is therefore the whole pin: drop it and the two lists
		// become a cross join, which is precisely the bug.
		"sqlc.narg('schema_contexts')::text[] IS NULL",
		"cardinality(sqlc.narg('schema_contexts')::text[]) = 0",
		"WITH ORDINALITY AS sc(ctx, n)",
		"WITH ORDINALITY AS st(typ, n) USING (n)",
		"st.typ = '' OR r.schema_type = st.typ",
	}

	retrievers := 0
	for _, query := range namedQueries(t, discoverQueries) {
		if !retrieves(query) && !strings.HasPrefix(query.name, "Count") {
			continue
		}
		retrievers++

		for _, clause := range required {
			if !strings.Contains(query.body, clause) {
				t.Errorf("%s decides candidates but does not carry %q", query.name, clause)
			}
		}
	}

	// Three retrievers and one counter. A mode dropped from the file is a mode
	// silently missing from every fused page.
	if retrievers != 4 {
		t.Fatalf("found %d candidate-deciding queries, want 4 — lexical, fuzzy, "+
			"semantic and the counter", retrievers)
	}
}

// The counter's text clause is the OR of every mode's, and that is what makes
// Total the size of the set the fusion draws from. A counter carrying one
// mode's clause returns a number no page can be paginated out of: fewer results
// than page 1 already showed, or more than exist.
//
// Asserted as "names every mode's parameter" rather than "contains every mode's
// operator", because semantic's operator is a DISTANCE and a distance is not a
// membership test. The pool the HNSW retriever draws its top-N from is every
// embedded row, so that is what the counter counts.
func TestTheCounterCarriesEveryModesTextClause(t *testing.T) {
	for _, query := range namedQueries(t, discoverQueries) {
		if !strings.HasPrefix(query.name, "Count") {
			continue
		}
		for _, clause := range []string{
			"r.search_tsv @@ discover_tsquery(",
			"r.name %",
			"sqlc.narg('query_vector')::vector IS NOT NULL",
		} {
			if !strings.Contains(query.body, clause) {
				t.Errorf("%s does not carry %q — Total would not count the union", query.name, clause)
			}
		}
		return
	}
	t.Fatal("discover.sql holds no counter")
}
