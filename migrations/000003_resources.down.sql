-- The six indexes go with the table. Naming them anyway, ahead of the DROP,
-- because this file is also the inventory a reviewer reads against Migration
-- 003's up: an index added there and forgotten here shows up as a line missing
-- from a list rather than as nothing at all.
DROP INDEX IF EXISTS idx_resources_embedding;
DROP INDEX IF EXISTS idx_resources_document;
DROP INDEX IF EXISTS idx_resources_schema;
DROP INDEX IF EXISTS idx_resources_name_trgm;
DROP INDEX IF EXISTS idx_resources_search_tsv;
DROP INDEX IF EXISTS idx_resources_visible_to;
DROP TABLE IF EXISTS resources;
