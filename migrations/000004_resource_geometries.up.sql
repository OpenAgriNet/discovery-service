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
-- fastupdate = off for the reason spelled out on idx_resources_visible_to in
-- Migration 003: a pending list is scanned by every search and flushed by none
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
