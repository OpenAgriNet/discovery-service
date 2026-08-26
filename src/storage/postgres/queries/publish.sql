-- The write path. Every statement `UpsertCatalog` issues, in the order it
-- issues them; see the plan's "Inside UpsertCatalog".
--
-- Nothing here concatenates SQL and nothing interpolates a JSONPath. Every
-- variable part is a bind parameter, including the id arrays the two FULL
-- deletes filter on.


-- ---------------------------------------------------------------------------
-- lock and load
-- ---------------------------------------------------------------------------

-- LockAndLoadCatalog opens the transaction by taking the catalog's row lock and
-- returning what is stored, in ONE statement.
--
-- `DO UPDATE SET updated_at = now()` rather than `DO NOTHING`, and the
-- difference is not cosmetic: `DO NOTHING` returns zero rows on conflict, so a
-- republish would merge against an empty document and silently delete
-- everything the publisher did not resend. It also takes no lock on the
-- conflicting row, which is what serialises two concurrent republishes of one
-- catalog into a safe order.
--
-- name: LockAndLoadCatalog :one
INSERT INTO catalogs (id) VALUES ($1)
ON CONFLICT (id) DO UPDATE SET updated_at = now()
RETURNING id, provider, visible_to, active,
          valid_from, valid_to, valid_time_from, valid_time_to;

-- ListStoredResources loads what the patch will merge against.
--
-- `search_tsv` is deliberately absent: nothing reads it back, the domain has no
-- field for it, and selecting it would carry the largest column on the widest
-- table through every publish for nobody.
--
-- name: ListStoredResources :many
SELECT id, catalog_id, visible_to, active,
       valid_from, valid_to, valid_time_from, valid_time_to,
       name, descriptor, attributes, schema_context, schema_type,
       embedding, embedding_source_hash
  FROM resources
 WHERE catalog_id = $1
 ORDER BY id;

-- name: ListStoredOffers :many
SELECT id, catalog_id, resource_ids, offer,
       valid_from, valid_to, valid_time_from, valid_time_to
  FROM offers
 WHERE catalog_id = $1
 ORDER BY id;

-- ListStoredGeometries reads the geometry rows back for GetCatalog.
--
-- The cell covers and the bounding box are not selected: they are derived from
-- `geojson`, `domain.Geometry` has no field for them, and a caller that could
-- read them back would be a caller that could pass a cover this service did not
-- compute.
--
-- name: ListStoredGeometries :many
SELECT catalog_id, resource_id, target_path, source_path, geojson
  FROM resource_geometries
 WHERE catalog_id = $1
 ORDER BY source_path, resource_id NULLS FIRST;


-- ---------------------------------------------------------------------------
-- the catalog row
-- ---------------------------------------------------------------------------

-- UpdateCatalogRow writes the merge result over the row the upsert above
-- created or locked.
--
-- All six gate columns unconditionally, read from the MERGE RESULT: under FULL
-- an omitted `validity` is a reset, and under MERGE the merge already resolved
-- what to carry forward. A conditional here would be a second implementation of
-- A8, in SQL.
--
-- name: UpdateCatalogRow :exec
UPDATE catalogs
   SET provider        = $2,
       visible_to      = $3,
       active          = $4,
       valid_from      = $5,
       valid_to        = $6,
       valid_time_from = $7,
       valid_time_to   = $8,
       updated_at      = now()
 WHERE id = $1;

-- name: DeleteCatalog :exec
DELETE FROM catalogs WHERE id = $1;


-- ---------------------------------------------------------------------------
-- FULL: delete what the payload omitted
-- ---------------------------------------------------------------------------

-- DeleteResourcesNotIn is half of what makes FULL a reset rather than an
-- update. MERGE never runs it — that is the whole difference between an update
-- and a silent data loss.
--
-- name: DeleteResourcesNotIn :exec
DELETE FROM resources
 WHERE catalog_id = $1
   AND id <> ALL(@kept::TEXT[]);

-- name: DeleteOffersNotIn :exec
DELETE FROM offers
 WHERE catalog_id = $1
   AND id <> ALL(@kept::TEXT[]);

