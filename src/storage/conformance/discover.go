package conformance

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
)

// DiscoverCases is the read-path suite: everything Search must do that both
// backends have to agree on.
//
// The resolution is a parameter rather than a constant here because it is the
// one setting the two backends must hold EQUAL for a spatial case to mean
// anything: Postgres covers stored geometry at publish time and the memory
// backend covers it at search time, and two covers taken at different
// resolutions produce cell sets that never intersect — a disagreement that
// reads as "nothing matched" rather than as an error.
//
// Three things constrain what can be asked here, and every case below is
// written around them:
//
//   - Only `lexical` is requested. Postgres declares `fuzzy` and the memory
//     backend does not, so any case naming it would pin a difference between
//     the backends rather than an agreement.
//   - Assertions are over the SET of ids, not the order, wherever the query
//     carries text. Postgres orders by ts_rank_cd first and the memory backend
//     has no relevance to rank by; they agree on the stable (catalog_id, id)
//     tail, which is what the one pagination case — deliberately text-free —
//     pins.
//   - No case asserts a total, because there is none (A19). What a paginating
//     caller actually depends on is pinned instead: that the offset SLICES a
//     stable order rather than re-ranking it.
//   - No case names `jsonpath` either. Since Task 22 Postgres executes the
//     subset and the memory backend declines it, so a filter case would pin
//     the difference between them rather than an agreement — which is what
//     puts the filter's own tests on the Postgres side.
func DiscoverCases(resolution int) []Case {
	return []Case{
		anOmittedNetworkSearchesEveryNetwork(),
		theGateHidesWhatIsNotLive(),
		aWindowThatWrapsMidnightIsLiveAndItsForwardTwinIsNot(),
		schemaFilteringComparesContextAndTypeAsAPair(),
		lexicalMatchesAnyTermRatherThanAllOfThem(),
		aRadiusSelectsTheNearShopAndNotTheFarOne(resolution),
		aCatalogGeometryMatchesEveryResourceAndAResourceOneOnlyItsOwn(resolution),
		targetsSelectsBetweenTwoGeometriesOnOneResource(resolution),
		offersAreTheOnesTouchingThePagePlusTheCatalogWideOnes(),
		anExpiredOfferIsNotReturnedWithALiveCatalog(),
		pageTwoDoesNotOverlapPageOne(),
		aModeTheBackendCannotRunIsDegradedAndDoesNotFailTheSearch(),
		askingForNoRetrievalModeAtAllReturnsNothing(),
		aSpatialOnlyIntentIsAnsweredRatherThanDegraded(resolution),
	}
}

// ---------------------------------------------------------------------------
// fixture builders
// ---------------------------------------------------------------------------

// lexical is the mode list every case but two requests. Named once, because
// "which modes" is the setting these fixtures are least free to vary.
var lexical = []domain.Capability{domain.CapabilityLexical}

// searchable carries the four derived fields as an attributes document, so that
// a fixture spells what a resource IS in one place and deriveSearchable puts it
// where the query reads it.
//
// Inside the document rather than onto the ResourcePatch directly because
// ResourcePatch has no such fields, deliberately: search text and the schema
// pair are `derive` output (A8), and a patch carrying them would be a second
// place they could disagree with the document they describe.
func searchable(id, name, text, schemaContext, schemaType string) domain.ResourcePatch {
	document, err := json.Marshal(map[string]any{
		"id": id,
		"resourceAttributes": map[string]string{
			"name": name, "text": text, "@context": schemaContext, "@type": schemaType,
		},
	})
	if err != nil {
		panic("fixture resource " + id + " will not marshal: " + err.Error())
	}
	return domain.ResourcePatch{ID: id, Document: document}
}

