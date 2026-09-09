package postgres

import (
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres/gen"
)

// This file is the one place a row becomes a domain object and a domain object
// becomes parameters, shared by the read and write sides. Four types carry a
// NULL-versus-zero decision — TIMESTAMPTZ, TIME, JSONB and VECTOR — and a second
// copy of any of them is a second chance to get it wrong.

// ---------------------------------------------------------------------------
// scalars
// ---------------------------------------------------------------------------

// timestamp maps a domain instant onto a nullable column. The ZERO time is
// NULL, not the year 1: both spell "unbounded on that axis".
func timestamp(at time.Time) pgtype.Timestamptz {
	if at.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: at, Valid: true}
}

// instant is timestamp's inverse: a NULL column reads back as the zero time.
func instant(column pgtype.Timestamptz) time.Time {
	if !column.Valid {
		return time.Time{}
	}
	return column.Time
}

const (
	microsPerSecond = int64(time.Second / time.Microsecond)
	secondsPerHour  = 3600
	secondsPerMin   = 60
)

// clock maps the daily-window bound onto TIME. A pointer, because nil is "no
// window" and 00:00:00 is a real bound.
func clock(at *domain.TimeOfDay) pgtype.Time {
	if at == nil {
		return pgtype.Time{}
	}
	seconds := int64(at.Hour)*secondsPerHour + int64(at.Minute)*secondsPerMin + int64(at.Second)
	return pgtype.Time{Microseconds: seconds * microsPerSecond, Valid: true}
}

// timeOfDay is clock's inverse. Sub-second precision is discarded: TimeOfDay
// has no field for it.
func timeOfDay(column pgtype.Time) *domain.TimeOfDay {
	if !column.Valid {
		return nil
	}
	seconds := column.Microseconds / microsPerSecond
	return &domain.TimeOfDay{
		Hour:   int(seconds / secondsPerHour),
		Minute: int(seconds / secondsPerMin % secondsPerMin),
		Second: int(seconds % secondsPerMin),
	}
}

// document maps a verbatim JSON column. Every JSONB column here is NOT NULL, so
// a nil RawMessage becomes the empty object rather than SQL NULL.
func document(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte(`{}`)
	}
	return raw
}

// list maps a TEXT[] parameter. pgx sends a nil slice as NULL, and every array
// column in this schema is NOT NULL.
func list(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// embedding maps a vector, and NULL is the ordinary case in Phase 1 (A5). Nil
// rather than a zero vector: `embedding IS NULL` is the backfill queue.
func embedding(values []float32) *pgvector.Vector {
	if len(values) == 0 {
		return nil
	}
	vector := pgvector.NewVector(values)
	return &vector
}

func floats(column *pgvector.Vector) []float32 {
	if column == nil {
		return nil
	}
	return column.Slice()
}

// owner maps a geometry's resource id, where NULL means CATALOG-LEVEL. The
// empty string is not a substitute: uq_resource_geometries coalesces a NULL
// resource_id to the empty string, so the two would upsert over each other, and
// the schema's CHECK refuses an empty resource_id for that reason.
func owner(resourceID string) pgtype.Text {
	if resourceID == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: resourceID, Valid: true}
}

// cells narrows H3 indexes to the BIGINT[] the column holds. Lossless: an H3
// index reserves its high bit, so every cell id is below 2^63.
func cells(indexes []uint64) []int64 {
	if indexes == nil {
		return nil
	}
	narrowed := make([]int64, len(indexes))
	for position, index := range indexes {
		narrowed[position] = int64(index)
	}
	return narrowed
}

// ---------------------------------------------------------------------------
// rows to domain
// ---------------------------------------------------------------------------

// storedCatalog rebuilds the catalog the patch will merge against.
//
// NetworkID is deliberately not set: the column does not exist, because nothing
// reads it back. MergeCatalog takes it from the patch. The parameter is the
// plain-SELECT row and not the field-for-field identical lock-and-load one so
// that the read path — see GetCatalog — spells no conversion at all.
func storedCatalog(row gen.GetCatalogRowRow) domain.Catalog {
	return domain.Catalog{
		ID:              row.ID,
		Document:        json.RawMessage(row.Document),
		VisibleTo:       row.VisibleTo,
		Active:          row.Active,
		ValidFrom:       instant(row.ValidFrom),
		ValidTo:         instant(row.ValidTo),
		ValidTimeFrom:   timeOfDay(row.ValidTimeFrom),
		ValidTimeTo:     timeOfDay(row.ValidTimeTo),
		ProtocolVersion: row.ProtocolVersion,
	}
}

