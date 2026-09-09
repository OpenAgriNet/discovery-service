package geo

import (
	"encoding/json"
	"math"

	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// earthRadiusM is the IUGG mean radius, and it is the same literal the SQL twin
// carries. A different radius in the two copies is a disagreement of a few
// hundred metres on a long search — small enough to look like rounding and
// large enough to move a result across a boundary.
const earthRadiusM = 6371008.8

// HaversineM is the great-circle distance in metres.
//
// A transliteration of geo_haversine_m, expression for expression and in the
// same order, because SQL cannot import Go and that is the only reason a second
// copy of this is tolerated. Task 16 pins the two against a fixed table of
// coordinate pairs; a disagreement reaches the caller as a result 10.1 km from
// a 10 km search.
//
// The clamp is not defensive tidying. Floating-point overshoot puts the
// argument at 1+1e-16 for antipodal points, which is NaN here and a hard error
// mid-query in PostgreSQL.
func HaversineM(from, to domain.GeoPoint) float64 {
	deltaLat := radians(to.Lat - from.Lat)
	deltaLon := radians(to.Lon - from.Lon)

	chord := math.Sin(deltaLat/2)*math.Sin(deltaLat/2) +
		math.Cos(radians(from.Lat))*math.Cos(radians(to.Lat))*
			math.Sin(deltaLon/2)*math.Sin(deltaLon/2)

	return 2 * earthRadiusM * math.Asin(math.Sqrt(math.Min(1, chord)))
}

// radians is spelled out rather than taken from a helper package so that the
// Go and the SQL read the same way: `radians(...)` appears in both.
func radians(degrees float64) float64 {
	return degrees * math.Pi / 180
}

// NearestGeometryM is the fold behind the Point-to-Point S_DWITHIN refinement,
// and the ONLY place an exact distance decides anything.
//
// ok == false means "no refinement applies" — nothing in the set is a Point —
// and NOT "no match". A caller that reads false as a miss drops every resource
// whose only geometry is a Polygon out of an S_DWITHIN, which is precisely the
// inversion this design corrected: the cell algebra has already decided those,
// and the caller must fall back to its answer.
//
// A Point whose coordinates cannot be read is not a Point for this purpose. The
// alternative — treating it as the origin — would make an unreadable geometry
// the nearest thing to every query on Earth.
func NearestGeometryM(center domain.GeoPoint, geometries []domain.Geometry) (float64, bool) {
	nearest, found := math.Inf(1), false

	for _, geometry := range geometries {
		at, isPoint := decodePoint(geometry.GeoJSON)
		if !isPoint {
			continue
		}
		found = true
		nearest = math.Min(nearest, HaversineM(center, at))
	}

	if !found {
		return 0, false
	}
	return nearest, true
}

// decodePoint reads a GeoJSON Point, and reports false for anything else.
//
// GeoJSON is [longitude, latitude] — index 0 is lon, the reverse of every
// argument list in this package. This function and its SQL twin are the two
// places that order is decided; swapping them puts Bengaluru in Somalia, and
// both values stay in range so nothing rejects it.
func decodePoint(raw json.RawMessage) (domain.GeoPoint, bool) {
	var shape struct {
		Type        string    `json:"type"`
		Coordinates []float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		return domain.GeoPoint{}, false
	}
	if shape.Type != "Point" || len(shape.Coordinates) < 2 {
		return domain.GeoPoint{}, false
	}
	return domain.GeoPoint{Lat: shape.Coordinates[1], Lon: shape.Coordinates[0]}, true
}
