-- The read path. Three retrievers, one counter, three hydration queries.
--
-- Every block below is repeated per query rather than factored into a view or a
-- function, and that is forced rather than chosen: sqlc compiles these files at
-- build time and has no macro. A view would hide the parameters from sqlc and a
-- function would make the whole predicate opaque to the planner, which is the
-- one thing this design cannot afford — see `plan_cache_mode` in pool.go. The
-- SQL source test in discover_sql_test.go is what keeps the copies identical:
-- it reads THIS file and fails when a query drops a clause.
--
-- Nothing here concatenates SQL and nothing interpolates a JSONPath. The
-- attribute filter (Task 22) is absent for that reason and lands as a bound
-- `@?` parameter when it arrives, never as text spliced into these queries.


-- ---------------------------------------------------------------------------
-- retrieval — one query per ranked mode, all sharing one WHERE
-- ---------------------------------------------------------------------------

-- LexicalCandidates ranks by full-text relevance.
--
-- `discover_tsquery` ORs its terms (Migration 005), so this is a BROAD
-- retriever by design: "wheat seeds for sale" matches every listing carrying
-- any one of those words and ts_rank_cd floats the ones matching more. Recall
-- is the retriever's job and precision is RRF's. That is also why the LIMIT is
-- not optional — without it a two-word query returns the corpus.
--
-- A NULL `query_text` matches every row the shared predicates admit rather than
-- none. An intent may carry only a spatial constraint, and a retriever that
-- returned nothing for it would make a geo-only discover answer empty; with no
-- text there is no relevance, so the ORDER BY falls through to the stable key.
--
-- name: LexicalCandidates :many
SELECT r.catalog_id, r.id
  FROM resources r
 WHERE (sqlc.narg('network_id')::text IS NULL
        OR r.visible_to @> ARRAY[sqlc.narg('network_id')::text])
   AND r.active
   AND (r.valid_from IS NULL OR r.valid_from <= now())
   AND (r.valid_to   IS NULL OR r.valid_to   >= now())
   AND within_daily_window(r.valid_time_from, r.valid_time_to,
                           (now() AT TIME ZONE 'UTC')::time)
   AND (   sqlc.narg('schema_contexts')::text[] IS NULL
        OR cardinality(sqlc.narg('schema_contexts')::text[]) = 0
        OR (    r.schema_context = ANY(sqlc.narg('schema_contexts')::text[])
            AND EXISTS (SELECT 1
                          FROM unnest(sqlc.narg('schema_contexts')::text[])
                                 WITH ORDINALITY AS sc(ctx, n)
                          JOIN unnest(sqlc.narg('schema_types')::text[])
                                 WITH ORDINALITY AS st(typ, n) USING (n)
                         WHERE r.schema_context = sc.ctx
                           AND (st.typ = '' OR r.schema_type = st.typ))))
   AND (
     sqlc.narg('spatial_op')::text IS NULL
     OR @geo_negate::boolean <> EXISTS (
       SELECT 1 FROM resource_geometries g
        WHERE g.catalog_id = r.catalog_id
          AND (g.resource_id IS NULL OR g.resource_id = r.id)
          AND (sqlc.narg('target_paths')::text[] IS NULL
               OR g.target_path = ANY(sqlc.narg('target_paths')::text[]))
          AND @match_negate::boolean <> (
                 (sqlc.narg('min_lat')::double precision IS NULL
                  OR sqlc.narg('spatial_op')::text = 'S_DISJOINT'
                  OR (    g.max_lat >= sqlc.narg('min_lat')::double precision
                      AND g.min_lat <= sqlc.narg('max_lat')::double precision
                      AND g.max_lon >= sqlc.narg('min_lon')::double precision
                      AND g.min_lon <= sqlc.narg('max_lon')::double precision))
                 AND (g.cells_cover IS NULL
                      OR sqlc.narg('q_cover')::bigint[] IS NULL
                      OR CASE sqlc.narg('spatial_op')::text
                           WHEN 'S_INTERSECTS' THEN g.cells_cover && sqlc.narg('q_cover')::bigint[]
                           WHEN 'S_DISJOINT'   THEN NOT (g.cells_full && sqlc.narg('q_full')::bigint[])
                                               AND NOT (g.cells_cover <@ sqlc.narg('q_full')::bigint[])
                                               AND NOT (sqlc.narg('q_cover')::bigint[] <@ g.cells_full)
                           WHEN 'S_WITHIN'     THEN g.cells_cover <@ sqlc.narg('q_cover')::bigint[]
                           WHEN 'S_CONTAINS'   THEN sqlc.narg('q_cover')::bigint[] <@ g.cells_cover
                           WHEN 'S_DWITHIN'    THEN g.cells_cover && sqlc.narg('q_cover')::bigint[]
                           WHEN 'S_OVERLAPS'   THEN g.cells_cover && sqlc.narg('q_cover')::bigint[]
                                               AND NOT (g.cells_cover <@ sqlc.narg('q_full')::bigint[])
                                               AND NOT (sqlc.narg('q_cover')::bigint[] <@ g.cells_full)
                           WHEN 'S_EQUALS'     THEN g.cells_cover = sqlc.narg('q_cover')::bigint[]
                                               AND g.cells_full  = sqlc.narg('q_full')::bigint[]
                           ELSE TRUE
                         END)
                 AND (sqlc.narg('center_lat')::double precision IS NULL
                      OR g.geojson->>'type' <> 'Point'
                      OR geo_distance_m(g.geojson,
                                        sqlc.narg('center_lat')::double precision,
                                        sqlc.narg('center_lon')::double precision)
                          <= sqlc.narg('radius_m')::double precision))
     )
   )
   AND (sqlc.narg('query_text')::text IS NULL
        OR r.search_tsv @@ discover_tsquery(sqlc.narg('query_text')::text))
 ORDER BY ts_rank_cd(r.search_tsv, discover_tsquery(sqlc.narg('query_text')::text)) DESC NULLS LAST,
          r.catalog_id, r.id
 LIMIT @row_limit::int;