// deriveSearchable is the stand-in for Task 17's derivation: it reads what
// `searchable` wrote back out of the MERGED document and onto the resource.
//
// Off the merged document and not off the patch, which is the whole reason this
// runs as a derive rather than in the fixture: a MERGE publish that changes only
// the name has to re-derive the search text from the merge result, and a
// fixture that pre-computed it would be asserting against its own arithmetic.
func deriveSearchable(merged *domain.Catalog, _ []string) []domain.Fault {
	for index := range merged.Resources {
		var document struct {
			Name    string `json:"name"`
			Text    string `json:"text"`
			Context string `json:"@context"`
			Type    string `json:"@type"`
		}
		if err := json.Unmarshal(merged.Resources[index].ResourceAttributes(), &document); err != nil {
			continue
		}
		merged.Resources[index].Name = document.Name
		merged.Resources[index].SearchText = document.Name + " " + document.Text
		merged.Resources[index].SchemaContext = document.Context
		merged.Resources[index].SchemaType = document.Type
	}
	return nil
}

// deriveInOrder runs several derivations as one, so a case needing both search
// text and geometry does not have to write a third derive that does both.
func deriveInOrder(steps ...domain.DeriveFunc) domain.DeriveFunc {
	return func(merged *domain.Catalog, touched []string) []domain.Fault {
		var faults []domain.Fault
		for _, step := range steps {
			faults = append(faults, step(merged, touched)...)
		}
		return faults
	}
}

// deriveResourceGeometry puts a shape on ONE resource, which is what the walker
// does for a geometry found inside that resource's own document.
//
// The catalog-level counterpart is deriveGeometries in publish.go, and the
// difference between them is the whole of A15: a catalog's shape is stored once
// with a NULL resource id and is shared, a resource's shape is not.
func deriveResourceGeometry(resourceID string, geometries ...domain.Geometry) domain.DeriveFunc {
	return func(merged *domain.Catalog, _ []string) []domain.Fault {
		for index := range merged.Resources {
			if merged.Resources[index].ID == resourceID {
				merged.Resources[index].Geometries = geometries
			}
		}
		return nil
	}
}

// within builds the S_DWITHIN filter a case searches with, covering the query
// geometry the same way the discover mapper will.
//
// Center is populated only because the query geometry IS a Point: that is the
// single case the exact haversine refinement applies to, and a Center set on
// any other shape would silently narrow an operator it was never meant to
// touch.
func within(center domain.GeoPoint, metres float64, resolution int) *domain.SpatialFilter {
	geometry := PointGeometryAt(0, center)

	full, cover, err := geo.CoverQuery(geometry, domain.OpDWithin, metres, resolution)
	if err != nil {
		panic("fixture query geometry will not cover: " + err.Error())
	}
	bounds, err := geo.BoundsFor(geometry, domain.OpDWithin, metres)
	if err != nil {
		panic("fixture query geometry has no bounds: " + err.Error())
	}

	return &domain.SpatialFilter{
		Op:         domain.OpDWithin,
		CellsFull:  full,
		CellsCover: cover,
		Bounds:     bounds,
		Center:     &center,
		RadiusM:    metres,
		Quantifier: domain.QuantifierAny,
	}
}

// bengaluru and chennai are ~290 km apart, which is far enough that no radius a
// fixture below uses can reach from one to the other by accident.
var (
	bengaluru = domain.GeoPoint{Lat: 12.9716, Lon: 77.5946}
	chennai   = domain.GeoPoint{Lat: 13.0827, Lon: 80.2707}
)

// pageLimit is the limit every case searches with.
//
// Eight, and not a larger round number, because Postgres refuses a page past
// its MaxCandidatesPerMode outright rather than answering it empty — so a
// fixture asking for more than the backend retrieves would fail as a fault
// rather than as a disagreement.
const pageLimit = 8

// searched runs one query and fails the case rather than returning a zero
// result, which would answer every assertion below with something plausible.
func searched(
	t *testing.T, backends Backends, query domain.SearchQuery, modes []domain.Capability,
) domain.SearchResult {
	t.Helper()

	if query.Limit == 0 {
		query.Limit = pageLimit
	}
	result, err := backends.Search.Search(t.Context(), query, modes)
	if err != nil {
		t.Fatalf("searching %+v: %v", query, err)
	}
	return result
}

// matchedIDs is the page's resource ids, SORTED.
//
// Sorted because the backends agree on membership everywhere and on order only
// where there is no relevance to rank by. The one case that cares about order
// reads pageOrder below instead.
func matchedIDs(result domain.SearchResult) []string {
	ids := pageOrder(result)
	slices.Sort(ids)
	return ids
}

