package fact_test

import (
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// The spec's item.prevstate and item.state are free-text, so the three values
// this service distinguishes are a decision rather than a reading. They are
// pinned here because a rename is invisible to the compiler and silently
// rewrites the history a grievance is answered from.
func TestCatalogStateNamesTheThreeStatesTheSpecDistinguishes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		exists bool
		active bool
		want   string
	}{
		{"a catalog that is not stored yet", false, false, "absent"},
		{"absent does not depend on the active flag", false, true, "absent"},
		{"a stored catalog the publisher marked active", true, true, "active"},
		{"a stored catalog the publisher marked inactive", true, false, "inactive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fact.CatalogState(tc.exists, tc.active); got != tc.want {
				t.Errorf("CatalogState(%t, %t) = %q, want %q", tc.exists, tc.active, got, tc.want)
			}
		})
	}
}
