package domain_test

import (
	"slices"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// The separator has to survive an id that looks like a separator. Every
// printable candidate appears in real publisher ids — a URN carries colons, a
// path-shaped id carries slashes — and the failure a bad choice produces is not
// an error but a DIFFERENT resource hydrated under the right name.
func TestAKeyRoundTripsThroughIdsFullOfSeparators(t *testing.T) {
	for _, pair := range [][2]string{
		{"c1", "r1"},
		{"urn:oan:catalog:1", "urn:oan:resource:2"},
		{"a/b/c", "d/e/f"},
		{"has|pipe", "has|pipe|too"},
		{"", ""},
		{"c1", ""},
	} {
		key := domain.ResourceKey(pair[0], pair[1])
		catalogID, resourceID, ok := domain.SplitResourceKey(key)
		if !ok || catalogID != pair[0] || resourceID != pair[1] {
			t.Errorf("ResourceKey(%q, %q) split to (%q, %q, %t)",
				pair[0], pair[1], catalogID, resourceID, ok)
		}
	}
}

func TestAKeyThatWasNeverAKeyIsRefused(t *testing.T) {
	if _, _, ok := domain.SplitResourceKey("c1:r1"); ok {
		t.Error("a string with no separator split as a key — it would hydrate the wrong resource")
	}
}

func TestAPageFlattensToTwoParallelArrays(t *testing.T) {
	catalogIDs, resourceIDs := domain.SplitResourceKeys([]string{
		domain.ResourceKey("c1", "r1"),
		domain.ResourceKey("c2", "r1"),
		"not a key",
	})

	if !slices.Equal(catalogIDs, []string{"c1", "c2"}) || !slices.Equal(resourceIDs, []string{"r1", "r1"}) {
		t.Fatalf("split to %v / %v, want [c1 c2] / [r1 r1] — the pairing is what stops "+
			"catalog c2's offer matching because catalog c1 has a resource of the same id",
			catalogIDs, resourceIDs)
	}
}
