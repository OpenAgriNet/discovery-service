package acceptance

import (
	"slices"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// The two halves of scenario 12 and 13 share a fixture: one catalog in
// Bengaluru and one 400 km away in Hyderabad. Built once because the negative
// half is only meaningful against the same corpus the positive half searched —
// a radius that returns nothing because nothing was published is not the
// assertion either scenario is making.
func twoCitiesApart(t *testing.T) *service {
	t.Helper()

	svc := newService(t)
	svc.publishCatalogs(t,
		aCatalog("c-here", availableAt(majestic), resources(aResource("r-here", "Here"))),
		aCatalog("c-far", availableAt(hyderabad), resources(aResource("r-far", "Far"))))
	return svc
}

// Scenario 12. A radius query returns what is inside it.
//
// Three layers have to agree for this to pass and any one of them can be wrong
// on its own: the cover written at publish time, the cell-set predicate that
// finds a candidate, and the haversine that refines it. A cover written at the
// wrong resolution, a predicate reading the wrong column, a distance in
// degrees — each answers this scenario with an empty page.
func TestGeoSearchFindsNearbyResources(t *testing.T) {
	svc := twoCitiesApart(t)

	got := resourceIDs(svc.near(t, majestic, 5000))
	if !slices.Equal(got, []string{"r-here"}) {
		t.Errorf("a 5 km radius around Majestic returned %v, want [r-here]", got)
	}
}

// Scenario 13. The negative half, and it is not redundant.
//
// Without it a predicate that returns EVERY row passes scenario 12 — the
// resource it wants is in the answer, along with everything else. This is the
// scenario that says the radius is a filter rather than a formality.
func TestGeoSearchOutsideTheRadiusReturnsNothing(t *testing.T) {
	svc := twoCitiesApart(t)

	// Centred on Hyderabad's catalog, small enough to exclude Bengaluru and
	// large enough to include the row it is centred on — so an empty answer
	// here would mean the radius excluded its own centre.
	if got := resourceIDs(svc.near(t, hyderabad, 5000)); !slices.Equal(got, []string{"r-far"}) {
		t.Fatalf("a 5 km radius around Hyderabad returned %v, want [r-far]", got)
	}

	// And a radius around a third place that holds nothing at all.
	empty := [2]float64{72.8777, 19.0760} // Mumbai
	if got := resourceIDs(svc.near(t, empty, 5000)); len(got) != 0 {
		t.Errorf("a 5 km radius around an empty place returned %v, want nothing", got)
	}
}

// Scenario 14. The boundary, to 306 metres.
//
// Two points due east of Majestic: one 9 836 m away and one 10 141 m away,
// straddling a 10 km radius with 306 m between them. H3 at r8 answers this to
// about 1.1 km, so both sit in the MAYBE band and cell algebra alone returns
// both. What separates them is the Point-to-Point haversine refinement, which
// is the whole reason that refinement exists — and the reason it runs in SQL,
// beside the candidate row, rather than in Go over a page already chosen.
//
// The distances are stated rather than computed, because a fixture that derived
// its own coordinates from the same formula the service uses would agree with
// the service by construction. dbtest's TestHaversineAgreesWithItsGoTwin is
// where the formula itself is pinned.
func TestTheRadiusBoundaryIsExact(t *testing.T) {
	var (
		inside  = [2]float64{77.662072, 12.9767} // 9 836 m from Majestic
		outside = [2]float64{77.664893, 12.9767} // 10 141 m from Majestic
	)

	svc := newService(t)
	svc.publishCatalogs(t,
		aCatalog("c-inside", availableAt(inside), resources(aResource("r-inside", "Inside"))),
		aCatalog("c-outside", availableAt(outside), resources(aResource("r-outside", "Outside"))))

	got := resourceIDs(svc.near(t, majestic, 10000))
	if !slices.Equal(got, []string{"r-inside"}) {
		t.Errorf("a 10 km radius returned %v, want [r-inside] only: the two fixtures are 306 m apart "+
			"and the cell cover cannot separate them", got)
	}
}

// Scenario 15. Three locations, forty resources, three rows.
//
// The nullable resource_id on resource_geometries is what makes a catalog's own
// locations storable once and shared by every resource in it. This scenario
// pins it in BOTH directions, and each direction fails differently:
//
//   - the storage saving — 3 rows and not 120 — which no response can show,
//     because a table holding forty copies of the same cover answers every
//     search exactly as a table holding one;
//   - the `g.resource_id IS NULL` half of the search predicate, without which a
//     catalog-level geometry belongs to no resource and every geo search in
//     this suite returns nothing.
func TestCatalogGeometryIsCoveredOnceAndSharedByEveryResource(t *testing.T) {
	svc := newService(t)

	const count = 40
	svc.publishCatalogs(t, aCatalog("c-shared",
		availableAt(majestic, koramangala, hyderabad),
		resources(manyResources(count)...)))

	rows := dbtest.ResourceGeometries(t, svc.pool, "c-shared")
	if len(rows) != 3 {
		t.Errorf("the catalog stored %d geometry rows, want 3 — one per provider location, "+
			"shared by every resource:\n%v", len(rows), rows)
	}
	// Every row is catalog-level — the "*" — and each names a different one of
	// the three locations. Forty rows per location would also be three distinct
	// paths, so the count above and the paths here are both needed.
	want := []string{
		"*|$['catalogs'][*]['provider']['availableAt'][0]['geo']",
		"*|$['catalogs'][*]['provider']['availableAt'][1]['geo']",
		"*|$['catalogs'][*]['provider']['availableAt'][2]['geo']",
	}
	if !slices.Equal(rows, want) {
		t.Errorf("the catalog stored\n%v\nwant\n%v", rows, want)
	}

	// The third location, which is the one a writer that stored only the first
	// would have dropped. Paged explicitly: the default page is twenty.
	found := svc.discoverPaged(t, "", count, spatial(dwithin(providerGeoPath, hyderabad, 5000)))
	if got := resourceIDs(found); len(got) != count {
		t.Errorf("a radius around the third location returned %d resources, want %d", len(got), count)
	}
}

// Scenario 16. A polygon answers the same question a point does.
//
// This pair is the regression test for the design this section replaced, where
// a polygon inside the radius was missing from ANY and present in NONE — the
// two halves disagreeing with each other, which is worse than either being
// wrong, because each half looked defensible alone.
//
// Two catalogs rather than the one the plan describes, which is strictly
// stronger: with a point and a polygon in the same catalog, ANY is satisfied by
// whichever of the two matched and the polygon's own answer is never visible.
// Separated, each type has to answer for itself.
func TestANonPointGeometryIsMatched(t *testing.T) {
	svc := newService(t)

	// A 2 km square centred on Majestic, and a bare point at the same place.
	svc.publishCatalogs(t,
		aCatalog("c-polygon",
			availableAtGeometry(boxAround(majestic, 1000)),
			resources(aResource("r-polygon", "Polygon"))),
		aCatalog("c-point",
			availableAt(majestic),
			resources(aResource("r-point", "Point"))))

	matched := resourceIDs(svc.near(t, majestic, 3000))
	slices.Sort(matched)
	if !slices.Equal(matched, []string{"r-point", "r-polygon"}) {
		t.Errorf("S_DWITHIN returned %v, want both types", matched)
	}

	// The same radius, inverted. Neither may come back: NONE asks for the rows
	// with no targeted geometry inside the radius, and both have one.
	excluded := resourceIDs(svc.discover(t,
		spatial(dwithin(providerGeoPath, majestic, 3000, quantified(beckn.QuantifierNone)))))
	if len(excluded) != 0 {
		t.Errorf("quantifier NONE over the same radius returned %v, want nothing: "+
			"a geometry that matches ANY must not also match NONE", excluded)
	}
}

// Scenario 17. Text and proximity in one intent, both applied.
//
// Four resources across two places and two vocabularies, so that dropping
// EITHER constraint is visible — but the two constraints are visible in
// DIFFERENT ways, and conflating them is how this scenario would end up
// asserting something false:
//
//   - the radius is a FILTER. It is carried by the WHERE clause every ranked
//     mode applies, so a resource outside it cannot appear at all. Asserted as
//     absence.
//   - the text is a RANKING. This deployment declares `semantic` as well as
//     `lexical`, and a vector retriever returns every row it is given, ordered
//     by distance; RRF then fuses the two lists by union. So a resource that
//     matches no term is still IN the answer, below the one that does.
//     Asserted as order.
//
// A scenario that demanded the tractor be absent would be demanding that
// hybrid retrieval stop being hybrid, and the way to make it pass would be to
// delete the semantic mode.
//
// One distinctive term, because discover_tsquery ORs its terms and stems
// nothing — a two-word probe would match on either word and couple this
// scenario to the tokeniser it is not about.
func TestHybridSpatialAndTextualSearch(t *testing.T) {
	svc := newService(t)

	svc.publishCatalogs(t,
		aCatalog("c-near", availableAt(majestic), resources(
			aResource("r-near-match", "cardamom"),
			aResource("r-near-other", "tractor"))),
		aCatalog("c-far", availableAt(hyderabad), resources(
			aResource("r-far-match", "cardamom"),
			aResource("r-far-other", "tractor"))))

	intent := spatial(dwithin(providerGeoPath, majestic, 5000))
	intent["textSearch"] = "cardamom"

	got := resourceIDs(svc.discover(t, intent))

	// The radius, as absence. Either of the Hyderabad resources appearing means
	// the spatial constraint was dropped from the predicate.
	for _, outside := range []string{"r-far-match", "r-far-other"} {
		if slices.Contains(got, outside) {
			t.Errorf("the hybrid intent returned %v, which includes %s 400 km away: "+
				"the radius was not applied", got, outside)
		}
	}

	// The text, as order. Both Bengaluru resources are in the answer; the one
	// that matches the term has to come first, and it does so only if the
	// lexical list was fused in.
	match := slices.Index(got, "r-near-match")
	other := slices.Index(got, "r-near-other")
	if match == -1 || other == -1 {
		t.Fatalf("the hybrid intent returned %v, want both Bengaluru resources", got)
	}
	if match > other {
		t.Errorf("the hybrid intent ranked %v: the resource matching the query text is below the one "+
			"that does not, so the lexical list was not fused in", got)
	}
}

// Scenario 19. NONE inverts the match, and absence satisfies it.
//
// Two halves, and the second is the one that belongs only to this scenario. The
// inversion is a single XOR on the geo predicate, one character away from
// silently inverting every geo search there is. The geometry-less catalog is
// the half a plain negation would miss: NOT EXISTS is satisfied by a row with
// no targeted geometry at all, so a catalog that published no location is in
// the NONE answer and in no other.
//
// Under NONE the result is a SUBSET of the exact answer rather than a superset,
// because a MAYBE cell is resolved against the row rather than for it. That is
// the inversion of the guarantee and it is the safe direction: a caller asking
// "what is not near me" is answered conservatively.
func TestQuantifierNoneInvertsTheMatch(t *testing.T) {
	svc := newService(t)

	svc.publishCatalogs(t,
		aCatalog("c-here", availableAt(majestic), resources(aResource("r-here", "Here"))),
		aCatalog("c-far", availableAt(hyderabad), resources(aResource("r-far", "Far"))),
		// No availableAt at all. It belongs to the NONE answer and to no other.
		aCatalog("c-nowhere", resources(aResource("r-nowhere", "Nowhere"))))

	if got := resourceIDs(svc.near(t, majestic, 5000)); !slices.Equal(got, []string{"r-here"}) {
		t.Fatalf("ANY over the radius returned %v, want [r-here]", got)
	}

	inverted := resourceIDs(svc.discover(t,
		spatial(dwithin(providerGeoPath, majestic, 5000, quantified(beckn.QuantifierNone)))))
	slices.Sort(inverted)
	if !slices.Equal(inverted, []string{"r-far", "r-nowhere"}) {
		t.Errorf("NONE over the same radius returned %v, want [r-far r-nowhere]: "+
			"everything ANY did not, plus the catalog that published no location", inverted)
	}
}

// Scenario 29. An omitted networkId searches every network.
//
// visibleTo restricts which networks a PUBLISHER chose to expose a catalog on.
// It is not an access boundary a network-less caller is presumed locked out
// of — a caller wanting isolation supplies networkId, and this is the scenario
// that keeps the two questions apart.
//
// The failure it prevents is a discover that borrowed publish's C8 default:
// with APP_NETWORK_ID standing in for an absent networkId, the unscoped search
// below would quietly return one catalog and look entirely correct.
func TestOmittedNetworkIDSearchesEveryNetwork(t *testing.T) {
	svc := newService(t)

	svc.publishWith(t,
		[]any{
			aCatalog("c-mahavistar", availableAt(majestic), resources(aResource("r-mahavistar", "Mahavistar"))),
			aCatalog("c-bharatvistar", availableAt(majestic), resources(aResource("r-bharatvistar", "Bharatvistar"))),
		},
		directive("c-mahavistar", visibleTo("mahavistar")),
		directive("c-bharatvistar", visibleTo("bharatvistar")))

	both := resourceIDs(svc.near(t, majestic, 5000))
	slices.Sort(both)
	if !slices.Equal(both, []string{"r-bharatvistar", "r-mahavistar"}) {
		t.Errorf("an intent with no networkId returned %v, want both networks", both)
	}

	one := resourceIDs(svc.discoverOn(t, "mahavistar", spatial(dwithin(providerGeoPath, majestic, 5000))))
	if !slices.Equal(one, []string{"r-mahavistar"}) {
		t.Errorf("an intent scoped to mahavistar returned %v, want [r-mahavistar]", one)
	}
}
