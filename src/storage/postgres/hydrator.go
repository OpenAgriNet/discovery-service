package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres/gen"
)

// Hydrator turns a decided page of ids into the catalogs a response renders
// from.
//
// Every query it runs is keyed by the page, never by the whole match, which is
// what lets the scope gate be re-applied here at no cost.
type Hydrator struct {
	queries *gen.Queries
}

var _ domain.Hydrator = (*Hydrator)(nil)

// NewHydrator builds the hydrator over a store.
func NewHydrator(store gen.DBTX) *Hydrator {
	return &Hydrator{queries: gen.New(store)}
}

// ScopeFilter narrows a set of ids to the ones the scope admits — the gate and
// nothing else, for a retriever whose index has no notion of validity or
// visibility.
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

	// Rebuilt in the CALLER's order, not the row order: the ids arrived ranked,
	// and reordering them would silently discard the fusion's work.
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
// Four queries and not one join: a join multiplies the resource row by its
// geometries and its offers, sending the provider document — the largest thing
// on the page — once per product.
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

	// Narrowed to what the gate ADMITTED, not what was asked for: a rejected
	// resource must not pull its catalog's provider, geometries or offers onto
	// the page behind it.
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

// assemble folds four flat row sets back into catalogs, in the PAGE's order —
// the fusion's, and the only ranking the caller ever sees. A map iteration or a
// sort by id here returns the right resources in the wrong order, which no
// assertion about set membership catches.
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
			// Skipped silently on purpose: a resource the gate rejected is one
			// this caller may not be told exists.
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
// Keyed on `position` rather than appended: the page decides which catalogs
// appear in the response, and this only fills them in.
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
// catalog-level shapes per catalog, resource-level shapes per resource key. A
// catalog-level row — NULL resource_id — is stored once and attached by
// catalog, not copied onto each resource.
//
// Owners is deliberately not reconstructed (A15): the page holds only some of
// them, so a rebuilt list would be wrong in a way nothing downstream detects.
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
