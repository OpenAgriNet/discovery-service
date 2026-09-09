package acceptance

import (
	"net/http"
	"slices"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// The shapes the operator scenarios are phrased over, as corners rather than
// rings, so what each pair does to the other is readable here rather than
// inside a coordinate list.
//
// districtBox is the stored service area. halfBox overlaps its eastern side and
// sticks out beyond it — the only configuration in which S_OVERLAPS is true and
// neither containment is. innerBox sits strictly within it, and hyderabadBox is
// 400 km away.
var (
	districtBox  = corners([2]float64{77.50, 12.90}, [2]float64{77.60, 13.00})
	halfBox      = corners([2]float64{77.55, 12.90}, [2]float64{77.65, 13.00})
	innerBox     = corners([2]float64{77.52, 12.92}, [2]float64{77.55, 12.95})
	hyderabadBox = corners([2]float64{78.45, 17.35}, [2]float64{78.55, 17.45})

	// One point inside each box, and neither is a stored shopfront. A query
	// Point coinciding with a stored Point makes S_CONTAINS true of that Point
	// too — correctly, a point contains itself — and these probes are about
	// the stored POLYGONS, so they are phrased where no shopfront stands.
	inDistrictBox  = [2]float64{77.55, 12.95}
	inHyderabadBox = [2]float64{78.50, 17.40}

	// The bounding box scenario 30a queries with — big enough to hold both
	// Bengaluru fixtures and nothing in Hyderabad.
	bengaluruBox = corners([2]float64{77.40, 12.85}, [2]float64{77.70, 13.10})
)

// nearMajestic is ~480 m east of majestic: a second location comfortably inside
// the 2 km circle scenario 34 draws, and distinct from the first so the fixture
// is two geometries rather than one written twice.
var nearMajestic = [2]float64{77.5757, 12.9767}

// corners builds an axis-aligned rectangle from its south-west and north-east
// corners.
func corners(southWest, northEast [2]float64) map[string]any {
	return geoPolygon([][2]float64{
		{southWest[0], southWest[1]},
		{northEast[0], southWest[1]},
		{northEast[0], northEast[1]},
		{southWest[0], northEast[1]},
	})
}

// aServiceArea publishes one catalog whose provider operates over a shape.
func aServiceArea(t *testing.T, svc *service, id string, area map[string]any) {
	t.Helper()

	results := svc.publishCatalogs(t, aCatalog(id,
		availableAtGeometry(area),
		resources(aResource("r-"+id, "wheat")),
	))
	if len(results) != 1 || results[0].Status != beckn.StatusAccepted {
		t.Fatalf("publish %s = %+v, want one ACCEPTED", id, results)
	}
}

// matching runs one operator against the provider geometry and returns what it
// found.
func (s *service) matching(t *testing.T, op string, geometry map[string]any) []string {
	t.Helper()

	return resourceIDs(s.discover(t, spatial(predicate(op, providerGeoPath, geometry))))
}

// Scenario 30. The operator set is answered as set algebra rather than as five
// spellings of "near".
//
// Two fixtures, five operators, because each operator is one CASE arm in the
// predicate and an arm nothing executes is an arm that only compiles. The
// overlapping query is the discriminating one: it is the single configuration
// where INTERSECTS and OVERLAPS agree and both containments disagree, so an
// implementation that had collapsed any of the four into the others shows up
// here rather than at a boundary nobody probes.
func TestTheOperatorSetIsAnsweredAsSetAlgebra(t *testing.T) {
	svc := newService(t)
	aServiceArea(t, svc, "district", districtBox)

	only := []string{"r-district"}
	for _, probe := range []struct {
		name  string
		op    string
		query map[string]any
		want  []string
	}{
		{"overlapping halves intersect", beckn.OpSIntersects, halfBox, only},
		{"overlapping halves are not disjoint", beckn.OpSDisjoint, halfBox, nil},
		{"the district is not inside the half", beckn.OpSWithin, halfBox, nil},
		{"the district does not contain the half", beckn.OpSContains, halfBox, nil},
		{"the district overlaps the half", beckn.OpSOverlaps, halfBox, only},

		// The second fixture. Containment turns on and overlap turns off,
		// which is the pair that separates S_OVERLAPS from S_INTERSECTS: a
		// contained shape intersects but does not overlap.
		{"the district contains the inner box", beckn.OpSContains, innerBox, only},
		{"the district does not overlap what it contains", beckn.OpSOverlaps, innerBox, nil},
	} {
		if got := svc.matching(t, probe.op, probe.query); !slices.Equal(got, probe.want) {
			t.Errorf("%s: %s = %v, want %v", probe.name, probe.op, got, probe.want)
		}
	}
}

// Scenario 30a. The same operators against a stored POINT, whose `cells_full`
// is empty.
//
// Scenario 30 cannot catch any of this. Polygon against Polygon is exactly the
// case where both covers have a non-empty `full` set, so a predicate phrased
// over `full` still behaves. Phrased that way, every negative assertion here
// inverts: `'{}' <@ anything` is TRUE, so a far-away point is WITHIN the query
// box; `NOT ('{}' && anything)` is TRUE, so everything is DISJOINT from
// everything. And a Point is the commonest geometry in the corpus — a shopfront
// — so the degenerate case is the ordinary one.
func TestDegenerateFullCoversDoNotDisableTheOperator(t *testing.T) {
	svc := newService(t)

	// Two stored points: one inside the query box, one 400 km away.
	shopAt(t, svc, "near", majestic)
	shopAt(t, svc, "away", hyderabad)

	// And two stored polygons, for the direction that runs the other way: a
	// query POINT against a stored area.
	aServiceArea(t, svc, "district", districtBox)
	aServiceArea(t, svc, "elsewhere", hyderabadBox)

	for _, probe := range []struct {
		name  string
		op    string
		query map[string]any
		want  []string
	}{
		// Queried with a Polygon. r-near is the assertion: a stored POINT is
		// inside the box, and is not disjoint from it. r-district rides along
		// because the box holds it too, and saying so is cheaper than a second
		// fixture set.
		{"a point inside the box is within it", beckn.OpSWithin, bengaluruBox,
			[]string{"r-district", "r-near"}},
		{"a point 400 km away is disjoint from it", beckn.OpSDisjoint, bengaluruBox,
			[]string{"r-away", "r-elsewhere"}},

		// And the other direction: a query POINT against stored polygons. Each
		// box contains the point in it and not the point in the other, which
		// is the pair a `full`-phrased S_CONTAINS answers with both.
		{"the district contains the point in it", beckn.OpSContains,
			geoPoint(inDistrictBox), []string{"r-district"}},
		{"the Hyderabad box contains the point in it", beckn.OpSContains,
			geoPoint(inHyderabadBox), []string{"r-elsewhere"}},
	} {
		got := svc.matching(t, probe.op, probe.query)
		slices.Sort(got)
		if !slices.Equal(got, probe.want) {
			t.Errorf("%s: %s = %v, want %v", probe.name, probe.op, got, probe.want)
		}
	}
}

// shopAt publishes a catalog whose provider is one point — the degenerate cover
// scenario 30a is about.
func shopAt(t *testing.T, svc *service, id string, point [2]float64) {
	t.Helper()

	results := svc.publishCatalogs(t, aCatalog(id,
		availableAt(point),
		resources(aResource("r-"+id, "wheat")),
	))
	if len(results) != 1 || results[0].Status != beckn.StatusAccepted {
		t.Fatalf("publish %s = %+v, want one ACCEPTED", id, results)
	}
}

// Scenario 31. S_DISJOINT is not bounding-box filtered.
//
// Split out from 30 because its failure has a different shape from every other
// spatial failure in this suite. A bounding box ANDed in ahead of the operator
// is conservative for the seven operators that mean "some kind of touching" —
// it can only reject things the operator would reject anyway. For S_DISJOINT it
// is INVERTED: the rows the box throws away are precisely the answers. And what
// comes back is an empty list, which reads as "there is nothing over there"
// rather than as a bug.
func TestDisjointIsNotBoundingBoxFiltered(t *testing.T) {
	svc := newService(t)
	aServiceArea(t, svc, "district", districtBox)
	aServiceArea(t, svc, "elsewhere", hyderabadBox)

	got := svc.matching(t, beckn.OpSDisjoint, halfBox)
	if !slices.Equal(got, []string{"r-elsewhere"}) {
		t.Errorf("S_DISJOINT from the half = %v, want [r-elsewhere]: "+
			"the district overlaps it, and the Hyderabad box is 400 km outside its bounding box",
			got)
	}
}

// Scenario 32. S_TOUCHES and S_CROSSES are refused, not approximated.
//
// Both are valid `beckn.yaml` enum values, so L1 validation passes them and the
// request arrives looking well-formed. Nothing downstream can answer them — a
// cell cover has no notion of a shared boundary — so the only alternative to
// this refusal is an answer that is quietly wrong, which is the one outcome a
// caller cannot detect.
func TestTouchesAndCrossesAreRefused(t *testing.T) {
	svc := newService(t)
	aServiceArea(t, svc, "district", districtBox)

	for _, op := range []string{beckn.OpSTouches, beckn.OpSCrosses} {
		answer := svc.discoverResponse(t, spatial(predicate(op, providerGeoPath, halfBox)))
		if answer.status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400\nbody: %s", op, answer.status, answer.body)
			continue
		}
		if code := answer.nack(t).Message.Error.Code; code != beckn.CodeSchemaTypeNotSupported {
			t.Errorf("%s rejected with %s, want %s", op, code, beckn.CodeSchemaTypeNotSupported)
		}
	}
}

