package jsonpath_test

import (
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/platform/jsonpath"
)

// The shape C7's own example uses.
func TestDotRendersTheWireSpelling(t *testing.T) {
	for canonical, want := range map[string]string{
		"$":                                     "$",
		"$['message']['publishDirectives'][1]":  "$.message.publishDirectives[1]",
		"$['catalogs'][2]['resources'][0]":      "$.catalogs[2].resources[0]",
		"$['catalogs'][*]['provider']['geo']":   "$.catalogs[*].provider.geo",
		"$['message']['catalogs'][0]['offers']": "$.message.catalogs[0].offers",
		// A digit that is not the first character is legal in a dotted
		// identifier — only a LEADING digit forces brackets.
		"$['ab1']": "$.ab1",
	} {
		if got := jsonpath.Dot(canonical); got != want {
			t.Errorf("Dot(%q) = %q, want %q", canonical, got, want)
		}
	}
}

// A name that is legal bracketed and illegal dotted keeps its brackets.
//
// `@type` is the one that matters: it is on every JSON-LD resource this service
// indexes, and `$.a.@type` is a string no JSONPath implementation would take
// back — so a renderer that unbracketed it would hand the publisher a path their
// own tooling refuses.
func TestDotKeepsANameThatCannotBeDotted(t *testing.T) {
	for canonical, want := range map[string]string{
		"$['resourceAttributes']['@type']": "$.resourceAttributes['@type']",
		"$['a']['resource-id']":            "$.a['resource-id']",
		"$['0abc']":                        "$['0abc']",
	} {
		if got := jsonpath.Dot(canonical); got != want {
			t.Errorf("Dot(%q) = %q, want %q", canonical, got, want)
		}
	}
}

// The same refusal Canonicalise makes, so a caller has one empty string to check
// rather than two different failure shapes.
func TestDotRefusesWhatItCannotRead(t *testing.T) {
	for _, path := range []string{
		"",
		"message.catalogs[0]",
		"$.message.catalogs[0]",
		"$['unterminated",
		"$['a'",
		"$[01]",
		"$[]",
		"$[55",          // an index segment with no closing ]
		"$['cat!alog']", // a quoted name with a byte this subset does not carry
	} {
		if got := jsonpath.Dot(path); got != "" {
			t.Errorf("Dot(%q) = %q, want the refusal", path, got)
		}
	}
}

// Everything Canonicalise emits, Dot reads. The two are one grammar written
// twice, and a member Canonicalise accepts that Dot rejects would silently drop
// the path out of a fault.
func TestDotReadsEverythingCanonicaliseEmits(t *testing.T) {
	for _, path := range []string{
		"$.catalogs[*].provider.geo",
		"$['catalogs'][0]['resourceAttributes']['@type']",
		"$.a-b.c_d[12][*]",
	} {
		canonical := jsonpath.Canonicalise(path)
		if canonical == "" {
			t.Fatalf("Canonicalise(%q) refused its own input", path)
		}
		if got := jsonpath.Dot(canonical); got == "" {
			t.Errorf("Dot refused %q, which Canonicalise emitted from %q", canonical, path)
		}
	}
}
