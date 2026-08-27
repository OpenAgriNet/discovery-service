-- The whole schema, at version 1.
--
-- It is ONE migration and not six because nothing had been deployed when the
-- six were written: they were authored in a single commit and then edited in
-- place twice, so their numbering recorded the order they were typed in rather
-- than any sequence of changes a running database had lived through. Six
-- versions that no database ever visited one at a time are six things to keep
-- consistent and one story to read.
--
-- golang-migrate tracks version NUMBERS and never file contents, so a database
-- that already ran the old 1..6 reports "version 6, clean" and will apply
-- nothing here for ever, while a fresh one gets this file. There is no way to
-- reconcile the two: such a database must be DROPPED and rebuilt
-- (`make down` — `docker compose --profile app down -v` — then `make up`).
-- That is the whole cost of this file being version 1, and it is payable
-- exactly once, before anything is deployed.
--
-- The order below is load-bearing and not editorial:
--   extensions          `vector` before resources.embedding and the HNSW index;
--                       `pg_trgm` before the gin_trgm_ops index
--   catalogs            before every table that references it
--   resources           before resource_geometries' composite foreign key
--   resource_geometries
--   functions           geo_haversine_m before geo_distance_m, whose body calls
--                       it and is parsed at CREATE time
--   offers
-- The reverse of this order is what 000001_initial_schema.down.sql runs.


-- ═══════════════════════════════════════════════════════════════════════════
-- Extensions
-- ═══════════════════════════════════════════════════════════════════════════

-- Two extensions, and no PostGIS: H3 cells are plain BIGINTs computed in Go,
-- full-text search and SQL/JSON path are core PostgreSQL.
CREATE EXTENSION IF NOT EXISTS vector;
-- Trigram similarity for the misspellings stemming cannot recover ("tracter").
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ═══════════════════════════════════════════════════════════════════════════
-- catalogs
-- ═══════════════════════════════════════════════════════════════════════════

