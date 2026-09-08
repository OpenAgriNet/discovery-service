package geo

import "testing"

// ringsFor's own error branch cannot be reached through CoverQuery: it is
// only ever called after fillBoth has already succeeded at the SAME
// resolution, and fillBoth's own walkCells call needs the same
// HexagonEdgeLengthAvgM lookup to succeed first — so a resolution invalid
// enough to fail ringsFor has already made CoverQuery decline earlier, before
// ringsFor is ever called (see TestAnOutOfRangeResolutionDeclinesRatherThanErrors
// in h3_test.go). Checked directly since it is otherwise untestable.
//
// package geo, not geo_test: ringsFor is unexported.
func TestRingsForAnOutOfRangeResolutionDeclines(t *testing.T) {
	const (
		anyDistanceMeters = 1000 // any positive value: decline is about the resolution, not the distance
		pastH3MaxRes15    = 99   // H3 defines resolutions 0 through 15; 99 is not one of them
	)
	if _, sized := ringsFor(anyDistanceMeters, pastH3MaxRes15); sized {
		t.Error("ringsFor(99) reported sized; want false for an out-of-range resolution")
	}
}

// MaxCollectionDepthForTests mirrors the unexported maxCollectionDepth so
// h3_test.go (package geo_test) can pin its recursion-limit test to the value
// itself rather than to a bare literal that would still pass if the limit
// changed underneath it.
const MaxCollectionDepthForTests = maxCollectionDepth

// distanceMeters <= 0 is "no dilation", not a failure — the pair with
// TestCoverQueryOfANonPointSDWithinAtZeroDistanceDilatesByNothing in
// h3_test.go, which checks the same thing through CoverQuery's public
// surface; this pins ringsFor's own return value directly.
func TestRingsForANonPositiveDistanceIsZeroRingsAndSized(t *testing.T) {
	const resolution = 8 // matches h3_test.go's res; not visible across the package/package_test split
	for _, distance := range []float64{0, -5} {
		rings, sized := ringsFor(distance, resolution)
		if !sized || rings != 0 {
			t.Errorf("ringsFor(%g) = (%d, %v), want (0, true)", distance, rings, sized)
		}
	}
}
