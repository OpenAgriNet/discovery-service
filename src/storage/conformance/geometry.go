// Package conformance is the one suite every backend must pass.
//
// A backend is accepted by passing the fixtures both existing backends pass,
// not by review — which is what makes "not tied to one database" (TRD §5, T7) a
// property of the build rather than a claim in a document. It holds the fixture
// TYPES here; the cases arrive with the tasks that give the repositories the
// behaviour worth pinning.
package conformance

import (
	"encoding/json"
	"fmt"

	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// providerGeoTarget is the wildcard path a catalog's own locations are found
// under, and it is byte-identical to what a caller's `targets` canonicalises
// to. Fixtures compare against this rather than spelling it again: a fixture
// with its own copy of the path passes while the walker stores something else.
const providerGeoTarget = `$['catalogs'][*]['provider']['availableAt'][*]['geo']`

// providerGeoSource is the same path with the availableAt index left concrete —
// the catalogs index stays wildcard, because it is a property of the request
// rather than of the catalog.
const providerGeoSource = `$['catalogs'][*]['provider']['availableAt'][%d]['geo']`

// PointGeometryAt builds the provider-path Point geometry at one availableAt
// index, so that "somewhere" is spelled one way across every fixture.
//
// Owners is left empty, because a CATALOG's provider location is catalog-level:
// it is stored once with a NULL resource id and shared by every resource under
// it. An OFFER's provider location is not, and the fixtures needing one give
// Owners the ids that offer covers.
func PointGeometryAt(index int, point domain.GeoPoint) domain.Geometry {
	return domain.Geometry{
		TargetPath: providerGeoTarget,
		SourcePath: fmt.Sprintf(providerGeoSource, index),
		Type:       "Point",
		// Longitude first: RFC 7946 orders coordinates x, y, and a fixture that
		// swapped them would put Bengaluru in the Arabian Sea and still index,
		// search and match perfectly well against itself.
		GeoJSON: json.RawMessage(fmt.Sprintf(`{"type":"Point","coordinates":[%g,%g]}`, point.Lon, point.Lat)),
	}
}

// PointGeometries builds a catalog's provider locations in the order given,
// numbering the paths so that two locations are distinguishable — which is the
// entire job SourcePath does.
func PointGeometries(points ...domain.GeoPoint) []domain.Geometry {
	geometries := make([]domain.Geometry, 0, len(points))
	for index, point := range points {
		geometries = append(geometries, PointGeometryAt(index, point))
	}
	return geometries
}
