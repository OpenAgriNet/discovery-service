package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/embeddings"
	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres"
	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// The read path against a real PostgreSQL. Everything here turns on behaviour
// the SQL source test cannot see: the source test proves a clause is PRESENT,
// these prove it means what it was written to mean.

// searchConfig is the config every repository below is built with, with a small
// candidate cap so the pagination-depth and cap-reporting cases can reach it
// without a corpus of five hundred rows.
//
// The page sizes shrink WITH the cap and not independently of it: config's
// validate rejects MaxCandidatesPerMode < MaxPageSize, because a candidate pool
// smaller than one page cannot fill it, and a fixture that broke that ratio
// would be asserting against a configuration the service refuses to start on.
func searchConfig() config.Search {
	return config.Search{
		DefaultPageSize:      4,
		MaxPageSize:          8,
		MaxCandidatesPerMode: 8,
		MaxRadiusMeters:      200000,
		ReadDeadline:         10 * time.Second,
	}
}

// deriveSearchable is the stand-in for Task 17's derivation: it gives every
// resource the name, search text and schema pair the read path indexes on,
// taken from the attributes the fixture published.
//
// A fixture that set `name` by hand and left `search_text` empty would publish
// rows the lexical retriever cannot match and the fuzzy one can, which is a
// corpus that silently tests one mode.
func deriveSearchable(merged *domain.Catalog, _ []string) []domain.Fault {
	for index := range merged.Resources {
		resource := &merged.Resources[index]

		var fields struct {
			Name    string `json:"name"`
			Context string `json:"@context"`
			Type    string `json:"@type"`
			Text    string `json:"text"`
		}
		if err := json.Unmarshal(resource.Attributes, &fields); err != nil {
			return []domain.Fault{{Code: "FIXTURE", Message: err.Error()}}
		}
		resource.Name = fields.Name
		resource.SchemaContext = fields.Context
		resource.SchemaType = fields.Type
		resource.SearchText = strings.TrimSpace(fields.Name + " " + fields.Text)
	}
	return nil
}

// kharifLots is a corpus of interchangeable resources sharing one token, for
// the cases that need more rows than the candidate cap admits.
func kharifLots(count int) []domain.ResourcePatch {
	lots := make([]domain.ResourcePatch, 0, count)
	for index := range count {
		lots = append(lots, searchable(
			fmt.Sprintf("r-%02d", index), fmt.Sprintf("kharif lot %02d", index),
			"", "https://beckn.org/Agri", "SeedLot"))
	}
	return lots
}

// deriveVectors stores a vector on the named resources, the way a write-path
// embedder would. The width is the column's, 768, and not the repository's,
// because those two disagreeing is a state this corpus has to be able to reach.
func deriveVectors(ids ...string) domain.DeriveFunc {
	return func(merged *domain.Catalog, touched []string) []domain.Fault {
		if faults := deriveSearchable(merged, touched); faults != nil {
			return faults
		}
		for index := range merged.Resources {
			if !slices.Contains(ids, merged.Resources[index].ID) {
				continue
			}
			vector := make([]float32, 768)
			for position := range vector {
				vector[position] = float32(position%7) / 7
			}
			merged.Resources[index].Embedding = vector
		}
		return nil
	}
}

// readFixture is one publishable catalog, spelled as the fields these cases
// actually vary.
type readFixture struct {
	catalog   string
	visibleTo []string
	resources []domain.ResourcePatch
	offers    []domain.OfferPatch
	derive    domain.DeriveFunc
}

// searchable builds a resource patch whose attributes carry everything
// deriveSearchable reads.
func searchable(id, name, text, context, resourceType string) domain.ResourcePatch {
	attributes, err := json.Marshal(map[string]string{
		"name": name, "text": text, "@context": context, "@type": resourceType,
	})
	if err != nil {
		panic("fixture attributes will not marshal: " + err.Error())
	}
	return domain.ResourcePatch{ID: id, Attributes: attributes}
}

