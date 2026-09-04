package memory

import (
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
	"github.com/OpenAgriNet/discovery-service/src/storage/conformance"
)

// An internal test, because the spatial stage is not part of this backend's
// port — it is how the port is going to be met. Exporting it to test it would
// put a function in the package's API that exists only for its own Search.
const testResolution = 8

// filterFor reduces a conformance case's constraint to the SpatialFilter a
// backend receives, exactly as the mapper will.
func filterFor(t *testing.T, spatial conformance.SpatialCase) domain.SpatialFilter {
	t.Helper()

	full, cover, err := geo.CoverQuery(spatial.Query, spatial.Op, spatial.DistanceM, testResolution)
	if err != nil {
		t.Fatalf("CoverQuery: %v", err)
	}
	bounds, err := geo.BoundsFor(spatial.Query, spatial.Op, spatial.DistanceM)
	if err != nil {
		t.Fatalf("BoundsFor: %v", err)
	}

	filter := domain.SpatialFilter{
		Op: spatial.Op, CellsFull: full, CellsCover: cover, Bounds: bounds,
		RadiusM: spatial.DistanceM, Quantifier: domain.QuantifierAny,
	}
	// Populated ONLY for a Point constraint under S_DWITHIN. On any other
	// operator a non-nil Center would silently narrow that operator's answer.
	if center, ok := conformance.CenterOf(spatial.Query); ok && spatial.Op == domain.OpDWithin {
		filter.Center = &center
	}
	return filter
}

func coverFor(t *testing.T, geometry domain.Geometry) geo.Cover {
	t.Helper()

	cover, err := geo.CoverGeometry(geometry, testResolution)
	if err != nil {
		t.Fatalf("CoverGeometry: %v", err)
	}
	return cover
}

// The whole spatial stage, over the table Postgres's CASE block will be run
// against in Task 16. Driving both from one fixture is what keeps the two from
// agreeing only by coincidence.
func TestTheSpatialStageAgreesWithTheConformanceTable(t *testing.T) {
	for _, spatial := range conformance.SpatialCases() {
		t.Run(spatial.Name, func(t *testing.T) {
			stored := coverFor(t, spatial.Stored)
			filter := filterFor(t, spatial)

			got := matchesSpatial(stored, []domain.Geometry{spatial.Stored}, filter)
			if got != spatial.Want {
				t.Errorf("matchesSpatial = %v, want %v", got, spatial.Want)
			}
		})
	}
}

// The box is where S_DISJOINT inverts, and it is the assertion MatchesOp alone
// cannot make: two shapes whose boxes miss entirely ARE disjoint, so ANDing the
// box in would answer this operator with exactly the rows NEAR the query — the
// complement of the truth, returned with no error.
func TestSDisjointSkipsTheBoundingBox(t *testing.T) {
	far := conformance.PointGeometryAt(0, domain.GeoPoint{Lat: 13.0827, Lon: 80.2707})
	near := conformance.PolygonGeometryAt(0, domain.GeoPoint{Lat: 12.9716, Lon: 77.5946}, 0.05)

	stored := coverFor(t, far)
	filter := filterFor(t, conformance.SpatialCase{Query: near, Op: domain.OpDisjoint})

	if stored.Bounds == nil || filter.Bounds == nil {
		t.Fatal("the fixture lost a bounding box, so this test cannot see the box stage at all")
	}
	if boxesMeet(stored.Bounds, filter.Bounds) {
		t.Fatal("the fixture's boxes overlap, so skipping the box would change nothing here")
	}
	if !matchesSpatial(stored, []domain.Geometry{far}, filter) {
		t.Error("S_DISJOINT against a far-away geometry returned false; the box must be skipped for it")
	}
}

