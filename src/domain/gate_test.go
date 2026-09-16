package domain

import (
	"testing"
	"time"
)

// ScopeGate.Matches has no production caller yet — grep confirms it — but it
// is a pure comparison, fully testable directly. It exists to let a caller
// skip rewriting a resource that already carries its catalog's exact gate.
func TestScopeGateMatchesEveryFieldIncludingBothNilClockBounds(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	gate := ScopeGate{
		VisibleTo: []string{"mahavistar"}, Active: true, ValidFrom: from, ValidTo: to,
	}

	same := Resource{VisibleTo: []string{"mahavistar"}, Active: true, ValidFrom: from, ValidTo: to}
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
	nine := TimeOfDay{Hour: 9}
	ten := TimeOfDay{Hour: 10}

	cases := []struct {
		name        string
		gateBound   *TimeOfDay
		resourceOwn *TimeOfDay
		want        bool
	}{
		{"both nil", nil, nil, true},
		{"gate set, resource nil", &nine, nil, false},
		{"gate nil, resource set", nil, &nine, false},
		{"both set and equal", &nine, &nine, true},
		{"both set and different", &nine, &ten, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			gate := ScopeGate{ValidTimeFrom: testCase.gateBound}
			resource := Resource{ValidTimeFrom: testCase.resourceOwn}
			if got := gate.Matches(resource); got != testCase.want {
				t.Errorf("Matches = %v, want %v", got, testCase.want)
			}
		})
	}
}

// resourceWord's own two branches: "a resource" for exactly one missing id,
// "resources" for any other count — checked at both 1 and 2+ so the singular
// spelling isn't the only one a passing suite ever exercised.
func TestFaultsNamesOneMissingResourceInTheSingularAndTwoInThePlural(t *testing.T) {
	one := PruneOfferReferences(&Catalog{
		Resources: []Resource{{ID: "wheat"}},
		Offers:    []Offer{{ID: "o1", ResourceIDs: []string{"wheat", "typo"}}},
	})
	if faults := Faults(one, "SCH_DANGLING"); len(faults) != 1 ||
		faults[0].Message != `offer "o1" references a resource this catalog does not have: typo` {
		t.Errorf("faults = %+v, want the singular phrasing naming one missing id", faults)
	}

	two := PruneOfferReferences(&Catalog{
		Resources: []Resource{{ID: "wheat"}},
		Offers:    []Offer{{ID: "o1", ResourceIDs: []string{"typo-a", "typo-b"}}},
	})
	faults := Faults(two, "SCH_DANGLING")
	if len(faults) != 1 ||
		faults[0].Message != `offer "o1" references resources this catalog does not have: typo-a, typo-b`+
			`; every id it named was missing, so the offer was not stored` {
		t.Errorf("faults = %+v, want the plural phrasing and the dropped note "+
			"— every id named was missing, so the offer itself was not kept", faults)
	}
}