// Scenario 33. A geometry too large to cover is still findable.
//
// Over geo.MaxIndexCoverCells the publish stores NULL in both cell columns,
// which means "too big to index" and not "covers nothing". The distinction is
// the `cells_cover IS NULL` short-circuit: without it the operator branch
// evaluates to NULL, NULL is a miss inside EXISTS, and the largest service
// areas in the corpus — a state-wide delivery region, exactly the kind a farmer
// is looking for — become the only ones nobody can find.
func TestAnOversizeGeometryIsFoundByItsBoundingBox(t *testing.T) {
	svc := newService(t)

	// A 400 km square: roughly 160,000 km² against a ceiling of about 6,000.
	aServiceArea(t, svc, "statewide", boxAround(majestic, 200000))

	if got := dbtest.CoveredGeometries(t, svc.pool, "c-statewide"); got != 0 {
		t.Fatalf("%d of the geometries carry a cover, want 0: "+
			"the fixture is under MaxIndexCoverCells and tests nothing", got)
	}

	got := resourceIDs(svc.discover(t, spatial(dwithin(providerGeoPath, koramangala, 1000))))
	if !slices.Equal(got, []string{"r-statewide"}) {
		t.Errorf("inside the uncovered area = %v, want [r-statewide]", got)
	}
}

