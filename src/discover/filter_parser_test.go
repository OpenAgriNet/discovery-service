package discover_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/discover"
	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// The attribute filter's edge. Everything refused here is something the caller
// cannot detect for themselves: three of the shapes below run happily against
// PostgreSQL and answer the wrong question without saying so, and the last one
// reads the whole catalogue to answer the right one.
//
// Driven through MapIntent rather than through the parser directly, because
// half of what this task ships is the WIRING: a parser that refuses correctly
// and is never called returns the whole corpus, which is the exact failure the
// refusals exist to prevent.

// filtered builds the smallest intent that carries a filter.
func filtered(expression string) beckn.Intent {
	return beckn.Intent{Filters: &beckn.Filters{Type: "jsonpath", Expression: expression}}
}

func TestAFilterThatWouldFailSilentlyIsRefused(t *testing.T) {
	cases := []struct {
		name       string
		intent     beckn.Intent
		wantCode   beckn.ErrorCode
		wantSaying string
	}{
		{
			// The spec's own example grammar. One bracket apart from the
			// accepted spelling, and it returns an empty page rather than an
			// error — indistinguishable from an honest empty page.
			name:       "RFC 9535, the grammar of the spec's own example",
			intent:     filtered(`$.catalogs[?(@.id == 'c1')]`),
			wantCode:   beckn.CodeSchemaInvalidJSONPath,
			wantSaying: "RFC 9535",
		},
		{
			// Predicate form. `@?` answers true for every row, so the caller
			// receives the entire corpus looking like a filtered page.
			name:       "predicate form, which matches every row",
			intent:     filtered(`$.catalogs[*].resources[*].grade == "A"`),
			wantCode:   beckn.CodeSchemaInvalidJSONPath,
			wantSaying: "no ? (...) filter",
		},
		{
			// Rooted at the RESPONSE document rather than at the column the
			// filter runs against, which matches nothing.
			name:       "wrong root, which matches no row",
			intent:     filtered(`$.resources[*] ? (@.grade == "A")`),
			wantCode:   beckn.CodeSchemaInvalidJSONPath,
			wantSaying: "rooted at $.catalogs",
		},
		{
			// Two roots parse as a predicate, so this behaves exactly like the
			// missing-`?` case above.
			name:       "two roots, which matches every row",
			intent:     filtered(`$.catalogs[*] ? (@.isActive == true) && $.other == 1`),
			wantCode:   beckn.CodeSchemaInvalidJSONPath,
			wantSaying: "two roots",
		},
		{
			// A grammar this receiver declines rather than a path it cannot
			// read — the same species of fault as the S_TOUCHES refusal.
			name: "a type naming another grammar",
			intent: beckn.Intent{Filters: &beckn.Filters{
				Type: "rfc9535", Expression: `$.catalogs[*] ? (@.id == "c1")`,
			}},
			wantCode:   beckn.CodeSchemaTypeNotSupported,
			wantSaying: "rfc9535",
		},
		{
			name:       "a filter with no expression",
			intent:     filtered("   "),
			wantCode:   beckn.CodeSchemaInvalidJSONPath,
			wantSaying: "empty",
		},
		{
			// Correct, and it reads every gated row to be correct. Refused
			// only when nothing else has narrowed the corpus — the same
			// posture as MaxRadiusMeters, which refuses rather than clamps.
			name:       "an unindexable filter with nothing else narrowing",
			intent:     filtered(`$.catalogs[*].resources[*] ? (@.rating >= 4)`),
			wantCode:   beckn.CodeSchemaInvalidFormat,
			wantSaying: "narrows nothing",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			query, fatal, partial := discover.MapIntent(
				testCase.intent, beckn.Context{}, discover.Page{}, settings())

			if len(fatal) != 1 {
				t.Fatalf("MapIntent returned %d fatal faults, want exactly 1: %s", len(fatal), codesOf(fatal))
			}
			if fatal[0].Code != string(testCase.wantCode) {
				t.Errorf("the fault is %s, want %s", fatal[0].Code, testCase.wantCode)
			}
			if !strings.Contains(fatal[0].Message, testCase.wantSaying) {
				t.Errorf("the message is %q, which does not say %q — a refusal the caller "+
					"cannot debug is a refusal they will retry unchanged",
					fatal[0].Message, testCase.wantSaying)
			}
			if !strings.Contains(fatal[0].Path, "filters") {
				t.Errorf("the fault points at %q, want the filters member", fatal[0].Path)
			}
			if len(partial) != 0 {
				t.Errorf("a refused filter also produced partial faults %s; it refuses the "+
					"request outright, because continuing WIDENS it", codesOf(partial))
			}
			if query.Filters != nil {
				t.Errorf("a refused filter still reached the query as %v; a refusal must "+
					"narrow nothing and must not be half-applied", query.Filters)
			}
		})
	}
}