-- FuzzyCandidates ranks by trigram similarity on `name`.
--
-- On `name` and not on the descriptor, because `name` is the duplicated column
-- Migration 003 pays for precisely so a `gin_trgm_ops` index exists. `%` reads
-- the session's `pg_trgm.similarity_threshold`; nothing here sets it, so it is
-- the 0.3 default, and a deployment that moves it moves what this mode admits.
--
-- name: FuzzyCandidates :many
SELECT r.catalog_id, r.id
  FROM resources r
 WHERE (sqlc.narg('network_id')::text IS NULL
        OR r.visible_to @> ARRAY[sqlc.narg('network_id')::text])
   AND r.active
   AND (r.valid_from IS NULL OR r.valid_from <= now())
   AND (r.valid_to   IS NULL OR r.valid_to   >= now())
   AND within_daily_window(r.valid_time_from, r.valid_time_to,
                           (now() AT TIME ZONE 'UTC')::time)
   AND (   sqlc.narg('schema_contexts')::text[] IS NULL
        OR cardinality(sqlc.narg('schema_contexts')::text[]) = 0
        OR (    r.schema_context = ANY(sqlc.narg('schema_contexts')::text[])
            AND EXISTS (SELECT 1
                          FROM unnest(sqlc.narg('schema_contexts')::text[])
                                 WITH ORDINALITY AS sc(ctx, n)
                          JOIN unnest(sqlc.narg('schema_types')::text[])
                                 WITH ORDINALITY AS st(typ, n) USING (n)
                         WHERE r.schema_context = sc.ctx
                           AND (st.typ = '' OR r.schema_type = st.typ))))
   AND (
     sqlc.narg('spatial_op')::text IS NULL
     OR @geo_negate::boolean <> EXISTS (
       SELECT 1 FROM resource_geometries g
        WHERE g.catalog_id = r.catalog_id
          AND (g.resource_id IS NULL OR g.resource_id = r.id)
          AND (sqlc.narg('target_paths')::text[] IS NULL
               OR g.target_path = ANY(sqlc.narg('target_paths')::text[]))
          AND @match_negate::boolean <> (
                 (sqlc.narg('min_lat')::double precision IS NULL
                  OR sqlc.narg('spatial_op')::text = 'S_DISJOINT'
                  OR (    g.max_lat >= sqlc.narg('min_lat')::double precision
                      AND g.min_lat <= sqlc.narg('max_lat')::double precision
                      AND g.max_lon >= sqlc.narg('min_lon')::double precision
                      AND g.min_lon <= sqlc.narg('max_lon')::double precision))
                 AND (g.cells_cover IS NULL
                      OR sqlc.narg('q_cover')::bigint[] IS NULL
                      OR CASE sqlc.narg('spatial_op')::text
                           WHEN 'S_INTERSECTS' THEN g.cells_cover && sqlc.narg('q_cover')::bigint[]
                           WHEN 'S_DISJOINT'   THEN NOT (g.cells_full && sqlc.narg('q_full')::bigint[])
                                               AND NOT (g.cells_cover <@ sqlc.narg('q_full')::bigint[])
                                               AND NOT (sqlc.narg('q_cover')::bigint[] <@ g.cells_full)
                           WHEN 'S_WITHIN'     THEN g.cells_cover <@ sqlc.narg('q_cover')::bigint[]
                           WHEN 'S_CONTAINS'   THEN sqlc.narg('q_cover')::bigint[] <@ g.cells_cover
                           WHEN 'S_DWITHIN'    THEN g.cells_cover && sqlc.narg('q_cover')::bigint[]
                           WHEN 'S_OVERLAPS'   THEN g.cells_cover && sqlc.narg('q_cover')::bigint[]
                                               AND NOT (g.cells_cover <@ sqlc.narg('q_full')::bigint[])
                                               AND NOT (sqlc.narg('q_cover')::bigint[] <@ g.cells_full)
                           WHEN 'S_EQUALS'     THEN g.cells_cover = sqlc.narg('q_cover')::bigint[]
                                               AND g.cells_full  = sqlc.narg('q_full')::bigint[]
                           ELSE TRUE
                         END)
                 AND (sqlc.narg('center_lat')::double precision IS NULL
                      OR g.geojson->>'type' <> 'Point'
                      OR geo_distance_m(g.geojson,
                                        sqlc.narg('center_lat')::double precision,
                                        sqlc.narg('center_lon')::double precision)
                          <= sqlc.narg('radius_m')::double precision))
     )
   )
   AND (sqlc.narg('query_text')::text IS NULL
        OR r.name % sqlc.narg('query_text')::text)
 ORDER BY similarity(r.name, sqlc.narg('query_text')::text) DESC NULLS LAST,
          r.catalog_id, r.id
 LIMIT @row_limit::int;

