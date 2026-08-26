package discover_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/discover"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
	"github.com/OpenAgriNet/discovery-service/src/storage/memory"
)

// indexResolution is the H3 resolution the memory backend covers with, matching
// settings().Geo.ResolutionCells.
const indexResolution = 8

// stubRepo answers with whatever it was built to answer and records what it was
// asked.
//
// A stub rather than the memory backend for the negotiation tests, because what
// those pin is which modes the SERVICE hands down — a backend that quietly
// ignored an unsupported mode would make the assertion pass with the
// negotiation deleted.
type stubRepo struct {
	capabilities domain.Capabilities
	result       domain.SearchResult
	err          error

	calls    int
	gotQuery domain.SearchQuery
	gotModes []domain.Capability
}

func (r *stubRepo) Capabilities() domain.Capabilities { return r.capabilities }

func (r *stubRepo) Search(
	_ context.Context, query domain.SearchQuery, modes []domain.Capability,
) (domain.SearchResult, error) {
	r.calls++
	r.gotQuery = query
	r.gotModes = slices.Clone(modes)
	return r.result, r.err
}

// everything is the backend that can do it all, so a test about something else
// is not silently also a test about degradation.
func everything() domain.Capabilities {
	return domain.Capabilities{
		domain.CapabilityLexical:  true,
		domain.CapabilityFuzzy:    true,
		domain.CapabilitySemantic: true,
		domain.CapabilitySpatial:  true,
		domain.CapabilityJSONPath: true,
	}
}

// phase1 is a default Phase 1 deployment: EMBEDDING_PROVIDER=noop, so the
// composition root hands the repository no embedder and `semantic` is the mode
// that is actually missing.
func phase1() domain.Capabilities {
	return domain.Capabilities{
		domain.CapabilityLexical: true,
		domain.CapabilityFuzzy:   true,
		domain.CapabilitySpatial: true,
	}
}

func hasMode(modes []domain.Capability, wanted domain.Capability) bool {
	return slices.Contains(modes, wanted)
}

func codeOf(t *testing.T, err error) beckn.ErrorCode {
	t.Helper()

	fault := apperrors.FromError(err)
	if fault == nil {
		t.Fatalf("expected a fault, got nil")
	}
	return fault.Code
}