// The refinement, and the direction it is allowed to move in.
//
// Both coarse stages admit a band past the radius — the query circle is covered
// in whole cells, and its box is a SQUARE circumscribing the circle — and the
// exact distance is what removes it. A refinement can only ever REMOVE a
// resource the coarse stages admitted, so the superset guarantee survives it.
//
// The band lives on the DIAGONALS. Due north the box's edge sits at exactly the
// radius and there is nothing left for the haversine to do; it is the corners,
// where the square reaches out to radius·√2, that the refinement exists for.
// Both stages are therefore asserted before the row counts, so a fixture that
// drifted out of the band fails rather than passing vacuously.
func TestThePointToPointRefinementOnlyNarrows(t *testing.T) {
	const radiusM = 1000

	center := domain.GeoPoint{Lat: 12.9716, Lon: 77.5946}
	filter := filterFor(t, conformance.SpatialCase{
		Query: conformance.PointGeometryAt(0, center), Op: domain.OpDWithin, DistanceM: radiusM,
	})

	tested := 0
	// North-east, in ~11 m steps: equal degrees of latitude and longitude is a
	// bearing of about 46° here, which is corner enough.
	for step := 1; step <= 40; step++ {
		offset := 0.0064 + 0.0001*float64(step)
		outside := domain.GeoPoint{Lat: center.Lat + offset, Lon: center.Lon + offset}
		geometry := conformance.PointGeometryAt(0, outside)

		if geo.HaversineM(center, outside) <= radiusM {
			continue
		}
		stored := coverFor(t, geometry)
		if !cellsAdmit(stored, filter) || !boxesMeet(stored.Bounds, filter.Bounds) {
			continue
		}

		tested++
		if matchesSpatial(stored, []domain.Geometry{geometry}, filter) {
			t.Errorf("a Point %.0f m away survived a %d m S_DWITHIN that both coarse stages "+
				"admitted; the exact distance must remove it", geo.HaversineM(center, outside), radiusM)
		}
	}
	if tested == 0 {
		t.Fatal("no Point past the radius reached the refinement, so it was never exercised")
	}
}

// ok == false from NearestGeometryM means "no refinement applies", not "no
// match". Reading it as a miss drops every resource whose only geometry is a
// Polygon out of an S_DWITHIN — the inversion this design corrected, so the
// fallback is asserted here rather than left to the caller.
func TestAPolygonFallsBackToTheCellAnswerUnderDWithin(t *testing.T) {
	center := domain.GeoPoint{Lat: 12.9716, Lon: 77.5946}
	around := conformance.PolygonGeometryAt(0, center, 0.02)

	stored := coverFor(t, around)
	filter := filterFor(t, conformance.SpatialCase{
		Query: conformance.PointGeometryAt(0, center), Op: domain.OpDWithin, DistanceM: 1000,
	})

	if _, refinable := geo.NearestGeometryM(center, []domain.Geometry{around}); refinable {
		t.Fatal("the fixture holds a Point after all, so the fallback is untested here")
	}
	if !matchesSpatial(stored, []domain.Geometry{around}, filter) {
		t.Error("a Polygon around the centre lost its S_DWITHIN match; ok=false is not a miss")
	}
}

// A declined cover disables the cell predicate only. The box still runs, so the
// answer stays a superset and the query degrades to a scan rather than to a
// wrong answer.
func TestADeclinedQueryCoverStillRunsTheBox(t *testing.T) {
	bengaluru := domain.GeoPoint{Lat: 12.9716, Lon: 77.5946}
	stored := coverFor(t, conformance.PointGeometryAt(0, bengaluru))

	near := domain.SpatialFilter{
		Op: domain.OpIntersects, Quantifier: domain.QuantifierAny,
		Bounds: &domain.BBox{MinLat: 12.9, MaxLat: 13.0, MinLon: 77.5, MaxLon: 77.7},
	}
	far := near
	far.Bounds = &domain.BBox{MinLat: 20, MaxLat: 21, MinLon: 70, MaxLon: 71}

	if !matchesSpatial(stored, nil, near) {
		t.Error("a declined cover rejected a geometry inside the query box")
	}
	if matchesSpatial(stored, nil, far) {
		t.Error("a declined cover matched a geometry outside the query box; the box must still run")
	}
}