-- SemanticCandidates ranks by cosine distance over the HNSW index.
--
-- `embedding IS NOT NULL` is not defensive: in Phase 1 EMBEDDING_PROVIDER is
-- noop and EVERY row is NULL (A5), so this mode returns nothing at all and the
-- fusion is lexical + fuzzy. It is written and exercised anyway — `make test`
-- pins EMBEDDING_PROVIDER=hashing for exactly that reason — because the day the
-- provider changes is not the day to discover this query does not compile.
--
-- The ORDER BY is what drives the index; there is no distance threshold,
-- because HNSW answers "the nearest N" and not "everything within d". The pool
-- this draws from is every embedded row the shared predicates admit, which is
-- what CountCandidates counts for this mode.
--
-- name: SemanticCandidates :many
SELECT r.catalog_id, r.id
  FROM resources r
 WHERE (sqlc.narg('network_id')::text IS NULL
        OR r.visible_to @> ARRAY[sqlc.narg('network_id')::text])
   AND r.active
   AND (r.valid_from IS NULL OR r.valid_from <= now())
   AND (r.valid_to   IS NULL OR r.valid_to   >= now())
   AND within_daily_window(r.valid_time_from, r.valid_time_to,
                           (now() AT TIME ZONE 'UTC')::time)
   AND (   sqlc.narg('schema_contexts')::text[] IS NULL
        OR cardinality(sqlc.narg('schema_contexts')::text[]) = 0
        OR (    r.schema_context = ANY(sqlc.narg('schema_contexts')::text[])
            AND EXISTS (SELECT 1
                          FROM unnest(sqlc.narg('schema_contexts')::text[])
                                 WITH ORDINALITY AS sc(ctx, n)
                          JOIN unnest(sqlc.narg('schema_types')::text[])
                                 WITH ORDINALITY AS st(typ, n) USING (n)
                         WHERE r.schema_context = sc.ctx
                           AND (st.typ = '' OR r.schema_type = st.typ))))
   AND (
     sqlc.narg('spatial_op')::text IS NULL
     OR @geo_negate::boolean <> EXISTS (
       SELECT 1 FROM resource_geometries g
        WHERE g.catalog_id = r.catalog_id
          AND (g.resource_id IS NULL OR g.resource_id = r.id)
          AND (sqlc.narg('target_paths')::text[] IS NULL
               OR g.target_path = ANY(sqlc.narg('target_paths')::text[]))
          AND @match_negate::boolean <> (
                 (sqlc.narg('min_lat')::double precision IS NULL
                  OR sqlc.narg('spatial_op')::text = 'S_DISJOINT'
                  OR (    g.max_lat >= sqlc.narg('min_lat')::double precision
                      AND g.min_lat <= sqlc.narg('max_lat')::double precision
                      AND g.max_lon >= sqlc.narg('min_lon')::double precision
                      AND g.min_lon <= sqlc.narg('max_lon')::double precision))
                 AND (g.cells_cover IS NULL
                      OR sqlc.narg('q_cover')::bigint[] IS NULL
                      OR CASE sqlc.narg('spatial_op')::text
                           WHEN 'S_INTERSECTS' THEN g.cells_cover && sqlc.narg('q_cover')::bigint[]
                           WHEN 'S_DISJOINT'   THEN NOT (g.cells_full && sqlc.narg('q_full')::bigint[])
                                               AND NOT (g.cells_cover <@ sqlc.narg('q_full')::bigint[])
                                               AND NOT (sqlc.narg('q_cover')::bigint[] <@ g.cells_full)
                           WHEN 'S_WITHIN'     THEN g.cells_cover <@ sqlc.narg('q_cover')::bigint[]
                           WHEN 'S_CONTAINS'   THEN sqlc.narg('q_cover')::bigint[] <@ g.cells_cover
                           WHEN 'S_DWITHIN'    THEN g.cells_cover && sqlc.narg('q_cover')::bigint[]
                           WHEN 'S_OVERLAPS'   THEN g.cells_cover && sqlc.narg('q_cover')::bigint[]
                                               AND NOT (g.cells_cover <@ sqlc.narg('q_full')::bigint[])
                                               AND NOT (sqlc.narg('q_cover')::bigint[] <@ g.cells_full)
                           WHEN 'S_EQUALS'     THEN g.cells_cover = sqlc.narg('q_cover')::bigint[]
                                               AND g.cells_full  = sqlc.narg('q_full')::bigint[]
                           ELSE TRUE
                         END)
                 AND (sqlc.narg('center_lat')::double precision IS NULL
                      OR g.geojson->>'type' <> 'Point'
                      OR geo_distance_m(g.geojson,
                                        sqlc.narg('center_lat')::double precision,
                                        sqlc.narg('center_lon')::double precision)
                          <= sqlc.narg('radius_m')::double precision))
     )
   )
   AND sqlc.narg('query_vector')::vector IS NOT NULL
   AND r.embedding IS NOT NULL
 ORDER BY r.embedding <=> sqlc.narg('query_vector')::vector,
          r.catalog_id, r.id
 LIMIT @row_limit::int;