// publish writes the fixtures and returns a read repository over the same pool.
//
// Published through the WRITE repository rather than by INSERT, so the corpus
// these cases read is the corpus a publish actually produces — the tsvector
// built by the real statement, the geometries covered by the real cover.
func publish(t *testing.T, embedder embeddings.Embedder, fixtures ...readFixture) (
	*postgres.SearchRepository, *pgxpool.Pool,
) {
	t.Helper()

	pool := dbtest.NewPostgres(t)
	writer := postgres.NewCatalogRepository(pool, resolution)

	for _, fixture := range fixtures {
		visibleTo := fixture.visibleTo
		if visibleTo == nil {
			visibleTo = []string{"bap.example.com"}
		}
		derive := fixture.derive
		if derive == nil {
			derive = deriveSearchable
		}

		faults, err := writer.UpsertCatalog(context.Background(), domain.CatalogPatch{
			ID:        fixture.catalog,
			NetworkID: visibleTo[0],
			Active:    true,
			VisibleTo: visibleTo,
			Resources: fixture.resources,
			Offers:    fixture.offers,
		}, domain.UpdateModeFull, derive)
		if err != nil {
			t.Fatalf("publish %s: %v", fixture.catalog, err)
		}
		if len(faults) > 0 {
			t.Fatalf("publish %s reported faults the fixture did not expect: %v", fixture.catalog, faults)
		}
	}

	return postgres.NewSearchRepository(pool, searchConfig(), embedder), pool
}

// bothTextModes is what a caller who typed something asks for. Named because
// almost every case below wants exactly this and a case that quietly ran one
// mode would pass while the other was broken.
var bothTextModes = []domain.Capability{domain.CapabilityLexical, domain.CapabilityFuzzy}

// matchedIDs flattens a result to the resource ids on it, in page order.
func matchedIDs(result domain.SearchResult) []string {
	ids := make([]string, 0, len(result.Catalogs))
	for _, catalog := range result.Catalogs {
		for _, resource := range catalog.Resources {
			ids = append(ids, resource.ID)
		}
	}
	return ids
}

func search(t *testing.T, repository *postgres.SearchRepository, query domain.SearchQuery) domain.SearchResult {
	t.Helper()

	if query.Limit == 0 {
		query.Limit = searchConfig().MaxPageSize
	}
	result, err := repository.Search(context.Background(), query, bothTextModes)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return result
}

// ---------------------------------------------------------------------------
// the network gate
// ---------------------------------------------------------------------------

// Both directions in ONE test, because a fix aimed at either one is a
// regression in the other: defaulting the empty case to the service's own
// network makes the first assertion fail, and dropping the predicate entirely
// makes the second.
func TestAnOmittedNetworkIdSearchesEveryNetworkAndAGivenOneNarrows(t *testing.T) {
	repository, _ := publish(t, nil,
		readFixture{
			catalog:   "cat-open",
			visibleTo: []string{"mahavistar"},
			resources: []domain.ResourcePatch{searchable("r-open", "wheat seed", "", "https://beckn.org/Agri", "SeedLot")},
		},
		readFixture{
			catalog:   "cat-closed",
			visibleTo: []string{"private.example.com"},
			resources: []domain.ResourcePatch{searchable("r-closed", "wheat seed", "", "https://beckn.org/Agri", "SeedLot")},
		},
	)

	everyNetwork := matchedIDs(search(t, repository, domain.SearchQuery{Text: "wheat"}))
	if !slices.Contains(everyNetwork, "r-closed") {
		t.Errorf("an omitted networkId returned %v; it must search EVERY network, "+
			"including one whose visibleTo names a network the caller never mentioned", everyNetwork)
	}
	if len(everyNetwork) != 2 {
		t.Errorf("an omitted networkId matched %d resources, want both: %v", len(everyNetwork), everyNetwork)
	}

	scoped := matchedIDs(search(t, repository, domain.SearchQuery{Text: "wheat", NetworkID: "mahavistar"}))
	if !slices.Equal(scoped, []string{"r-open"}) {
		t.Errorf("networkId=mahavistar returned %v, want only that network's row", scoped)
	}
}

// ---------------------------------------------------------------------------
// the schema pair
// ---------------------------------------------------------------------------

