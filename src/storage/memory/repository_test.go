package memory_test

import (
	"errors"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/storage/conformance"
	"github.com/OpenAgriNet/discovery-service/src/storage/memory"
)

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
	store := memory.New()

	_, err := store.GetCatalog(t.Context(), "nothing-published")
	if !errors.Is(err, domain.ErrCatalogNotFound) {
		t.Fatalf("a fresh store answered GetCatalog with %v, want domain.ErrCatalogNotFound", err)
	}
}

// The write-path suite, run against this backend.
//
// The factory is all this file supplies: a case added for Postgres in Task 15
// runs against memory the same day, which is the one thing keeping the two from
// drifting on semantics neither SQL nor a map can claim as its own.
func TestMemorySatisfiesThePublishConformanceSuite(t *testing.T) {
	conformance.Run(t, func(*testing.T) conformance.Backends {
		store := memory.New()
		return conformance.Backends{Catalogs: store, Search: store}
	}, conformance.PublishCases())
}
