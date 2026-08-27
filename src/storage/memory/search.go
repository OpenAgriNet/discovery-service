package memory

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
)

// Capabilities declares what this backend answers, and nothing it cannot.
//
// Lexical is a token match over the same 'simple' configuration Postgres
// indexes with — no stemming on either side — so the two agree on ordinary
// words. Spatial is the real cell algebra, shared with the Postgres side
// through geo.MatchesOp and the conformance table.
//
// Fuzzy and semantic are absent because trigram similarity and a vector index
// are not things a map can approximate: a backend that declared `fuzzy` and
// answered it with a substring match would return a different page from the one
// Postgres returns and no fixture could say which was right. Declaring them
// missing is what puts them in Degraded, which is the honest answer. JSONPath is
// absent on both backends until Task 22.
func (r *Repository) Capabilities() domain.Capabilities {
	return domain.Capabilities{
		domain.CapabilityLexical: true,
		domain.CapabilitySpatial: true,
	}
}

// Search answers a query over the map, in the same order and with the same
// arithmetic the Postgres adapter uses.
//
// The instant is captured ONCE, here, for the same reason Postgres captures one
// (A6): every stage of one search must agree on "now", or a catalog can be live
// for the gate and expired for the offer join in the same response.
func (r *Repository) Search(
	_ context.Context, query domain.SearchQuery, modes []domain.Capability,
) (domain.SearchResult, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	scope := domain.Scope{NetworkID: query.NetworkID, Now: time.Now().UTC()}

	ranked, filtering, degraded := r.negotiate(modes)
	matched := r.candidates(query, scope)

	// Every ranked mode this backend has returns the same list — there is one —
	// so fusing is the identity and is spelled as such rather than as an RRF
	// over a single input, which would only look like it was doing something.
	page := matched

	// Nothing was asked for: no ranked mode ran and no filter was named either.
	// The Postgres side answers that with an empty fusion and a total of zero,
	// and this must agree — a backend that returned the whole corpus for a
	// request naming nothing would be answering a query nobody made.
	//
	// `filtering` is what keeps a geo-only intent out of this branch. A spatial
	// constraint names no ranked mode and is still a query: the predicate IS
	// the query, candidates has already applied it, and emptying the page here
	// would answer "what is near me" with nothing at all.
	if len(ranked) == 0 && !filtering {
		page = nil
		if len(degraded) == 0 {
			matched = nil
		}
	}

	return domain.SearchResult{
		Catalogs: r.hydrate(pageOf(page, query.Offset, query.Limit), scope),

		// The count is over what the predicate admits and not over the page,
		// and it does NOT shrink when a mode degrades: the pool did not get
		// smaller because this backend cannot run a trigram index. There is no
		// per-mode cap here to make it larger than the fused list either, which
		// is why it is never a second query the way it is on the Postgres side.
		Total:    len(matched),
		Degraded: degraded,
	}, nil
}

// negotiate splits the requested modes into the ranked ones this backend will
// run and the ones it has to report as missing, and says whether a filter was
// asked for at all.
//
// A filter mode (domain.Capability.Ranked) is neither ranked nor missing: it is
// carried by the predicate every retrieval already applies, so asking for it is
// satisfied by the search itself — reporting it degraded would tell a caller
// their geometry was ignored when it was applied.
//
// `filtering` is reported for what was REQUESTED rather than for what this
// backend can do, which is what makes jsonpath behave: a backend that declines
// jsonpath still ran the rest of the query, so the caller gets the page it
// narrowed by everything else, plus the degradation.
func (r *Repository) negotiate(
	modes []domain.Capability,
) (ranked []domain.Capability, filtering bool, degraded []string) {
	declared := r.Capabilities()
	for _, mode := range modes {
		switch {
		case !mode.Ranked():
			filtering = true
			if !declared.Has(mode) {
				degraded = append(degraded, string(mode))
			}
		case !declared.Has(mode):
			degraded = append(degraded, string(mode))
		default:
			ranked = append(ranked, mode)
		}
	}
	return ranked, filtering, degraded
}

// candidates is every stored resource the query admits, in the stable order
// both backends fall back to.
//
// (catalog_id, id) and not insertion order: Postgres's retrievers end their
// ORDER BY on exactly that pair, so a query with no relevance to rank by — a
// geo-only intent, or one whose matches all score the same — produces the same
// page here as it does there. Insertion order would agree with it only by
// accident.
func (r *Repository) candidates(query domain.SearchQuery, scope domain.Scope) []domain.Resource {
	matched := make([]domain.Resource, 0)

	for _, catalog := range r.catalogs {
		for _, resource := range catalog.Resources {
			if !admitted(resource, scope) ||
				!matchesSchema(resource, query.Schemas) ||
				!matchesText(resource, query.Text) ||
				!r.matchesGeometry(catalog, resource, query) {
				continue
			}
			matched = append(matched, resource)
		}
	}

	slices.SortFunc(matched, func(left, right domain.Resource) int {
		if byCatalog := strings.Compare(left.CatalogID, right.CatalogID); byCatalog != 0 {
			return byCatalog
		}
		return strings.Compare(left.ID, right.ID)
	})
	return matched
}

