package jsonpath_test

import (
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/platform/jsonpath"
)

// The canonical spelling of the path the plan uses throughout, and the one the
// publish walker stores in `target_path`.
const canonical = `$['catalogs'][*]['provider']['availableAt'][*]['geo']`

// The assertion the whole package exists for.
//
// `g.target_path = ANY($targets)` is plain SQL equality, so the publish side and
// the discover side have to produce the same BYTES for the same location. A
// caller who writes the dot form and a caller who writes the bracket form are
// asking the same question; if only one of them canonicalises to what the
// walker stored, the other gets a 200 with an empty list and no error to
// explain it — the exact failure this function was extracted to prevent.
func TestBothPathFormsCanonicaliseIdentically(t *testing.T) {
	forms := []string{
		`$.catalogs[*].provider.availableAt[*].geo`,
		`$['catalogs'][*]['provider']['availableAt'][*]['geo']`,
		`$["catalogs"][*]["provider"]["availableAt"][*]["geo"]`,
		`$.catalogs[*].provider['availableAt'][*].geo`,
		`$.catalogs.*.provider.availableAt.*.geo`,
	}

	for _, form := range forms {
		if got := jsonpath.Canonicalise(form); got != canonical {
			t.Errorf("Canonicalise(%s) = %s, want %s", form, got, canonical)
		}
	}
}

// Canonicalising an already-canonical path must be the identity, because the
// walker canonicalises a path it built in canonical form and the mapper
// canonicalises whatever a caller sent. A function that moved on the second
// application would put the two sides one call apart from each other.
func TestCanonicaliseIsIdempotent(t *testing.T) {
	once := jsonpath.Canonicalise(`$.catalogs[0].resources[2].resourceAttributes.pickup.point`)
	if once == "" {
		t.Fatal("Canonicalise refused a path it should read")
	}
	if twice := jsonpath.Canonicalise(once); twice != once {
		t.Errorf("Canonicalise(%s) = %s, want the input unchanged", once, twice)
	}
}

// Concrete indices survive, because `source_path` is the column that
// distinguishes two geometries found under one wildcard. Folding them to [*]
// here would make every location in an array collide.
func TestConcreteIndicesAreKept(t *testing.T) {
	const want = `$['catalogs'][*]['provider']['availableAt'][2]['geo']`

	if got := jsonpath.Canonicalise(`$.catalogs[*].provider.availableAt[2].geo`); got != want {
		t.Errorf("Canonicalise dropped the index: got %s, want %s", got, want)
	}
}

// The empty string is the refusal, and the intent mapper turns it into a 400.
//
// Refusing is the whole point: an expression this service reads as "no targets"
// would drop the spatial predicate and answer with the entire index, which is
// the silently-widened result every branch of the spatial path is written to
// avoid.
func TestCanonicaliseRefusesWhatItCannotRead(t *testing.T) {
	unreadable := []string{
		"",
		"catalogs[*].geo",             // no root
		"$..geo",                      // recursive descent
		"$.catalogs[?(@.id == 'c1')]", // a filter expression
		"$.catalogs[1:3]",             // a slice
		"$.catalogs[0,1]",             // a union
		"$['catalogs'",                // unterminated bracket
		"$['catalogs]",                // unterminated quote
		"$.",                          // a dot naming nothing
		"$[]",                         // an empty bracket
		"$catalogs",                   // a root with no separator
		"$.catalogs[01]",              // an index with a leading zero
		"$.catalogs[-1]",              // a negative index
		`$['cat\'alogs']`,             // an escaped quote, which this subset does not read
		"$[0",                         // a bracket segment with no closing ]
		"$['cat!alog']",               // a quoted name with a byte this subset does not carry
		"$.cat!alog",                  // the same, in dot form
	}

	for _, path := range unreadable {
		if got := jsonpath.Canonicalise(path); got != "" {
			t.Errorf("Canonicalise(%q) = %q, want the empty refusal", path, got)
		}
	}
}

// The root alone is a legal path and canonicalises to itself. It names the
// whole document, which is a question the walker never asks but the parser must
// not choke on.
func TestTheBareRootIsCanonical(t *testing.T) {
	if got := jsonpath.Canonicalise("$"); got != "$" {
		t.Errorf("Canonicalise(\"$\") = %q, want \"$\"", got)
	}
}
