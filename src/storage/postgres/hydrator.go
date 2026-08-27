package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/embeddings"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres/gen"
)

// Hydrator turns a decided page of ids into the catalogs a response renders
// from, and answers the count.
//
// Every query it runs is keyed by the page — twenty resources and their
// catalogs, never the whole match — which is what lets the scope gate be
// re-applied here at no cost. Count is the exception and is deliberately
// unbounded; see below.
type Hydrator struct {
	queries *gen.Queries

	// embedder is the query-side vectoriser, held only so that Count's text
	// clause can name the same vector the semantic retriever searched with. Nil
	// when this deployment has no semantic mode.
	embedder embeddings.Embedder
}

var _ domain.Hydrator = (*Hydrator)(nil)

// NewHydrator builds the hydrator over a store.
func NewHydrator(store gen.DBTX, embedder embeddings.Embedder) *Hydrator {
	return &Hydrator{queries: gen.New(store), embedder: embedder}
}

// ScopeFilter narrows a set of ids to the ones the scope admits.
//
// It exists for a retriever whose index has no notion of validity or
// visibility — a vector index is one — and it applies the gate and nothing
// else. The caller has already applied text, geometry and schema; this answers
// only "may this caller see it now".
func (h *Hydrator) ScopeFilter(
	ctx context.Context, ids []string, scope domain.Scope,
) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	catalogIDs, resourceIDs := domain.SplitResourceKeys(ids)
	rows, err := h.queries.ScopeFilterResources(ctx, gen.ScopeFilterResourcesParams{
		CatalogIds:  catalogIDs,
		ResourceIds: resourceIDs,
		NetworkID:   nullableText(scope.NetworkID),
	})
	if err != nil {
		return nil, fmt.Errorf("apply the scope gate to a candidate set: %w", err)
	}

	// Rebuilt in the CALLER's order rather than in the row order, because the
	// ids arrived ranked and a filter that reordered them would silently
	// discard the fusion's work.
	admitted := make(map[string]bool, len(rows))
	for _, row := range rows {
		admitted[domain.ResourceKey(row.CatalogID, row.ID)] = true
	}

	kept := make([]string, 0, len(rows))
	for _, id := range ids {
		if admitted[id] {
			kept = append(kept, id)
		}
	}
	return kept, nil
}

