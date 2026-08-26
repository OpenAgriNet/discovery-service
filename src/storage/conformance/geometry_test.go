package conformance_test

import (
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/storage/conformance"
)

const bengaluruLat, bengaluruLon = 12.9716, 77.5946

func TestAProviderPointIsBuiltAtItsIndex(t *testing.T) {
	geometry := conformance.PointGeometryAt(2, domain.GeoPoint{Lat: bengaluruLat, Lon: bengaluruLon})

	if want := `$['catalogs'][*]['provider']['availableAt'][*]['geo']`; geometry.TargetPath != want {
		t.Errorf("TargetPath = %q, want %q", geometry.TargetPath, want)
	}
	if want := `$['catalogs'][*]['provider']['availableAt'][2]['geo']`; geometry.SourcePath != want {
		t.Errorf("SourcePath = %q, want %q", geometry.SourcePath, want)
	}
	if geometry.Type != "Point" {
		t.Errorf("Type = %q, want Point", geometry.Type)
	}
	// Longitude first: RFC 7946 orders coordinates x, y.
	if want := `{"type":"Point","coordinates":[77.5946,12.9716]}`; string(geometry.GeoJSON) != want {
		t.Errorf("GeoJSON = %s, want %s", geometry.GeoJSON, want)
	}
}

// A catalog's provider location belongs to the catalog, so it has no owning
// resource. A helper that guessed one would put a `resource_geometries` row
// under a resource the walker never found it in.
func TestAProviderPointHasNoOwner(t *testing.T) {
	if owners := conformance.PointGeometryAt(0, domain.GeoPoint{}).Owners; len(owners) != 0 {
		t.Errorf("Owners = %v, want empty — a provider location is catalog-level", owners)
	}
}

func TestSeveralPointsAreNumberedInOrder(t *testing.T) {
	geometries := conformance.PointGeometries(
		domain.GeoPoint{Lat: bengaluruLat, Lon: bengaluruLon},
		domain.GeoPoint{Lat: 13.0827, Lon: 80.2707},
	)

	if len(geometries) != 2 {
		t.Fatalf("built %d geometries, want 2", len(geometries))
	}
	for index, geometry := range geometries {
		want := conformance.PointGeometryAt(index, domain.GeoPoint{}).SourcePath
		if geometry.SourcePath != want {
			t.Errorf("geometry %d has SourcePath %q, want %q", index, geometry.SourcePath, want)
		}
	}
	if geometries[0].SourcePath == geometries[1].SourcePath {
		t.Error("two provider locations share a SourcePath; nothing could tell them apart")
	}
}

func TestNoPointsIsNoGeometries(t *testing.T) {
	if geometries := conformance.PointGeometries(); len(geometries) != 0 {
		t.Errorf("built %d geometries from no points", len(geometries))
	}
}
