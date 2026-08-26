CREATE TABLE resources (
    id          TEXT NOT NULL CHECK (id <> ''),
    catalog_id  TEXT NOT NULL REFERENCES catalogs (id) ON DELETE CASCADE,

    -- ---- the scope gate, copied from the catalog -------------------------
    -- Written by UpsertCatalog in the same transaction as the catalog row, for
    -- EVERY resource of the catalog on EVERY publish. This is what removes the
    -- join from the read path; the unconditional rewrite is what keeps the two
    -- copies from drifting.
    visible_to      TEXT[]      NOT NULL DEFAULT '{}',
    active          BOOLEAN     NOT NULL DEFAULT TRUE,
    valid_from      TIMESTAMPTZ,
    valid_to        TIMESTAMPTZ,
    valid_time_from TIME,
    valid_time_to   TIME,
    -- `Resource` in the spec has NEITHER `isActive` NOR `validity` — only
    -- `{id, descriptor, resourceAttributes}`. Every column in this block is
    -- therefore a derived copy of the catalog's, never publisher-supplied,
    -- which is exactly why the unconditional rewrite below is safe.
    -- ----------------------------------------------------------------------

    -- A duplicate of descriptor->>'name', and knowingly so: fuzzy search needs
    -- GIN (name gin_trgm_ops), and a trigram index over a JSONB extraction is
    -- worse to build, to read and to explain. A duplicate paid for by an index.
    name        TEXT  NOT NULL DEFAULT '',
    descriptor  JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- JSON-LD domain attributes, already validated by L2.
    attributes  JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- resourceAttributes.@context and .@type, both scalar `string` and both
    -- REQUIRED by the Attributes schema (C4). Two plain columns, because the
    -- filter that reads them must match them as a PAIR — see below. Default ''
    -- covers the resource that carries no resourceAttributes at all, which the
    -- Resource schema permits (only `id` is required).
    schema_context TEXT NOT NULL DEFAULT '',
    schema_type    TEXT NOT NULL DEFAULT '',

    -- No geometry columns. Every geometry lives in resource_geometries, one row
    -- per geometry, keyed by its path — see that table for why.

    -- Derived in Go at publish (stripping JSON-LD keywords and attribute keys)
    -- and passed in as a parameter, so the Go function stays the one source of
    -- truth for what is searchable. Only the tsvector is kept.
    --
    -- There is no `search_text` column. It would be the concatenation
    -- of `name`, `descriptor` and every value in `attributes` — the largest
    -- text on the widest, hottest table, holding a second copy of bytes already
    -- in three columns of the same row. Its only reader was the Phase 2
    -- embedding backfill, which already loads those three columns and can call
    -- `deriveSearchText` on them for far less than the cost of storing the
    -- answer on every row for ever.
    search_tsv  TSVECTOR NOT NULL DEFAULT ''::tsvector,

    -- Nullable: a publish must succeed when the embedding service is down, and
    -- the resource stays discoverable lexically and geospatially. NULL for
    -- every row in Phase 1 (A5) — which makes `embedding IS NULL` the backfill
    -- queue, and is why no outbox table exists.
    --
    -- That queue gets no index HERE and wants one THEN. In Phase 1 every row
    -- is NULL, so a sequential scan is the optimal plan and a partial index
    -- would be a second copy of the whole table. During the Phase 2 backfill
    -- the remainder shrinks while the table does not, so the same scan gets
    -- steadily more wasteful with each batch. `CREATE INDEX … ON resources
    -- (catalog_id, id) WHERE embedding IS NULL` is the Phase 2 migration, and
    -- it self-empties as the backfill drains it. Written down here because the
    -- absence is deliberate now and a bug later, which is not something the
    -- schema can say on its own.
    embedding   VECTOR(768),

    -- blake2b-256 of the derived text `embedding` was (or will be) computed
    -- from. WRITTEN FROM DAY ONE, including under the noop embedder, because it
    -- is a hash of the TEXT and not of the vector: it is well defined whether
    -- or not an embedding exists, and it is the only thing that can answer "did
    -- the searchable content of this resource actually change?" on a republish.
    --
    -- Leaving it NULL in Phase 1 would break two things quietly. The Phase 2
    -- backfill would have no baseline and would re-embed the entire corpus on
    -- first run; and the A8 test that asserts an untouched resource keeps its
    -- hash would be comparing NULL to NULL, passing while proving nothing —
    -- which is the regression it exists to catch. 32 bytes is not what makes a
    -- row wide.
    -- It is what makes that queue correct rather than approximate: without it a
    -- republished resource whose text changed keeps its old vector for ever,
    -- and a stale vector is worse than a missing one — a missing one degrades
    -- visibly, a stale one returns confident nonsense silently.
    embedding_source_hash BYTEA,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A resource id is unique within its catalog, not globally: two providers
    -- may both publish "r1".
    PRIMARY KEY (catalog_id, id)
);

