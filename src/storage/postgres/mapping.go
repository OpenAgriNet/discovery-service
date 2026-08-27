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

// This file is the ONE place a row becomes a domain object and a domain object
// becomes parameters, and it is shared with the read side.
//
// It exists as a file rather than as conversions written where they are needed
// because there are four types with a NULL-versus-zero decision each — a
// TIMESTAMPTZ, a TIME, a JSONB and a VECTOR — and a second copy of any of them
// is a second chance to read "no validity" as "valid from the zero year".

// ---------------------------------------------------------------------------
// scalars
// ---------------------------------------------------------------------------

// timestamp maps a domain instant onto a nullable column.
//
// The ZERO time is NULL, not the year 1. `domain.Catalog.ValidFrom` is a plain
// time.Time and its zero value means "unbounded on that axis" — the same thing
// the column's NULL means — so this is where the two vocabularies meet. Storing
// 0001-01-01 instead would satisfy every write test and would make every
// validity predicate compare against a date rather than short-circuit on NULL.
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

// clock maps the daily-window bound onto TIME.
//
// A pointer in the domain because nil is "no window" and 00:00:00 is a real
// bound — the distinction a plain TimeOfDay could not carry, and the reason
// this function exists rather than a cast.
func clock(at *domain.TimeOfDay) pgtype.Time {
	if at == nil {
		return pgtype.Time{}
	}
	seconds := int64(at.Hour)*secondsPerHour + int64(at.Minute)*secondsPerMin + int64(at.Second)
	return pgtype.Time{Microseconds: seconds * microsPerSecond, Valid: true}
}

// timeOfDay is clock's inverse. Sub-second precision is discarded because
// TimeOfDay has no field for it and the column is only ever written from one.
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

// document maps a verbatim JSON column, and its whole job is the nil case.
//
// `provider`, `descriptor`, `attributes`, `offer` and `geojson` are all JSONB
// NOT NULL. A nil json.RawMessage — a catalog that carried no provider — would
// go down as SQL NULL and be rejected by the column, so it becomes the empty
// object the DEFAULT would have supplied.
func document(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte(`{}`)
	}
	return raw
}

// list maps a TEXT[] parameter. A nil slice is a legal empty array here, but
// pgx sends nil as NULL, and every array column in this schema is NOT NULL.
func list(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// embedding maps a vector, and NULL is the ordinary case in Phase 1 (A5).
//
// Nil rather than a zero vector: `embedding IS NULL` is the Phase 2 backfill
// queue, and a row holding 768 zeros is a row that queue would skip while
// answering every semantic search with the same meaningless distance.
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

// owner maps a geometry's resource id, where NULL means CATALOG-LEVEL.
//
// The empty string is not a substitute: `uq_resource_geometries` keys on
// COALESCE(resource_id, ”), so a ” resource id and a catalog-level row would
// upsert over each other. The schema's CHECK refuses ” for exactly that
// reason, and this is the Go side of the same rule.
func owner(resourceID string) pgtype.Text {
	if resourceID == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: resourceID, Valid: true}
}

// cells narrows H3 indexes to the BIGINT[] the column holds.
//
// Lossless: an H3 index reserves its high bit, so every cell id is below 2^63
// and the conversion cannot change a value. Written as its own function so the
// claim is stated once rather than assumed at three call sites.
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
// reads it back. MergeCatalog takes it from the patch.
// storedCatalog takes the plain-SELECT row rather than the lock-and-load one,
// even though the lock-and-load path is the busier caller. The two row types
// are field-for-field identical, so one converts to the other; picking the READ
// type as the parameter means the read path — the one that must never route
// through a statement that creates rows — spells no conversion at all.
func storedCatalog(row gen.GetCatalogRowRow) domain.Catalog {
	return domain.Catalog{
		ID:            row.ID,
		Document:      json.RawMessage(row.Document),
		VisibleTo:     row.VisibleTo,
		Active:        row.Active,
		ValidFrom:     instant(row.ValidFrom),
		ValidTo:       instant(row.ValidTo),
		ValidTimeFrom: timeOfDay(row.ValidTimeFrom),
		ValidTimeTo:   timeOfDay(row.ValidTimeTo),
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

// geometriesFrom regroups geometry ROWS back into geometry VALUES.
//
// One shape owned by three resources is three rows and one domain.Geometry with
// three Owners, so the read is a fold over (target_path, source_path) rather
// than a row-per-value map. Catalog-level rows — resource_id NULL — fold into
// their own value with no owners, which is what the caller separates them by.
func geometriesFrom(rows []gen.ListStoredGeometriesRow) (catalogLevel []domain.Geometry, byResource map[string][]domain.Geometry) {
	byResource = make(map[string][]domain.Geometry)

	// Keyed on source_path alone, not on the pair: source_path is unique per
	// (catalog, resource) by uq_resource_geometries, and a shape shared by
	// several resources carries the SAME source_path on each of its rows —
	// which is precisely the grouping this fold needs.
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

// geometryType reads the GeoJSON `type` back out.
//
// There is no geom_type column — it would be this same string copied out and
// kept in step by hand — so the type is read from the document on the way back
// exactly as it was read from it on the way in.
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
		ID:            catalog.ID,
		Document:      document(catalog.Document),
		VisibleTo:     list(catalog.VisibleTo),
		Active:        catalog.Active,
		ValidFrom:     timestamp(catalog.ValidFrom),
		ValidTo:       timestamp(catalog.ValidTo),
		ValidTimeFrom: clock(catalog.ValidTimeFrom),
		ValidTimeTo:   clock(catalog.ValidTimeTo),
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
// `ownerID` is the id of the resource the shape was found ON, and "" is the
// catalog itself: a NULL resource_id, stored once for the whole catalog rather
// than once per resource. It is deliberately NOT read back off
// `Geometry.Owners`. The walk has already SPENT Owners deciding placement,
// putting an offer's shape on the list of every resource that offer covers, so
// an adapter that fanned out over Owners a second time would turn a shape
// already sitting on N lists into N x N rows and collide with
// uq_resource_geometries on the very first publish.
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