-- PruneOfferResourceIDs strips ids that no longer name a resource from the
-- offers a FULL republish did NOT name.
--
-- It runs AFTER the delete above, never before: the point is to catch the
-- offers the patch never mentioned, and before the delete their ids all still
-- resolve. `resource_ids` carries no foreign key — PostgreSQL cannot declare
-- one into an array — so this statement is the constraint.
--
-- name: PruneOfferResourceIDs :exec
UPDATE offers o
   SET resource_ids = kept.ids,
       updated_at   = now()
  FROM (
        SELECT o2.id,
               COALESCE(
                 ARRAY(
                   SELECT unnest(o2.resource_ids)
                   INTERSECT
                   SELECT r.id FROM resources r WHERE r.catalog_id = o2.catalog_id
                 ),
                 '{}'::TEXT[]
               ) AS ids
          FROM offers o2
         WHERE o2.catalog_id = $1
           AND cardinality(o2.resource_ids) > 0
       ) AS kept
 WHERE o.catalog_id = $1
   AND o.id = kept.id
   AND o.resource_ids IS DISTINCT FROM kept.ids;

-- DeleteOffersPrunedToEmpty removes the offers the prune above emptied.
--
-- Two statements rather than one because an empty `resource_ids` is a MEANING —
-- catalog-wide — not an absence. An offer that arrives empty must be kept; one
-- pruned to empty must go, because leaving it would promote a one-resource
-- offer to the provider's entire inventory. The `cardinality > 0` guard in the
-- prune is what keeps the two apart, and this statement is only safe beside it.
--
-- name: DeleteOffersPrunedToEmpty :exec
DELETE FROM offers
 WHERE catalog_id = $1
   AND id = ANY(@candidates::TEXT[])
   AND cardinality(resource_ids) = 0;


-- ---------------------------------------------------------------------------
-- resources, geometries, offers — each of these is queued into one pgx.Batch
-- ---------------------------------------------------------------------------

-- UpsertResource writes the WHOLE row, gate columns included, in both modes.
--
-- Whole-row because the merge already happened up in Go: a partial UPDATE here
-- would be a second implementation of RFC 7396, in SQL, disagreeing with the Go
-- one by next quarter. Only `touched` resources are sent — a resource the patch
-- never named is already byte-identical to what is stored, and rewriting it
-- would burn a row version and an embedding.
--
-- `to_tsvector` is applied HERE rather than in Go because there is no tsvector
-- to build in Go; the searchable TEXT is derived there and passed in, which
-- keeps `deriveSearchText` the one source of truth for what is searchable.
-- 'simple' matches `beckn_websearch_tsquery` in migration 005 — a publish that
-- indexed with a stemming configuration and a query that did not would silently
-- match nothing.
--
-- name: UpsertResource :batchexec
INSERT INTO resources (
    catalog_id, id, visible_to, active,
    valid_from, valid_to, valid_time_from, valid_time_to,
    name, descriptor, attributes, schema_context, schema_type,
    search_tsv, embedding, embedding_source_hash, updated_at
) VALUES (
    @catalog_id, @id, @visible_to, @active,
    @valid_from, @valid_to, @valid_time_from, @valid_time_to,
    @name, @descriptor, @attributes, @schema_context, @schema_type,
    to_tsvector('simple', @search_text::TEXT), @embedding, @embedding_source_hash, now()
)
ON CONFLICT (catalog_id, id) DO UPDATE SET
    visible_to            = EXCLUDED.visible_to,
    active                = EXCLUDED.active,
    valid_from            = EXCLUDED.valid_from,
    valid_to              = EXCLUDED.valid_to,
    valid_time_from       = EXCLUDED.valid_time_from,
    valid_time_to         = EXCLUDED.valid_time_to,
    name                  = EXCLUDED.name,
    descriptor            = EXCLUDED.descriptor,
    attributes            = EXCLUDED.attributes,
    schema_context        = EXCLUDED.schema_context,
    schema_type           = EXCLUDED.schema_type,
    search_tsv            = EXCLUDED.search_tsv,
    embedding             = EXCLUDED.embedding,
    embedding_source_hash = EXCLUDED.embedding_source_hash,
    updated_at            = now();