func TestSchemaFilteringComparesContextAndTypeAsAPair(t *testing.T) {
	repository, _ := publish(t, nil, readFixture{
		catalog: "cat-schema",
		resources: []domain.ResourcePatch{
			searchable("grocery", "wheat flour", "", "https://schema.org", "GroceryItem"),
			searchable("ride", "wheat transport", "", "https://beckn.org/Mobility", "RideService"),
			searchable("other", "wheat storage", "", "https://beckn.org/Agri", "SeedLot"),
		},
	})

	// One static query, run at one, two and three entries. The failure this
	// shape replaced was a clause count that varied with the request, so a case
	// exercising a single length would not have seen it.
	for _, testCase := range []struct {
		name    string
		schemas []domain.SchemaFilter
		want    []string
	}{
		{
			name:    "one entry",
			schemas: []domain.SchemaFilter{{Context: "https://schema.org", Type: "GroceryItem"}},
			want:    []string{"grocery"},
		},
		{
			name: "two entries",
			schemas: []domain.SchemaFilter{
				{Context: "https://schema.org", Type: "GroceryItem"},
				{Context: "https://beckn.org/Mobility", Type: "RideService"},
			},
			want: []string{"grocery", "ride"},
		},
		{
			name: "three entries",
			schemas: []domain.SchemaFilter{
				{Context: "https://schema.org", Type: "GroceryItem"},
				{Context: "https://beckn.org/Mobility", Type: "RideService"},
				{Context: "https://beckn.org/Agri", Type: "SeedLot"},
			},
			want: []string{"grocery", "other", "ride"},
		},
		{
			// The cross-match: schema.org is a published context and
			// RideService a published type, but never on the same row. Two
			// independent IN lists return `ride`; a paired comparison returns
			// nothing.
			name:    "the cross-match is refused",
			schemas: []domain.SchemaFilter{{Context: "https://schema.org", Type: "RideService"}},
			want:    nil,
		},
		{
			// An empty schemaContext must emit NO predicate rather than one
			// matching nothing — the difference between "every result" and
			// "every response is empty".
			name:    "an empty list filters nothing",
			schemas: nil,
			want:    []string{"grocery", "other", "ride"},
		},
		{
			// An entry with no fragment is "any type under this context".
			name:    "a context with no type admits every type under it",
			schemas: []domain.SchemaFilter{{Context: "https://beckn.org/Mobility"}},
			want:    []string{"ride"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := matchedIDs(search(t, repository, domain.SearchQuery{
				Text: "wheat", Schemas: testCase.schemas,
			}))
			slices.Sort(got)
			if !slices.Equal(got, testCase.want) {
				t.Errorf("got %v, want %v", got, testCase.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Total, and pagination over it
// ---------------------------------------------------------------------------

// The corpus is built so lexical and fuzzy match DIFFERENT resources: only a
// counter carrying the OR of both clauses reports the size of the set the
// fusion drew from. A counter carrying lexical's alone returns a number smaller
// than page 1 already showed.
func TestTotalIsTheSizeOfTheUnionOfEveryModesTextClause(t *testing.T) {
	repository, _ := publish(t, nil, readFixture{
		catalog: "cat-union",
		resources: []domain.ResourcePatch{
			// Lexical only: the token is in the search text, and the name is
			// nothing like the query.
			searchable("lex-1", "zzz alpha", "kharif", "https://beckn.org/Agri", "SeedLot"),
			searchable("lex-2", "zzz beta", "kharif", "https://beckn.org/Agri", "SeedLot"),
			// Fuzzy only: a near-miss NAME with none of the query's tokens in
			// its text, so `%` matches and `@@` does not.
			searchable("fuz-1", "kharrif", "", "https://beckn.org/Agri", "SeedLot"),
		},
	})

	result := search(t, repository, domain.SearchQuery{Text: "kharif"})

	if got := len(matchedIDs(result)); got != 3 {
		t.Fatalf("the page holds %d resources, want the 3 in the union: %v",
			got, matchedIDs(result))
	}
	if result.Total != 3 {
		t.Errorf("Total is %d, want 3 — the union of the lexical and fuzzy clauses, "+
			"not either one alone", result.Total)
	}
}

func TestPageTwoDoesNotOverlapPageOne(t *testing.T) {
	repository, _ := publish(t, nil, readFixture{catalog: "cat-paged", resources: kharifLots(6)})

	first := matchedIDs(search(t, repository, domain.SearchQuery{Text: "kharif", Limit: 3}))
	second := matchedIDs(search(t, repository, domain.SearchQuery{Text: "kharif", Limit: 3, Offset: 3}))

	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("pages hold %d and %d, want 3 each: %v then %v", len(first), len(second), first, second)
	}
	for _, id := range second {
		if slices.Contains(first, id) {
			t.Errorf("%q is on both pages: %v then %v", id, first, second)
		}
	}
}

// ---------------------------------------------------------------------------
// the count skip, and its four guards
// ---------------------------------------------------------------------------

// oracle is Total computed the long way: the count query itself, run against
// the same corpus with the same predicates and no skip in front of it.
//
// This is what "the same query with the skip forced on and forced off" means
// without a switch in production code to force it — a switch that would exist
// only for the test and would itself be the thing most likely to drift.
func oracle(
	t *testing.T, pool *pgxpool.Pool, embedder embeddings.Embedder, query domain.SearchQuery,
) int {
	t.Helper()

	total, err := postgres.NewHydrator(pool, embedder).
		Count(context.Background(), query, domain.Scope{NetworkID: query.NetworkID, Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return total
}

// The skip is an optimisation, so the number it produces must be the number the
// query would have produced. Every sub-case asserts against the count query
// rather than against a literal, which is what makes this an agreement test and
// not two independent guesses at the same corpus.
func TestTheCountSkipAgreesWithTheCountItSkips(t *testing.T) {
	// Eleven rows sharing a token, against a cap of eight, so the capped guard
	// has something to be capped by — plus one row nothing else resembles, so
	// there is a query narrow enough for the skip itself to fire.
	resources := append(kharifLots(11), searchable(
		"r-papaya", "singular papaya", "", "https://beckn.org/Agri", "SeedLot"))
	repository, pool := publish(t, nil, readFixture{catalog: "cat-count", resources: resources})

	for _, testCase := range []struct {
		name  string
		query domain.SearchQuery
	}{
		// offset 0, a short page, nothing degraded, nothing capped: len(fused)
		// IS the count and no query is issued. The one case where the skip
		// actually fires, and therefore the one that would catch it firing
		// wrongly.
		{name: "the skip fires", query: domain.SearchQuery{Text: "papaya"}},

		// Past page 1: nothing in hand can say how much sits behind the caller.
		{name: "offset past zero", query: domain.SearchQuery{Text: "kharif", Limit: 4, Offset: 4}},

		// Both text modes return exactly the cap, so the fused list is a
		// truncation of the corpus rather than the answer.
		//
		// The capped guard sits BEHIND the full-page guard here and cannot be
		// reached alone: config's validate keeps MaxPageSize <= the cap, so a
		// capped mode always fills the page and the page-length guard fires
		// first. It is kept because it is the guard that stays right if that
		// ratio is ever relaxed — and what this case pins is the outcome, which
		// is wrong if BOTH are dropped.
		{name: "a capped mode", query: domain.SearchQuery{Text: "kharif", Limit: 4}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result := search(t, repository, testCase.query)
			if want := oracle(t, pool, nil, testCase.query); result.Total != want {
				t.Errorf("Total is %d; the count query says %d", result.Total, want)
			}
		})
	}
}

// The degraded guard, given a corpus where it changes the answer.
//
// The vectors are 768 wide because the column is, and the repository embeds at
// 384: the semantic retriever's `<=>` is a width mismatch and errors, while the
// counter — which only asks whether a row HAS an embedding — does not. So the
// pool the count describes holds three rows the fused list cannot, which is the
// under-report the guard exists to prevent. Every other degradation shape has
// the same shape as this one and a less visible arithmetic.
func TestADegradedModeForcesTheCountAndTheCountIsWider(t *testing.T) {
	embedded := kharifLots(3)
	repository, pool := publish(t, embeddings.NewHashing(384), readFixture{
		catalog:   "cat-degraded-count",
		resources: append(embedded, searchable("r-papaya", "singular papaya", "", "https://beckn.org/Agri", "SeedLot")),
		derive:    deriveVectors("r-00", "r-01", "r-02"),
	})

	query := domain.SearchQuery{Text: "papaya", Limit: searchConfig().MaxPageSize}
	result, err := repository.Search(context.Background(), query,
		[]domain.Capability{domain.CapabilityLexical, domain.CapabilityFuzzy, domain.CapabilitySemantic})
	if err != nil {
		t.Fatalf("a mode that could not run failed the whole search: %v", err)
	}
	if !slices.Contains(result.Degraded, string(domain.CapabilitySemantic)) {
		t.Fatalf("Degraded is %v, want it to name semantic", result.Degraded)
	}
	if got := matchedIDs(result); !slices.Equal(got, []string{"r-papaya"}) {
		t.Fatalf("the page holds %v, want the one row the text modes matched", got)
	}

	if want := oracle(t, pool, embeddings.NewHashing(384), query); result.Total != want {
		t.Errorf("Total is %d; the count query says %d", result.Total, want)
	}
	if result.Total != 4 {
		t.Errorf("Total is %d, want 4 — one text match plus the three rows the "+
			"semantic pool holds; the skip would have said 1", result.Total)
	}
}

// An embedder that is DOWN must degrade the mode, not fail the request.
//
// The counter embeds the same text with the same embedder as the retriever, so
// a provider that is unreachable breaks both. The retriever's failure is
// already reported in Degraded; the counter's must therefore narrow the count
// to the modes that ran rather than take the whole response down with it — a
// page was produced, and answering 502 because the total could not be widened
// throws away work the caller can use.
func TestAnUnreachableEmbedderDegradesTheModeRatherThanFailingTheSearch(t *testing.T) {
	repository, _ := publish(t, unreachable{}, readFixture{
		catalog:   "cat-unreachable",
		resources: []domain.ResourcePatch{searchable("r-1", "kharif seed", "", "https://beckn.org/Agri", "SeedLot")},
	})

	result, err := repository.Search(context.Background(),
		domain.SearchQuery{Text: "kharif", Limit: searchConfig().MaxPageSize},
		[]domain.Capability{domain.CapabilityLexical, domain.CapabilitySemantic})
	if err != nil {
		t.Fatalf("an unreachable embedder failed the whole search: %v", err)
	}
	if !slices.Contains(result.Degraded, string(domain.CapabilitySemantic)) {
		t.Errorf("Degraded is %v, want it to name semantic", result.Degraded)
	}
	if result.Total != 1 {
		t.Errorf("Total is %d, want the 1 the modes that ran found", result.Total)
	}
}

// unreachable is the provider whose service is down: every call fails, and none
// of them is a reason to fail a publish or a search.
type unreachable struct{}

func (unreachable) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("the embedding service is unreachable")
}

func (unreachable) Dimensions() int { return 768 }

// ---------------------------------------------------------------------------
// the retrieval depth
// ---------------------------------------------------------------------------

// Asserted against the empty catalogs array it would otherwise return, which
// reads at the caller exactly like the end of the results — while Total is
// still reporting a corpus of twelve.
func TestAPagePastTheRetrievalDepthIsAFaultAndNotAnEmptyPage(t *testing.T) {
	repository, _ := publish(t, nil, readFixture{catalog: "cat-depth", resources: kharifLots(12)})

	depth := searchConfig().MaxCandidatesPerMode
	result, err := repository.Search(context.Background(),
		domain.SearchQuery{Text: "kharif", Limit: 4, Offset: depth}, bothTextModes)

	if err == nil {
		t.Fatalf("a page at offset %d returned %d catalogs and no error; the boundary "+
			"must be named, because an empty page is indistinguishable from the end "+
			"of the results", depth, len(result.Catalogs))
	}
	if !strings.Contains(err.Error(), fmt.Sprint(depth)) {
		t.Errorf("the error does not name the boundary it refused at: %v", err)
	}
}

// A mode that returns exactly its cap is a mode whose list is a truncation
// rather than the answer, and that is the one state the count guards read. The
// corpus is wider than the cap on the ordinary query, not on a pathological
// one: `discover_tsquery` ORs its terms.
func TestARetrieverNeverReturnsMoreThanItsCap(t *testing.T) {
	depth := searchConfig().MaxCandidatesPerMode
	resources := kharifLots(depth + 4)

	pool := dbtest.NewPostgres(t)
	writer := postgres.NewCatalogRepository(pool, resolution)
	if _, err := writer.UpsertCatalog(context.Background(), domain.CatalogPatch{
		ID: "cat-cap", NetworkID: "bap.example.com", Active: true,
		VisibleTo: []string{"bap.example.com"}, Resources: resources,
	}, domain.UpdateModeFull, deriveSearchable); err != nil {
		t.Fatalf("publish: %v", err)
	}

	retriever := postgres.NewLexicalRetriever(pool, depth)
	ids, err := retriever.Retrieve(context.Background(),
		domain.SearchQuery{Text: "kharif"}, domain.Scope{Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(ids) != depth {
		t.Errorf("the retriever returned %d ids against a corpus of %d and a cap of %d",
			len(ids), depth+4, depth)
	}
}

// ---------------------------------------------------------------------------
// degradation
// ---------------------------------------------------------------------------

// Three modes returning is a better answer than none — but only if the caller
// is TOLD. Silence here is the failure this whole degrade-and-report design
// exists to avoid.
func TestAModeThisBackendCannotRunIsDegradedAndDoesNotFailTheSearch(t *testing.T) {
	repository, _ := publish(t, nil, readFixture{
		catalog:   "cat-degraded",
		resources: []domain.ResourcePatch{searchable("r-1", "kharif seed", "", "https://beckn.org/Agri", "SeedLot")},
	})

	result, err := repository.Search(context.Background(),
		domain.SearchQuery{Text: "kharif", Limit: searchConfig().MaxPageSize},
		[]domain.Capability{domain.CapabilityLexical, domain.CapabilitySemantic})
	if err != nil {
		t.Fatalf("a missing mode failed the whole search: %v", err)
	}
	if !slices.Equal(result.Degraded, []string{string(domain.CapabilitySemantic)}) {
		t.Errorf("Degraded is %v, want exactly [semantic]", result.Degraded)
	}
	if got := matchedIDs(result); !slices.Equal(got, []string{"r-1"}) {
		t.Errorf("the surviving mode returned %v, want the one match", got)
	}
}

// The semantic mode is declared only when a query-side embedder exists, because
// EMBEDDING_PROVIDER=noop embeds nothing (A5): a repository that declared it
// anyway would run a query that can only return zero rows and report nothing
// wrong.
func TestSemanticIsACapabilityOnlyWhenAnEmbedderIsConfigured(t *testing.T) {
	pool := dbtest.NewPostgres(t)

	without := postgres.NewSearchRepository(pool, searchConfig(), nil)
	if without.Capabilities().Has(domain.CapabilitySemantic) {
		t.Error("a repository with no embedder declared the semantic capability")
	}

	with := postgres.NewSearchRepository(pool, searchConfig(), embeddings.NewHashing(768))
	if !with.Capabilities().Has(domain.CapabilitySemantic) {
		t.Error("a repository holding an embedder did not declare the semantic capability")
	}
}

// ---------------------------------------------------------------------------
// geometry
// ---------------------------------------------------------------------------

// pointAt builds the geometry a walker would have produced for one location.
func pointAt(targetPath, sourcePath string, lat, lon float64, owners ...string) domain.Geometry {
	return domain.Geometry{
		TargetPath: targetPath,
		SourcePath: sourcePath,
		Owners:     owners,
		Type:       "Point",
		GeoJSON:    json.RawMessage(fmt.Sprintf(`{"type":"Point","coordinates":[%g,%g]}`, lon, lat)),
	}
}

// deriveShapes places geometries the way the walk does: a shape on the list of
// every resource it names, and a shape with no owners on the catalog.
func deriveShapes(shapes ...domain.Geometry) domain.DeriveFunc {
	return func(merged *domain.Catalog, touched []string) []domain.Fault {
		if faults := deriveSearchable(merged, touched); faults != nil {
			return faults
		}
		for _, shape := range shapes {
			if len(shape.Owners) == 0 {
				merged.Geometries = append(merged.Geometries, shape)
				continue
			}
			for index := range merged.Resources {
				if slices.Contains(shape.Owners, merged.Resources[index].ID) {
					merged.Resources[index].Geometries =
						append(merged.Resources[index].Geometries, shape)
				}
			}
		}
		return nil
	}
}

// within builds the S_DWITHIN filter a mapper would have produced.
func within(t *testing.T, lat, lon, metres float64) *domain.SpatialFilter {
	t.Helper()

	shape := pointAt("", "", lat, lon)
	full, cover, err := geo.CoverQuery(shape, domain.OpDWithin, metres, resolution)
	if err != nil {
		t.Fatalf("cover the query geometry: %v", err)
	}
	bounds, err := geo.BoundsFor(shape, domain.OpDWithin, metres)
	if err != nil {
		t.Fatalf("bound the query geometry: %v", err)
	}
	return &domain.SpatialFilter{
		Op: domain.OpDWithin, CellsFull: full, CellsCover: cover, Bounds: bounds,
		Center: &domain.GeoPoint{Lat: lat, Lon: lon}, RadiusM: metres,
		Quantifier: domain.QuantifierAny,
	}
}

// Bengaluru and Chennai: far enough apart that no radius under 200 km confuses
// them, and both well inside one H3 cell's worth of rounding at resolution 8.
const (
	bengaluruLat, bengaluruLon = 12.9716, 77.5946
	chennaiLat, chennaiLon     = 13.0827, 80.2707
)

// `targets` selects between two shapes on ONE resource, so the pin is that the
// stored target_path is byte-identical to the filter's: `= ANY($1)` is plain
// equality, and a dot-form row against a bracket-form filter is a 200 with an
// empty list and nothing anywhere to explain it.
func TestTargetsSelectsBetweenTwoGeometriesOnOneResource(t *testing.T) {
	const (
		warehouse = "$.catalogs[*].resources[*].warehouse.geo"
		field     = "$.catalogs[*].resources[*].field.geo"
	)

	repository, _ := publish(t, nil, readFixture{
		catalog:   "cat-targets",
		resources: []domain.ResourcePatch{searchable("r-1", "kharif seed", "", "https://beckn.org/Agri", "SeedLot")},
		derive: deriveShapes(
			pointAt(warehouse, "$.catalogs[0].resources[0].warehouse.geo", bengaluruLat, bengaluruLon, "r-1"),
			pointAt(field, "$.catalogs[0].resources[0].field.geo", chennaiLat, chennaiLon, "r-1"),
		),
	})

	nearBengaluru := within(t, bengaluruLat, bengaluruLon, 10000)

	t.Run("targeting the warehouse finds it", func(t *testing.T) {
		got := matchedIDs(search(t, repository, domain.SearchQuery{
			Text: "kharif", Spatial: nearBengaluru, TargetPaths: []string{warehouse},
		}))
		if !slices.Equal(got, []string{"r-1"}) {
			t.Errorf("got %v, want the resource whose warehouse is here", got)
		}
	})

	t.Run("targeting the field does not", func(t *testing.T) {
		got := matchedIDs(search(t, repository, domain.SearchQuery{
			Text: "kharif", Spatial: nearBengaluru, TargetPaths: []string{field},
		}))
		if len(got) != 0 {
			t.Errorf("got %v; the field is 300 km away and only the warehouse is here", got)
		}
	})

	t.Run("no targets searches every shape the resource carries", func(t *testing.T) {
		got := matchedIDs(search(t, repository, domain.SearchQuery{
			Text: "kharif", Spatial: nearBengaluru,
		}))
		if !slices.Equal(got, []string{"r-1"}) {
			t.Errorf("got %v, want the resource found through its warehouse", got)
		}
	})
}

// A catalog-level shape — NULL resource_id — is the provider's own location and
// belongs to every resource under it. A resource-level one belongs to its own
// resource and to nothing else.
func TestACatalogLevelGeometryMatchesEveryResourceAndAResourceLevelOneOnlyItsOwn(t *testing.T) {
	const providerPath = "$.catalogs[*].provider.availableAt[*].geo"

	repository, _ := publish(t, nil,
		readFixture{
			catalog: "cat-provider",
			resources: []domain.ResourcePatch{
				searchable("p-1", "kharif seed", "", "https://beckn.org/Agri", "SeedLot"),
				searchable("p-2", "kharif grain", "", "https://beckn.org/Agri", "SeedLot"),
			},
			// No owners: the provider's location, stored once for the catalog.
			derive: deriveShapes(pointAt(providerPath,
				"$.catalogs[*].provider.availableAt[0].geo", bengaluruLat, bengaluruLon)),
		},
		readFixture{
			catalog: "cat-resource",
			resources: []domain.ResourcePatch{
				searchable("q-1", "kharif pulse", "", "https://beckn.org/Agri", "SeedLot"),
				searchable("q-2", "kharif millet", "", "https://beckn.org/Agri", "SeedLot"),
			},
			derive: deriveShapes(pointAt("$.catalogs[*].resources[*].geo",
				"$.catalogs[0].resources[0].geo", bengaluruLat, bengaluruLon, "q-1")),
		},
	)

	got := matchedIDs(search(t, repository, domain.SearchQuery{
		Text: "kharif", Spatial: within(t, bengaluruLat, bengaluruLon, 10000),
	}))
	slices.Sort(got)

	if !slices.Equal(got, []string{"p-1", "p-2", "q-1"}) {
		t.Errorf("got %v, want both resources of the catalog whose PROVIDER is here "+
			"plus the one resource that carries its own shape", got)
	}
}

// ---------------------------------------------------------------------------
// offers
// ---------------------------------------------------------------------------

func offerPatch(id string, resourceIDs []string, validity *domain.TimePeriodPatch) domain.OfferPatch {
	return domain.OfferPatch{
		ID:          id,
		Document:    json.RawMessage(fmt.Sprintf(`{"id":%q}`, id)),
		ResourceIDs: resourceIDs,
		Validity:    validity,
	}
}

func offerIDs(catalog domain.Catalog) []string {
	ids := make([]string, 0, len(catalog.Offers))
	for _, offer := range catalog.Offers {
		ids = append(ids, offer.ID)
	}
	slices.Sort(ids)
	return ids
}

// A caller who searched for wheat gets the offers on the wheat plus any
// catalog-wide offer, and not the other offers in that catalog. Offer validity
// is checked here and nowhere else: a live catalog routinely carries last
// month's offer.
func TestHydrationReturnsOnlyTheOffersTouchingThePagePlusTheCatalogWideOnes(t *testing.T) {
	lastMonth := mustInstant(t, "2020-01-01T00:00:00Z")
	repository, _ := publish(t, nil, readFixture{
		catalog: "cat-offers",
		resources: []domain.ResourcePatch{
			searchable("wheat", "kharif wheat", "", "https://beckn.org/Agri", "SeedLot"),
			searchable("barley", "rabi barley", "", "https://beckn.org/Agri", "SeedLot"),
		},
		offers: []domain.OfferPatch{
			offerPatch("on-wheat", []string{"wheat"}, nil),
			offerPatch("on-barley", []string{"barley"}, nil),
			// An empty resource_ids is CATALOG-WIDE and therefore always
			// applies. It is never "no resources yet".
			offerPatch("catalog-wide", []string{}, nil),
			offerPatch("expired", []string{"wheat"}, &domain.TimePeriodPatch{
				EndDate: domain.Nullable[time.Time]{Set: true, Value: lastMonth},
			}),
		},
	})

	result := search(t, repository, domain.SearchQuery{Text: "kharif"})
	if len(result.Catalogs) != 1 {
		t.Fatalf("the page holds %d catalogs, want 1", len(result.Catalogs))
	}
	if got := matchedIDs(result); !slices.Equal(got, []string{"wheat"}) {
		t.Fatalf("the page holds %v, want only the wheat", got)
	}

	if got := offerIDs(result.Catalogs[0]); !slices.Equal(got, []string{"catalog-wide", "on-wheat"}) {
		t.Errorf("the page carries offers %v; want the wheat's own and the catalog-wide one — "+
			"not the barley's, and not the expired one", got)
	}
}

func mustInstant(t *testing.T, literal string) time.Time {
	t.Helper()

	instant, err := time.Parse(time.RFC3339, literal)
	if err != nil {
		t.Fatalf("fixture carries an unparseable time %q: %v", literal, err)
	}
	return instant
}
