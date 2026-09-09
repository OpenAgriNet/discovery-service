package conformance

import (
	"slices"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// Backends is the pair of ports one case runs against. Both, not one each: a
// catalog written through the write port has to be visible through the read
// port.
type Backends struct {
	Catalogs domain.CatalogRepository
	Search   domain.SearchRepository
}

// NewBackends builds a FRESH, empty pair.
//
// A factory rather than a Backends value, because each case must start from an
// empty store: a suite where case three passes only after case two has run pins
// the order of the file, not the backend. The *testing.T lets a backend register
// its own cleanup against the case that used it.
type NewBackends func(t *testing.T) Backends

// Publish is one catalog write in a case's setup.
type Publish struct {
	Patch  domain.CatalogPatch
	Mode   domain.UpdateMode
	Derive domain.DeriveFunc

	// The codes UpsertCatalog must return, in order; the zero value means none.
	// A partial is an ordinary setup step, and a runner that ignored faults
	// would let a fixture swallow the one thing it was written to produce.
	WantFaultCodes []string
}

// Case is one behaviour every backend must show: a setup expressed as publishes
// and an assertion expressed against the ports.
//
// Given is data so the runner applies it identically to every backend; Then is a
// function because the assertion is what differs between cases.
type Case struct {
	Name  string
	Given []Publish
	Then  func(t *testing.T, backends Backends)
}

// Run applies each case's Given to a fresh pair of backends and then its Then.
//
// A backend's own test file supplies nothing but a factory, so a case added for
// Postgres runs against memory the same day — which is what keeps the two from
// drifting.
func Run(t *testing.T, newBackends NewBackends, cases []Case) {
	t.Helper()

	for _, testCase := range cases {
		t.Run(testCase.Name, func(t *testing.T) {
			backends := newBackends(t)
			for index, publish := range testCase.Given {
				apply(t, backends, index, publish)
			}
			testCase.Then(t, backends)
		})
	}
}

// apply performs one setup publish and fails the case if it did not go as the
// fixture said it would. t.Fatalf and not t.Errorf: a case whose setup did not
// happen never ran, and letting the assertion proceed reports the wrong thing
// broken.
func apply(t *testing.T, backends Backends, index int, publish Publish) {
	t.Helper()

	faults, err := backends.Catalogs.UpsertCatalog(t.Context(), publish.Patch, publish.Mode, publish.Derive)
	if err != nil {
		t.Fatalf("given[%d]: publishing %q: %v", index, publish.Patch.ID, err)
	}

	codes := make([]string, 0, len(faults))
	for _, fault := range faults {
		codes = append(codes, fault.Code)
	}
	if !slices.Equal(codes, publish.WantFaultCodes) {
		t.Fatalf("given[%d]: publishing %q returned faults %v, want %v",
			index, publish.Patch.ID, codes, publish.WantFaultCodes)
	}
}
