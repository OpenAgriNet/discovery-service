package jsonpath_test

import (
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/platform/jsonpath"
)

// The shapes a caller is meant to send, and every one of them runs verbatim.
//
// There is no rebase (A18): `resources.filter_doc` is itself rooted at
// `$.catalogs`, so an expression that gets past Accept is handed to
// `@filter::jsonpath` with no edit at all. That is the whole reason this
// function is a gate rather than a rewriter — an expression this service
// modified would be one the caller cannot debug against their own document.
func TestAcceptTakesFilterForm(t *testing.T) {
	for _, expression := range []string{
		// One level, each of the three.
		`$.catalogs[*] ? (@.isActive == true)`,
		`$.catalogs[*].resources[*] ? (@.resourceAttributes.grade == "A")`,
		`$.catalogs[*].offers[*] ? (@.offerAttributes.channel == "retail")`,

		// The shape A18 exists for: catalog AND resource AND offer at once,
		// which no per-column routing can answer.
		`$.catalogs[*] ? (@.isActive == true && exists(@.resources[*] ? (@.resourceAttributes.grade == "A")) && exists(@.offers[*] ? (@.offerAttributes.channel == "retail")))`,

		// And the same across OR, which is the one that cannot even be
		// decomposed into per-table queries and reassembled.
		`$.catalogs[*] ? (@.descriptor.code == "HUL-BLR" || exists(@.offers[*] ? (@.offerAttributes.channel == "wholesale")))`,

		// A concrete index is not the trap and must not be treated as one.
		// Measured: `[0]` does not break filter form, and `[*]` does not
		// rescue predicate form — the `?` is the only thing that matters.
		`$.catalogs[0] ? (@.id == "c-hul")`,

		// The bracket-quoted root spelling Canonicalise normalises elsewhere.
		`$['catalogs'][*] ? (@.id == "c-hul")`,
		`$["catalogs"][*] ? (@.id == "c-hul")`,

		// A string may contain anything, including the very tokens this gate
		// scans for. A scanner that did not skip string bodies would refuse
		// this and call it predicate form.
		`$.catalogs[*] ? (@.descriptor.name == "why? this && that || other $")`,

		// A colon-bearing member has to be quoted, and quoting it must not
		// look like a second root or a comparison.
		`$.catalogs[*].resources[*] ? (@."schema:price" > 100)`,

		// Non-equality predicates are correct but scan (jsonb_path_ops
		// extracts equality only). Correct is what this gate judges; the
		// planner's opinion is not this function's business.
		`$.catalogs[*].resources[*] ? (@.resourceAttributes.grade like_regex "^A")`,
		`$.catalogs[*].resources[*] ? (@.descriptor.name starts with "wheat")`,

		// Leading whitespace and PostgreSQL's explicit path modes.
		`  $.catalogs[*] ? (@.isActive == true)  `,
		`strict $.catalogs[*] ? (@.isActive == true)`,
		`lax $.catalogs[*] ? (@.isActive == true)`,

		// A single & or | is neither && nor ||, so it is not a second root —
		// the scanner must move past it rather than treat it as the start of
		// a conjunction it never completes.
		`$.catalogs[*] ? (@.rating & 1 == 1)`,

		// An escaped quote inside a string body. closingQuote must skip the
		// byte after the backslash rather than close on it.
		`$.catalogs[*] ? (@.descriptor.name == "why \" this")`,
	} {
		if err := jsonpath.Accept(expression); err != nil {
			t.Errorf("Accept(%q) = %v, want nil", expression, err)
		}
	}
}

// The three shapes that are wrong and say nothing about it.
//
// Every one of these is a `400`, and the reason each must be is that PostgreSQL
// answers all three without an error — so a caller has no way to learn they
// asked the wrong question. Measured on PG16 over a 100k-row corpus:
//
//	predicate form  -> matched every row
//	wrong root      -> matched no row
//	two roots       -> matched every row
//
// The first is the dangerous one. `@?` asks only whether the expression yielded
// an item, and a bare comparison always yields one — `false` is an item. Drop a
// single `?` and the whole corpus comes back looking like a filtered page.
func TestAcceptRefusesTheShapesThatFailSilently(t *testing.T) {
	for expression, want := range map[string]string{
		// PREDICATE FORM. Same intent as the accepted cases above, one
		// character short.
		`$.catalogs[*].resources[*].resourceAttributes.grade == "A"`: "filter",
		`$.catalogs[0].isActive == true`:                             "filter",
		`$.catalogs[*].isActive == true`:                             "filter",

		// No predicate at all. Selects every catalog, so `@?` is true for
		// every row — a filter that filters nothing, which reads to the
		// caller exactly like one that worked.
		`$.catalogs[*]`:        "filter",
		`$.catalogs[*].offers`: "filter",

		// WRONG ROOT. `filter_doc` is rooted at `$.catalogs`; anything else
		// navigates off the document and matches nothing at all.
		`$.resources[*] ? (@.resourceAttributes.grade == "A")`: "root",
		`$ ? (@.isActive == true)`:                             "root",
		`$.message.catalogs[*] ? (@.isActive == true)`:         "root",
		`$.catalogues[*] ? (@.isActive == true)`:               "root",
		// A root written in bracket form that is not a quoted member —
		// rootMember's bracketSegment succeeds but the segment isn't ['name'].
		`$[0] ? (@.isActive == true)`: "root",

		// TWO ROOTS. Legal jsonpath, and it parses as a predicate expression
		// — so under `@?` it behaves like the predicate-form case and returns
		// the corpus. `&&` at the top level is the tell.
		`$.catalogs[*] ? (@.isActive == true) && $.other == 1`:            "root",
		`$.catalogs[*] ? (@.isActive == true) || $.catalogs[*].id == "x"`: "root",
	} {
		err := jsonpath.Accept(expression)
		if err == nil {
			t.Errorf("Accept(%q) = nil, want a refusal naming %q", expression, want)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Accept(%q) = %q, want it to name %q", expression, err, want)
		}
	}
}

