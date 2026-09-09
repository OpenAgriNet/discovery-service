package conformance

import "github.com/OpenAgriNet/discovery-service/src/domain"

// SpatialCase is one stored geometry, one constraint, and the answer every
// backend must give.
//
// It is data rather than a test, because the two implementations that must
// agree are written in different languages: the memory backend runs
// geo.MatchesOp plus its own bounding-box stage, Postgres runs a `CASE
// spatial_op` block over GIN-indexed arrays. Both are driven from THIS table,
// so a disagreement is a failing test rather than a support ticket about a
// result 10 km from where it should be.
type SpatialCase struct {
	Name   string
	Stored domain.Geometry
	Query  domain.Geometry
	Op     domain.SpatialOp

	// DistanceM is meaningful only under S_DWITHIN, the one operator carrying a
	// distance.
	DistanceM float64

	Want bool
}

// The fixture coordinates. Real places, ~290 km apart, which is far enough that
// no cover at any sane resolution puts them in the same MAYBE band and close
// enough that a reader can check the claim on a map.
var (
	fixtureCenter = domain.GeoPoint{Lat: 12.9716, Lon: 77.5946}
	fixtureFar    = domain.GeoPoint{Lat: 13.0827, Lon: 80.2707}
)

// SpatialCases is the table both backends are run against.
//
// The negative rows carry the weight. Phrased over `cells_full` rather than
// `cells_cover` — as the operator table once was — every one of them passes
// VACUOUSLY, because a Point's full set is empty and the empty set is a subset
// of everything. Both backends would then have been wrong in the same
// direction, and agreeing, which is the one failure a conformance suite cannot
// catch by construction. They are written out one per operator for that reason.
func SpatialCases() []SpatialCase {
	inside := PolygonGeometryAt(0, fixtureCenter, 0.05)
	here, far := PointGeometryAt(0, fixtureCenter), PointGeometryAt(1, fixtureFar)

	cases := []SpatialCase{
		{"a Point inside a query Polygon is within it", here, inside, domain.OpWithin, 0, true},
		{"a Point inside a query Polygon intersects it", here, inside, domain.OpIntersects, 0, true},
		{"a Point inside a query Polygon is not disjoint from it", here, inside, domain.OpDisjoint, 0, false},
		{"a Point 290 km outside is disjoint", far, inside, domain.OpDisjoint, 0, true},
		{"a Point 290 km outside is not within", far, inside, domain.OpWithin, 0, false},
		{"a Point 290 km outside does not intersect", far, inside, domain.OpIntersects, 0, false},
		{"a Point 290 km outside does not contain", far, inside, domain.OpContains, 0, false},
		{"a Point 290 km outside does not overlap", far, inside, domain.OpOverlaps, 0, false},
		{"a Point 290 km outside is not equal", far, inside, domain.OpEquals, 0, false},
		{"a Polygon contains a Point inside it", inside, here, domain.OpContains, 0, true},
		{"a Polygon equals itself", inside, inside, domain.OpEquals, 0, true},
		{"a Point is within 5 km of itself", here, here, domain.OpDWithin, 5000, true},
		{"a Point 290 km away is not within 5 km", far, here, domain.OpDWithin, 5000, false},
	}

	// Refused, not approximated, and asserted as ordinary rows so that a
	// backend which quietly starts answering one fails here rather than in
	// production.
	for _, op := range []domain.SpatialOp{domain.OpTouches, domain.OpCrosses} {
		cases = append(cases, SpatialCase{
			Name:   "a Point does not match the refused operator " + string(op),
			Stored: here, Query: inside, Op: op,
		})
	}
	return cases
}
