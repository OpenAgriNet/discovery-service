-- geo_distance_m before geo_haversine_m: the first calls the second, and
-- PostgreSQL does not track that dependency across SQL function bodies, so the
-- order is a statement of intent rather than something the engine enforces.
-- Reversed, both DROPs still succeed and geo_distance_m spends the interval
-- referring to a function that is gone.
DROP FUNCTION IF EXISTS within_daily_window(TIME, TIME, TIME);
DROP FUNCTION IF EXISTS discover_tsquery(TEXT);
DROP FUNCTION IF EXISTS geo_distance_m(JSONB, DOUBLE PRECISION, DOUBLE PRECISION);
DROP FUNCTION IF EXISTS geo_haversine_m(DOUBLE PRECISION, DOUBLE PRECISION, DOUBLE PRECISION, DOUBLE PRECISION);