func storedResource(row gen.ListStoredResourcesRow) domain.Resource {
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

func storedOffer(row gen.ListStoredOffersRow) domain.Offer {
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

// geometriesFrom regroups geometry ROWS back into geometry VALUES: one shape
// owned by three resources is three rows and one domain.Geometry with three
// Owners. Catalog-level rows — resource_id NULL — fold into their own value with
// no owners, which is how the caller tells the two apart.
func geometriesFrom(rows []gen.ListStoredGeometriesRow) (catalogLevel []domain.Geometry, byResource map[string][]domain.Geometry) {
	byResource = make(map[string][]domain.Geometry)

	// Keyed on source_path alone: it is unique per (catalog, resource) by
	// uq_resource_geometries, and a shape shared by several resources carries
	// the same source_path on each row — the grouping this fold needs.
	order := make([]string, 0, len(rows))
	grouped := make(map[string]*domain.Geometry, len(rows))

	for _, row := range rows {
		shape, seen := grouped[row.SourcePath]
		if !seen {
			shape = &domain.Geometry{
				TargetPath: row.TargetPath,
				SourcePath: row.SourcePath,
				Type:       geometryType(row.Geojson),
				GeoJSON:    json.RawMessage(row.Geojson),
			}
			grouped[row.SourcePath] = shape
			order = append(order, row.SourcePath)
		}
		if row.ResourceID.Valid {
			shape.Owners = append(shape.Owners, row.ResourceID.String)
		}
	}

	for _, sourcePath := range order {
		shape := *grouped[sourcePath]
		if len(shape.Owners) == 0 {
			catalogLevel = append(catalogLevel, shape)
			continue
		}
		for _, ownerID := range shape.Owners {
			byResource[ownerID] = append(byResource[ownerID], shape)
		}
	}
	return catalogLevel, byResource
}

// geometryType reads the GeoJSON `type` back out of the document. There is no
// geom_type column to read it from.
func geometryType(raw []byte) string {
	var shape struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		return ""
	}
	return shape.Type
}

// ---------------------------------------------------------------------------
// domain to parameters
// ---------------------------------------------------------------------------

func catalogRowParams(catalog domain.Catalog) gen.UpdateCatalogRowParams {
	return gen.UpdateCatalogRowParams{
		ID:              catalog.ID,
		Document:        document(catalog.Document),
		VisibleTo:       list(catalog.VisibleTo),
		Active:          catalog.Active,
		ValidFrom:       timestamp(catalog.ValidFrom),
		ValidTo:         timestamp(catalog.ValidTo),
		ValidTimeFrom:   clock(catalog.ValidTimeFrom),
		ValidTimeTo:     clock(catalog.ValidTimeTo),
		ProtocolVersion: catalog.ProtocolVersion,
	}
}

func resourceParams(catalogID string, resource domain.Resource) gen.UpsertResourceParams {
	return gen.UpsertResourceParams{
		CatalogID:           catalogID,
		ID:                  resource.ID,
		VisibleTo:           list(resource.VisibleTo),
		Active:              resource.Active,
		ValidFrom:           timestamp(resource.ValidFrom),
		ValidTo:             timestamp(resource.ValidTo),
		ValidTimeFrom:       clock(resource.ValidTimeFrom),
		ValidTimeTo:         clock(resource.ValidTimeTo),
		Name:                resource.Name,
		Document:            document(resource.Document),
		SchemaContext:       resource.SchemaContext,
		SchemaType:          resource.SchemaType,
		SearchText:          resource.SearchText,
		Embedding:           embedding(resource.Embedding),
		EmbeddingSourceHash: resource.EmbeddingSourceHash,
	}
}

func offerParams(catalogID string, offer domain.Offer) gen.UpsertOfferParams {
	return gen.UpsertOfferParams{
		CatalogID:     catalogID,
		ID:            offer.ID,
		ResourceIds:   list(offer.ResourceIDs),
		Document:      document(offer.Document),
		ValidFrom:     timestamp(offer.ValidFrom),
		ValidTo:       timestamp(offer.ValidTo),
		ValidTimeFrom: clock(offer.ValidTimeFrom),
		ValidTimeTo:   clock(offer.ValidTimeTo),
	}
}

// geometryParams turns one already-covered shape into the ONE row that stores
// it for one owner.
//
// ownerID is the resource the shape was found ON, and "" is the catalog itself.
// It must NOT be read back off Geometry.Owners: the walk has already spent
// Owners deciding placement, so fanning out over them again would turn a shape
// already sitting on N lists into N x N rows and collide with
// uq_resource_geometries on the first publish.
func geometryParams(
	catalogID, ownerID string, shape domain.Geometry, cover geo.Cover,
) gen.InsertGeometryParams {
	return gen.InsertGeometryParams{
		CatalogID:  catalogID,
		ResourceID: owner(ownerID),
		TargetPath: shape.TargetPath,
		SourcePath: shape.SourcePath,
		Geojson:    document(shape.GeoJSON),
		CellsFull:  cells(cover.CellsFull),
		CellsCover: cells(cover.CellsCover),
		MinLat:     cover.Bounds.MinLat,
		MaxLat:     cover.Bounds.MaxLat,
		MinLon:     cover.Bounds.MinLon,
		MaxLon:     cover.Bounds.MaxLon,
	}
}
