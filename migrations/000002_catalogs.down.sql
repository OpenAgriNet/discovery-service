-- The GIN index goes with the table, but it is named here because it is the
-- one object on `catalogs` that a partially-applied migration could leave
-- behind, and DROP TABLE would then fail on a name it does not own.
DROP INDEX IF EXISTS idx_catalogs_document;
DROP TABLE IF EXISTS catalogs;