// admitted is the scope gate, read off the resource's own denormalised copy.
//
// Off the RESOURCE and not off its catalog, deliberately: the write path copies
// the gate onto every resource unconditionally, and reading it from anywhere
// else here would let this backend answer correctly while the copy Postgres
// reads was wrong.
//
// An empty scope network is UNSCOPED and emits no predicate at all — never a
// literal that matches nothing, and never a fallback to this service's own
// network id.
func admitted(resource domain.Resource, scope domain.Scope) bool {
	if scope.NetworkID != "" && !slices.Contains(resource.VisibleTo, scope.NetworkID) {
		return false
	}
	return resource.Active && live(
		resource.ValidFrom, resource.ValidTo,
		resource.ValidTimeFrom, resource.ValidTimeTo, scope.Now)
}

// live is the validity half of the gate: the calendar range ANDed with the
// daily window.
//
// The ZERO time is unbounded, because that is what the Postgres mapping stores
// as NULL and what the SQL reads as `valid_from IS NULL OR valid_from <= now()`.
// Reading it as the year 1 would agree with SQL on the lower bound and get the
// upper one exactly backwards.
//
// The daily window is domain.WithinDailyWindow and is not open-coded here. It
// is the one branch of this that a BETWEEN gets silently wrong — a window that
// wraps midnight — and a second copy of it is a second place to omit the wrap.
func live(from, to time.Time, timeFrom, timeTo *domain.TimeOfDay, now time.Time) bool {
	if !from.IsZero() && from.After(now) {
		return false
	}
	if !to.IsZero() && to.Before(now) {
		return false
	}
	return domain.WithinDailyWindow(timeFrom, timeTo, timeOfDay(now))
}

// timeOfDay is the Go half of `(now() AT TIME ZONE 'UTC')::time`: the wall
// clock with the date thrown away.
func timeOfDay(instant time.Time) *domain.TimeOfDay {
	utc := instant.UTC()
	return &domain.TimeOfDay{Hour: utc.Hour(), Minute: utc.Minute(), Second: utc.Second()}
}

// matchesSchema compares context and type as a PAIR.
//
// A request for [schema.org#GroceryItem, mobility#RideService] must not match a
// resource that is schema.org + RideService, which is what two independent
// membership tests would return. An empty filter list emits no predicate rather
// than one matching nothing, and an entry with no type is "any type under this
// context".
func matchesSchema(resource domain.Resource, filters []domain.SchemaFilter) bool {
	if len(filters) == 0 {
		return true
	}
	for _, filter := range filters {
		if filter.Context == resource.SchemaContext &&
			(filter.Type == "" || filter.Type == resource.SchemaType) {
			return true
		}
	}
	return false
}

// matchesText is the lexical predicate: any query token equal to any indexed
// token.
//
// ANY and not ALL, matching `discover_tsquery`, which rewrites
// websearch_to_tsquery's `&` into `|` — "wheat seeds for sale" must not match
// nothing because no listing carries all four words. Recall is the retriever's
// job.
//
// Token equality is exact because Postgres indexes and queries with the
// 'simple' configuration, which does not stem either side. It is the whole
// reason this comparison can be four lines: under 'english' this would have to
// reproduce a stemmer, and it would agree with it right up until a fixture used
// a word whose stem was interesting.
//
// Empty text is NO predicate rather than a predicate matching nothing — a
// geo-only intent carries no text and must not come back empty.
func matchesText(resource domain.Resource, text string) bool {
	wanted := tokens(text)
	if len(wanted) == 0 {
		return true
	}
	indexed := tokens(resource.SearchText)
	for _, token := range wanted {
		if slices.Contains(indexed, token) {
			return true
		}
	}
	return false
}

// tokens lower-cases and splits on everything that is not a letter or a digit,
// which is what the 'simple' configuration does to a word it is not asked to
// stem.
func tokens(text string) []string {
	split := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return split
}

