-- The exact reverse of 000001_initial_schema.up.sql, and read in that order:
-- offers, functions, resource_geometries, resources, catalogs, extensions.
--
-- Every index is named ahead of the DROP TABLE that would take it anyway. That
-- is deliberate: this file is also the inventory a reviewer reads against the
-- up migration, so an index added there and forgotten here shows up as a line
-- missing from a list rather than as nothing at all.

-- ═══════════════════════════════════════════════════════════════════════════
-- offers
-- ═══════════════════════════════════════════════════════════════════════════
DROP INDEX IF EXISTS idx_offers_resource_ids;
DROP TABLE IF EXISTS offers;

-- ═══════════════════════════════════════════════════════════════════════════
-- Functions
-- ═══════════════════════════════════════════════════════════════════════════
-- geo_distance_m before geo_haversine_m: the first calls the second, and
-- PostgreSQL does not track that dependency across SQL function bodies, so the
-- order is a statement of intent rather than something the engine enforces.
-- Reversed, both DROPs still succeed and geo_distance_m spends the interval
-- referring to a function that is gone.
DROP FUNCTION IF EXISTS within_daily_window(TIME, TIME, TIME);
DROP FUNCTION IF EXISTS discover_tsquery(TEXT);
DROP FUNCTION IF EXISTS geo_distance_m(JSONB, DOUBLE PRECISION, DOUBLE PRECISION);
DROP FUNCTION IF EXISTS geo_haversine_m(DOUBLE PRECISION, DOUBLE PRECISION, DOUBLE PRECISION, DOUBLE PRECISION);

-- ═══════════════════════════════════════════════════════════════════════════
-- resource_geometries
-- ═══════════════════════════════════════════════════════════════════════════
DROP INDEX IF EXISTS idx_rg_catalog_target_path;
DROP INDEX IF EXISTS idx_rg_catalog_resource;
DROP INDEX IF EXISTS idx_rg_cells_cover;
DROP INDEX IF EXISTS idx_rg_cells_full;
DROP INDEX IF EXISTS uq_resource_geometries;
DROP TABLE IF EXISTS resource_geometries;

-- ═══════════════════════════════════════════════════════════════════════════
-- resources
-- ═══════════════════════════════════════════════════════════════════════════
DROP INDEX IF EXISTS idx_resources_embedding;
DROP INDEX IF EXISTS idx_resources_filter_doc;
DROP INDEX IF EXISTS idx_resources_schema;
DROP INDEX IF EXISTS idx_resources_name_trgm;
DROP INDEX IF EXISTS idx_resources_search_tsv;
DROP INDEX IF EXISTS idx_resources_visible_to;
DROP TABLE IF EXISTS resources;

-- ═══════════════════════════════════════════════════════════════════════════
-- catalogs
-- ═══════════════════════════════════════════════════════════════════════════
-- Nothing to drop ahead of the table: since A18 `catalogs` carries no index
-- beyond its primary key, and a primary key goes with the table it constrains.
DROP TABLE IF EXISTS catalogs;

-- ═══════════════════════════════════════════════════════════════════════════
-- Extensions
-- ═══════════════════════════════════════════════════════════════════════════
-- Reverse order, so that if a later schema ever comes to depend on one of these
-- the failure names the extension still in use rather than the one after it.
DROP EXTENSION IF EXISTS pg_trgm;
DROP EXTENSION IF EXISTS vector;