// Hydrate loads the page: resources, one provider per catalog, the geometries
// and the offers that touch them.
//
// Four queries and not one join. A join would multiply the resource row by its
// geometries and its offers and send the provider document once per product,
// and the provider document is the largest thing on the page.
func (h *Hydrator) Hydrate(
	ctx context.Context, ids []string, scope domain.Scope,
) ([]domain.Catalog, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	catalogIDs, resourceIDs := domain.SplitResourceKeys(ids)

	resources, err := h.queries.HydrateResources(ctx, gen.HydrateResourcesParams{
		CatalogIds:  catalogIDs,
		ResourceIds: resourceIDs,
		NetworkID:   nullableText(scope.NetworkID),
	})
	if err != nil {
		return nil, fmt.Errorf("hydrate the page's resources: %w", err)
	}
	if len(resources) == 0 {
		return nil, nil
	}

	// Narrowed to what the gate ADMITTED, not to what was asked for. A resource
	// the retriever named and the gate then rejected must not pull its
	// catalog's provider, geometries or offers onto the page behind it.
	admitted := make([]string, 0, len(resources))
	for _, row := range resources {
		admitted = append(admitted, domain.ResourceKey(row.CatalogID, row.ID))
	}
	catalogIDs, resourceIDs = domain.SplitResourceKeys(admitted)
	onPage := slices.Compact(slices.Sorted(slices.Values(catalogIDs)))

	catalogs, err := h.queries.HydrateCatalogs(ctx, onPage)
	if err != nil {
		return nil, fmt.Errorf("hydrate the page's catalogs: %w", err)
	}

	geometries, err := h.queries.HydrateGeometries(ctx, gen.HydrateGeometriesParams{
		CatalogIds:  catalogIDs,
		ResourceIds: resourceIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("hydrate the page's geometries: %w", err)
	}

	offers, err := h.queries.HydrateOffers(ctx, gen.HydrateOffersParams{
		CatalogIds:  catalogIDs,
		ResourceIds: resourceIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("hydrate the page's offers: %w", err)
	}

	return assemble(ids, resources, catalogs, geometries, offers), nil
}

// Count is the size of the set the fusion drew from.
//
// It carries no LIMIT, deliberately: capping truncates the PAGE's candidate
// pool and must never truncate the count, or a capped mode would make Total
// wrong in the one state a caller has no way to detect. The text clause is the
// OR of every mode's, because the page is a union of every mode's.
//
// Every mode's text is bound whether or not that mode ran. The alternative —
// binding only the enabled modes — would make Total shrink when a mode
// degrades, which is the opposite of what a caller paginating through the
// results needs: the pool did not get smaller because a retriever died. The one
// exception is a mode whose provider is unreachable, below: a vector that
// cannot be produced cannot be bound.
func (h *Hydrator) Count(
	ctx context.Context, query domain.SearchQuery, _ domain.Scope,
) (int, error) {
	// A provider that is DOWN narrows the count rather than failing it. The
	// retriever embeds the same text with the same embedder, so an unreachable
	// provider has already failed there and the mode is already named in
	// Degraded — which is what explains the narrower Total to the caller. Taking
	// the whole response down here would throw away a page that was successfully
	// produced, to avoid reporting a number the caller has been told is partial.
	vector, err := queryVector(ctx, h.embedder, query.Text)
	if err != nil {
		vector = nil
	}

	shared := sharedPredicates(query)
	total, err := h.queries.CountCandidates(ctx, gen.CountCandidatesParams{
		NetworkID:      shared.networkID,
		SchemaContexts: shared.schemaContexts,
		SchemaTypes:    shared.schemaTypes,
		SpatialOp:      shared.spatialOp,
		GeoNegate:      shared.geoNegate,
		TargetPaths:    shared.targetPaths,
		MatchNegate:    shared.matchNegate,
		MinLat:         shared.minLat,
		MaxLat:         shared.maxLat,
		MinLon:         shared.minLon,
		MaxLon:         shared.maxLon,
		QCover:         shared.qCover,
		QFull:          shared.qFull,
		CenterLat:      shared.centerLat,
		CenterLon:      shared.centerLon,
		RadiusM:        shared.radiusM,
		LexicalText:    nullableText(query.Text),
		FuzzyText:      nullableText(query.Text),
		QueryVector:    vector,
	})
	if err != nil {
		return 0, fmt.Errorf("count the candidate set: %w", err)
	}
	return int(total), nil
}

// assemble folds four flat row sets back into catalogs, in the PAGE's order.
//
// The order is the fusion's, and it is the only ranking the caller ever sees. A
// map iteration or a sort by id here would silently discard it and return the
// right twenty resources in the wrong order, which no assertion about set
// membership catches.
func assemble(
	page []string,
	resources []gen.HydrateResourcesRow,
	catalogs []gen.HydrateCatalogsRow,
	geometries []gen.HydrateGeometriesRow,
	offers []gen.HydrateOffersRow,
) []domain.Catalog {
	byResource := make(map[string]domain.Resource, len(resources))
	for _, row := range resources {
		byResource[domain.ResourceKey(row.CatalogID, row.ID)] = hydratedResource(row)
	}

	catalogLevel, ownedGeometries := hydratedGeometries(geometries)

	assembled := make([]domain.Catalog, 0, len(catalogs))
	position := make(map[string]int, len(catalogs))

	for _, key := range page {
		resource, found := byResource[key]
		if !found {
			// Named by a retriever and refused by the gate on the way back in.
			// Skipped silently and on purpose: the gate is the authority, and a
			// resource it rejected is one this caller may not be told exists.
			continue
		}
		resource.Geometries = ownedGeometries[key]

		index, seen := position[resource.CatalogID]
		if !seen {
			position[resource.CatalogID] = len(assembled)
			index = len(assembled)
			assembled = append(assembled, domain.Catalog{
				ID:         resource.CatalogID,
				Geometries: catalogLevel[resource.CatalogID],
			})
		}
		assembled[index].Resources = append(assembled[index].Resources, resource)
	}

	attachCatalogs(assembled, position, catalogs)
	attachOffers(assembled, position, offers)
	return assembled
}

// attachCatalogs folds the catalog rows onto the catalogs the page produced.
//
// Keyed on `position` rather than appended, because a catalog row no resource
// on this page belongs to is one the caller must not see: the page decides
// which catalogs exist in the response, and this only fills them in.
//
// Named for the catalog rather than the provider since A17 — the row now
// carries the whole stored document, of which the provider is one member.
func attachCatalogs(
	assembled []domain.Catalog, position map[string]int, catalogs []gen.HydrateCatalogsRow,
) {
	for _, row := range catalogs {
		index, onPage := position[row.ID]
		if !onPage {
			continue
		}
		assembled[index].Document = json.RawMessage(row.Document)
		assembled[index].VisibleTo = row.VisibleTo
		assembled[index].Active = row.Active
		assembled[index].ValidFrom = instant(row.ValidFrom)
		assembled[index].ValidTo = instant(row.ValidTo)
		assembled[index].ValidTimeFrom = timeOfDay(row.ValidTimeFrom)
		assembled[index].ValidTimeTo = timeOfDay(row.ValidTimeTo)
	}
}

// attachOffers folds the offer rows on, in the order the query returned them
// and onto the catalogs the page produced.
func attachOffers(
	assembled []domain.Catalog, position map[string]int, offers []gen.HydrateOffersRow,
) {
	for _, row := range offers {
		index, onPage := position[row.CatalogID]
		if !onPage {
			continue
		}
		assembled[index].Offers = append(assembled[index].Offers, hydratedOffer(row))
	}
}

func hydratedResource(row gen.HydrateResourcesRow) domain.Resource {
	return domain.Resource{
		ID:                  row.ID,
		CatalogID:           row.CatalogID,
		Name:                row.Name,
		Document:            json.RawMessage(row.Document),
		SchemaContext:       row.SchemaContext,
		SchemaType:          row.SchemaType,
		Embedding:           floats(row.Embedding),
		EmbeddingSourceHash: row.EmbeddingSourceHash,
		VisibleTo:           row.VisibleTo,
		Active:              row.Active,
		ValidFrom:           instant(row.ValidFrom),
		ValidTo:             instant(row.ValidTo),
		ValidTimeFrom:       timeOfDay(row.ValidTimeFrom),
		ValidTimeTo:         timeOfDay(row.ValidTimeTo),
	}
}

func hydratedOffer(row gen.HydrateOffersRow) domain.Offer {
	return domain.Offer{
		ID:            row.ID,
		CatalogID:     row.CatalogID,
		ResourceIDs:   row.ResourceIds,
		Document:      json.RawMessage(row.Document),
		ValidFrom:     instant(row.ValidFrom),
		ValidTo:       instant(row.ValidTo),
		ValidTimeFrom: timeOfDay(row.ValidTimeFrom),
		ValidTimeTo:   timeOfDay(row.ValidTimeTo),
	}
}

// hydratedGeometries splits the geometry rows the way the domain holds them:
// catalog-level shapes per catalog, resource-level shapes per resource key.
//
// A catalog-level row — NULL resource_id — belongs to EVERY resource in its
// catalog and is stored once for the whole catalog, so it is returned keyed by
// catalog and attached there rather than copied onto each resource.
//
// Owners is deliberately not reconstructed. The walk SPENDS it deciding
// placement, and a shape read back onto the resource whose row it is on is the
// same shape the walk put there (A15); rebuilding a list of owners from a page
// that holds only some of them would produce a value that is wrong in a way
// nothing downstream could detect.
func hydratedGeometries(
	rows []gen.HydrateGeometriesRow,
) (catalogLevel map[string][]domain.Geometry, owned map[string][]domain.Geometry) {
	catalogLevel = make(map[string][]domain.Geometry)
	owned = make(map[string][]domain.Geometry)

	for _, row := range rows {
		shape := domain.Geometry{
			TargetPath: row.TargetPath,
			SourcePath: row.SourcePath,
			Type:       geometryType(row.Geojson),
			GeoJSON:    json.RawMessage(row.Geojson),
		}
		if !row.ResourceID.Valid {
			catalogLevel[row.CatalogID] = append(catalogLevel[row.CatalogID], shape)
			continue
		}
		key := domain.ResourceKey(row.CatalogID, row.ResourceID.String)
		shape.Owners = []string{row.ResourceID.String}
		owned[key] = append(owned[key], shape)
	}
	return catalogLevel, owned
}