// matchesGeometry applies the spatial constraint across the shapes a resource
// can be found by, under the query's quantifier.
//
// The shapes are the resource's OWN plus its catalog's, because a catalog-level
// geometry — the provider's location — belongs to every resource under it. That
// is the same set the SQL's `g.resource_id IS NULL OR g.resource_id = r.id`
// selects.
func (r *Repository) matchesGeometry(
	catalog domain.Catalog, resource domain.Resource, query domain.SearchQuery,
) bool {
	if query.Spatial == nil {
		return true
	}

	shapes := make([]domain.Geometry, 0, len(catalog.Geometries)+len(resource.Geometries))
	for _, shape := range append(slices.Clone(catalog.Geometries), resource.Geometries...) {
		// `targets` is plain equality against the canonicalised stored path, so
		// an empty list is every shape and a non-empty one is exactly those.
		if len(query.TargetPaths) == 0 || slices.Contains(query.TargetPaths, shape.TargetPath) {
			shapes = append(shapes, shape)
		}
	}

	switch query.Spatial.Quantifier {
	case domain.QuantifierNone:
		return !slices.ContainsFunc(shapes, r.shapeMatches(*query.Spatial))
	case domain.QuantifierAll:
		// NOT EXISTS(NOT matches), which is vacuously TRUE for a resource with
		// no shapes at all — the same answer the SQL's EXISTS gives, and the
		// reason ALL is spelled this way rather than as "every shape matches"
		// over a loop that would have to decide the empty case for itself.
		return !slices.ContainsFunc(shapes, func(shape domain.Geometry) bool {
			return !r.shapeMatches(*query.Spatial)(shape)
		})
	default:
		return slices.ContainsFunc(shapes, r.shapeMatches(*query.Spatial))
	}
}

// shapeMatches is the per-shape predicate, one shape at a time.
//
// One at a time because that is how the SQL evaluates it: the box, the cells
// and the distance refinement are all inside the same EXISTS over one geometry
// row. Handing the whole set to the refinement instead would let a resource's
// nearby Polygon rescue its distant Point, which no `EXISTS` over rows can do.
func (r *Repository) shapeMatches(filter domain.SpatialFilter) func(domain.Geometry) bool {
	return func(shape domain.Geometry) bool {
		cover, err := geo.CoverGeometry(shape, r.resolution)
		if err != nil {
			// A shape that will not cover cannot be matched by cells. It is not
			// an error the caller can act on — the geometry was accepted at
			// publish time and this is a read — so it drops out of the spatial
			// answer the same way a NULL cover does in SQL.
			return false
		}
		return matchesSpatial(cover, []domain.Geometry{shape}, filter)
	}
}

// pageOf slices the ranked list, clamping rather than panicking.
func pageOf(ranked []domain.Resource, offset, limit int) []domain.Resource {
	if offset >= len(ranked) {
		return nil
	}
	end := offset + limit
	if end > len(ranked) {
		end = len(ranked)
	}
	return ranked[offset:end]
}

// hydrate folds the page back into catalogs, in the PAGE's order.
//
// The order is the only ranking a caller ever sees, so this walks the page and
// not the map. A map iteration here would return the right resources in an
// order that changed between two runs of the same query.
func (r *Repository) hydrate(page []domain.Resource, scope domain.Scope) []domain.Catalog {
	assembled := make([]domain.Catalog, 0, len(page))
	position := make(map[string]int, len(page))
	onPage := make(map[string][]string, len(page))

	for _, resource := range page {
		index, seen := position[resource.CatalogID]
		if !seen {
			stored := r.catalogs[resource.CatalogID]
			index = len(assembled)
			position[resource.CatalogID] = index
			assembled = append(assembled, domain.Catalog{
				ID:            stored.ID,
				NetworkID:     stored.NetworkID,
				Document:      stored.Document,
				VisibleTo:     slices.Clone(stored.VisibleTo),
				Active:        stored.Active,
				ValidFrom:     stored.ValidFrom,
				ValidTo:       stored.ValidTo,
				ValidTimeFrom: stored.ValidTimeFrom,
				ValidTimeTo:   stored.ValidTimeTo,
				Geometries:    slices.Clone(stored.Geometries),
			})
		}
		assembled[index].Resources = append(assembled[index].Resources, resource)
		onPage[resource.CatalogID] = append(onPage[resource.CatalogID], resource.ID)
	}

	for catalogID, index := range position {
		assembled[index].Offers = r.offersFor(catalogID, onPage[catalogID], scope)
	}
	return assembled
}

// offersFor is the offers that touch this catalog's share of the page, and only
// those.
//
// An EMPTY ResourceIDs is CATALOG-WIDE and always applies; it is never "no
// resources yet". Offer validity is checked here and nowhere else, because a
// live catalog routinely carries last month's offer and the catalog's own gate
// says nothing about it.
func (r *Repository) offersFor(catalogID string, resourceIDs []string, scope domain.Scope) []domain.Offer {
	stored := r.catalogs[catalogID]

	kept := make([]domain.Offer, 0, len(stored.Offers))
	for _, offer := range stored.Offers {
		touches := len(offer.ResourceIDs) == 0 ||
			slices.ContainsFunc(offer.ResourceIDs, func(id string) bool {
				return slices.Contains(resourceIDs, id)
			})
		if !touches {
			continue
		}
		if !live(offer.ValidFrom, offer.ValidTo,
			offer.ValidTimeFrom, offer.ValidTimeTo, scope.Now) {
			continue
		}
		kept = append(kept, offer)
	}
	return kept
}