// Scenario 34. ALL means every targeted geometry, not merely more than one.
//
// ALL is answerable at all only because every geometry type became decidable;
// while any of them was not, the quantifier was a fault. So this scenario is
// two assertions at once — that ALL discriminates, and that it stopped being
// refused.
func TestQuantifierAllRequiresEveryTargetedGeometry(t *testing.T) {
	svc := newService(t)

	// One provider straddling the radius, one wholly inside it. koramangala
	// and majestic are 4.6 km apart, so a 2 km circle on majestic holds one
	// of the pair and not the other.
	svc.publishCatalogs(t,
		aCatalog("straddling",
			availableAt(majestic, koramangala),
			resources(aResource("r-straddling", "wheat"))),
		aCatalog("both-inside",
			availableAt(majestic, nearMajestic),
			resources(aResource("r-both-inside", "wheat"))),
	)

	for _, probe := range []struct {
		quantifier string
		want       []string
	}{
		{beckn.QuantifierAny, []string{"r-both-inside", "r-straddling"}},
		{beckn.QuantifierAll, []string{"r-both-inside"}},
	} {
		got := resourceIDs(svc.discover(t, spatial(dwithin(
			providerGeoPath, majestic, 2000, quantified(probe.quantifier)))))
		slices.Sort(got)
		if !slices.Equal(got, probe.want) {
			t.Errorf("%s = %v, want %v", probe.quantifier, got, probe.want)
		}
	}
}

