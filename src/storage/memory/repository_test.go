package memory_test

import (
	"errors"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/storage/conformance"
	"github.com/OpenAgriNet/discovery-service/src/storage/memory"
)

// resolution is the H3 resolution these fixtures cover at, and it must be the
// one the Postgres suite uses: the conformance cases compare the two backends'
// answers to the same spatial query, and two backends covering at different
// resolutions produce cell sets that never intersect.
const resolution = 8

// The skeleton's only pin, and it is a compile-time one: this backend has no
// behaviour yet, so there is nothing to assert about its answers. What there IS
// to assert is that it still satisfies both ports — which is the whole claim
// the memory backend exists to make, and the one a later task breaks by adding
// a method to an interface and fixing up only Postgres.
var (
	_ domain.CatalogRepository = (*memory.Repository)(nil)
	_ domain.SearchRepository  = (*memory.Repository)(nil)
)

func TestNewReturnsAnEmptyStore(t *testing.T) {
	store := memory.New(resolution)

	_, err := store.GetCatalog(t.Context(), "nothing-published")
	if !errors.Is(err, domain.ErrCatalogNotFound) {
		t.Fatalf("a fresh store answered GetCatalog with %v, want domain.ErrCatalogNotFound", err)
	}
}

// ListCatalogResources has no caller in this repository's own conformance
// run — Postgres's own ListCatalogResources is exercised through
// GetCatalogRow's merge path, and the memory twin has no equivalent caller —
// so it is checked directly here: the found case and ErrCatalogNotFound.
func TestListCatalogResources(t *testing.T) {
	store := memory.New(resolution)

	if _, err := store.ListCatalogResources(t.Context(), "nothing-published"); !errors.Is(err, domain.ErrCatalogNotFound) {
		t.Fatalf("a fresh store answered ListCatalogResources with %v, want domain.ErrCatalogNotFound", err)
	}

	if _, err := store.UpsertCatalog(t.Context(), domain.CatalogPatch{
		ID:        "c1",
		NetworkID: "bap.example.com",
		Active:    true,
		VisibleTo: []string{"bap.example.com"},
		Resources: []domain.ResourcePatch{{ID: "r1"}, {ID: "r2"}},
	}, domain.UpdateModeFull, nil); err != nil {
		t.Fatalf("publish: %v", err)
	}

	resources, err := store.ListCatalogResources(t.Context(), "c1")
	if err != nil {
		t.Fatalf("ListCatalogResources: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("resources = %+v, want the two published", resources)
	}
}

// A filter mode this backend does not declare — jsonpath, per Capabilities'
// own doc comment: the store holds documents, not PostgreSQL's jsonpath
// engine — is reported degraded rather than silently narrowing nothing.
// negotiate's other arms (a ranked mode missing, a filter mode present) are
// pinned by the conformance suite; this is the one combination — an
// UNRANKED mode this backend lacks — nothing else here reaches.
func TestSearchDegradesAFilterModeThisBackendLacks(t *testing.T) {
	store := memory.New(resolution)

	result, err := store.Search(t.Context(), domain.SearchQuery{}, []domain.Capability{domain.CapabilityJSONPath})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Degraded) != 1 || result.Degraded[0] != string(domain.CapabilityJSONPath) {
		t.Errorf("Degraded = %v, want [jsonpath]", result.Degraded)
	}
}

// The write-path suite, run against this backend.
//
// The factory is all this file supplies: a case added for Postgres in Task 15
// runs against memory the same day, which is the one thing keeping the two from
// drifting on semantics neither SQL nor a map can claim as its own.
func TestMemorySatisfiesThePublishConformanceSuite(t *testing.T) {
	conformance.Run(t, func(*testing.T) conformance.Backends {
		store := memory.New(resolution)
		return conformance.Backends{Catalogs: store, Search: store}
	}, conformance.PublishCases())
}

// The read-path suite, run against this backend.
//
// The resolution handed to the store and the one handed to the cases is the
// same constant on purpose: it is the setting the two backends must hold equal
// for a spatial case to mean anything, and a fixture covered at a different
// resolution from the store fails by matching nothing rather than by erroring.
func TestMemorySatisfiesTheDiscoverConformanceSuite(t *testing.T) {
	conformance.Run(t, func(*testing.T) conformance.Backends {
		store := memory.New(resolution)
		return conformance.Backends{Catalogs: store, Search: store}
	}, conformance.DiscoverCases(resolution))
}
