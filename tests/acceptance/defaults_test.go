package acceptance

import (
	"slices"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
)

// The two networks scenario 26 turns on. Neither is the deployment's own — the
// service runs as `oan` — because a default that resolved to the configured
// network would pass this scenario for the wrong reason.
const (
	republishedOn = "mahavistar"
	alsoVisibleOn = "bharatvistar"
)

// Scenario 26. A field a republish does not mention goes back to its default,
// in MERGE exactly as in FULL.
//
// This is A9 on the two fields where it costs something. `isActive` and
// `visibleTo` both have defaults that OPEN the catalog up — live, and visible
// on the network the request arrived over — so a republish that quietly kept
// the stored value is not a conservative choice: it is the difference between a
// catalog the publisher believes is retired and one that is answering searches.
//
// It exists to make the surprising half of A9 deliberate. MERGE reads as "leave
// what I did not mention alone", and someone will one day read that as covering
// the directive too. It does not: RFC 7396 governs the catalog DOCUMENT, and
// the directive is the envelope that says how to apply it. The day the two
// modes disagree about what silence means, this is the test that fails.
func TestAnOmittedDefaultResetsInBothModes(t *testing.T) {
	for _, mode := range []struct {
		name       string
		directives []map[string]any
	}{
		// MERGE is spelled by sending NO directive at all, which is the only
		// way to reach the field-wise default for every member at once: the
		// schema puts catalogType in a directive's required list, so a
		// directive exists to be partly filled, never empty.
		{"merge", nil},
		{"full", []map[string]any{directive("c-retired", updateMode(beckn.UpdateModeFull))}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			svc := newService(t)
			retired(t, svc)

			// The patch: one attribute, no isActive, no visibleTo, and — for
			// MERGE — no directive either. Under FULL the same document is the
			// whole catalog, which is why the resource is carried in both.
			svc.publishOnWith(t, republishedOn,
				[]any{aCatalog("c-retired",
					availableAt(majestic),
					resources(aResource("r-retired", "wheat",
						withAttributes(map[string]any{"grade": "A"}))),
				)},
				mode.directives...)

			// Live again: isActive was false and the patch never mentions it.
			onNew := svc.discoverOn(t, republishedOn, spatial(dwithin(providerGeoPath, majestic, 5000)))
			if got := resourceIDs(onNew); !slices.Equal(got, []string{"r-retired"}) {
				t.Errorf("on %s = %v, want [r-retired]: an omitted isActive defaults to live",
					republishedOn, got)
			}

			// And visible only where the republish came FROM. The stored
			// visibleTo named this network too; an omitted visibleTo replaces
			// that list rather than adding to it.
			onOld := svc.discoverOn(t, alsoVisibleOn, spatial(dwithin(providerGeoPath, majestic, 5000)))
			if got := resourceIDs(onOld); len(got) != 0 {
				t.Errorf("on %s = %v, want none: an omitted visibleTo resets to [%s]",
					alsoVisibleOn, got, republishedOn)
			}
		})
	}
}

// retired publishes the catalog both halves of the scenario start from: switched
// off by its publisher and visible on two networks, neither of them the
// deployment's own.
func retired(t *testing.T, svc *service) {
	t.Helper()

	results := svc.publishWith(t,
		[]any{aCatalog("c-retired",
			availableAt(majestic),
			resources(aResource("r-retired", "wheat")),
			inactive(),
		)},
		directive("c-retired", visibleTo(republishedOn, alsoVisibleOn)))
	if len(results) != 1 || results[0].Status != beckn.StatusAccepted {
		t.Fatalf("the first publish = %+v, want one ACCEPTED", results)
	}

	// Stated rather than assumed: everything the republish then changes has to
	// be a change from somewhere, and an inactive catalog answering nothing is
	// the baseline both halves of the assertion are measured against.
	if got := resourceIDs(svc.discoverOn(t, republishedOn,
		spatial(dwithin(providerGeoPath, majestic, 5000)))); len(got) != 0 {
		t.Fatalf("the retired catalog answers = %v, want none: isActive is false", got)
	}
}