-- ---------------------------------------------------------------------------
-- the counter
-- ---------------------------------------------------------------------------

-- CountCandidates is the size of the set the fusion draws from.
--
-- Deliberately no LIMIT: capping truncates the PAGE's candidate pool and must
-- never truncate the count, or a capped mode would make Total wrong in the one
-- state a caller has no way to detect.
--
-- The text clause is the OR of every mode's, because the page is a union of
-- every mode's. A counter carrying only lexical's returns a number no page can
-- be paginated out of — fewer results than page 1 already showed. The first
-- disjunct covers the geo-only intent, where no mode carries text and every row
-- the other predicates admit is a candidate.
--
-- `r.name % NULL` and `search_tsv @@ NULL` are NULL, not FALSE, and NULL is a
-- miss under a WHERE — which is exactly the behaviour a disabled mode wants.
-- They can only fail to contribute; they can never turn another mode's TRUE
-- into a miss, because `TRUE OR NULL` is TRUE.
--
-- name: CountCandidates :one
SELECT count(*)
  FROM resources r
 WHERE (sqlc.narg('network_id')::text IS NULL
        OR r.visible_to @> ARRAY[sqlc.narg('network_id')::text])
   AND r.active
   AND (r.valid_from IS NULL OR r.valid_from <= now())
   AND (r.valid_to   IS NULL OR r.valid_to   >= now())
   AND within_daily_window(r.valid_time_from, r.valid_time_to,
                           (now() AT TIME ZONE 'UTC')::time)
   AND (   sqlc.narg('schema_contexts')::text[] IS NULL
        OR cardinality(sqlc.narg('schema_contexts')::text[]) = 0
        OR (    r.schema_context = ANY(sqlc.narg('schema_contexts')::text[])
            AND EXISTS (SELECT 1
                          FROM unnest(sqlc.narg('schema_contexts')::text[])
                                 WITH ORDINALITY AS sc(ctx, n)
                          JOIN unnest(sqlc.narg('schema_types')::text[])
                                 WITH ORDINALITY AS st(typ, n) USING (n)
                         WHERE r.schema_context = sc.ctx
                           AND (st.typ = '' OR r.schema_type = st.typ))))
   AND (
     sqlc.narg('spatial_op')::text IS NULL
     OR @geo_negate::boolean <> EXISTS (
       SELECT 1 FROM resource_geometries g
        WHERE g.catalog_id = r.catalog_id
          AND (g.resource_id IS NULL OR g.resource_id = r.id)
          AND (sqlc.narg('target_paths')::text[] IS NULL
               OR g.target_path = ANY(sqlc.narg('target_paths')::text[]))
          AND @match_negate::boolean <> (
                 (sqlc.narg('min_lat')::double precision IS NULL
                  OR sqlc.narg('spatial_op')::text = 'S_DISJOINT'
                  OR (    g.max_lat >= sqlc.narg('min_lat')::double precision
                      AND g.min_lat <= sqlc.narg('max_lat')::double precision
                      AND g.max_lon >= sqlc.narg('min_lon')::double precision
                      AND g.min_lon <= sqlc.narg('max_lon')::double precision))
                 AND (g.cells_cover IS NULL
                      OR sqlc.narg('q_cover')::bigint[] IS NULL
                      OR CASE sqlc.narg('spatial_op')::text
                           WHEN 'S_INTERSECTS' THEN g.cells_cover && sqlc.narg('q_cover')::bigint[]
                           WHEN 'S_DISJOINT'   THEN NOT (g.cells_full && sqlc.narg('q_full')::bigint[])
                                               AND NOT (g.cells_cover <@ sqlc.narg('q_full')::bigint[])
                                               AND NOT (sqlc.narg('q_cover')::bigint[] <@ g.cells_full)
                           WHEN 'S_WITHIN'     THEN g.cells_cover <@ sqlc.narg('q_cover')::bigint[]
                           WHEN 'S_CONTAINS'   THEN sqlc.narg('q_cover')::bigint[] <@ g.cells_cover
                           WHEN 'S_DWITHIN'    THEN g.cells_cover && sqlc.narg('q_cover')::bigint[]
                           WHEN 'S_OVERLAPS'   THEN g.cells_cover && sqlc.narg('q_cover')::bigint[]
                                               AND NOT (g.cells_cover <@ sqlc.narg('q_full')::bigint[])
                                               AND NOT (sqlc.narg('q_cover')::bigint[] <@ g.cells_full)
                           WHEN 'S_EQUALS'     THEN g.cells_cover = sqlc.narg('q_cover')::bigint[]
                                               AND g.cells_full  = sqlc.narg('q_full')::bigint[]
                           ELSE TRUE
                         END)
                 AND (sqlc.narg('center_lat')::double precision IS NULL
                      OR g.geojson->>'type' <> 'Point'
                      OR geo_distance_m(g.geojson,
                                        sqlc.narg('center_lat')::double precision,
                                        sqlc.narg('center_lon')::double precision)
                          <= sqlc.narg('radius_m')::double precision))
     )
   )
   AND (   (sqlc.narg('lexical_text')::text IS NULL
            AND sqlc.narg('fuzzy_text')::text IS NULL
            AND sqlc.narg('query_vector')::vector IS NULL)
        OR r.search_tsv @@ discover_tsquery(sqlc.narg('lexical_text')::text)
        OR r.name % sqlc.narg('fuzzy_text')::text
        OR (sqlc.narg('query_vector')::vector IS NOT NULL AND r.embedding IS NOT NULL));

