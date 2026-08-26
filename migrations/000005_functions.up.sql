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
