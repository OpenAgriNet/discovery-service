package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres/gen"
)

// assemble, attachCatalogs and attachOffers each carry a skip branch for a row
// that does not belong on the page — a resource the gate refused on the way
// back in, or a catalog/offer row keyed by a catalog nothing on the page
// belongs to. HydrateResources/Catalogs/Offers are queried in a way that
// should never actually produce the second case, so it is pinned here, against
// hand-built rows, rather than chased through a real query.
//
// package postgres, not postgres_test: all three functions are unexported.

// geometryType has no geom_type column to fall back on — the type is read off
// the stored document, and a document that will not decode reads as no type
// rather than panicking. hydratedGeometries is the discover-side caller;
// geometriesFrom is publish's own and out of this branch's scope.
func TestGeometryTypeOfUnreadableJSONIsEmpty(t *testing.T) {
	if got := geometryType([]byte("not json")); got != "" {
		t.Errorf("geometryType(malformed) = %q, want empty", got)
	}
}

// A page naming two ids where only one comes back from HydrateResources is
// exactly what happens when a retriever names a resource the gate then
// refuses — assemble must drop it rather than panic on a missing key.
func TestAssembleDropsAPageIdTheGateRefused(t *testing.T) {
	page := []string{domain.ResourceKey("cat-1", "wheat"), domain.ResourceKey("cat-1", "barley")}
	resources := []gen.HydrateResourcesRow{
		{CatalogID: "cat-1", ID: "wheat", Document: []byte(`{"id":"wheat"}`)},
		// "barley" is on the page but never came back from HydrateResources —
		// the gate refused it.
	}
	catalogs := []gen.HydrateCatalogsRow{{ID: "cat-1", Document: []byte(`{"id":"cat-1"}`)}}

	assembled := assemble(page, resources, catalogs, nil, nil)
	if len(assembled) != 1 || len(assembled[0].Resources) != 1 || assembled[0].Resources[0].ID != "wheat" {
		t.Fatalf("assembled = %+v, want one catalog carrying only wheat", assembled)
	}
}

// A catalog row keyed by a catalog no resource on the page belongs to must not
// reach the response — attachCatalogs is the last thing standing between a row
// HydrateCatalogs should never return and a caller seeing it anyway.
func TestAttachCatalogsSkipsARowNotOnThePage(t *testing.T) {
	assembled := []domain.Catalog{{ID: "cat-1"}}
	position := map[string]int{"cat-1": 0}
	rows := []gen.HydrateCatalogsRow{
		{ID: "cat-1", Document: []byte(`{"id":"cat-1"}`)},
		{ID: "cat-2", Document: []byte(`{"id":"cat-2"}`)}, // not on the page
	}

	attachCatalogs(assembled, position, rows)
	if string(assembled[0].Document) != `{"id":"cat-1"}` {
		t.Errorf("catalogs[0].Document = %s, want cat-1's own", assembled[0].Document)
	}
}

// The same for offers: a row keyed by a catalog not on the page is skipped
// rather than indexing past the assembled slice or attaching to the wrong one.
func TestAttachOffersSkipsARowNotOnThePage(t *testing.T) {
	assembled := []domain.Catalog{{ID: "cat-1"}}
	position := map[string]int{"cat-1": 0}
	rows := []gen.HydrateOffersRow{
		{CatalogID: "cat-1", ID: "on-cat-1", Document: []byte(`{"id":"on-cat-1"}`)},
		{CatalogID: "cat-2", ID: "on-cat-2", Document: []byte(`{"id":"on-cat-2"}`)}, // not on the page
	}

	attachOffers(assembled, position, rows)
	if len(assembled[0].Offers) != 1 || assembled[0].Offers[0].ID != "on-cat-1" {
		t.Errorf("offers = %+v, want only on-cat-1", assembled[0].Offers)
	}
}

// stubRetriever answers a fixed id list, so Search reaches hydration without a
// real corpus behind it.
type stubRetriever struct{ ids []string }

func (r stubRetriever) Retrieve(context.Context, domain.SearchQuery, domain.Scope) ([]string, error) {
	return r.ids, nil
}

// failingHydrator's Hydrate always errors — the fault Search.Hydrate wraps
// with nothing else, unlike ScopeFilter and Hydrate's own four queries above,
// which each name what they were doing.
type failingHydrator struct{}

func (failingHydrator) ScopeFilter(context.Context, []string, domain.Scope) ([]string, error) {
	return nil, nil
}

func (failingHydrator) Hydrate(context.Context, []string, domain.Scope) ([]domain.Catalog, error) {
	return nil, errors.New("hydration boom")
}

// Search's own error branch: a hydrator failure fails the whole search rather
// than answering an empty page, the same posture TestABackendErrorIsNotAnEmptyPage
// pins at the discover.Service level. Built with a struct literal — package
// postgres, not postgres_test — because SearchRepository has no exported way
// to swap in a faulty hydrator once NewSearchRepository has built the real one.
func TestSearchFailsWhenTheHydratorDoes(t *testing.T) {
	repository := &SearchRepository{
		retrievers: map[domain.Capability]domain.Retriever{
			domain.CapabilityLexical: stubRetriever{ids: []string{domain.ResourceKey("cat-1", "wheat")}},
		},
		hydrator: failingHydrator{},
		search:   config.Search{MaxCandidatesPerMode: 100, ReadDeadline: 10 * time.Second},
	}

	_, err := repository.Search(context.Background(),
		domain.SearchQuery{Text: "wheat", Limit: 10}, []domain.Capability{domain.CapabilityLexical})
	if err == nil {
		t.Fatal("Search answered despite the hydrator failing; want the error surfaced")
	}
}
