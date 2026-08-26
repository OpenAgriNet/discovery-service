-- No index to drop first: this table deliberately carries nothing beyond the
-- btree PostgreSQL builds for its primary key, and that goes with the table.
DROP TABLE IF EXISTS catalogs;