-- ---------------------------------------------------------------------------
-- hydration — three queries, all keyed by the decided page
-- ---------------------------------------------------------------------------

-- ScopeFilterResources narrows a set of ids to the ones the scope admits.
--
-- It exists for retrievers that come from an index with no notion of validity
-- or visibility — a vector index is one — and it is the gate and nothing else:
-- no text, no geometry, no schema, because the caller has already applied those
-- and this answers only "may this caller see it now".
--
-- name: ScopeFilterResources :many
SELECT r.catalog_id, r.id
  FROM resources r
  JOIN (SELECT c.v AS catalog_id, i.v AS resource_id
          FROM unnest(@catalog_ids::text[])  WITH ORDINALITY AS c(v, n)
          JOIN unnest(@resource_ids::text[]) WITH ORDINALITY AS i(v, n) USING (n)) AS p
    ON r.catalog_id = p.catalog_id AND r.id = p.resource_id
 WHERE (sqlc.narg('network_id')::text IS NULL
        OR r.visible_to @> ARRAY[sqlc.narg('network_id')::text])
   AND r.active
   AND (r.valid_from IS NULL OR r.valid_from <= now())
   AND (r.valid_to   IS NULL OR r.valid_to   >= now())
   AND within_daily_window(r.valid_time_from, r.valid_time_to,
                           (now() AT TIME ZONE 'UTC')::time);