// pageOrder is the page's resource ids in the order the backend returned them.
func pageOrder(result domain.SearchResult) []string {
	ids := make([]string, 0)
	for _, catalog := range result.Catalogs {
		for _, resource := range catalog.Resources {
			ids = append(ids, resource.ID)
		}
	}
	return ids
}

// offerIDs is every offer hydrated onto the page, sorted.
func offerIDs(result domain.SearchResult) []string {
	ids := make([]string, 0)
	for _, catalog := range result.Catalogs {
		for _, offer := range catalog.Offers {
			ids = append(ids, offer.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// assertPage fails with both lists spelled out, because "want 2 got 1" sends
// the reader back to the fixture to find out which one went missing.
func assertPage(t *testing.T, result domain.SearchResult, wantIDs []string) {
	t.Helper()

	if got := matchedIDs(result); !slices.Equal(got, wantIDs) {
		t.Errorf("the page holds %v, want %v", got, wantIDs)
	}
}

// atOffset builds a validity patch whose daily window opens and closes at the
// given offsets from now, in UTC.
//
// Offsets rather than literals because neither backend's clock can be set:
// Postgres's gate calls now() inside the transaction. A fixture pinned to
// 22:00 would therefore pass or fail depending on the hour the suite ran, which
// is the one property a conformance case cannot have.
func dailyWindow(fromOffset, toOffset time.Duration) *domain.TimePeriodPatch {
	now := time.Now().UTC()
	from, to := now.Add(fromOffset), now.Add(toOffset)
	return &domain.TimePeriodPatch{
		StartTime: domain.Nullable[domain.TimeOfDay]{Set: true, Value: domain.TimeOfDay{
			Hour: from.Hour(), Minute: from.Minute(), Second: from.Second()}},
		EndTime: domain.Nullable[domain.TimeOfDay]{Set: true, Value: domain.TimeOfDay{
			Hour: to.Hour(), Minute: to.Minute(), Second: to.Second()}},
	}
}

// dateRange builds the calendar half of a validity patch.
func dateRange(from, to time.Time) *domain.TimePeriodPatch {
	return &domain.TimePeriodPatch{
		StartDate: domain.Nullable[time.Time]{Set: true, Value: from},
		EndDate:   domain.Nullable[time.Time]{Set: true, Value: to},
	}
}

// ---------------------------------------------------------------------------
// the cases
// ---------------------------------------------------------------------------

// An empty scope network is UNSCOPED, and that is not the same as a network id
// that matches nothing.
//
// A backend that read "" as a literal would return an empty page, and a backend
// that fell back to this service's own APP_NETWORK_ID would return one network's
// rows while reporting a total for all of them. The two failures are
// indistinguishable at the caller, which is why both halves are asserted here.
func anOmittedNetworkSearchesEveryNetwork() Case {
	return Case{
		Name: "an omitted network searches every network and a given one narrows",
		Given: []Publish{{
			Patch: domain.CatalogPatch{
				ID: "c-north", NetworkID: "north.example.com", Active: true,
				VisibleTo: []string{"north.example.com"},
				Resources: []domain.ResourcePatch{searchable("r-north", "north", "", "", "")},
			},
			Mode: domain.UpdateModeMerge, Derive: deriveSearchable,
		}, {
			Patch: domain.CatalogPatch{
				ID: "c-south", NetworkID: "south.example.com", Active: true,
				VisibleTo: []string{"south.example.com"},
				Resources: []domain.ResourcePatch{searchable("r-south", "south", "", "", "")},
			},
			Mode: domain.UpdateModeMerge, Derive: deriveSearchable,
		}},
		Then: func(t *testing.T, backends Backends) {
			unscoped := searched(t, backends, domain.SearchQuery{}, lexical)
			assertPage(t, unscoped, []string{"r-north", "r-south"})

			scoped := searched(t, backends,
				domain.SearchQuery{NetworkID: "south.example.com"}, lexical)
			assertPage(t, scoped, []string{"r-south"})
		},
	}
}

// The gate, read off the resource's own denormalised copy of it.
//
// All three halves in one case because they fail the same way — a row that
// should be invisible answering a query — and a suite that pinned only `active`
// would let an expired catalog keep selling.
func theGateHidesWhatIsNotLive() Case {
	lastMonth := time.Now().UTC().AddDate(0, -1, 0)
	nextMonth := time.Now().UTC().AddDate(0, 1, 0)
	longAgo := lastMonth.AddDate(0, -1, 0)

	live := catalogPatch("c-live", searchable("r-live", "live", "", "", ""))

	withdrawn := catalogPatch("c-withdrawn", searchable("r-withdrawn", "withdrawn", "", "", ""))
	withdrawn.Active = false

	expired := catalogPatch("c-expired", searchable("r-expired", "expired", "", "", ""))
	expired.Validity = dateRange(longAgo, lastMonth)

	future := catalogPatch("c-future", searchable("r-future", "future", "", "", ""))
	future.Validity = dateRange(nextMonth, nextMonth.AddDate(0, 1, 0))

	return Case{
		Name: "an inactive, an expired and a not-yet-started catalog are all invisible",
		Given: []Publish{
			{Patch: live, Mode: domain.UpdateModeMerge, Derive: deriveSearchable},
			{Patch: withdrawn, Mode: domain.UpdateModeMerge, Derive: deriveSearchable},
			{Patch: expired, Mode: domain.UpdateModeMerge, Derive: deriveSearchable},
			{Patch: future, Mode: domain.UpdateModeMerge, Derive: deriveSearchable},
		},
		Then: func(t *testing.T, backends Backends) {
			assertPage(t, searched(t, backends, domain.SearchQuery{}, lexical),
				[]string{"r-live"})
		},
	}
}

// The case the whole daily-window rule exists for, and the one that separates a
// correct implementation from a BETWEEN.
//
// A window from now+2min to now+1min WRAPS midnight: it is open for all but one
// minute of the day, and it contains this instant. A backend comparing
// `now BETWEEN from AND to` answers false — a shop open all night reading as a
// shop never open, which is an absent search result and therefore a failure
// nobody reports.
//
// Its forward twin, now+1min to now+2min, is the control. It does NOT wrap and
// does NOT contain now, so a backend that "fixed" the wrap by ignoring the
// window entirely would pass the first half and fail this one.
func aWindowThatWrapsMidnightIsLiveAndItsForwardTwinIsNot() Case {
	wrapping := catalogPatch("c-wrapping", searchable("r-wrapping", "wrapping", "", "", ""))
	wrapping.Validity = dailyWindow(2*time.Minute, time.Minute)

	forward := catalogPatch("c-forward", searchable("r-forward", "forward", "", "", ""))
	forward.Validity = dailyWindow(time.Minute, 2*time.Minute)

	return Case{
		Name: "a daily window that wraps midnight is live now and its forward twin is not",
		Given: []Publish{
			{Patch: wrapping, Mode: domain.UpdateModeMerge, Derive: deriveSearchable},
			{Patch: forward, Mode: domain.UpdateModeMerge, Derive: deriveSearchable},
		},
		Then: func(t *testing.T, backends Backends) {
			assertPage(t, searched(t, backends, domain.SearchQuery{}, lexical),
				[]string{"r-wrapping"})
		},
	}
}

// Context and type are compared as a PAIR.
//
// The cross-product case is the one that matters: a request for
// [agri#SeedLot, mobility#RideService] must not match agri#RideService, which is
// exactly what two independent IN lists return.
func schemaFilteringComparesContextAndTypeAsAPair() Case {
	const (
		agri     = "https://beckn.org/Agri"
		mobility = "https://beckn.org/Mobility"
	)

	return Case{
		Name: "schema filtering compares context and type as a pair",
		Given: []Publish{{
			Patch: catalogPatch("c1",
				searchable("r-seed", "seed", "", agri, "SeedLot"),
				searchable("r-ride", "ride", "", mobility, "RideService"),
				searchable("r-cross", "cross", "", agri, "RideService"),
			),
			Mode: domain.UpdateModeMerge, Derive: deriveSearchable,
		}},
		Then: func(t *testing.T, backends Backends) {
			pair := searched(t, backends, domain.SearchQuery{Schemas: []domain.SchemaFilter{
				{Context: agri, Type: "SeedLot"},
				{Context: mobility, Type: "RideService"},
			}}, lexical)
			assertPage(t, pair, []string{"r-ride", "r-seed"})

			// An entry with no type is every type under that context, which is
			// what picks the cross-product row up.
			anyType := searched(t, backends, domain.SearchQuery{Schemas: []domain.SchemaFilter{
				{Context: agri},
			}}, lexical)
			assertPage(t, anyType, []string{"r-cross", "r-seed"})

			// An empty list is NO predicate, never one matching nothing.
			none := searched(t, backends, domain.SearchQuery{}, lexical)
			assertPage(t, none, []string{"r-cross", "r-ride", "r-seed"})
		},
	}
}

// Lexical retrieval ORs its terms.
//
// `discover_tsquery` rewrites websearch_to_tsquery's `&` into `|` on purpose:
// "wheat seeds for sale" must not match nothing because no listing carries all
// four words. Recall is the retriever's job, precision is the fusion's — and a
// backend that ANDed would return an empty page for every multi-word intent.
func lexicalMatchesAnyTermRatherThanAllOfThem() Case {
	return Case{
		Name: "a multi-word query matches a resource carrying any one of its terms",
		Given: []Publish{{
			Patch: catalogPatch("c1",
				searchable("r-wheat", "wheat", "", "", ""),
				searchable("r-seeds", "seeds", "", "", ""),
				searchable("r-tractor", "tractor", "", "", ""),
			),
			Mode: domain.UpdateModeMerge, Derive: deriveSearchable,
		}},
		Then: func(t *testing.T, backends Backends) {
			assertPage(t, searched(t, backends,
				domain.SearchQuery{Text: "wheat seeds"}, lexical),
				[]string{"r-seeds", "r-wheat"})

			// And an empty text is no predicate rather than one matching
			// nothing: a geo-only intent carries no text at all.
			assertPage(t, searched(t, backends, domain.SearchQuery{}, lexical),
				[]string{"r-seeds", "r-tractor", "r-wheat"})
		},
	}
}

// The exact refinement, which is the one place a distance decides anything.
//
// Both shops sit inside their own H3 cells and the query's cover is a superset
// by construction, so the cells alone would admit neither or both depending on
// the resolution. What separates them is `geo_distance_m` on one side and
// geo.NearestGeometryM on the other, and this case is what holds those two
// functions to the same answer through the port.
func aRadiusSelectsTheNearShopAndNotTheFarOne(resolution int) Case {
	near := catalogPatch("c-near", searchable("r-near", "near", "", "", ""))
	far := catalogPatch("c-far", searchable("r-far", "far", "", "", ""))

	return Case{
		Name: "a radius selects the shop inside it and not the one 290km away",
		Given: []Publish{{
			Patch: near, Mode: domain.UpdateModeMerge,
			Derive: deriveInOrder(deriveSearchable,
				deriveGeometries(PointGeometryAt(0, bengaluru))),
		}, {
			Patch: far, Mode: domain.UpdateModeMerge,
			Derive: deriveInOrder(deriveSearchable,
				deriveGeometries(PointGeometryAt(0, chennai))),
		}},
		Then: func(t *testing.T, backends Backends) {
			assertPage(t, searched(t, backends,
				domain.SearchQuery{Spatial: within(bengaluru, 5000, resolution)}, lexical),
				[]string{"r-near"})

			// Wide enough to reach both, which is what proves the narrow answer
			// above came from the radius and not from a cover that declined.
			assertPage(t, searched(t, backends,
				domain.SearchQuery{Spatial: within(bengaluru, 400000, resolution)}, lexical),
				[]string{"r-far", "r-near"})
		},
	}
}

// A catalog's provider location belongs to every resource under it; a
// resource's own belongs to that resource alone.
//
// This is the read half of A15. The write half — that a catalog-level shape is
// stored ONCE with a NULL resource id rather than copied per resource — is
// pinned by providerLocationsAreStoredOnceForTheCatalog in the publish suite;
// what this asserts is that storing it once still finds all of them.
func aCatalogGeometryMatchesEveryResourceAndAResourceOneOnlyItsOwn(resolution int) Case {
	shared := catalogPatch("c-shared",
		searchable("r-a", "a", "", "", ""),
		searchable("r-b", "b", "", "", ""))

	own := catalogPatch("c-own",
		searchable("r-here", "here", "", "", ""),
		searchable("r-elsewhere", "elsewhere", "", "", ""))

	return Case{
		Name: "a catalog geometry matches every resource and a resource one only its own",
		Given: []Publish{{
			Patch: shared, Mode: domain.UpdateModeMerge,
			Derive: deriveInOrder(deriveSearchable,
				deriveGeometries(PointGeometryAt(0, bengaluru))),
		}, {
			Patch: own, Mode: domain.UpdateModeMerge,
			Derive: deriveInOrder(deriveSearchable,
				deriveResourceGeometry("r-here", PointGeometryAt(0, bengaluru))),
		}},
		Then: func(t *testing.T, backends Backends) {
			assertPage(t, searched(t, backends,
				domain.SearchQuery{Spatial: within(bengaluru, 5000, resolution)}, lexical),
				[]string{"r-a", "r-b", "r-here"})
		},
	}
}

// `targets` picks between two shapes on ONE resource.
//
// Without it, a resource with a shopfront in Bengaluru and a service area
// around Chennai answers every query either of them matches — which is right
// for "where can I be found" and wrong for "where do you deliver". The two
// shapes here are deliberately under different target paths and 290km apart, so
// a backend that ignored `targets` returns the resource for both queries and a
// backend that applied it to the wrong shape returns it for neither.
func targetsSelectsBetweenTwoGeometriesOnOneResource(resolution int) Case {
	shopfront := PointGeometryAt(0, bengaluru)
	serviceArea := PointGeometryAt(0, chennai)
	serviceArea.TargetPath = `$['catalogs'][*]['resources'][*]['serviceArea'][*]['geo']`
	serviceArea.SourcePath = `$['catalogs'][*]['resources'][0]['serviceArea'][0]['geo']`

	return Case{
		Name: "targets selects between two geometries on one resource",
		Given: []Publish{{
			Patch: catalogPatch("c1", searchable("r-both", "both", "", "", "")),
			Mode:  domain.UpdateModeMerge,
			Derive: deriveInOrder(deriveSearchable,
				deriveResourceGeometry("r-both", shopfront, serviceArea)),
		}},
		Then: func(t *testing.T, backends Backends) {
			atShopfront := searched(t, backends, domain.SearchQuery{
				Spatial:     within(bengaluru, 5000, resolution),
				TargetPaths: []string{shopfront.TargetPath},
			}, lexical)
			assertPage(t, atShopfront, []string{"r-both"})

			// The same radius, the same resource, the other target: the shape
			// under it is 290km away, so this must be empty.
			atServiceArea := searched(t, backends, domain.SearchQuery{
				Spatial:     within(bengaluru, 5000, resolution),
				TargetPaths: []string{serviceArea.TargetPath},
			}, lexical)
			assertPage(t, atServiceArea, nil)

			// No targets is every shape the resource can be found by, which is
			// what proves the two above were narrowed rather than broken.
			untargeted := searched(t, backends, domain.SearchQuery{
				Spatial: within(chennai, 5000, resolution),
			}, lexical)
			assertPage(t, untargeted, []string{"r-both"})
		},
	}
}

// Hydration returns the offers touching the page, plus the catalog-wide ones.
//
// An EMPTY ResourceIDs is CATALOG-WIDE and is never "no resources": a backend
// reading it as "none" drops every promotion a publisher wrote against the
// whole catalog, and does it silently. The scoped offer on the resource that is
// NOT on the page is the other half — a hydration keyed on the catalog rather
// than on the page would return it.
func offersAreTheOnesTouchingThePagePlusTheCatalogWideOnes() Case {
	patch := catalogPatch("c1",
		searchable("r-onpage", "onpage", "", "", ""),
		searchable("r-offpage", "offpage", "", "", ""))
	patch.Offers = []domain.OfferPatch{
		{ID: "o-wide", Document: json.RawMessage(`{"price":"10"}`), ResourceIDs: []string{}},
		{ID: "o-onpage", Document: json.RawMessage(`{"price":"20"}`),
			ResourceIDs: []string{"r-onpage"}},
		{ID: "o-offpage", Document: json.RawMessage(`{"price":"30"}`),
			ResourceIDs: []string{"r-offpage"}},
	}

	return Case{
		Name: "hydration returns the offers touching the page plus the catalog-wide ones",
		Given: []Publish{
			{Patch: patch, Mode: domain.UpdateModeMerge, Derive: deriveSearchable},
		},
		Then: func(t *testing.T, backends Backends) {
			result := searched(t, backends, domain.SearchQuery{Text: "onpage"}, lexical)
			assertPage(t, result, []string{"r-onpage"})

			if got := offerIDs(result); !slices.Equal(got, []string{"o-onpage", "o-wide"}) {
				t.Errorf("the page carries offers %v, want [o-onpage o-wide]", got)
			}
		},
	}
}

// An offer's validity is its own, and nothing else checks it.
//
// A live catalog routinely carries last month's promotion: the catalog's gate
// says nothing about it, so a backend that hydrated offers without their own
// date check would return an expired price beside a current listing.
func anExpiredOfferIsNotReturnedWithALiveCatalog() Case {
	lastMonth := time.Now().UTC().AddDate(0, -1, 0)

	patch := catalogPatch("c1", searchable("r1", "listing", "", "", ""))
	patch.Offers = []domain.OfferPatch{
		{ID: "o-live", Document: json.RawMessage(`{"price":"10"}`), ResourceIDs: []string{}},
		{ID: "o-expired", Document: json.RawMessage(`{"price":"1"}`), ResourceIDs: []string{},
			Validity: dateRange(lastMonth.AddDate(0, -1, 0), lastMonth)},
	}

	return Case{
		Name: "an expired offer is not returned with a live catalog",
		Given: []Publish{
			{Patch: patch, Mode: domain.UpdateModeMerge, Derive: deriveSearchable},
		},
		Then: func(t *testing.T, backends Backends) {
			result := searched(t, backends, domain.SearchQuery{}, lexical)
			assertPage(t, result, []string{"r1"})

			if got := offerIDs(result); !slices.Equal(got, []string{"o-live"}) {
				t.Errorf("the page carries offers %v, want [o-live]", got)
			}
		},
	}
}

// Pagination walks a stable order and the total does not move under it.
//
// No text, deliberately: with nothing to rank by both backends fall through to
// the stable (catalog_id, id) key, which is the only order they are required to
// agree on. It is also what lets this assert the ORDER rather than the set —
// two pages that overlapped by one id would still be a correct set.
func pageTwoDoesNotOverlapPageOne() Case {
	resources := make([]domain.ResourcePatch, 0, 5)
	for index := range 5 {
		id := fmt.Sprintf("r-%02d", index)
		resources = append(resources, searchable(id, id, "", "", ""))
	}

	return Case{
		Name: "page two does not overlap page one",
		Given: []Publish{{
			Patch: catalogPatch("c1", resources...),
			Mode:  domain.UpdateModeMerge, Derive: deriveSearchable,
		}},
		Then: func(t *testing.T, backends Backends) {
			first := searched(t, backends, domain.SearchQuery{Limit: 2}, lexical)
			second := searched(t, backends, domain.SearchQuery{Limit: 2, Offset: 2}, lexical)

			if got := pageOrder(first); !slices.Equal(got, []string{"r-00", "r-01"}) {
				t.Errorf("page one is %v, want [r-00 r-01]", got)
			}
			if got := pageOrder(second); !slices.Equal(got, []string{"r-02", "r-03"}) {
				t.Errorf("page two is %v, want [r-02 r-03]", got)
			}

			// There is no total to assert (A19). What remains is the property
			// a caller walking pages actually depends on: the offset SLICES a
			// stable order rather than re-ranking it, so page two holds the
			// next two and not two of the same.
		},
	}
}

// A mode neither backend can run is REPORTED, not silently dropped.
//
// The mode is a name NO backend declares, and that is deliberate. Since Task 22
// there is no real capability both decline — Postgres executes the jsonpath
// subset and the memory backend does not, Postgres declares `fuzzy` and the
// memory backend does not — so naming a real one would pin which backend lacks
// what, when the contract being pinned is what happens to a mode a backend
// cannot run, whichever mode that turns out to be.
//
// The page must still be the page: a request for two modes of which one is
// missing is a degraded answer, and a degraded answer is not an error.
func aModeTheBackendCannotRunIsDegradedAndDoesNotFailTheSearch() Case {
	return Case{
		Name: "a mode this backend cannot run is degraded and does not fail the search",
		Given: []Publish{{
			Patch: catalogPatch("c1", searchable("r1", "listing", "", "", "")),
			Mode:  domain.UpdateModeMerge, Derive: deriveSearchable,
		}},
		Then: func(t *testing.T, backends Backends) {
			const unrunnable = domain.Capability("no-such-mode")

			result := searched(t, backends, domain.SearchQuery{},
				[]domain.Capability{domain.CapabilityLexical, unrunnable})

			assertPage(t, result, []string{"r1"})
			if !slices.Equal(result.Degraded, []string{string(unrunnable)}) {
				t.Errorf("Degraded is %v, want [%s]", result.Degraded, unrunnable)
			}
		},
	}
}

// No modes at all is an empty answer, not the whole corpus.
//
// The negotiation in front of Search decides the mode list, so an empty one
// means it decided on nothing. A backend that answered it with everything the
// gate admits would be answering a query nobody made — and would do it with a
// full page and a plausible total, which is the kind of wrong that survives
// review.
func askingForNoRetrievalModeAtAllReturnsNothing() Case {
	return Case{
		Name: "asking for no retrieval mode at all returns nothing rather than everything",
		Given: []Publish{{
			Patch: catalogPatch("c1", searchable("r1", "listing", "", "", "")),
			Mode:  domain.UpdateModeMerge, Derive: deriveSearchable,
		}},
		Then: func(t *testing.T, backends Backends) {
			assertPage(t, searched(t, backends, domain.SearchQuery{}, nil), nil)
		},
	}
}

// A spatial constraint is a filter, not a ranked mode, and an intent carrying
// only one is still a query.
//
// This is the case the suite was missing, and its absence is what let the two
// backends diverge unnoticed: every geo case above asks with `lexical`, because
// that is the mode list a TEXT query produces, while the discover service asks
// a geo-only intent with `spatial` and nothing else. Postgres then found no
// retriever under that key and reported the mode missing; the memory backend
// found no ranked mode left and emptied the page. Both answered nothing, and
// both told the caller the geometry had been ignored — while it was the only
// thing that had been applied.
//
// The fixture and the radius are aRadiusSelectsTheNearShopAndNotTheFarOne's, on
// purpose: the two mode lists are then held to ONE answer rather than to two
// separately plausible ones.
func aSpatialOnlyIntentIsAnsweredRatherThanDegraded(resolution int) Case {
	near := catalogPatch("c-near", searchable("r-near", "near", "", "", ""))
	far := catalogPatch("c-far", searchable("r-far", "far", "", "", ""))

	return Case{
		Name: "a spatial-only intent is answered rather than reported degraded",
		Given: []Publish{{
			Patch: near, Mode: domain.UpdateModeMerge,
			Derive: deriveInOrder(deriveSearchable,
				deriveGeometries(PointGeometryAt(0, bengaluru))),
		}, {
			Patch: far, Mode: domain.UpdateModeMerge,
			Derive: deriveInOrder(deriveSearchable,
				deriveGeometries(PointGeometryAt(0, chennai))),
		}},
		Then: func(t *testing.T, backends Backends) {
			result := searched(t, backends,
				domain.SearchQuery{Spatial: within(bengaluru, 5000, resolution)},
				[]domain.Capability{domain.CapabilitySpatial})

			assertPage(t, result, []string{"r-near"})
			if len(result.Degraded) > 0 {
				t.Errorf("Degraded is %v, want none: the geometry was applied, not dropped",
					result.Degraded)
			}
		},
	}
}
