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
// the three retrievers. They are the queries that must ALSO carry the spatial,
// schema and attribute-filter predicates, because a candidate the retriever
// admits is a candidate the page can contain, and the hydrator is keyed by
// primary key and narrows nothing.
//
// There is no counter to include (A19), which is why nothing here special-cases
// a `Count` prefix any more.
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
	if gated < 4 {
		t.Fatalf("only %d discover queries read `FROM resources r`; the three retrievers "+
			"and the hydrator all must", gated)
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
		if !retrieves(query) {
			continue
		}
		retrievers++

		for _, clause := range required {
			if !strings.Contains(query.body, clause) {
				t.Errorf("%s decides candidates but does not carry %q", query.name, clause)
			}
		}
	}

	// A mode dropped from the file is a mode silently missing from every fused
	// page.
	if retrievers != 3 {
		t.Fatalf("found %d candidate-deciding queries, want 3 — lexical, fuzzy "+
			"and semantic", retrievers)
	}
}

// The attribute filter is pinned as SOURCE for a reason no result can supply.
//
// A retriever that drops the `@?` clause returns MORE rows, not fewer, and the
// fusion then hands them to a page that looks perfectly ordinary. A functional
// test only catches that if its corpus happens to hold a row the filter should
// have excluded AND that row happens to reach the page — so the day the clause
// goes missing from one of three near-identical queries is the day nothing
// fails. Hence: present in every query that decides candidates, spelled the one
// way that is correct.
//
// The spelling matters as much as the presence:
//
//	filter_doc   — not `document`. `document` is one level of one object; the
//	               composite is the only value a cross-level predicate can be
//	               evaluated against (A18), and `document @? …` would silently
//	               answer a narrower question.
//	@?           — never `@@`. `@@` takes a PREDICATE expression, and pairing
//	               it with the filter form this service accepts matches ZERO
//	               rows, while `@?` with a predicate expression matches EVERY
//	               row. Admitting both operators would make the trap
//	               jsonpath.Accept exists to close indistinguishable from an
//	               intentional query.
//	narg + ::jsonpath — a BOUND parameter cast by PostgreSQL's own parser. The
//	               nullable form is what makes "no filter" mean "no predicate"
//	               rather than an expression that matches nothing.
func TestEveryRetrievalQueryCarriesTheAttributeFilter(t *testing.T) {
	filtered := 0
	for _, query := range namedQueries(t, discoverQueries) {
		if !retrieves(query) {
			continue
		}
		filtered++

		for _, clause := range []string{
			"sqlc.narg('attribute_filter')::text IS NULL",
			"r.filter_doc @? sqlc.narg('attribute_filter')::text::jsonpath",
		} {
			if !strings.Contains(query.body, clause) {
				t.Errorf("%s decides candidates but does not carry %q", query.name, clause)
			}
		}
	}
	if filtered != 3 {
		t.Fatalf("found %d candidate-deciding queries, want 3", filtered)
	}
}

// `@@` must not appear anywhere in the file.
//
// Spelled as an absence rather than folded into the test above because the two
// fail for different reasons: the clause going MISSING is a filter that stops
// filtering, and `@@` APPEARING is a filter that silently inverts — every row
// or no row depending on which form the caller sent, with no error either way.
// A file-wide check also catches it arriving somewhere the loop above skips.
func TestTheJSONPathMatchOperatorIsNeverUsed(t *testing.T) {
	for _, query := range namedQueries(t, discoverQueries) {
		// `search_tsv @@ discover_tsquery(...)` is full-text's own operator
		// and is unrelated; it is the JSONB spelling that is forbidden.
		if strings.Contains(query.body, "filter_doc @@") {
			t.Errorf("%s uses `@@` on filter_doc: it takes a predicate expression, "+
				"and this service accepts only filter form, which `@@` matches zero "+
				"rows against", query.name)
		}
	}
}