-- HydrateResources loads the rows of one page.
--
-- The gate is applied AGAIN here, and deliberately. It is the same gate the
-- retrievers ran, on twenty rows by primary key, where it costs nothing — and
-- it is the last line between a retriever bug and a leak.
--
-- `search_tsv` is absent for the same reason it is absent from the publish
-- loader: nothing reads it back and it is the largest column on the table.
--
-- name: HydrateResources :many
SELECT r.catalog_id, r.id, r.visible_to, r.active,
       r.valid_from, r.valid_to, r.valid_time_from, r.valid_time_to,
       r.name, r.descriptor, r.attributes, r.schema_context, r.schema_type,
       r.embedding, r.embedding_source_hash
  FROM resources r
  JOIN (SELECT c.v AS catalog_id, i.v AS resource_id
          FROM unnest(@catalog_ids::text[])  WITH ORDINALITY AS c(v, n)
          JOIN unnest(@resource_ids::text[]) WITH ORDINALITY AS i(v, n) USING (n)) AS p
    ON r.catalog_id = p.catalog_id AND r.id = p.resource_id
 WHERE (sqlc.narg('network_id')::text IS NULL
        OR r.visible_to @> ARRAY[sqlc.narg('network_id')::text])
   AND r.active
   AND (r.valid_from IS NULL OR r.valid_from <= now())
   AND (r.valid_to   IS NULL OR r.valid_to   >= now())
   AND within_daily_window(r.valid_time_from, r.valid_time_to,
                           (now() AT TIME ZONE 'UTC')::time);