func TestAConformantFilterReachesTheQueryUntouched(t *testing.T) {
	// Crosses all three levels, including under exists() — the shape three
	// separate document columns could not answer at all, and the reason
	// filter_doc is one value (A18).
	const expression = `$.catalogs[*] ? (@.isActive == true && exists(@.offers[*] ? (@.channel == "retail")))`

	query, fatal, partial := discover.MapIntent(
		filtered(expression), beckn.Context{}, discover.Page{}, settings())

	if len(fatal) != 0 || len(partial) != 0 {
		t.Fatalf("a conformant filter faulted: fatal %s, partial %s", codesOf(fatal), codesOf(partial))
	}
	if len(query.Filters) != 1 {
		t.Fatalf("the query carries %d filters, want 1 — an intent that names a filter and "+
			"a query that carries none is the whole corpus returned as a filtered page",
			len(query.Filters))
	}
	if query.Filters[0].Expression != expression {
		t.Errorf("the expression reached the query as %q, want it VERBATIM: filter_doc is "+
			"already rooted at $.catalogs, so there is nothing to rebase, and an "+
			"expression this service edited is one the caller cannot debug against "+
			"their own document", query.Filters[0].Expression)
	}
}

// An unindexable filter is served when something else has already narrowed the
// corpus — a text search, a spatial constraint or a schema predicate.
//
// The guard refuses a full scan of the catalogue, not inequality. Refusing it
// unconditionally would make `rating >= 4` unusable beside the text search it
// almost always accompanies.
func TestAnUnindexableFilterIsServedWhenSomethingElseNarrows(t *testing.T) {
	intent := filtered(`$.catalogs[*].resources[*] ? (@.rating >= 4)`)
	intent.TextSearch = "wheat atta"

	query, fatal, partial := discover.MapIntent(intent, beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 0 || len(partial) != 0 {
		t.Fatalf("an unindexable filter beside a text search faulted: fatal %s, partial %s",
			codesOf(fatal), codesOf(partial))
	}
	if len(query.Filters) != 1 {
		t.Errorf("the query carries %d filters, want 1", len(query.Filters))
	}
}

// An absent filter is not a filter.
func TestNoFilterIsNoFault(t *testing.T) {
	query, fatal, partial := discover.MapIntent(
		beckn.Intent{}, beckn.Context{}, discover.Page{}, settings())

	if len(fatal) != 0 || len(partial) != 0 {
		t.Fatalf("an intent with no filter faulted: fatal %s, partial %s", codesOf(fatal), codesOf(partial))
	}
	if query.Filters != nil {
		t.Errorf("an absent filter produced %v, want none", query.Filters)
	}
}

// AttributeFilter carries an expression and nothing else (A18).
//
// A `Root` field is what per-column routing needed, and per-column routing is
// what A18 removed: there is ONE filter column, so a root is either $.catalogs
// or the expression is refused. A field that can only hold one value is a field
// two code paths will eventually disagree about.
func TestAnAttributeFilterIsAnExpressionAndNothingElse(t *testing.T) {
	shape := reflect.TypeOf(domain.AttributeFilter{})

	var fields []string
	for i := range shape.NumField() {
		fields = append(fields, shape.Field(i).Name)
	}

	if !slices.Equal(fields, []string{"Expression"}) {
		t.Errorf("domain.AttributeFilter holds %v, want [Expression] alone — a second field "+
			"here is a routing decision, and A18 removed the thing that routed", fields)
	}
}