CREATE TABLE catalogs (
    -- `CHECK (id <> '')` on every id column in this schema, for one reason
    -- that only surfaces three tables further down this file:
    -- `uq_resource_geometries` keys on `COALESCE(resource_id, '')`, so an
    -- empty-string resource id and a catalog-level geometry become the same
    -- key. The constraint lives on all four id columns rather than only that
    -- one, because "ids are never empty" is the invariant, and enforcing it in
    -- the one place that currently depends on it is how it stops being true
    -- somewhere else.
    id           TEXT PRIMARY KEY CHECK (id <> ''),

    -- The Catalog as the publisher sent it, with `resources` and `offers`
    -- STRIPPED — those own their own rows (A17). Everything else on this table
    -- is derived from it and exists to be indexed; this column is the only
    -- thing stored, so a member the protocol adds tomorrow survives without a
    -- migration and comes back out of a discover unchanged.
    --
    -- Stripped rather than kept whole because a resource would otherwise exist
    -- in two places with nothing keeping them agreed, and one MERGE would have
    -- two documents to apply itself to.
    --
    -- DEFAULT so the lock-and-load INSERT that opens every publish can name
    -- `id` alone. See `updateMode` — MERGE and FULL.
    document     JSONB       NOT NULL DEFAULT '{}'::jsonb,

    -- publishDirective.visibleTo: the network ids this catalog is discoverable
    -- from. An array because that is what the directive carries — a publisher
    -- naming two networks publishes into both from one call.
    --
    -- DEFAULT '{}' is a fail-safe, not a valid state: the writer fills an empty
    -- list with the request's network first, because a catalog visible to
    -- nobody is findable by nobody while reporting success.
    visible_to   TEXT[]      NOT NULL DEFAULT '{}',

    -- The publisher's own off switch (catalog.isActive). Withdrawing is not the
    -- same as narrowing.
    active       BOOLEAN     NOT NULL DEFAULT TRUE,

    -- catalog.validity is a TimePeriod, and a TimePeriod carries TWO windows
    -- that the spec's anyOf lets appear separately or together:
    --   startDate/endDate  a one-off calendar range   ("live Jan -> Mar")
    --   startTime/endTime  a window that REPEATS DAILY ("open 09:00 -> 17:00")
    -- They are independent, so they are two independent column pairs, and a
    -- row must satisfy both to be live. NULL means unbounded on that axis.
    valid_from      TIMESTAMPTZ,
    valid_to        TIMESTAMPTZ,
    valid_time_from TIME,
    valid_time_to   TIME,

    -- published_at is set on first publish and never moves; updated_at moves on
    -- every republish. The upsert must set updated_at explicitly, because
    -- DEFAULT now() only fires on INSERT.
    published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- NO index over `document`. A filter rooted at the catalog path,
-- `$.catalogs[*] ? (...)`, does NOT resolve here: since A18 it runs against
-- `resources.filter_doc`, which carries this catalog's own members copied down
-- onto each of its resource rows. That is what lets one expression mix a
-- catalog predicate with a resource or offer one — including under OR, which
-- no set of per-table results can reassemble — and it is why a catalog-level
-- filter costs neither a join nor a second query.
--
-- This table is therefore reached only by primary key, to hydrate the catalogs
-- a page of resources belongs to. `catalogs_pkey` is the whole inventory.

-- ═══════════════════════════════════════════════════════════════════════════
-- resources
-- ═══════════════════════════════════════════════════════════════════════════

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

    -- A duplicate of document->'descriptor'->>'name', and knowingly so: fuzzy
    -- search needs GIN (name gin_trgm_ops), and a trigram index over a JSONB
    -- extraction is worse to build, to read and to explain. A duplicate paid
    -- for by an index.
    name        TEXT  NOT NULL DEFAULT '',

    -- The Resource as the publisher sent it — `{id, descriptor,
    -- resourceAttributes}` — verbatim and entire (A17). There is no
    -- `descriptor` column and no `attributes` column: they were two halves of
    -- this one document, and splitting them meant a filter predicate naming
    -- both had no single column to run against, while a member the protocol
    -- adds later had nowhere at all to go.
    document    JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- The COMPOSITE the attribute filter runs against (A18) — this resource,
    -- its catalog, and the offers that apply to it, in ONE jsonb value:
    --
    --   {"catalogs": [{ <catalog scalars>, "descriptor": {...},
    --                   "provider":  {...},        -- availableAt STRIPPED
    --                   "resources": [ <THIS resource, and only it> ],
    --                   "offers":    [ <offers naming it + catalog-wide> ] }]}
    --
    -- Derived at publish from the three documents. Never publisher-supplied,
    -- and rebuildable from them at any time.
    --
    -- ONE value because PostgreSQL evaluates a jsonpath against ONE jsonb: no
    -- `@?` spans tables. `$.catalogs[*] ? (@.isActive == true && exists(
    -- @.offers[*] ? (@.channel == "retail")))` is unwritable without it, and an
    -- OR across levels is a ROW-level disjunction that no set of per-table
    -- results can reassemble.
    --
    -- `resources` holds exactly ONE element, and that is correctness rather
    -- than thrift: with siblings present, `$.catalogs[*].resources[*] ?
    -- (@.grade == "A")` matches a grade-B resource's row because its NEIGHBOUR
    -- passed — measured, not reasoned. The single element is also what makes
    -- `@.resources[*]` mean "this resource" by construction.
    --
    -- `provider.availableAt` is stripped: geometry is answered by
    -- resource_geometries, and polygons are the bulky half of a block that is
    -- already copied onto every resource of the catalog.
    filter_doc  JSONB NOT NULL DEFAULT '{}'::jsonb,

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
    -- There is no `search_text` column. It would be the concatenation of every
    -- value in `document` — the largest text on the widest, hottest table,
    -- holding a second copy of bytes already in the same row. Its only reader
    -- was the Phase 2 embedding backfill, which already loads `document` and
    -- can call `deriveSearchText` on it for far less than the cost of storing
    -- the answer on every row for ever.
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
-- fastupdate = off, deliberately, and the same on the two cell indexes on
-- resource_geometries below. With GIN's default fastupdate = on, inserts land
-- in a pending list, and PostgreSQL's own warning is that "searches must scan
-- the list of pending entries in addition to searching the regular index, so a
-- large list of pending entries will slow searches significantly". A search
-- does not clean that list — only VACUUM, autoanalyze,
-- `gin_clean_pending_list()`, or an insert that pushes it past
-- `gin_pending_list_limit` does. So the cost is not one unlucky discover
-- paying for a flush; it is EVERY discover degrading in proportion to how much
-- publishing has happened since the last one, which is exactly the shape a p95
-- SLA cannot absorb. Turning it off moves the work to the writer, where there
-- is no SLA to miss.
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

-- The one filter index, over the composite (A18). jsonb_path_ops, not the
-- default jsonb_ops: a third the size and faster for the path-exists queries
-- this service issues (Task 22). It cannot serve key-existence (?) queries,
-- which nothing here needs.
--
-- Measured on PG16 over 100k rows: all 20 expression shapes tried are captured
-- by this index — AND and OR across catalog, resource and offer, every 3-way
-- ordering, (a&&b)||c, (a||b)&&c, exists(), and quoted colon-bearing field
-- names. Equality is what jsonb_path_ops extracts; inequality, like_regex and
-- `starts with` remain correct but scan, which is the operator class and not a
-- choice this plan made.
--
-- There is NO idx_resources_document, NO idx_catalogs_document and NO
-- idx_offers_document. filter_doc contains what all three indexed, so any of
-- them would be write cost with no reader.
CREATE INDEX idx_resources_filter_doc ON resources
    USING GIN (filter_doc jsonb_path_ops);

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

-- ═══════════════════════════════════════════════════════════════════════════
-- resource_geometries
-- ═══════════════════════════════════════════════════════════════════════════

CREATE TABLE resource_geometries (
    catalog_id    TEXT NOT NULL,

    -- NULL = catalog-level: found on the catalog's provider, shared by every
    -- resource in it, stored once. Non-NULL = found on that one resource.
    --
    -- NULL and '' must stay distinguishable here: the unique index below folds
    -- them together with COALESCE, so an empty-string resource id would key
    -- identically to a catalog-level row and one would silently upsert over the
    -- other. The FK to `resources` already makes '' unstorable — `resources.id`
    -- carries the same CHECK — and this one states the dependency where the
    -- index that needs it can be read beside it.
    resource_id   TEXT CHECK (resource_id IS NULL OR resource_id <> ''),

    -- Wildcard form, byte-identical to what a caller sends in `targets`, and
    -- the ONLY column a spatial constraint is compared against — which is what
    -- the name says, where a bare `path` beside a `source_path` did not:
    --   $.catalogs[*].provider.availableAt[*].geo
    target_path   TEXT NOT NULL,

    -- The same path with concrete indices:
    --   $.catalogs[0].provider.availableAt[2].geo
    -- In the key instead of the reference implementation's positional `seq`.
    -- It is NOT stable under array reordering — it is positional too — but it
    -- names its own source, which a bare ordinal cannot, and it has no SMALLINT
    -- ceiling. Reordering is handled by the writer, which deletes a catalog's
    -- geometry rows and re-inserts them rather than trying to match them up.
    source_path TEXT NOT NULL,

    -- No `geom_type` column. It held `geojson->>'type'` copied out, and the
    -- one place the type is tested — `geo_distance_m` — reads it from `geojson`
    -- directly. A future "polygons only" filter reads it the same way, or gets
    -- an expression index; neither needs a stored copy kept in step by hand.

    -- Verbatim. The reference implementation stores only a parsed form, which
    -- is how it drops five of the seven types and every polygon hole — a donut
    -- service area becomes a filled disc and S_CONTAINS starts answering true
    -- for addresses in the hole. Keeping the original costs one JSONB column.
    geojson       JSONB NOT NULL,

    -- The two covers the CQL2 operator set is answered from, both at
    -- `ResolutionCells`. See the plan's Geospatial Design — the invariant is
    -- `cells_full ⊆ the true geometry ⊆ cells_cover`, and it is the reason
    -- there are two columns rather than one.
    --
    -- ContainmentFull: cells lying ENTIRELY inside the geometry. A guaranteed
    -- SUBSET, and therefore the only column that can prove a positive. Empty
    -- for every Point and LineString and for any polygon smaller than a cell —
    -- correctly, since none of them contains a cell.
    cells_full    BIGINT[],

    -- ContainmentOverlapping: cells touching the geometry at all. A guaranteed
    -- SUPERSET, and therefore the only column that can prove a negative.
    cells_cover   BIGINT[],

    -- Both arrays are stored ASCENDING and DEDUPLICATED, and the writer is the
    -- only place that can guarantee it. `&&` and `<@` do not care, but
    -- `S_EQUALS` compares with array `=`, which in PostgreSQL is element-wise
    -- IN ORDER: two identical cell sets emitted by H3 in different orders
    -- compare unequal. That is a false negative on the one operator whose
    -- stated property is that it has none. Query covers are sorted the same
    -- way, in `geo.CoverQuery`, for the same reason.

    -- Both nullable together, and NULL is load-bearing: it means "over
    -- geo.MaxIndexCoverCells", not "covers nothing". Such a row is decided by
    -- the bounding box alone and is always MAYBE inside it. A truncated array
    -- instead would make a state-sized service polygon undiscoverable outside
    -- whichever corner the fill happened to reach.
    --
    -- They are NULL as a pair, never one without the other: a row holding a
    -- `cells_full` with no `cells_cover` would prove positives it cannot
    -- bound, and the predicate has no branch for that state. Stated as a
    -- CHECK below rather than left to the writer, on the same argument the
    -- `CHECK (id <> '')` block on `catalogs` makes: enforcing an invariant
    -- only where something currently depends on it is how it stops being true
    -- somewhere else.

    -- NOT NULL: a row exists only because a geometry parsed, and a geometry
    -- that parsed has a box. Load-bearing in a way it was not when the box was
    -- merely a second filtering stage — for an oversize row (both cell columns
    -- NULL) this box is the ENTIRE predicate.
    min_lat DOUBLE PRECISION NOT NULL,
    max_lat DOUBLE PRECISION NOT NULL,
    min_lon DOUBLE PRECISION NOT NULL,
    max_lon DOUBLE PRECISION NOT NULL,

    -- ---- three invariants the predicate's correctness rests on -----------
    -- Both cell columns NULL or neither. The predicate short-circuits on
    -- `cells_cover IS NULL` alone and never re-tests `cells_full`, so a
    -- half-NULL row would reach the operator CASE with a NULL operand, and a
    -- NULL inside EXISTS is a miss.
    CHECK ((cells_full IS NULL) = (cells_cover IS NULL)),

    -- A stored cover is never the EMPTY array. `cells_full` legitimately is —
    -- a Point contains no cell — but `cells_cover` cannot be, because every
    -- geometry that parsed touches at least one cell. The constraint is
    -- load-bearing rather than tidy: `S_WITHIN`, `S_CONTAINS` and `S_DISJOINT`
    -- are all refuted through `cells_cover <@ …`, and `'{}' <@ anything` is
    -- TRUE in PostgreSQL, so an empty cover would silently answer those three
    -- operators with "cannot refute" for the row it belongs to. It is also the
    -- backstop for a geometry whose `coordinates` is a well-formed but EMPTY
    -- array — `looksLikeGeoJSON` accepts the shape, and the parser must fault
    -- it rather than emit a row with nothing in it.
    CHECK (cells_cover IS NULL OR cardinality(cells_cover) > 0),

    -- The box is well-ordered. A geometry crossing the antimeridian gets
    -- min_lon = -179, max_lon = 179 — the whole globe, which is over-inclusive
    -- and therefore safe under the superset rule, and matters only for an
    -- oversize row where the box is the entire predicate. This CHECK exists to
    -- stop the well-meant "fix" that stores [179, -179] instead: `max_lon >=
    -- $min AND min_lon <= $max` is then false for every query, and the
    -- geometry becomes undiscoverable rather than over-discoverable, which is
    -- the one direction this design never moves in.
    CHECK (min_lat <= max_lat AND min_lon <= max_lon),

    -- Catalog-level rows hang off the catalog directly, which is also what
    -- cascades them when it is deleted.
    FOREIGN KEY (catalog_id) REFERENCES catalogs (id) ON DELETE CASCADE,

    -- Resource-level rows additionally hang off their resource. Under MATCH
    -- SIMPLE a NULL resource_id makes this constraint pass trivially, which is
    -- exactly the behaviour a catalog-level row needs.
    FOREIGN KEY (catalog_id, resource_id)
        REFERENCES resources (catalog_id, id) ON DELETE CASCADE
);

-- The key, as a unique index rather than a PRIMARY KEY, because a PK may not
-- contain an expression and NULL resource_ids must still collide on duplicates.
--
-- COALESCE picks '' as the sentinel for "catalog-level", which is only safe
-- because no resource id can BE '': `resources.id` and this table's
-- `resource_id` both carry a CHECK saying so. Without that pair the sentinel is
-- a value in the domain it is standing outside of, and a resource published
-- with `"id": ""` would share a key with the catalog's own geometry at the same
-- source path — one upserting over the other, in silence, at publish time.
CREATE UNIQUE INDEX uq_resource_geometries
    ON resource_geometries (catalog_id, COALESCE(resource_id, ''), source_path);

-- The operator predicates are array overlap (`&&`) and containment (`<@`,
-- `@>`), which is precisely what GIN's array_ops answers.
--
-- fastupdate = off for the reason spelled out on idx_resources_visible_to
-- above: a pending list is scanned by every search and flushed by none
-- of them. These two carry it worst — a cover is up to MaxIndexCoverCells
-- entries for ONE geometry, so a single republish can bloat the list far past
-- anything a scalar column would.
CREATE INDEX idx_rg_cells_full ON resource_geometries USING GIN (cells_full)
    WITH (fastupdate = off);
CREATE INDEX idx_rg_cells_cover ON resource_geometries USING GIN (cells_cover)
    WITH (fastupdate = off);

-- A note on `<@` for the reader who checks: GIN supports contained-by, but
-- PostgreSQL estimates its selectivity poorly, and `S_WITHIN`/`S_CONTAINS` are
-- built on it. Both predicates are correlated inside an EXISTS already scoped
-- to one catalog_id, so the row count reaching them is small whatever the
-- planner believes. Task 16's EXPLAIN assertions cover the operators, not just
-- the overlap case, for exactly this reason.

-- The cascade delete and the per-resource rewrite.
CREATE INDEX idx_rg_catalog_resource ON resource_geometries (catalog_id, resource_id);

-- `targets` is an equality filter on `target_path`, and the walker finds geometry
-- anywhere in the document, so this column now holds many distinct values
-- rather than one. It is part of the same predicate as the cell overlap, so it
-- rides the composite rather than getting an index of its own.
CREATE INDEX idx_rg_catalog_target_path
    ON resource_geometries (catalog_id, target_path);

-- NO index on the bounding box:
--
-- A bounding-box overlap is `max_lat >= $1 AND min_lat <= $2` — two open-ended
-- ranges. A btree leading with min_lat can only range-scan on the first column,
-- so it reads up to half the table before max_lat can help; btrees do not do
-- overlap, which is what GiST exists for. The box is evaluated inside an EXISTS
-- already correlated to one catalog_id, so it is a cheap FILTER over that
-- catalog's geometry rows and wants no index of its own — including for the
-- oversize rows where it is the only predicate, because there are few of them
-- and they are reached through the same correlation.

-- ═══════════════════════════════════════════════════════════════════════════
-- Functions
-- ═══════════════════════════════════════════════════════════════════════════

-- IMMUTABLE so the function stays eligible for an expression index later, and
-- so PostgreSQL MAY inline this body into the calling query. Not constant
-- folding: the arguments are columns, so there is nothing to fold. PARALLEL
-- SAFE so a parallel seq scan may evaluate it.
--
-- STRICT so a NULL coordinate propagates instead of reading as zero — and
-- here that is not the usual argument, because without STRICT this body does
-- something worse than return zero. `least()` IGNORES NULLs: with a NULL
-- latitude the haversine term is NULL, `least(1, NULL)` is 1, `asin(1)` is
-- pi/2, and the function confidently returns ~20,015 km — half the Earth's
-- circumference — for a coordinate it could not read. STRICT is what makes it
-- return NULL instead, which the call site is written to expect.
--
-- STRICT and inlining are in tension here, and the plan states which side it
-- takes rather than assuming the question away. `least()` is non-strict, so
-- this body is non-strict, and PostgreSQL declines to inline a STRICT SQL
-- function whose body is non-strict — the same rule cited two functions down
-- as the reason `geo_distance_m` is NOT marked STRICT. The clamp cannot simply
-- go: floating-point overshoot puts the argument at 1 + 1e-16 for antipodal
-- points and `asin()` then raises "input is out of range", a hard error on a
-- live query. So correctness wins and this one is expected NOT to inline,
-- costing a function call per candidate row on the Point-to-Point refinement
-- path only. Task 14 asserts the outcome with `EXPLAIN (VERBOSE)` rather than
-- leaving the reader to assume it was checked — if a later PostgreSQL inlines
-- it after all, the test is what tells us.
CREATE OR REPLACE FUNCTION geo_haversine_m(
    lat1 DOUBLE PRECISION, lon1 DOUBLE PRECISION,
    lat2 DOUBLE PRECISION, lon2 DOUBLE PRECISION
) RETURNS DOUBLE PRECISION
LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS $$
    SELECT 2 * 6371008.8 * asin(sqrt(least(1,
        power(sin(radians(lat2 - lat1) / 2), 2) +
        cos(radians(lat1)) * cos(radians(lat2)) *
        power(sin(radians(lon2 - lon1) / 2), 2)
    )));
$$;

-- Distance to one stored Point, and ONLY a stored Point. Every geometry type
-- including this one is decided by the cell algebra in the plan's Geospatial
-- Design; this function exists to SHARPEN the single commonest case —
-- `S_DWITHIN` from a Point to a stored Point — from cell accuracy (~1.1 km at
-- r8) to exact.
--
-- It returns NULL for the other six types, and the call site guards on
-- `geom->>'type' = 'Point'` so that NULL is never compared. The guard is not
-- belt-and-braces: an unguarded `NULL <= radius` is UNKNOWN, which fails inside
-- EXISTS and SUCCEEDS inside NOT EXISTS, and that asymmetry is what previously
-- returned a Polygon lying inside the radius from a "nowhere near here" query.
-- The function keeps returning NULL because that is honest; the predicate is
-- what must never ask.
--
-- GeoJSON is [longitude, latitude]: index 0 is lon, index 1 is lat, the reverse
-- of every argument list in this file. Swapping them puts Bengaluru (12.97,
-- 77.64) in Somalia, and both values stay in range so nothing rejects it. This
-- cast is the one place the order is decided.
--
-- NOT STRICT, unlike geo_haversine_m above, and the asymmetry is deliberate. It
-- would be redundant — a NULL geom makes `geom->>'type'` NULL, the CASE falls
-- through, and the result is NULL anyway — and it would cost something:
-- PostgreSQL declines to inline a STRICT SQL function whose body is non-strict,
-- and a CASE body is non-strict. Marking it STRICT buys nothing and adds a
-- function call per candidate row.
CREATE OR REPLACE FUNCTION geo_distance_m(
    geom JSONB, lat DOUBLE PRECISION, lon DOUBLE PRECISION
) RETURNS DOUBLE PRECISION
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE
        WHEN geom->>'type' = 'Point'
         AND jsonb_typeof(geom->'coordinates') = 'array'
         AND jsonb_array_length(geom->'coordinates') >= 2
         AND jsonb_typeof(geom->'coordinates'->0) = 'number'
         AND jsonb_typeof(geom->'coordinates'->1) = 'number'
        THEN geo_haversine_m(lat, lon,
                             (geom->'coordinates'->>1)::DOUBLE PRECISION,
                             (geom->'coordinates'->>0)::DOUBLE PRECISION)
    END;
$$;

-- websearch_to_tsquery joins terms with AND, the wrong default for discovery:
-- "wheat seeds for sale" matches nothing because no listing has all four words.
-- OR semantics return every wheat and every seed listing and let ts_rank_cd
-- float the ones matching more of the query. Precision is RRF's job, not the
-- retrieval predicate's.
--
-- The rewrite is applied to websearch_to_tsquery's OUTPUT, which is what makes
-- it safe: PostgreSQL has already parsed and escaped the caller's text, so no
-- amount of punctuation produces a tsquery the caller wrote.
--
-- A query containing an exclusion keeps AND. Under a disjunction a negated term
-- stops excluding and starts matching everything that lacks it, so
-- "tractor -diesel" would return the whole catalogue.
CREATE OR REPLACE FUNCTION discover_tsquery(query TEXT) RETURNS tsquery
LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS $$
    SELECT CASE
        WHEN websearch_to_tsquery('simple', query)::text LIKE '%!%'
            THEN websearch_to_tsquery('simple', query)
        ELSE replace(websearch_to_tsquery('simple', query)::text, ' & ', ' | ')::tsquery
    END;
$$;

-- The daily half of validity. A separate function because the wrap-around case
-- is the one every hand-written BETWEEN gets wrong: a shop open 22:00 -> 02:00
-- has from > to, and `t BETWEEN from AND to` is then false for every t. Every
-- retriever, the counter, the hydrator and the offer join carry it, so it is
-- one definition and one place to be right — plus one Go twin,
-- domain.WithinDailyWindow, for the backends that are not this one.
--
-- Takes the instant as an argument rather than calling now(). A function that
-- called now() could not honestly be IMMUTABLE, and IMMUTABLE is what lets
-- PostgreSQL inline this body into the calling query rather than call it per
-- row. Nothing here is folded at plan time: `(now() AT TIME ZONE 'UTC')::time`
-- is STABLE, evaluated once per execution.
--
-- NOT STRICT, deliberately. The NULL branch IS the answer for "no daily window
-- set". STRICT would return NULL there instead, the gate clause would fail, and
-- every catalog that never set opening hours would vanish from discover.
CREATE OR REPLACE FUNCTION within_daily_window(
    from_t TIME, to_t TIME, at_utc TIME
) RETURNS BOOLEAN
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE
        WHEN from_t IS NULL OR to_t IS NULL THEN TRUE   -- no daily window set
        WHEN from_t <= to_t THEN at_utc >= from_t AND at_utc <= to_t
        ELSE                     at_utc >= from_t OR  at_utc <= to_t   -- wraps
    END;
$$;

-- ═══════════════════════════════════════════════════════════════════════════
-- offers
-- ═══════════════════════════════════════════════════════════════════════════

CREATE TABLE offers (
    id           TEXT NOT NULL CHECK (id <> ''),
    catalog_id   TEXT NOT NULL REFERENCES catalogs (id) ON DELETE CASCADE,

    -- Which resources this offer applies to. EMPTY MEANS CATALOG-WIDE — an
    -- offer on everything the provider sells — so it is a meaningful state and
    -- not a default to be pruned into. See the FULL-republish rule below.
    resource_ids TEXT[]      NOT NULL DEFAULT '{}',

    -- The offer document, verbatim. This table got the rule right first and
    -- A17 is that rule applied to the other two: `catalogs.document` and
    -- `resources.document` are now the same thing for the same reasons.
    --
    -- There are no `descriptor` and `price` columns beside it. They would be
    -- `document->'descriptor'` and `document->'price'` copied out — the same
    -- bytes a second time, on a column already read in full by the one query
    -- that touches this table, indexed by nothing and filtered on by nothing.
    -- Unlike `resources.name`, which exists to carry a trigram index, they
    -- would pay for themselves nowhere and would drift the moment a republish
    -- updated the document and missed a projection. That is precisely what
    -- `catalogs.descriptor` and `resources.attributes` did.
    --
    -- Named `document` rather than `offer` because every table now spells it
    -- the same way, `OfferPatch.Document` in the domain already did, and
    -- `offer.offer` reads as a mistake at every call site.
    document     JSONB       NOT NULL DEFAULT '{}'::jsonb,

    -- An expired offer must not be returned. The catalog's own validity does
    -- not cover this: a live catalog routinely carries last month's offer.
    -- Offer.validity is the same TimePeriod, so it gets the same two pairs —
    -- a lunch special is a daily window and nothing else.
    valid_from      TIMESTAMPTZ,
    valid_to        TIMESTAMPTZ,
    valid_time_from TIME,
    valid_time_to   TIME,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (catalog_id, id)
);

-- NO `idx_offers_catalog_id`, for the same reason: `PRIMARY KEY (catalog_id,
-- id)` already leads with catalog_id, and the delete-then-prune pair and the
-- hydration fetch are both catalog_id-prefix scans.

-- Hydration asks "which offers touch any of these resource ids" — an array
-- overlap (&&), which is a GIN scan.
CREATE INDEX idx_offers_resource_ids ON offers USING GIN (resource_ids);

-- NO GIN over `document`. An attribute filter rooted at the offer path
-- (`$.catalogs[*].offers[*] ? (...)`) does NOT resolve here: since A18 it runs
-- against `resources.filter_doc`, which carries the offers applying to each
-- resource copied down beside it. That is what lets one expression mix an offer
-- predicate with a catalog or resource predicate — including under OR, which no
-- per-table result set can reassemble. This table is hydration-only: read by
-- `resource_ids` overlap after the page is decided, never scanned by a filter.