// Scenario 35. A geometry hanging off an offer belongs to that offer's
// resources, and follows them when the offer's list changes.
//
// Three legs, and each one fails differently.
//
// The first is the ordinary reading: an offer's location narrows the page to
// the resources that offer names, not to its whole catalog.
//
// The second is the one that needs `touched` to follow `resourceIds`. The
// republish patches the OFFER and names no resource at all, so a writer that
// derived the rows to rewrite from the catalog's resources would leave the old
// per-resource rows in place and write nothing new.
//
// The third is the one no assertion on a response can make. Moving the offer
// from one resource to another leaves a stale row behind, and a stale row is a
// row too MANY — the search still answers, and it answers with something. Only
// the table shows the difference, which is what pins `touched` to the UNION of
// the offer's ids before and after the merge rather than to the merged ones.
func TestAnOfferGeometryFindsOnlyThatOffersResources(t *testing.T) {
	svc := newService(t)

	svc.publishCatalogs(t, aCatalog("c-three",
		availableAt(majestic),
		resources(
			aResource("r-one", "wheat"),
			aResource("r-two", "wheat"),
			aResource("r-three", "wheat"),
		),
		offers(offerWith("o-pickup", []func(map[string]any){offerAt(koramangala)}, "r-two")),
	))

	// Leg one. The offer's own location, targeted, finds only what it covers —
	// even though all three resources share a provider 4.6 km away.
	page := svc.discover(t, spatial(dwithin(offerGeoPath, koramangala, 1000)))
	if got := resourceIDs(page); !slices.Equal(got, []string{"r-two"}) {
		t.Fatalf("the offer's location = %v, want [r-two]", got)
	}
	if got := offerIDs(page); !slices.Equal(got, []string{"o-pickup"}) {
		t.Errorf("offers on that page = %v, want [o-pickup]", got)
	}

	// Leg two. Emptied, the same offer is catalog-wide, and so is its geometry
	// — the two readings of `'{}'` have to agree, because one is what the
	// search finds and the other is what the response then hydrates.
	svc.republishOffer(t, offerWith("o-pickup", []func(map[string]any){
		offerAt(koramangala), covers(),
	}))

	got := resourceIDs(svc.discover(t, spatial(dwithin(offerGeoPath, koramangala, 1000))))
	slices.Sort(got)
	if !slices.Equal(got, []string{"r-one", "r-three", "r-two"}) {
		t.Fatalf("after emptying resourceIds = %v, want all three", got)
	}

	// Leg three. Moved to the third resource: the page follows, and — the half
	// the page cannot show — nothing is left behind on the second.
	svc.republishOffer(t, offerWith("o-pickup", []func(map[string]any){
		offerAt(koramangala), covers("r-three"),
	}))

	if got := resourceIDs(svc.discover(t, spatial(dwithin(offerGeoPath, koramangala, 1000)))); !slices.Equal(got, []string{"r-three"}) {
		t.Errorf("after moving the offer = %v, want [r-three]", got)
	}
	// The whole table rather than "no row for r-two", because a stale row is a
	// row too many and stating the exact set catches the other direction too:
	// a rewrite that dropped the catalog-level provider row while chasing the
	// offer's would leave every ordinary search answering nothing.
	want := []string{
		`*|$['catalogs'][*]['provider']['availableAt'][0]['geo']`,
		`r-three|$['catalogs'][*]['offers'][0]['provider']['availableAt'][0]['geo']`,
	}
	if rows := dbtest.ResourceGeometries(t, svc.pool, "c-three"); !slices.Equal(rows, want) {
		t.Errorf("resource_geometries = %v,\nwant %v", rows, want)
	}
}

// republishOffer patches one offer into an existing catalog and names no
// resources, which is the shape all three legs of scenario 35 turn on: MERGE,
// and a document whose only member below the catalog is `offers`.
func (s *service) republishOffer(t *testing.T, offer map[string]any) {
	t.Helper()

	results := s.publishCatalogs(t, aCatalog("c-three", offers(offer)))
	if len(results) != 1 || results[0].Status != beckn.StatusAccepted {
		t.Fatalf("republish the offer = %+v, want one ACCEPTED", results)
	}
}