-- HydrateProviders loads the provider document once per catalog on the page.
--
-- The ONE query in the read path that touches `catalogs`, and it runs after the
-- page is decided — roughly twenty rows by primary key. That is the whole
-- reason the scope gate was copied onto `resources`: a retriever that joined
-- here would probe this table once per match rather than once per page.
--
-- No gate. The gate columns on this row are the source the resources' copies
-- were written from, and every resource on the page has already been through
-- them; re-testing here would reject a catalog for a state its own resources
-- were just admitted under, which can only be a bug.
--
-- name: HydrateProviders :many
SELECT c.id, c.provider, c.visible_to, c.active,
       c.valid_from, c.valid_to, c.valid_time_from, c.valid_time_to
  FROM catalogs c
 WHERE c.id = ANY(@catalog_ids::text[]);

-- HydrateGeometries loads the shapes belonging to the page.
--
-- Both kinds: the catalog-level rows (NULL resource_id), which belong to every
-- resource of their catalog, and the resource-level rows for the page's own
-- resources. Assembling them back onto the domain objects is the adapter's job.
--
-- name: HydrateGeometries :many
SELECT g.catalog_id, g.resource_id, g.target_path, g.source_path, g.geojson
  FROM resource_geometries g
 WHERE g.catalog_id = ANY(@catalog_ids::text[])
   AND (g.resource_id IS NULL
        OR EXISTS (SELECT 1
                     FROM unnest(@catalog_ids::text[])  WITH ORDINALITY AS c(v, n)
                     JOIN unnest(@resource_ids::text[]) WITH ORDINALITY AS i(v, n) USING (n)
                    WHERE c.v = g.catalog_id
                      AND i.v = g.resource_id));

-- HydrateOffers loads the offers that touch the page, and only those.
--
-- A caller who searched for wheat gets the offers on the wheat plus any
-- catalog-wide offer, not the other thirty-eight offers in that catalog.
--
-- The page arrives FLATTENED, as two text[]s of equal length holding one
-- (catalog_id, resource_id) pair per element. The pairing is what matters: a
-- flat `resource_ids && all_matched_ids` would let catalog A's offer match
-- because catalog B happens to hold a resource of the same id. One row per
-- catalog carrying that catalog's ids is not an option — `text[][]` is
-- rectangular by definition, and a page holding 3 matches in one catalog and 1
-- in another is rejected outright.
--
-- An EMPTY `resource_ids` is CATALOG-WIDE and therefore always applies. It is
-- never "no resources yet".
--
-- Offer validity is checked here and nowhere else: a live catalog routinely
-- carries last month's offer, so the catalog's own gate does not cover it.
--
-- name: HydrateOffers :many
SELECT o.catalog_id, o.id, o.resource_ids, o.offer,
       o.valid_from, o.valid_to, o.valid_time_from, o.valid_time_to
  FROM offers o
 WHERE o.catalog_id = ANY(@catalog_ids::text[])
   AND (cardinality(o.resource_ids) = 0
        OR EXISTS (SELECT 1
                     FROM unnest(@catalog_ids::text[])  WITH ORDINALITY AS c(v, n)
                     JOIN unnest(@resource_ids::text[]) WITH ORDINALITY AS i(v, n) USING (n)
                    WHERE c.v = o.catalog_id
                      AND i.v = ANY(o.resource_ids)))
   AND (o.valid_from IS NULL OR o.valid_from <= now())
   AND (o.valid_to   IS NULL OR o.valid_to   >= now())
   AND within_daily_window(o.valid_time_from, o.valid_time_to,
                           (now() AT TIME ZONE 'UTC')::time);