// RFC 9535 is a different language and is refused as one (C10).
//
// This is not pedantry about spelling. `$.catalogs[?(@.x == 'y')]` is what every
// JSONPath tutorial teaches, PostgreSQL's parser rejects it outright, and the
// difference between the two dialects is exactly one character in a position no
// error message would otherwise point at.
func TestAcceptRefusesRFC9535(t *testing.T) {
	for _, expression := range []string{
		`$.catalogs[?(@.isActive == true)]`,
		`$.catalogs[*].resources[?(@.resourceAttributes.grade == 'A')]`,
		`$..resources[?(@.grade == 'A')]`,
	} {
		if err := jsonpath.Accept(expression); err == nil {
			t.Errorf("Accept(%q) = nil, want a refusal", expression)
		}
	}
}

// Malformed input is refused here rather than at the cast.
//
// The cast would catch these too — `@filter::jsonpath` is PostgreSQL's own
// parser and is the last word on syntax. Catching them earlier is about WHERE
// the caller is told: a refusal from this function carries the position and
// reaches the response as SCH_INVALID_JSONPATH, while a cast error surfaces
// from inside a query as something the error mapper has to guess at.
func TestAcceptRefusesWhatItCannotRead(t *testing.T) {
	for _, expression := range []string{
		``,
		`   `,
		`catalogs[*] ? (@.isActive == true)`,  // no root
		`$.catalogs[*] ? @.isActive == true`,  // filter without parens
		`$.catalogs[*] ? (@.isActive == true`, // unbalanced parens
		`$.catalogs[*] ? (@.name == "unterminated`, // unterminated string
		`$.catalogs[*] ? (@.name == 'unterminated`,
		`$.catalogs[* ? (@.isActive == true)`,       // unbalanced bracket
		`$.catalogs[*] ? (@.isActive == true))`,     // a ) closing nothing
		`$.catalogs[*]] ? (@.isActive == true)`,     // a ] closing nothing
		`$.catalogs[*] ? (@.isActive == true) [`,    // a [ never closed, past the filter
		`$.catalogs[*] == 1 ? (@.isActive == true)`, // a comparison before the filter
	} {
		if err := jsonpath.Accept(expression); err == nil {
			t.Errorf("Accept(%q) = nil, want a refusal", expression)
		}
	}
}

// HasIndexableEquality answers the one question the caller's page size depends
// on: whether GIN can extract anything at all from this expression.
//
// `jsonb_path_ops` extracts clauses of the form accessor-chain `==` constant.
// Everything else — inequality, like_regex, starts with — is answered
// CORRECTLY and by reading every gated row to do it, which is why the caller of
// this function refuses such an expression when nothing else narrows the query.
func TestIndexableEqualityIsWhatTheGINIndexCanExtract(t *testing.T) {
	cases := map[string]bool{
		// Equality, which is the whole of what the opclass extracts.
		`$.catalogs[*].resources[*] ? (@.grade == "A")`:    true,
		`$.catalogs[*] ? (@.descriptor.code == "HUL-BLR")`: true,

		// A conjunction one of whose arms is an equality: the equality is
		// extracted and the rest is rechecked on the rows it returns.
		`$.catalogs[*].resources[*] ? (@.rating >= 4 && @.grade == "A")`: true,

		// The three that read every row.
		`$.catalogs[*].resources[*] ? (@.rating >= 4)`:             false,
		`$.catalogs[*].resources[*] ? (@.rating != 4)`:             false,
		`$.catalogs[*].descriptor ? (@.name like_regex "wheat.*")`: false,
		`$.catalogs[*].descriptor ? (@.name starts with "wheat")`:  false,

		// `==` inside a string body is not a comparison. Reading it as one
		// would let a regex search past the guard that exists to catch it.
		`$.catalogs[*].descriptor ? (@.name like_regex "a == b")`: false,

		// An unterminated string, with no == before it. Accept has already
		// refused this by the time this function would see it; asked
		// directly, it must not read on into what is really the rest of a
		// broken string body looking for a comparison that isn't there.
		`$.catalogs[*] ? (@.name like_regex "unterminated`: false,
	}

	for expression, want := range cases {
		if got := jsonpath.HasIndexableEquality(expression); got != want {
			t.Errorf("HasIndexableEquality(%q) = %v, want %v", expression, got, want)
		}
	}
}
