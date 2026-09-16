package geo_test

import (
	"slices"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
)

// cells builds a sorted, deduplicated run — the shape both cover functions
// guarantee and every set operation here assumes.
func cells(from, to uint64) []uint64 {
	built := make([]uint64, 0, to-from+1)
	for cell := from; cell <= to; cell++ {
		built = append(built, cell)
	}
	return built
}

// The fixture pair. A occupies cells 10..20, of which 11..19 lie entirely
// inside it — the sandwich, in one dimension, which is all the set algebra can
// see. Synthetic rather than real H3 indices on purpose: MatchesOp is pure set
// algebra, and a fixture built by the cover functions would test them instead.
var (
	aFull  = cells(11, 19)
	aCover = cells(10, 20)
)

// The six relations Q can stand in to A, each with its own sandwich.
var relations = map[string]struct{ full, cover []uint64 }{
	"disjoint":    {cells(51, 59), cells(50, 60)},
	"touching":    {cells(21, 29), cells(20, 30)}, // one shared COVER cell, no shared full
	"overlapping": {cells(16, 24), cells(15, 25)},
	"contained":   {cells(1, 29), cells(0, 30)},   // A lies inside Q
	"containing":  {cells(14, 16), cells(13, 17)}, // Q lies inside A
	"equal":       {aFull, aCover},
}

// The truth table, one row per relation and one column per operator. Read
// against the table in Geospatial Design: an entry is true when the operator is
// provably TRUE or lands in the MAYBE band, and false only when it is provably
// FALSE — because a geometry that cannot be proven to fail is returned.
//
// The rows worth arguing about are `touching`, where S_INTERSECTS and
// S_DISJOINT are BOTH true because touching is the measure-zero condition cells
// cannot express, and `equal`, where S_WITHIN and S_CONTAINS are both true
// because equal shapes genuinely are within and do contain each other.
var truthTable = map[string]map[domain.SpatialOp]bool{
	"disjoint": {
		domain.OpIntersects: false, domain.OpDisjoint: true, domain.OpWithin: false,
		domain.OpContains: false, domain.OpDWithin: false, domain.OpOverlaps: false,
		domain.OpEquals: false,
	},
	"touching": {
		domain.OpIntersects: true, domain.OpDisjoint: true, domain.OpWithin: false,
		domain.OpContains: false, domain.OpDWithin: true, domain.OpOverlaps: true,
		domain.OpEquals: false,
	},
	"overlapping": {
		domain.OpIntersects: true, domain.OpDisjoint: false, domain.OpWithin: false,
		domain.OpContains: false, domain.OpDWithin: true, domain.OpOverlaps: true,
		domain.OpEquals: false,
	},
	"contained": {
		domain.OpIntersects: true, domain.OpDisjoint: false, domain.OpWithin: true,
		domain.OpContains: false, domain.OpDWithin: true, domain.OpOverlaps: false,
		domain.OpEquals: false,
	},
	"containing": {
		domain.OpIntersects: true, domain.OpDisjoint: false, domain.OpWithin: false,
		domain.OpContains: true, domain.OpDWithin: true, domain.OpOverlaps: false,
		domain.OpEquals: false,
	},
	"equal": {
		domain.OpIntersects: true, domain.OpDisjoint: false, domain.OpWithin: true,
		domain.OpContains: true, domain.OpDWithin: true, domain.OpOverlaps: true,
		domain.OpEquals: true,
	},
}

func TestMatchesOpAgainstTheTruthTable(t *testing.T) {
	for name, relation := range relations {
		for op, want := range truthTable[name] {
			t.Run(name+"/"+string(op), func(t *testing.T) {
				got := geo.MatchesOp(op, aFull, aCover, relation.full, relation.cover)
				if got != want {
					t.Errorf("MatchesOp(%s) with Q %s A = %v, want %v", op, name, got, want)
				}
			})
		}
	}
}

// The degenerate-full case, which is the commonest row in the table: a
// shopfront is a Point and a Point contains no cell, so its cells_full is
// permanently empty. Phrased over `full`, three of these refutations stop being
// predicates — `{} <@ anything` is true — and S_DISJOINT would return every
// Point in the corpus.
func TestMatchesOpDegradesNoOperatorOnAnEmptyFull(t *testing.T) {
	storedPoint := cells(15, 15) // its cover; its full is empty, permanently
	inside := relations["contained"]
	far := relations["disjoint"]

	if !geo.MatchesOp(domain.OpWithin, nil, storedPoint, inside.full, inside.cover) {
		t.Error("a stored Point inside a query Polygon is not S_WITHIN")
	}
	if geo.MatchesOp(domain.OpDisjoint, nil, storedPoint, inside.full, inside.cover) {
		t.Error("a stored Point inside a query Polygon came back S_DISJOINT")
	}
	if geo.MatchesOp(domain.OpWithin, nil, storedPoint, far.full, far.cover) {
		t.Error("a stored Point far outside a query Polygon came back S_WITHIN")
	}
	if !geo.MatchesOp(domain.OpDisjoint, nil, storedPoint, far.full, far.cover) {
		t.Error("a stored Point far outside a query Polygon is not S_DISJOINT")
	}
}

// The only operator that can see a sort, and therefore the only test that
// catches one dropped from either cover function. PostgreSQL's array `=` is
// element-wise in order, so an unsorted pair of identical sets compares unequal
// there — and the two backends would disagree on the same data.
func TestEqualsIgnoresTheOrderTheCellsWereBuiltIn(t *testing.T) {
	shuffled := []uint64{20, 10, 15, 12}
	sorted := slices.Clone(shuffled)
	slices.Sort(sorted)

	if !geo.MatchesOp(domain.OpEquals, sorted, sorted, sorted, sorted) {
		t.Error("S_EQUALS on one sorted set is not equal to itself")
	}
	if geo.MatchesOp(domain.OpEquals, sorted, sorted, shuffled, shuffled) {
		t.Error("S_EQUALS accepted an unsorted set; sorted is a precondition, not a convenience")
	}
}

// Refused, not unimplemented. They are rejected at the mapper with a 400, so
// this is unreachable — and false rather than true so that a leak shows up as
// an empty result rather than as the whole corpus silently matching a predicate
// nobody wrote.
func TestTheTwoRefusedOperatorsMatchNothing(t *testing.T) {
	for _, op := range []domain.SpatialOp{domain.OpTouches, domain.OpCrosses} {
		if geo.MatchesOp(op, aFull, aCover, aFull, aCover) {
			t.Errorf("MatchesOp(%s) matched; it is refused, not approximated", op)
		}
	}
}

// A nil cover is a cover that DECLINED — antimeridian, or over budget — not an
// empty one. Nothing can be refuted from it, so under the superset rule every
// meeting operator matches and the bounding box decides.
func TestADeclinedCoverRefutesNothing(t *testing.T) {
	if !geo.MatchesOp(domain.OpIntersects, nil, nil, aFull, aCover) {
		t.Error("a declined stored cover refuted S_INTERSECTS")
	}
	if !geo.MatchesOp(domain.OpWithin, aFull, aCover, nil, nil) {
		t.Error("a declined query cover refuted S_WITHIN")
	}
}
