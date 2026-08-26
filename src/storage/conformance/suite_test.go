package conformance_test

import (
	"errors"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/storage/conformance"
	"github.com/OpenAgriNet/discovery-service/src/storage/memory"
)

// memoryBackends is what every backend's own suite supplies: a factory, not an
// instance, so each case starts from an empty store and a case that leaves
// state behind cannot decide the next one.
func memoryBackends(*testing.T) conformance.Backends {
	store := memory.New()
	return conformance.Backends{Catalogs: store, Search: store}
}

// The suite has no fixtures yet — the cases arrive with the tasks that give the
// repositories behaviour. What is pinned here is the harness itself, because a
// runner that silently skipped its setup would make every later fixture pass.
func TestRunAppliesACasesGivenBeforeItsAssertion(t *testing.T) {
	conformance.Run(t, memoryBackends, []conformance.Case{{
		Name: "a published catalog is readable",
		Given: []conformance.Publish{{
			Patch: domain.CatalogPatch{ID: "c1", NetworkID: "n1", Active: true},
			Mode:  domain.UpdateModeMerge,
		}},
		Then: func(t *testing.T, backends conformance.Backends) {
			stored, err := backends.Catalogs.GetCatalog(t.Context(), "c1")
			if err != nil {
				t.Fatalf("GetCatalog after the given publish: %v", err)
			}
			if !stored.Active {
				t.Error("the published catalog came back inactive")
			}
		},
	}})
}

func TestRunGivesEachCaseItsOwnStore(t *testing.T) {
	published := conformance.Publish{
		Patch: domain.CatalogPatch{ID: "c1", NetworkID: "n1", Active: true},
		Mode:  domain.UpdateModeMerge,
	}

	conformance.Run(t, memoryBackends, []conformance.Case{{
		Name:  "first case publishes",
		Given: []conformance.Publish{published},
		Then:  func(*testing.T, conformance.Backends) {},
	}, {
		Name: "second case sees nothing the first wrote",
		Then: func(t *testing.T, backends conformance.Backends) {
			_, err := backends.Catalogs.GetCatalog(t.Context(), "c1")
			if !errors.Is(err, domain.ErrCatalogNotFound) {
				t.Fatalf("GetCatalog = %v, want ErrCatalogNotFound — the store leaked between cases", err)
			}
		},
	}})
}

// A fixture that needs a partial in its setup — a geometry that will not parse,
// say — has to be able to say so. The alternative is a fixture that swallows
// the fault and then wonders why the row it expected is missing.
func TestAGivenPublishMayExpectFaults(t *testing.T) {
	conformance.Run(t, memoryBackends, []conformance.Case{{
		Name: "a partial publish still commits",
		Given: []conformance.Publish{{
			Patch: domain.CatalogPatch{ID: "c1", NetworkID: "n1", Active: true},
			Mode:  domain.UpdateModeMerge,
			Derive: func(*domain.Catalog, []string) []domain.Fault {
				return []domain.Fault{{Path: "$.geo", Code: "DOM_BAD_GEOMETRY"}}
			},
			WantFaultCodes: []string{"DOM_BAD_GEOMETRY"},
		}},
		Then: func(t *testing.T, backends conformance.Backends) {
			if _, err := backends.Catalogs.GetCatalog(t.Context(), "c1"); err != nil {
				t.Fatalf("a partial publish did not commit: %v", err)
			}
		},
	}})
}
