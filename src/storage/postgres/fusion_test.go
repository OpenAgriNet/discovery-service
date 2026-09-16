package postgres_test

import (
	"slices"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/storage/postgres"
)

// RRF is the only place the modes are compared, and it compares them by RANK
// and never by score. That is the whole reason it exists: `ts_rank_cd` returns
// a relevance, `similarity` returns a fraction of trigrams and cosine distance
// returns a distance, and no scaling makes those three commensurable. Rank is.

// The property that makes RRF worth using rather than "take the union": two
// mediocre agreements outweigh one strong disagreement. With k = 60 the gap
// between rank 1 and rank 3 is 1/61 - 1/63 = 0.0005, while a second list
// naming the same id at all adds at least 1/(60 + len) — an order of magnitude
// more. A k small enough to invert this turns fusion back into "whatever the
// first mode said".
func TestConsensusAcrossTwoModesOutranksOneModesTopHit(t *testing.T) {
	lexical := []string{"solo", "shared-a", "shared-b"}
	fuzzy := []string{"shared-b", "shared-a"}

	fused := postgres.RRF(lexical, fuzzy)

	solo := slices.Index(fused, "solo")
	for _, id := range []string{"shared-a", "shared-b"} {
		if slices.Index(fused, id) > solo {
			t.Errorf("%q is named by both modes and %q by one, yet %q outranks it: %v",
				id, "solo", "solo", fused)
		}
	}
}

// Every id any mode returned survives fusion. RRF re-ORDERS the union; it is
// not a filter, and a mode whose ids were dropped here would be a mode that
// silently did not run.
func TestFusionKeepsEveryIdEveryModeReturned(t *testing.T) {
	fused := postgres.RRF([]string{"a", "b"}, []string{"b", "c"}, []string{"d"})

	for _, id := range []string{"a", "b", "c", "d"} {
		if !slices.Contains(fused, id) {
			t.Errorf("a mode returned %q and the fusion dropped it: %v", id, fused)
		}
	}
	if len(fused) != 4 {
		t.Errorf("fused holds %d ids, want the 4 distinct ones: %v", len(fused), fused)
	}
}

// A tie has to break the same way every time, because the page is a SLICE of
// this list. Two ids with equal RRF scores that swap between two runs of the
// same query put one of them on both page 1 and page 2 and the other on
// neither — and nothing in the response says so.
func TestATieBreaksByIdAndTheSameWayEveryTime(t *testing.T) {
	// Symmetric input: `b` and `a` sit at rank 1 and 2 in opposite lists, so
	// their scores are identical to the last bit and only the tiebreak decides.
	first := postgres.RRF([]string{"b", "a"}, []string{"a", "b"})
	second := postgres.RRF([]string{"b", "a"}, []string{"a", "b"})

	if !slices.Equal(first, second) {
		t.Fatalf("the same input fused two ways: %v then %v", first, second)
	}
	if !slices.Equal(first, []string{"a", "b"}) {
		t.Errorf("a tie broke to %v, want the ids in ascending order", first)
	}
}

// A mode that errored contributes an empty list, and one that ran contributes
// its own. Fusing them must be the running mode's answer — not an empty page,
// which is what an implementation that intersected rather than unioned would
// return, and which reads at the caller exactly like "nothing matched".
func TestAModeThatReturnedNothingDoesNotEmptyThePage(t *testing.T) {
	fused := postgres.RRF(nil, []string{"a", "b"}, []string{})

	if !slices.Equal(fused, []string{"a", "b"}) {
		t.Errorf("fusing an empty mode with a live one gave %v, want the live one's order", fused)
	}
}

func TestFusingNothingIsNotAPanic(t *testing.T) {
	if fused := postgres.RRF(); len(fused) != 0 {
		t.Errorf("fusing no modes gave %v, want an empty page", fused)
	}
}