// A textSearch on a deployment with no semantic mode answers, and says what it
// could not run (C11).
//
// Semantic rather than structured: `filters` is evaluated in Phase 1, so a
// degradation pinned on a trigger that no longer degrades pins nothing.
func TestAModeTheBackendLacksIsNamedRatherThanDropped(t *testing.T) {
	repo := &stubRepo{
		capabilities: phase1(),
		result:       domain.SearchResult{Catalogs: []domain.Catalog{{ID: "c1"}}},
	}

	catalogs, degraded, err := discover.NewService(repo, settings()).
		Discover(t.Context(), beckn.Context{}, beckn.Intent{TextSearch: "wheat seeds"}, discover.Page{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(catalogs) != 1 {
		t.Errorf("catalogs = %d, want the one the backend found — degrading is not failing", len(catalogs))
	}
	if !slices.Equal(degraded, []string{string(domain.CapabilitySemantic)}) {
		t.Errorf("degraded = %v, want [semantic]", degraded)
	}
	if hasMode(repo.gotModes, domain.CapabilitySemantic) {
		t.Errorf("modes = %v, want semantic withheld from a backend that cannot run it", repo.gotModes)
	}
	if !hasMode(repo.gotModes, domain.CapabilityLexical) || !hasMode(repo.gotModes, domain.CapabilityFuzzy) {
		t.Errorf("modes = %v, want the two the backend does have", repo.gotModes)
	}
}

// The same request under SEARCH_FAIL_ON_UNAVAILABLE_MODE=true. The deployment
// asked to be told rather than served, so it is.
func TestAMissingModeIsRefusedWhenTheDeploymentAsksToBe(t *testing.T) {
	repo := &stubRepo{capabilities: phase1()}

	cfg := settings()
	cfg.Search.FailOnUnavailableMode = true

	_, _, err := discover.NewService(repo, cfg).
		Discover(t.Context(), beckn.Context{}, beckn.Intent{TextSearch: "wheat seeds"}, discover.Page{})
	if err == nil {
		t.Fatal("Discover succeeded; want a refusal naming the missing mode")
	}
	if got := codeOf(t, err); got != beckn.CodeNetworkCatalogSourceUnavailable {
		t.Errorf("code = %q, want NET_CATALOG_SOURCE_UNAVAILABLE", got)
	}
	if !strings.Contains(err.Error(), string(domain.CapabilitySemantic)) {
		t.Errorf("message = %q, want the missing mode named", err.Error())
	}
	if repo.calls != 0 {
		t.Errorf("the backend was searched %d times; a refusal runs no query", repo.calls)
	}
}

// Nothing degrades when the backend has everything, and the header is then
// absent rather than empty.
func TestNothingIsReportedDegradedWhenNothingIs(t *testing.T) {
	repo := &stubRepo{capabilities: everything()}

	_, degraded, err := discover.NewService(repo, settings()).Discover(
		t.Context(), beckn.Context{},
		beckn.Intent{TextSearch: "wheat", Filters: &beckn.Filters{Type: "jsonpath", Expression: "$"}},
		discover.Page{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(degraded) != 0 {
		t.Errorf("degraded = %v, want none", degraded)
	}
}

// Scenario 29, from the service's side: an omitted networkId searches EVERY
// network, and does NOT fall back to APP_NETWORK_ID.
//
// The config carries a network id precisely so the assertion has something to
// fail against: with App.Network empty, a service that wrongly defaulted would
// pass this test by coincidence.
func TestAnOmittedNetworkIDSearchesEveryNetwork(t *testing.T) {
	repo := &stubRepo{capabilities: everything()}

	cfg := settings()
	cfg.App.Network = "mahavistar"

	_, _, err := discover.NewService(repo, cfg).
		Discover(t.Context(), beckn.Context{}, beckn.Intent{TextSearch: "wheat"}, discover.Page{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if repo.gotQuery.NetworkID != "" {
		t.Errorf("NetworkID = %q, want unscoped — publish's visibleTo default is a different question (C8)",
			repo.gotQuery.NetworkID)
	}
}

// The other half: a networkId that IS sent scopes the search to it.
func TestANetworkIDThatWasSentScopesTheSearch(t *testing.T) {
	repo := &stubRepo{capabilities: everything()}

	_, _, err := discover.NewService(repo, settings()).Discover(
		t.Context(), beckn.Context{NetworkID: "bharatvistar"},
		beckn.Intent{TextSearch: "wheat"}, discover.Page{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if repo.gotQuery.NetworkID != "bharatvistar" {
		t.Errorf("NetworkID = %q, want the envelope's own", repo.gotQuery.NetworkID)
	}
}

// A limit over MaxPageSize is CLAMPED, not refused: the caller still gets the
// results they asked about. A page past the retrieval depth is the one clamp
// this service will not perform quietly, and it is refused in the same test so
// the two cannot be conflated.
func TestAnOversizeLimitIsClampedAndAnUnreachablePageIsRefused(t *testing.T) {
	repo := &stubRepo{capabilities: everything()}
	service := discover.NewService(repo, settings())

	_, _, err := service.Discover(
		t.Context(), beckn.Context{}, beckn.Intent{TextSearch: "wheat"},
		discover.Page{Limit: 1000})
	if err != nil {
		t.Fatalf("an oversize limit was refused: %v", err)
	}
	if repo.gotQuery.Limit != settings().Search.MaxPageSize {
		t.Errorf("Limit = %d, want it clamped to %d", repo.gotQuery.Limit, settings().Search.MaxPageSize)
	}

	if _, _, err := service.Discover(
		t.Context(), beckn.Context{}, beckn.Intent{TextSearch: "wheat"},
		discover.Page{Limit: 20, Offset: 490}); err == nil {
		t.Error("a page past the retrieval depth was answered; want a refusal naming the boundary")
	}
}

// A fatal mapper fault refuses the request and runs no query. Continuing would
// WIDEN it, and a widened answer is indistinguishable at the caller from a
// correct one.
func TestAFatalMapperFaultRefusesBeforeAnythingIsSearched(t *testing.T) {
	repo := &stubRepo{capabilities: everything()}

	_, _, err := discover.NewService(repo, settings()).Discover(
		t.Context(), beckn.Context{},
		spatialIntent(beckn.SpatialConstraint{
			Op:       beckn.OpSTouches,
			Targets:  beckn.Targets{"$.catalogs[*].provider.availableAt[*].geo"},
			Geometry: bengaluru(),
		}),
		discover.Page{})
	if err == nil {
		t.Fatal("S_TOUCHES was answered; want SCH_TYPE_NOT_SUPPORTED")
	}
	if got := codeOf(t, err); got != beckn.CodeSchemaTypeNotSupported {
		t.Errorf("code = %q, want SCH_TYPE_NOT_SUPPORTED", got)
	}
	if repo.calls != 0 {
		t.Errorf("the backend was searched %d times; a refused intent runs no query", repo.calls)
	}
}

// A backend error is the caller's 500, not an empty 200. An empty page and a
// dead backend read identically at the caller, which is the whole reason this
// is not swallowed.
func TestABackendErrorIsNotAnEmptyPage(t *testing.T) {
	repo := &stubRepo{capabilities: everything(), err: errors.New("pool closed")}

	if _, _, err := discover.NewService(repo, settings()).Discover(
		t.Context(), beckn.Context{}, beckn.Intent{TextSearch: "wheat"},
		discover.Page{}); err == nil {
		t.Fatal("a failed search answered 200 with nothing; want an error")
	}
}

// The backend's own degraded list joins the negotiation's rather than replacing
// it: a mode this deployment lacks and a mode that died mid-search are both
// things the caller has to be told.
func TestTheBackendsOwnDegradationIsCarriedToo(t *testing.T) {
	repo := &stubRepo{
		capabilities: phase1(),
		result:       domain.SearchResult{Degraded: []string{string(domain.CapabilityFuzzy)}},
	}

	_, degraded, err := discover.NewService(repo, settings()).
		Discover(t.Context(), beckn.Context{}, beckn.Intent{TextSearch: "wheat"}, discover.Page{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !slices.Contains(degraded, string(domain.CapabilitySemantic)) ||
		!slices.Contains(degraded, string(domain.CapabilityFuzzy)) {
		t.Errorf("degraded = %v, want both the missing mode and the failed one", degraded)
	}
}

// The offer reaches the caller as the publisher wrote it.
//
// `offers.offer` is stored verbatim for exactly this, so a member this
// service's own struct never named must survive the round trip — a projection
// the storage layer does not keep is one the response must not invent, and one
// it does keep is one the response must not drop.
func TestARenderedCatalogCarriesItsOffersVerbatim(t *testing.T) {
	repo := &stubRepo{capabilities: everything(), result: domain.SearchResult{Catalogs: []domain.Catalog{{
		ID:       "c1",
		Provider: json.RawMessage(`{"id":"p1"}`),
		Resources: []domain.Resource{{
			ID:         "r1",
			CatalogID:  "c1",
			Descriptor: json.RawMessage(`{"name":"Wheat"}`),
			Attributes: json.RawMessage(`{"@context":"https://beckn.org/Agri","@type":"SeedLot"}`),
		}},
		Offers: []domain.Offer{{
			ID:        "o1",
			CatalogID: "c1",
			Document:  json.RawMessage(`{"id":"o1","resourceIds":["r1"],"vendorNote":"ten per cent off"}`),
		}},
	}}}}

	catalogs, _, err := discover.NewService(repo, settings()).
		Discover(t.Context(), beckn.Context{}, beckn.Intent{TextSearch: "wheat"}, discover.Page{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(catalogs) != 1 || len(catalogs[0].Offers) != 1 {
		t.Fatalf("rendered = %+v, want one catalog carrying one offer", catalogs)
	}

	catalog := catalogs[0]
	if catalog.ID != "c1" || string(catalog.Provider) != `{"id":"p1"}` {
		t.Errorf("catalog = %+v, want the id and the provider it was stored with", catalog)
	}
	if len(catalog.Resources) != 1 ||
		string(catalog.Resources[0].ResourceAttributes) != `{"@context":"https://beckn.org/Agri","@type":"SeedLot"}` {
		t.Errorf("resources = %+v, want the attributes stored on them", catalog.Resources)
	}

	encoded, err := json.Marshal(catalog.Offers[0])
	if err != nil {
		t.Fatalf("marshalling the rendered offer: %v", err)
	}
	if !strings.Contains(string(encoded), "vendorNote") {
		t.Errorf("offer = %s, want the member this service never named to survive", encoded)
	}
}

// Offers are scoped to the page, and this runs against a real backend because
// the scoping is the backend's — a stub would be asserting against its own
// fixture.
//
// Two resources, one offer naming only the second. A search that matches only
// the first must not carry it.
func TestNoOfferWhoseResourcesAreAllOffThePageIsRendered(t *testing.T) {
	repo := memory.New(indexResolution)

	if _, err := repo.UpsertCatalog(t.Context(), domain.CatalogPatch{
		ID:        "c1",
		Active:    true,
		VisibleTo: []string{"mahavistar"},
		Resources: []domain.ResourcePatch{{ID: "wheat"}, {ID: "rice"}},
		Offers: []domain.OfferPatch{{
			ID:          "on-rice",
			ResourceIDs: []string{"rice"},
			Document:    json.RawMessage(`{"id":"on-rice","resourceIds":["rice"]}`),
		}},
	}, domain.UpdateMode("FULL"), searchTextIsTheResourceID); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	catalogs, _, err := discover.NewService(repo, settings()).
		Discover(t.Context(), beckn.Context{}, beckn.Intent{TextSearch: "wheat"}, discover.Page{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(catalogs) != 1 || len(catalogs[0].Resources) != 1 || catalogs[0].Resources[0].ID != "wheat" {
		t.Fatalf("rendered = %+v, want only the matched resource", catalogs)
	}
	if len(catalogs[0].Offers) != 0 {
		t.Errorf("offers = %+v, want none — that offer names only a resource off this page", catalogs[0].Offers)
	}
}

// searchTextIsTheResourceID is the smallest derivation that makes a lexical
// search over the memory backend mean something. The real one is the publish
// path's; this test is about rendering, not about text derivation.
func searchTextIsTheResourceID(merged *domain.Catalog, _ []string) []domain.Fault {
	for index := range merged.Resources {
		merged.Resources[index].SearchText = merged.Resources[index].ID
	}
	return nil
}

// A fault the mapper raises against the CONTEXT keeps its own family.
//
// CTX_INVALID_FIELD and the SCH_ codes beside it are answered by the same
// switch, and the six family constructors have identical bodies — so picking
// the wrong one changes nothing at the call site and both `error_type` and the
// status on the wire. This is the behavioural half of
// TestEveryCodeTheMapperMintsIsTyped.
func TestAContextFaultIsNotReportedAsASchemaOne(t *testing.T) {
	repo := &stubRepo{capabilities: everything()}

	_, _, err := discover.NewService(repo, settings()).Discover(
		t.Context(),
		beckn.Context{SchemaContext: []string{"#SeedLot"}},
		beckn.Intent{TextSearch: "wheat"}, discover.Page{})
	if err == nil {
		t.Fatal("a schemaContext entry with no context URI was accepted")
	}
	if got := codeOf(t, err); got != beckn.CodeContextInvalidField {
		t.Errorf("code = %q, want CTX_INVALID_FIELD", got)
	}
	if got := apperrors.FromError(err).Type(); got != apperrors.TypeContext {
		t.Errorf("error_type = %q, want CONTEXT", got)
	}
}
