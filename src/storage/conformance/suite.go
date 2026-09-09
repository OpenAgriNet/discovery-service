package conformance

import (
	"slices"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// Backends is the pair of ports one case runs against.
//
// Both, not one each: a catalog written through the write port has to be
// visible through the read port, and a suite that could only see one of them
// would pin half of what a backend does.
type Backends struct {
	Catalogs domain.CatalogRepository
	Search   domain.SearchRepository
}

// NewBackends builds a FRESH, empty pair.
//
// A factory rather than a Backends value, because each case must start from an
// empty store: a suite in which case three only passes after case two has run
// pins the order of the file, not the behaviour of the backend. The *testing.T
// is there so a backend can register its own cleanup — dropping a schema,
// closing a pool — against the case that used it.
type NewBackends func(t *testing.T) Backends

// Publish is one catalog write in a case's setup.
//
// WantFaultCodes is the codes UpsertCatalog must return, in order, and the zero
// value means none. It exists because a partial is an ordinary setup step — a
// fixture whose geometry deliberately will not parse has to be able to say so —
// and a runner that ignored faults would let a fixture swallow the one thing it
// was written to produce.
type Publish struct {
	Patch          domain.CatalogPatch
	Mode           domain.UpdateMode
	Derive         domain.DeriveFunc
	WantFaultCodes []string
}

// Case is one behaviour every backend must show: a setup expressed as publishes
// and an assertion expressed against the ports.
//
// Given is data rather than a function, so the runner can apply it identically
// to every backend; Then is a function, because what a case asserts is the
// thing that differs between cases.
type Case struct {
	Name  string
	Given []Publish
	Then  func(t *testing.T, backends Backends)
}

// Run applies each case's Given to a fresh pair of backends and then its Then.
//
// The whole point of this indirection: a backend's own test file supplies
// nothing but a factory, so a case added for Postgres runs against memory the
// same day. That is the one thing keeping the two from drifting.
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
// fixture said it would.
//
// t.Fatalf and not t.Errorf: a case whose setup did not happen is not a case
// that failed, it is a case that never ran, and letting the assertion proceed
// reports the wrong thing broken.
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