-- PropagateGate copies the catalog's gate onto the resources the loop above did
-- NOT write.
--
-- This statement is the only thing that makes the denormalised gate safe to
-- read without a join. Without it a publisher who changes `visibleTo` while
-- sending no resources updates the catalog and nothing else, and the change
-- silently does nothing, because discover reads the copy on `resources`.
--
-- `<> ALL(@touched)` because the upsert above already wrote the gate into every
-- touched row; without it each touched resource is written TWICE in one
-- transaction — two row versions, two WAL records, a dead tuple, and two
-- insertions into a GIN index built with `fastupdate = off` for a value that
-- did not change. An EMPTY @touched, which is the catalog-only publish this
-- statement exists for, makes `<> ALL('{}')` true for every row.
--
-- `IS DISTINCT FROM` over the whole six-column row makes the ordinary republish
-- free without weakening the rule. The rule is that every resource ENDS this
-- transaction carrying the catalog's gate, not that every resource is
-- rewritten. Row-comparison rather than six ANDed tests because NULL validity
-- is the common case and `=` on NULL is NULL.
--
-- name: PropagateGate :exec
UPDATE resources
   SET visible_to      = $2,
       active          = $3,
       valid_from      = $4,
       valid_to        = $5,
       valid_time_from = $6,
       valid_time_to   = $7,
       updated_at      = now()
 WHERE catalog_id = $1
   AND id <> ALL(@touched::TEXT[])
   AND (visible_to, active, valid_from, valid_to, valid_time_from, valid_time_to)
       IS DISTINCT FROM ($2, $3, $4, $5, $6, $7);

-- DeleteCatalogGeometries removes the catalog-level rows before they are
-- rebuilt. Catalog-level and resource-level rows are replaced separately, so
-- neither wipes the other.
--
-- name: DeleteCatalogGeometries :exec
DELETE FROM resource_geometries
 WHERE catalog_id = $1 AND resource_id IS NULL;

-- name: DeleteResourceGeometries :batchexec
DELETE FROM resource_geometries
 WHERE catalog_id = $1 AND resource_id = $2;

-- InsertGeometry writes one covered shape.
--
-- A NULL `resource_id` is a catalog-level row: the provider's own locations,
-- covered once for the catalog rather than once per resource, which is what
-- turns 3 shapes into 3 rows on a 40-resource catalog instead of 120.
--
-- Both cell columns are NULL together when the shape ran over
-- geo.MaxIndexCoverCells. That means "too big to index", not "covers nothing" —
-- such a row is decided by the bounding box alone, and the schema's CHECK
-- refuses the half-NULL state the predicate has no branch for.
--
-- name: InsertGeometry :batchexec
INSERT INTO resource_geometries (
    catalog_id, resource_id, target_path, source_path, geojson,
    cells_full, cells_cover, min_lat, max_lat, min_lon, max_lon
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9, $10, $11
);

-- UpsertOffer writes the whole offer row.
--
-- `resource_ids` arrives already pruned of ids naming no resource — see
-- domain.PruneOfferReferences — and an offer pruned to EMPTY never reaches this
-- statement at all, because empty means catalog-wide.
--
-- name: UpsertOffer :batchexec
INSERT INTO offers (
    catalog_id, id, resource_ids, offer,
    valid_from, valid_to, valid_time_from, valid_time_to, updated_at
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7, $8, now()
)
ON CONFLICT (catalog_id, id) DO UPDATE SET
    resource_ids    = EXCLUDED.resource_ids,
    offer           = EXCLUDED.offer,
    valid_from      = EXCLUDED.valid_from,
    valid_to        = EXCLUDED.valid_to,
    valid_time_from = EXCLUDED.valid_time_from,
    valid_time_to   = EXCLUDED.valid_time_to,
    updated_at      = now();

-- GetCatalogRow reads the catalog row WITHOUT touching it.
--
-- Separate from LockAndLoadCatalog and not a convenience duplicate of it: that
-- statement is an upsert, so a read routed through it would CREATE the catalog
-- it was asked about and turn "not found" into "found, and now it exists".
--
-- name: GetCatalogRow :one
SELECT id, provider, visible_to, active,
       valid_from, valid_to, valid_time_from, valid_time_to
  FROM catalogs
 WHERE id = $1;
