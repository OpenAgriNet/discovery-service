-- Named ahead of the DROP for the reason Migration 003's down gives: this list
-- is the inventory a reviewer reads against the up migration.
DROP INDEX IF EXISTS idx_rg_catalog_target_path;
DROP INDEX IF EXISTS idx_rg_catalog_resource;
DROP INDEX IF EXISTS idx_rg_cells_cover;
DROP INDEX IF EXISTS idx_rg_cells_full;
DROP INDEX IF EXISTS uq_resource_geometries;
DROP TABLE IF EXISTS resource_geometries;
