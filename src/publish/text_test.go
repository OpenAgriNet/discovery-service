package publish

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// contains is spelled out rather than asserted with a substring on the whole
// string, because "grade" is inside "upgraded" and a substring check would pass
// on a derivation that emitted the wrong token entirely.
func contains(derived, word string) bool {
	for _, token := range strings.Fields(derived) {
		if token == word {
			return true
		}
	}
	return false
}

func mustContain(t *testing.T, derived string, words ...string) {
	t.Helper()

	for _, word := range words {
		if !contains(derived, word) {
			t.Errorf("derived %q is missing %q", derived, word)
		}
	}
}

func mustNotContain(t *testing.T, derived string, words ...string) {
	t.Helper()

	for _, word := range words {
		if contains(derived, word) {
			t.Errorf("derived %q holds %q, which is a key or a keyword", derived, word)
		}
	}
}

// The whole contract in one fixture: values are searchable, the keys naming
// them are not, and the JSON-LD keywords are neither.
//
// Keys are stripped because they are a vocabulary, not content: every resource
// of a type carries the same ones, so indexing them makes "moisture" match
// every grain listing in the corpus rather than the ones that say something
// about it. The keywords go for a second reason — C4 makes `@context` and
// `@type` filter COLUMNS, matched exactly, and a term that is both a filter and
// a free-text token can be matched two ways that disagree.
func TestDerivationKeepsValuesAndStripsKeys(t *testing.T) {
	resource := resourceWith("Sona Masuri Rice",
		`{"name":"Sona Masuri","shortDesc":"Premium paddy","longDesc":"Grown in Raichur"}`,
		`{
			"@context":"https://beckn.org/Agri",
			"@type":"AgriProduce",
			"grade":"A",
			"moisture":"low",
			"origin":{"district":"Raichur","state":"Karnataka"}
		}`)

	derived := deriveSearchText(resource)

	mustContain(t, derived,
		"Sona", "Masuri", "Rice", "Premium", "paddy", "Grown", "Raichur", "A", "low", "Karnataka")
	mustNotContain(t, derived,
		"@context", "@type", "grade", "moisture", "origin", "district", "state",
		"AgriProduce", "shortDesc", "longDesc", "name")
}

// Deterministic against KEY ORDER, not just against repetition. The derived
// text is hashed into embedding_source_hash, and that hash is the A5 re-embed
// decision: two byte-different spellings of the same document must hash the
// same, or a republish that changed nothing re-embeds the whole catalog.
func TestDerivationDoesNotDependOnKeyOrder(t *testing.T) {
	one := resourceWith("Rice", "", `{"grade":"A","moisture":"low","origin":"Raichur"}`)
	other := resourceWith("Rice", "", `{"origin":"Raichur","grade":"A","moisture":"low"}`)

	if deriveSearchText(one) != deriveSearchText(other) {
		t.Errorf("the same document in two key orders derived differently:\n %q\n %q",
			deriveSearchText(one), deriveSearchText(other))
	}
}

// Called twice on one resource it must give the same answer, which is the
// weaker half of determinism and the one a map iteration breaks silently: Go
// randomises range order per run, so a derivation that walked a map without
// sorting would pass every single-call test and churn hashes in production.
func TestDerivationIsStableAcrossCalls(t *testing.T) {
	resource := resourceWith("Rice", "", `{"a":"one","b":"two","c":"three","d":"four","e":"five"}`)

	first := deriveSearchText(resource)
	for range 32 {
		if again := deriveSearchText(resource); again != first {
			t.Fatalf("two calls on one resource disagreed:\n %q\n %q", first, again)
		}
	}
}

// Nesting and arrays are walked to the leaves. A publisher is free to structure
// resourceAttributes however the domain schema allows, and a derivation that
// stopped at the first object would index the top level of a document and
// silently miss everything a deep one actually says.
func TestDerivationWalksNestedObjectsAndArrays(t *testing.T) {
	resource := resourceWith("Rice", "",
		`{"certifications":[{"body":"FSSAI"},{"body":"Organic"}],"packing":{"inner":{"material":"jute"}}}`)

	mustContain(t, deriveSearchText(resource), "FSSAI", "Organic", "jute")
	mustNotContain(t, deriveSearchText(resource), "certifications", "body", "packing", "inner", "material")
}

// A resource with nothing but a name still derives, and derives to its name. A
// first publish carrying no descriptor and no attributes is ordinary, and a
// derivation that failed on it would fail the publish.
func TestDerivationOfABareResourceIsItsName(t *testing.T) {
	if derived := deriveSearchText(domain.Resource{Name: "Rice"}); strings.TrimSpace(derived) != "Rice" {
		t.Errorf("a bare resource derived %q, want %q", derived, "Rice")
	}
}

// The resource `id` is in reach for the first time, and must stay out.
//
// Before A17 the derivation read a `descriptor` column and an `attributes`
// column, so the id was not merely excluded — it was in a third place the
// function never opened. It is now a member of the same document, one
// `appendValues(words, resource.Document)` away from being indexed, and an id
// is the one string on a resource guaranteed to discriminate nothing: it
// matches exactly one row, which every lookup already has a primary key for.
// Worse, ids are commonly slugs — `wheat-atta-5kg` — so indexing them would
// make a corpus of them collide with the words publishers actually search for.
//
// This is what `Resource.Descriptor()` and `Resource.ResourceAttributes()` are
// FOR. Deleting them in favour of the whole document is a two-character change
// that compiles, and this is the test that says no.
func TestDerivationDoesNotReachTheResourceID(t *testing.T) {
	resource := resourceWith("Rice", `{"name":"Sona Masuri"}`, `{"grade":"A"}`)

	mustContain(t, deriveSearchText(resource), "Rice", "Sona", "Masuri", "A")
	mustNotContain(t, deriveSearchText(resource), "r1", "id")
}

// Unreadable JSON is not a reason to lose the name. Nothing upstream guarantees
// these bytes parse — L1 validates the request, not the merge result — and a
// derivation that gave up would leave the resource with an EMPTY tsvector,
// undiscoverable by any lexical query, rather than merely under-indexed.
//
// Since A17 the descriptor and the attributes are two members of ONE document,
// so bytes that will not parse cost both rather than one. That is the honest
// cost of storing the resource whole, and it is bounded: Name is a column,
// derived and written before any of this, so the resource stays findable by the
// thing publishers actually search for.
func TestDerivationOfAnUnreadableDocumentKeepsWhatItCanRead(t *testing.T) {
	resource := domain.Resource{
		Name:     "Rice",
		Document: json.RawMessage(`{"descriptor":{"shortDesc":"Premium paddy"`),
	}

	mustContain(t, deriveSearchText(resource), "Rice")
	mustNotContain(t, deriveSearchText(resource), "Premium", "paddy")
}

// resourceWith builds the stored shape: one document holding the descriptor and
// the attributes, with the derived Name beside it.
//
// Either member may be empty, which is the ordinary case — only `id` is
// required on a wire Resource.
func resourceWith(name, descriptor, attributes string) domain.Resource {
	members := map[string]json.RawMessage{"id": json.RawMessage(`"r1"`)}
	if descriptor != "" {
		members["descriptor"] = json.RawMessage(descriptor)
	}
	if attributes != "" {
		members["resourceAttributes"] = json.RawMessage(attributes)
	}

	document, err := json.Marshal(members)
	if err != nil {
		panic("fixture resource will not marshal: " + err.Error())
	}
	return domain.Resource{Name: name, Document: document}
}
