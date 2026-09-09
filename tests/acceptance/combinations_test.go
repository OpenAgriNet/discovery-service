package acceptance

import (
	"fmt"
	"slices"
	"sort"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

// The dimension matrix: every non-empty combination of the three things a
// discover intent can narrow on.
//
// Before this file the Go suite had exactly ONE multi-dimension scenario —
// TestHybridSpatialAndTextualSearch, which is text+geo — and the six other
// cells were covered only by examples/verify.sh, which talks to a service
// somebody has to remember to start. A predicate silently dropped from the
// shared WHERE would leave `make test` green.
//
// The cells are covered here as a leave-one-out triangle rather than as seven
// independent assertions, because an assertion that a combined intent returns
// the right rows is much weaker than it looks: if each dimension in a case
// admits everything the others admit, the case passes identically with any one
// of them deleted, and it is then pinning a result while asserting nothing
// about composition. See combinationFixture for how the three are made to
// disagree.

// Semantic retrieval is OFF for this file, and that is the subject rather than
// a convenience.
//
// Retrieval modes UNION: lexical, fuzzy and semantic are separate retrievers
// over one shared WHERE, fused by RRF, so a resource ANY of them admits is in
// the answer. Under the suite default (hashing) the semantic mode ranks every
// resource and admits every resource, so `textSearch` cannot EXCLUDE anything —
// which is exactly what TestHybridSpatialAndTextualSearch documents when it
// says demanding the tractor be absent would be demanding that hybrid retrieval
// stop being hybrid.
//
// A matrix that needs text to act as a constraint therefore cannot run with a
// semantic mode present. noop returns a nil embedder (container.go:204), the
// mode is declared absent, and the response carries X-Beckn-Degraded: semantic.
// It is also production's own default (A5), so these cases run the
// configuration a deployment actually has.
func withoutSemantic(cfg *config.Config) { cfg.Embeddings.Provider = "noop" }

// The three resources, one per catalog so that each gets its own provider
// location — geometry hangs off the catalog's provider, so three resources
// under one catalog would share one place and the geo dimension could not
// distinguish them.
const (
	rCardamomNear = "r-cardamom-near" // matches text, inside the radius
	rTractorNear  = "r-tractor-near"  // inside the radius, in the offer
	rCardamomFar  = "r-cardamom-far"  // matches text, in the offer, far away
)

// combinationFixture publishes three resources arranged so that each dimension
// reaches exactly TWO of them, each pair of dimensions overlaps in exactly ONE,
// and all three share NONE:
//
//	text "cardamom"            -> cardamom-near, cardamom-far
//	S_DWITHIN 5km of Majestic  -> cardamom-near, tractor-near
//	offer FREE-TIER            -> tractor-near,  cardamom-far
//
// That arrangement is the whole point. It makes every pair yield a DIFFERENT
// single resource and the triple yield nothing, so removing any one dimension
// from the three-way case changes the answer — which is the only way to
// demonstrate that all three are being applied rather than that one of them is
// doing the work and the others are inert.
func combinationFixture(t *testing.T) *service {
	t.Helper()

	svc := newService(t, withoutSemantic)

	svc.publishCatalogs(t,
		aCatalog("c-cardamom-near", availableAt(majestic),
			resources(aResource(rCardamomNear, "cardamom"))),
		aCatalog("c-tractor-near", availableAt(koramangala),
			resources(aResource(rTractorNear, "tractor")),
			offering("o-near", "FREE-TIER", rTractorNear)),
		aCatalog("c-cardamom-far", availableAt(hyderabad),
			resources(aResource(rCardamomFar, "cardamom")),
			offering("o-far", "FREE-TIER", rCardamomFar)),
	)
	return svc
}

// offering puts one offer on a catalog, naming the resources it covers.
//
// The filter dimension is expressed as an OFFER-rooted predicate selecting
// RESOURCES on purpose: that is the cross-level case A18's single composite
// filter_doc column exists for, and running the matrix through it means the
// matrix also guards the property that a filter rooted at one level can narrow
// another. A resource-rooted predicate would have been easier and would have
// left that untested.
func offering(offerID, code string, resourceIDs ...string) func(map[string]any) {
	return func(catalog map[string]any) {
		catalog["offers"] = []any{map[string]any{
			"id":          offerID,
			"descriptor":  map[string]any{"name": code, "code": code},
			"resourceIds": resourceIDs,
		}}
	}
}

const freeTier = `$.catalogs[*].offers[*] ? (@.descriptor.code == "FREE-TIER")`

// theRadius is the geo dimension: 10 km of Majestic, which reaches Koramangala
// and not Hyderabad.
//
// The distances are 7.4 km and 500 km, measured rather than assumed — the
// comment on those constants in suite_test.go says 4.6 km and 400 km, and both
// are wrong. A 5 km radius therefore falls in the gap: it excludes Koramangala,
// which collapses two cells of the matrix and reads as a spatial predicate
// bug. 10 km sits clear of both ends.
func theRadius() map[string]any { return dwithin(providerGeoPath, majestic, 10000) }

// intent assembles the three dimensions a case names and leaves out the ones it
// does not. Built from the same three parts every time so that a case differs
// from its neighbours in exactly the dimension its name says it does.
func intent(withText, withGeo, withFilter bool) map[string]any {
	built := map[string]any{}
	if withText {
		built["textSearch"] = "cardamom"
	}
	if withGeo {
		built["spatial"] = []any{theRadius()}
	}
	if withFilter {
		built["filters"] = map[string]any{"type": "jsonpath", "expression": freeTier}
	}
	return built
}

// TestDimensionMatrix walks all seven non-empty combinations.
//
// Table-driven rather than seven functions because the cases are only
// meaningful NEXT TO EACH OTHER: what each one proves is the difference between
// its answer and its neighbours', and split across seven functions the first
// person to change a fixture would have no way to see that they had flattened
// the triangle.
func TestDimensionMatrix(t *testing.T) {
	cases := []struct {
		name              string
		text, geo, filter bool
		want              []string
		why               string
	}{
		{
			name: "text alone", text: true,
			want: []string{rCardamomFar, rCardamomNear},
			why: "both cardamom resources, wherever they are and whatever offers " +
				"them: with no other dimension present nothing else narrows.",
		},
		{
			name: "geo alone", geo: true,
			want: []string{rCardamomNear, rTractorNear},
			why:  "the two Bengaluru resources. Hyderabad is 400 km outside the radius.",
		},
		{
			name: "filter alone", filter: true,
			want: []string{rCardamomFar, rTractorNear},
			why: "the two resources a FREE-TIER offer names. This is the only case " +
				"that reaches the candidates path — an intent naming no ranked mode " +
				"is answered by the lexical retriever with a NULL query_text — " +
				"carrying a filter. If that fallthrough stopped applying filter_doc " +
				"it would return all three and no other case in this table would notice.",
		},
		{
			name: "text and geo", text: true, geo: true,
			want: []string{rCardamomNear},
			why:  "cardamom-far is cardamom but not near; tractor-near is near but not cardamom.",
		},
		{
			name: "text and filter", text: true, filter: true,
			want: []string{rCardamomFar},
			why:  "cardamom-near is cardamom but unoffered; tractor-near is offered but not cardamom.",
		},
		{
			name: "geo and filter", geo: true, filter: true,
			want: []string{rTractorNear},
			why:  "cardamom-near is near but unoffered; cardamom-far is offered but far.",
		},
		{
			name: "all three", text: true, geo: true, filter: true,
			want: nil,
			why: "no resource is all of cardamom, near and offered. THE case: it is " +
				"empty only because the three intersect, and the three pairs above " +
				"each return a different single resource, so dropping any one " +
				"dimension from here changes the answer.",
		},
	}

	svc := combinationFixture(t)

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			got := resourceIDs(svc.discover(t, intent(one.text, one.geo, one.filter)))

			// Sorted before comparing. This table is about WHICH resources come
			// back and not about their order — the order is RRF's, and the
			// scenario that pins ranking is TestHybridSpatialAndTextualSearch.
			// Comparing unsorted would make every case here fail the day the
			// fusion is tuned, for a reason none of them is about.
			sort.Strings(got)

			if !slices.Equal(got, one.want) {
				t.Errorf("%s returned %v, want %v\n%s\nintent: %v",
					one.name, got, one.want, one.why,
					intent(one.text, one.geo, one.filter))
			}
		})
	}
}

