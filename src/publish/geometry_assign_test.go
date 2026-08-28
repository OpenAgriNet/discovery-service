package publish

import (
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// An offer may name the same resource twice, and the shape it carries must
// still land on that resource ONCE.
//
// Internal, because assignGeometries is where the property lives and the
// exported path reaches it only through a repository. `offer.ResourceIDs` is
// publisher-supplied and nothing upstream dedupes it — MapCatalog resolves the
// empty case to a catalog-wide slice and passes the rest through — so this is
// reachable from a request, not hypothetical. Two rows for one shape on one
// resource is a duplicate the geometry table has no key to reject.
func TestARepeatedOwnerStoresTheShapeOnce(t *testing.T) {
	merged := &domain.Catalog{
		ID:        "c1",
		Resources: []domain.Resource{{ID: "wheat"}, {ID: "rice"}},
	}

	assignGeometries(merged, []domain.Geometry{{
		SourcePath: "$['catalogs'][0]['offers'][0]['provider']['availableAt'][0]['geo']",
		Owners:     []string{"wheat", "rice", "wheat"},
	}})

	if got := len(merged.Resources[0].Geometries); got != 1 {
		t.Errorf("wheat carries %d copies of the shape, want 1: the owner list named it twice", got)
	}
	if got := len(merged.Resources[1].Geometries); got != 1 {
		t.Errorf("rice carries %d copies of the shape, want 1", got)
	}
	if got := len(merged.Geometries); got != 0 {
		t.Errorf("the catalog carries %d shapes, want 0: an owned shape is not catalog-wide", got)
	}
}

// An owner naming a resource the catalog does not hold is skipped rather than
// panicking. Reachable the same way: offer.ResourceIDs is not checked against
// the resource list anywhere on the publish path.
func TestAnOwnerTheCatalogDoesNotHoldIsSkipped(t *testing.T) {
	merged := &domain.Catalog{
		ID:        "c1",
		Resources: []domain.Resource{{ID: "wheat"}},
	}

	assignGeometries(merged, []domain.Geometry{{
		SourcePath: "$['catalogs'][0]['offers'][0]['provider']['availableAt'][0]['geo']",
		Owners:     []string{"wheat", "sorghum"},
	}})

	if got := len(merged.Resources[0].Geometries); got != 1 {
		t.Errorf("wheat carries %d shapes, want 1", got)
	}
}