// A nil box, on either side, is "no box" and cannot reject anything — a
// declined cover or a declined query cover, not an empty box that meets
// nothing. Neither existing box test leaves Bounds nil.
func TestANilBoxMeetsAnything(t *testing.T) {
	box := &domain.BBox{MinLat: 12.9, MaxLat: 13.0, MinLon: 77.5, MaxLon: 77.7}

	if !boxesMeet(nil, box) {
		t.Error("a nil stored box rejected a query box; a declined cover cannot reject anything")
	}
	if !boxesMeet(box, nil) {
		t.Error("a nil query box was rejected; a declined query cover cannot reject anything")
	}
	if !boxesMeet(nil, nil) {
		t.Error("two nil boxes were read as not meeting")
	}
}

// The quantifiers over a resource with several shapes, some matching and some
// not — the case a single-shape fixture cannot tell apart from "the whole
// resource matched" or "it didn't". Neither existing spatial test sets
// Quantifier to NONE or ALL, so matchesGeometry's own branches for both are
// otherwise unexercised here (the acceptance suite pins them against
// Postgres; this pins the memory backend's own answer).
func TestMatchesGeometryUnderNoneAndAll(t *testing.T) {
	center := domain.GeoPoint{Lat: 12.9716, Lon: 77.5946}
	far := domain.GeoPoint{Lat: center.Lat + 5, Lon: center.Lon + 5}

	filter := filterFor(t, conformance.SpatialCase{
		Query: conformance.PointGeometryAt(0, center), Op: domain.OpDWithin, DistanceM: 1000,
	})
	none := filter
	none.Quantifier = domain.QuantifierNone
	all := filter
	all.Quantifier = domain.QuantifierAll

	near := conformance.PointGeometryAt(0, center)
	away := conformance.PointGeometryAt(0, far)
	r := New(testResolution)

	mixed := domain.Resource{Geometries: []domain.Geometry{near, away}}
	if r.matchesGeometry(domain.Catalog{}, mixed, domain.SearchQuery{Spatial: &none}) {
		t.Error("NONE matched a resource where one of its two shapes matched")
	}
	if r.matchesGeometry(domain.Catalog{}, mixed, domain.SearchQuery{Spatial: &all}) {
		t.Error("ALL matched a resource where only one of its two shapes matched")
	}

	everyShapeNear := domain.Resource{Geometries: []domain.Geometry{near, near}}
	if !r.matchesGeometry(domain.Catalog{}, everyShapeNear, domain.SearchQuery{Spatial: &all}) {
		t.Error("ALL did not match a resource whose every shape matched")
	}
	if r.matchesGeometry(domain.Catalog{}, everyShapeNear, domain.SearchQuery{Spatial: &none}) {
		t.Error("NONE matched a resource whose every shape matched")
	}

	// NOT EXISTS(NOT matches) over no shapes at all is vacuously true — the
	// same answer the SQL's EXISTS gives a resource with no geometry.
	noShapes := domain.Resource{}
	if !r.matchesGeometry(domain.Catalog{}, noShapes, domain.SearchQuery{Spatial: &all}) {
		t.Error("ALL over a resource with no shapes at all must be vacuously true")
	}
}

// A shape that will not cover — accepted at publish time, unreadable now —
// drops out of the spatial answer rather than erroring the whole search.
func TestShapeMatchesOfAnUncoverableShapeIsFalse(t *testing.T) {
	broken := domain.Geometry{Type: "Point", GeoJSON: []byte("not geojson")}
	filter := domain.SpatialFilter{Op: domain.OpIntersects, Quantifier: domain.QuantifierAny}

	r := New(testResolution)
	if r.shapeMatches(filter)(broken) {
		t.Error("a shape that will not cover was matched rather than dropped")
	}
}