// TestEveryDimensionIsLoadBearing is the leave-one-out proof, stated as its own
// assertion rather than left for a reader to infer from the table.
//
// For each of the three dimensions: take the full three-way intent, remove that
// one dimension, and require the answer to CHANGE. A predicate that had been
// dropped from the shared WHERE would leave the answer identical with and
// without it, and this is the shape of assertion that says so directly — the
// table above would report it as one wrong row among seven, which reads as a
// fixture that drifted rather than as a predicate that stopped running.
func TestEveryDimensionIsLoadBearing(t *testing.T) {
	svc := combinationFixture(t)

	all := resourceIDs(svc.discover(t, intent(true, true, true)))
	if len(all) != 0 {
		t.Fatalf("the three-way intent returned %v, want none: the triangle this "+
			"test rests on has been flattened, and the leave-one-out results "+
			"below no longer prove anything", all)
	}

	dropped := []struct {
		dimension         string
		text, geo, filter bool
		want              string
	}{
		{"text", false, true, true, rTractorNear},
		{"geo", true, false, true, rCardamomFar},
		{"filter", true, true, false, rCardamomNear},
	}

	// Each dimension must yield a DIFFERENT resource when removed. Three
	// distinct answers is what makes this a proof about three predicates: if
	// two of them returned the same row, one of the two could be inert and the
	// test would not be able to tell.
	seen := map[string]string{}
	for _, one := range dropped {
		got := resourceIDs(svc.discover(t, intent(one.text, one.geo, one.filter)))
		if !slices.Equal(got, []string{one.want}) {
			t.Errorf("dropping the %s dimension returned %v, want exactly [%s]: "+
				"the remaining two constraints did not narrow to it, so the %s "+
				"predicate is not the thing that was excluding it",
				one.dimension, got, one.want, one.dimension)
			continue
		}
		if previous, clash := seen[one.want]; clash {
			t.Errorf("dropping %s and dropping %s both return %s: the dimensions "+
				"overlap, so one of them could be inert and this test could not "+
				"tell", one.dimension, previous, one.want)
		}
		seen[one.want] = one.dimension
	}

	if len(seen) != len(dropped) {
		t.Errorf("the leave-one-out answers were %v, want three distinct resources",
			fmt.Sprint(seen))
	}
}
