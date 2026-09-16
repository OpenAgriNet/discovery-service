package acceptance

import (
	"slices"
	"testing"

	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// The two places one resource is in scenario 27, and the pointer that reaches
// the second of them.
const serviceAreaMember = "serviceArea"

// aShopWithAServiceArea is the fixture no publish payload could build before the
// walker generalised: ONE resource with two geometries at two different paths —
// a shopfront where the provider is, and a service polygon 500 km away.
//
// While the extractor emitted a single path, the predicate could only ever be
// tested against hand-written rows, and a test that writes its own rows cannot
// tell whether a publish would have produced them.
func aShopWithAServiceArea(t *testing.T, svc *service) {
	t.Helper()

	svc.publishCatalogs(t, aCatalog("c-shop",
		availableAt(majestic),
		resources(aResource("r-shop", "wheat",
			withAttributes(map[string]any{serviceAreaMember: boxAround(hyderabad, 5000)}))),
	))
}

// Scenario 27. `targets` selects WHICH geometry answers, which is the whole
// reason it is a JSONPath and not a constant.
//
// The four searches are the two places crossed with the two pointers, and the
// diagonal is what matters: near the shopfront the service-area pointer must
// find nothing, and near the service area the provider pointer must. A
// predicate that ignored `targets` — matching any geometry of the row — would
// return the resource in all four and pass a scenario that only ever asked the
// matching pair.
func TestTargetsSelectTheRightGeometry(t *testing.T) {
	svc := newService(t)
	aShopWithAServiceArea(t, svc)

	serviceAreaPath := resourceGeoPath(serviceAreaMember)

	for _, probe := range []struct {
		name   string
		centre [2]float64
		target string
		want   []string
	}{
		{"the shopfront, through the provider", majestic, providerGeoPath, []string{"r-shop"}},
		{"the shopfront, through the service area", majestic, serviceAreaPath, nil},
		{"the service area, through the provider", hyderabad, providerGeoPath, nil},
		{"the service area, through its own path", hyderabad, serviceAreaPath, []string{"r-shop"}},
	} {
		got := resourceIDs(svc.discover(t, spatial(dwithin(probe.target, probe.centre, 5000))))
		if !slices.Equal(got, probe.want) {
			t.Errorf("%s = %v, want %v", probe.name, got, probe.want)
		}
	}

	// And with no pointer at all, the resource answers from EITHER location —
	// the same row, reached twice, because nothing narrowed which of its two
	// geometries was allowed to match.
	for _, centre := range [][2]float64{majestic, hyderabad} {
		got := resourceIDs(svc.discover(t, spatial(dwithin("", centre, 5000, everywhere()))))
		if !slices.Equal(got, []string{"r-shop"}) {
			t.Errorf("untargeted at %v = %v, want [r-shop]", centre, got)
		}
	}
}

// Scenario 28. A geometry the plan never names is indexed anyway, and the path
// it is indexed under is the one a caller's pointer canonicalises to.
//
// The first half is about the walker: `resourceAttributes.pickup.point` is not
// in any list, and it still has to be found — the extractor walks for
// geometries rather than reading known keys.
//
// The second half is the one that matters, because its failure is invisible.
// target_path is compared by string equality, so a caller sending the bracket
// form and a caller sending the dot form must arrive at the same string or one
// of them gets a 200 with an empty list — a result indistinguishable from "no
// shop is near you", which no other scenario in this suite would notice.
func TestAGeometryAnywhereIsFoundAndCanonicalised(t *testing.T) {
	svc := newService(t)

	svc.publishCatalogs(t, aCatalog("c-pickup",
		resources(aResource("r-pickup", "wheat",
			resourceGeo("pickup", "point", geoPoint(majestic)))),
	))

	// Spelled out rather than computed through jsonpath.Canonicalise: running
	// the canonicaliser on both sides would assert that it agrees with itself,
	// which it does even when both are wrong.
	const canonical = `$['catalogs'][*]['resources'][*]['resourceAttributes']['pickup']['point']`
	if got := dbtest.ResourceTargetPaths(t, svc.pool, "c-pickup"); !slices.Equal(got, []string{canonical}) {
		t.Fatalf("stored target paths = %v, want [%s]", got, canonical)
	}

	for _, form := range []struct {
		name    string
		pointer string
	}{
		{"dot", resourceGeoPath("pickup", "point")},
		{"bracket", canonical},
	} {
		got := resourceIDs(svc.discover(t, spatial(dwithin(form.pointer, majestic, 5000))))
		if !slices.Equal(got, []string{"r-pickup"}) {
			t.Errorf("the %s form (%s) = %v, want [r-pickup]", form.name, form.pointer, got)
		}
	}
}
