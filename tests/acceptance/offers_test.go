package acceptance

import (
	"slices"
	"testing"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
)

// outletGeo is where the offer scenarios put a resource's own geometry.
//
// They need a search that can tell two resources of ONE catalog apart, and a
// catalog-level provider location cannot do it — it belongs to every resource
// in its catalog by design, which is exactly what scenario 15 pins. Text cannot
// do it either: this deployment runs the hashing embedder, so the semantic
// retriever returns every row and RRF unions it with the lexical one, making a
// text query a ranking rather than a filter (see scenario 17). A per-resource
// geometry is the only handle in Phase 1 that removes rows.
const (
	outletContainer = "outlet"
	outletMember    = "geo"
)

// twoOutletsApart is one catalog whose two resources sit 500 km apart, each
// found through its own geometry.
func twoOutletsApart(t *testing.T, svc *service, offerList ...map[string]any) {
	t.Helper()

	catalog := aCatalog("c-grain",
		resources(
			aResource("r-city", "wheat",
				resourceGeo(outletContainer, outletMember, geoPoint(majestic))),
			aResource("r-far", "wheat",
				resourceGeo(outletContainer, outletMember, geoPoint(hyderabad))),
		),
	)
	if len(offerList) > 0 {
		offers(offerList...)(catalog)
	}

	results := svc.publishCatalogs(t, catalog)
	if len(results) != 1 || results[0].Status != beckn.StatusAccepted {
		t.Fatalf("publish = %+v, want one ACCEPTED", results)
	}
}

// nearOutlet is a discover that resolves geometries through the resource's own
// attributes rather than through the provider.
func (s *service) nearOutlet(t *testing.T, centre [2]float64) []beckn.Catalog {
	t.Helper()

	return s.discover(t, spatial(dwithin(
		resourceGeoPath(outletContainer, outletMember), centre, 5000)))
}

// Scenario 20. An offer naming one resource travels with that resource and with
// no other.
//
// Two things at once, and the second is the one a per-catalog dump would pass
// anyway: the array-overlap join has to match `resource_ids` against the page,
// and the offers have to be scoped TO the page. A response that returned every
// offer of every matched catalog would satisfy the first half of this scenario
// and fail the second.
func TestOffersOnMatchedResourcesAreReturned(t *testing.T) {
	svc := newService(t)
	twoOutletsApart(t, svc, anOffer("o-city", "r-city"))

	city := svc.nearOutlet(t, majestic)
	if got := resourceIDs(city); !slices.Equal(got, []string{"r-city"}) {
		t.Fatalf("near the city = %v, want [r-city]", got)
	}
	if got := offerIDs(city); !slices.Equal(got, []string{"o-city"}) {
		t.Errorf("offers near the city = %v, want [o-city]", got)
	}

	far := svc.nearOutlet(t, hyderabad)
	if got := resourceIDs(far); !slices.Equal(got, []string{"r-far"}) {
		t.Fatalf("near hyderabad = %v, want [r-far]", got)
	}
	if got := offerIDs(far); len(got) != 0 {
		t.Errorf("offers near hyderabad = %v, want none: o-city names r-city only", got)
	}
}

// Scenario 21. An offer covering no named resource covers all of them.
//
// The distinction that can be lost silently is in the WRITER: `'{}'` is a
// catalog-wide offer, and a writer reading it as "no resources yet" would store
// something the reader then hides. Both halves of this scenario would still
// pass if the offer were merely absent from one page, so it asserts the offer
// arrives with EACH resource — including the one 500 km from the other.
func TestACatalogWideOfferIsReturnedWithEveryResource(t *testing.T) {
	svc := newService(t)
	twoOutletsApart(t, svc, anOffer("o-wide"))

	for _, where := range []struct {
		name   string
		centre [2]float64
		want   string
	}{
		{"the city outlet", majestic, "r-city"},
		{"the far outlet", hyderabad, "r-far"},
	} {
		page := svc.nearOutlet(t, where.centre)
		if got := resourceIDs(page); !slices.Equal(got, []string{where.want}) {
			t.Fatalf("near %s = %v, want [%s]", where.name, got, where.want)
		}
		if got := offerIDs(page); !slices.Equal(got, []string{"o-wide"}) {
			t.Errorf("offers near %s = %v, want [o-wide]", where.name, got)
		}
	}
}

// Scenario 22. An offer whose window has closed is absent from a page whose
// catalog and resources are both live.
//
// Offer validity is checked at hydration and nowhere else: the retrieval gate
// reads `resources`, so an expired offer costs nothing until the page is
// assembled. That is also why this scenario has to be end-to-end — the offer is
// live by every gate the candidate query applies.
func TestAnExpiredOfferIsNotReturned(t *testing.T) {
	svc := newService(t)
	now := time.Now()

	expired := offerWith("o-expired", []func(map[string]any){
		withValidity(map[string]any{
			"startDate": rfc3339(now.Add(-48 * time.Hour)),
			"endDate":   rfc3339(now.Add(-time.Hour)),
		}),
	}, "r-city")

	svc.publishCatalogs(t, aCatalog("c-grain",
		availableAt(majestic),
		resources(aResource("r-city", "wheat")),
		offers(anOffer("o-live", "r-city"), expired),
	))

	page := svc.near(t, majestic, 5000)
	if got := resourceIDs(page); !slices.Equal(got, []string{"r-city"}) {
		t.Fatalf("the resource itself = %v, want [r-city]: it carries no validity", got)
	}
	if got := offerIDs(page); !slices.Equal(got, []string{"o-live"}) {
		t.Errorf("offers = %v, want [o-live]: o-expired ended an hour ago", got)
	}
}
