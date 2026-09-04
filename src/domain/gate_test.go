package domain_test

import (
	"testing"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// ScopeGate.Matches has no production caller yet — grep confirms it — but it
// is a pure comparison, fully testable directly. It exists to let a caller
// skip rewriting a resource that already carries its catalog's exact gate.
func TestScopeGateMatchesEveryFieldIncludingBothNilClockBounds(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	gate := domain.ScopeGate{
		VisibleTo: []string{"mahavistar"}, Active: true, ValidFrom: from, ValidTo: to,
	}

	same := domain.Resource{VisibleTo: []string{"mahavistar"}, Active: true, ValidFrom: from, ValidTo: to}
	if !gate.Matches(same) {
		t.Error("Matches = false for a resource carrying the identical gate, want true")
	}

	different := same
	different.Active = false
	if gate.Matches(different) {
		t.Error("Matches = true for a resource whose Active disagrees, want false")
	}
}

// sameTimeOfDay's own three cases: both nil, one nil, both set and equal or
// not — Matches only ever calls it, so it is exercised through the gate
// rather than in isolation.
func TestScopeGateMatchesOnClockBoundsBothPresentBothAbsentAndOneEach(t *testing.T) {
	nine := domain.TimeOfDay{Hour: 9}
	ten := domain.TimeOfDay{Hour: 10}

	cases := []struct {
		name        string
		gateBound   *domain.TimeOfDay
		resourceOwn *domain.TimeOfDay
		want        bool
	}{
		{"both nil", nil, nil, true},
		{"gate set, resource nil", &nine, nil, false},
		{"gate nil, resource set", nil, &nine, false},
		{"both set and equal", &nine, &nine, true},
		{"both set and different", &nine, &ten, false},
	}
	for _, testCase := range cases {
		gate := domain.ScopeGate{ValidTimeFrom: testCase.gateBound}
		resource := domain.Resource{ValidTimeFrom: testCase.resourceOwn}
		if got := gate.Matches(resource); got != testCase.want {
			t.Errorf("%s: Matches = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

// resourceWord's own two branches: "a resource" for exactly one missing id,
// "resources" for any other count — checked at both 1 and 2+ so the singular
// spelling isn't the only one a passing suite ever exercised.
func TestFaultsNamesOneMissingResourceInTheSingularAndTwoInThePlural(t *testing.T) {
	one := domain.PruneOfferReferences(&domain.Catalog{
		Resources: []domain.Resource{{ID: "wheat"}},
		Offers:    []domain.Offer{{ID: "o1", ResourceIDs: []string{"wheat", "typo"}}},
	})
	if faults := domain.Faults(one, "SCH_DANGLING"); len(faults) != 1 ||
		faults[0].Message != `offer "o1" references a resource this catalog does not have: typo` {
		t.Errorf("faults = %+v, want the singular phrasing naming one missing id", faults)
	}

	two := domain.PruneOfferReferences(&domain.Catalog{
		Resources: []domain.Resource{{ID: "wheat"}},
		Offers:    []domain.Offer{{ID: "o1", ResourceIDs: []string{"typo-a", "typo-b"}}},
	})
	faults := domain.Faults(two, "SCH_DANGLING")
	if len(faults) != 1 ||
		faults[0].Message != `offer "o1" references resources this catalog does not have: typo-a, typo-b`+
			`; every id it named was missing, so the offer was not stored` {
		t.Errorf("faults = %+v, want the plural phrasing and the dropped note "+
			"— every id named was missing, so the offer itself was not kept", faults)
	}
}
