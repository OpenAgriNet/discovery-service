// Package conformance is the one suite every backend must pass.
//
// A backend is accepted by passing the fixtures both existing backends pass,
// which is what makes "not tied to one database" (TRD §5, T7) a property of the
// build rather than a claim in a document.
package conformance

import (
	"encoding/json"
	"fmt"

	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// providerGeoTarget is the wildcard path a catalog's own locations are found
// under, byte-identical to what a caller's `targets` canonicalises to. Fixtures
// compare against this rather than spelling it again: a fixture with its own
// copy passes while the walker stores something else.
const providerGeoTarget = `$['catalogs'][*]['provider']['availableAt'][*]['geo']`

// providerGeoSource is the same path with the availableAt index left concrete.
// The catalogs index stays wildcard: it is a property of the request rather than
// of the catalog.
const providerGeoSource = `$['catalogs'][*]['provider']['availableAt'][%d]['geo']`

// PointGeometryAt builds the provider-path Point geometry at one availableAt
// index, so "somewhere" is spelled one way across every fixture.
//
// Owners is empty because a catalog's provider location is catalog-level —
// stored once with a NULL resource id and shared (A15,
// discover-and-publish.md:140). The fixtures needing an offer's location give
// Owners the ids it covers.
func PointGeometryAt(index int, point domain.GeoPoint) domain.Geometry {
	return domain.Geometry{
		TargetPath: providerGeoTarget,
		SourcePath: fmt.Sprintf(providerGeoSource, index),
		Type:       "Point",
		// Longitude first (RFC 7946 orders x, y): a fixture that swapped them
		// would put Bengaluru in the Arabian Sea and still index, search and
		// match perfectly well against itself.
		GeoJSON: json.RawMessage(fmt.Sprintf(`{"type":"Point","coordinates":[%g,%g]}`, point.Lon, point.Lat)),
	}
}

// PointGeometries builds a catalog's provider locations in the order given,
// numbering the paths so two locations are distinguishable — SourcePath's whole
// job.
func PointGeometries(points ...domain.GeoPoint) []domain.Geometry {
	geometries := make([]domain.Geometry, 0, len(points))
	for index, point := range points {
		geometries = append(geometries, PointGeometryAt(index, point))
	}
	return geometries
}

// providerGeoPolygonSource is the polygon form of the same path. A polygon is a
// service AREA rather than a shopfront, and the two must be distinguishable in
// the store or a case cannot say which one it published.
const providerGeoPolygonSource = `$['catalogs'][*]['provider']['serviceArea'][%d]['geo']`

// PolygonGeometryAt builds a square service area of the given half-width in
// degrees, centred on a point.
//
// Squares rather than realistic outlines: a fixture needs a known inside, a
// known outside and a known area, and a shape whose containment a reader has to
// compute is one whose failure they cannot diagnose.
func PolygonGeometryAt(index int, center domain.GeoPoint, halfDegrees float64) domain.Geometry {
	corners := [][2]float64{
		{center.Lon - halfDegrees, center.Lat - halfDegrees},
		{center.Lon + halfDegrees, center.Lat - halfDegrees},
		{center.Lon + halfDegrees, center.Lat + halfDegrees},
		{center.Lon - halfDegrees, center.Lat + halfDegrees},
		{center.Lon - halfDegrees, center.Lat - halfDegrees},
	}

	ring := ""
	for position, corner := range corners {
		if position > 0 {
			ring += ","
		}
		// Longitude first, as in PointGeometryAt and for the same reason.
		ring += fmt.Sprintf("[%g,%g]", corner[0], corner[1])
	}

	return domain.Geometry{
		TargetPath: providerGeoTarget,
		SourcePath: fmt.Sprintf(providerGeoPolygonSource, index),
		Type:       "Polygon",
		GeoJSON:    json.RawMessage(`{"type":"Polygon","coordinates":[[` + ring + `]]}`),
	}
}

// CenterOf reports the coordinate of a Point geometry, and false for anything
// else. The exact S_DWITHIN refinement applies to Point-to-Point alone, so a
// Center populated on any other shape would silently narrow an operator it was
// never meant to touch.
func CenterOf(geometry domain.Geometry) (domain.GeoPoint, bool) {
	var shape struct {
		Type        string    `json:"type"`
		Coordinates []float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(geometry.GeoJSON, &shape); err != nil {
		return domain.GeoPoint{}, false
	}
	if shape.Type != "Point" || len(shape.Coordinates) < 2 {
		return domain.GeoPoint{}, false
	}
	return domain.GeoPoint{Lat: shape.Coordinates[1], Lon: shape.Coordinates[0]}, true
}