-- The variable half of the gate. Bitmap-ANDed with whichever search index the
-- query mode uses — both on this table, so neither costs a join.
--
-- fastupdate = off, deliberately, and the same on the two cell indexes in
-- Migration 004. With GIN's default fastupdate = on, inserts land in a pending
-- list, and PostgreSQL's own warning is that "searches must scan the list of
-- pending entries in addition to searching the regular index, so a large list
-- of pending entries will slow searches significantly". A search does not clean
-- that list — only VACUUM, autoanalyze, `gin_clean_pending_list()`, or an insert
-- that pushes it past `gin_pending_list_limit` does. So the cost is not one
-- unlucky discover paying for a flush; it is EVERY discover degrading in
-- proportion to how much publishing has happened since the last one, which is
-- exactly the shape a p95 SLA cannot absorb. Turning it off moves the work to
-- the writer, where there is no SLA to miss.
CREATE INDEX idx_resources_visible_to ON resources USING GIN (visible_to)
    WITH (fastupdate = off);

-- The constant half of the gate goes INSIDE each search index, so a withdrawn
-- catalog's resources are not merely skipped, they are not in the index at all.
-- Validity cannot join them: now() is not IMMUTABLE, so it stays a filter.
CREATE INDEX idx_resources_search_tsv ON resources USING GIN (search_tsv)
    WHERE active;
CREATE INDEX idx_resources_name_trgm  ON resources USING GIN (name gin_trgm_ops)
    WHERE active;
-- Composite, in this order, and NOT two separate indexes. Every schemaContext
-- entry constrains `schema_context`; only some also constrain `schema_type`. A
-- btree leading with schema_context serves both shapes from one structure.
CREATE INDEX idx_resources_schema ON resources (schema_context, schema_type)
    WHERE active;
-- NO `idx_resources_catalog_id`. `PRIMARY KEY (catalog_id, id)` builds a btree
-- leading with catalog_id, which serves every catalog_id-prefix lookup this
-- plan issues — the per-catalog rewrite, the FULL-republish delete, and the
-- cascade probe from resource_geometries — at no extra write cost. A second
-- index on the same leading column is a duplicate that only the write path
-- pays for.

-- jsonb_path_ops, not the default jsonb_ops: a third the size and faster for
-- the path-exists queries this service issues (Task 22). It cannot serve
-- key-existence (?) queries, which nothing here needs.
CREATE INDEX idx_resources_attributes ON resources USING GIN (attributes jsonb_path_ops);

-- HNSW, not IVFFlat: no training pass, so it works from the first row. Not
-- partial: a partial HNSW would have to be rebuilt when `active` flips.
--
-- It ships EMPTY, because Phase 1 writes no vectors, and an empty HNSW costs
-- nothing to carry. The Phase 2 backfill should DROP and recreate it rather
-- than insert through it: building a graph incrementally, one row at a time,
-- over a corpus that is already there is markedly slower than one bulk build
-- at the end, and the index is useless until the backfill completes anyway.
-- Noted here beside the "not partial" decision because both are choices about
-- when this index is built, and only one of them was written down.
CREATE INDEX idx_resources_embedding ON resources
    USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);
