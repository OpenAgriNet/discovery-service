-- Two extensions, and no PostGIS: H3 cells are plain BIGINTs computed in Go,
-- full-text search and SQL/JSON path are core PostgreSQL.
CREATE EXTENSION IF NOT EXISTS vector;
-- Trigram similarity for the misspellings stemming cannot recover ("tracter").
CREATE EXTENSION IF NOT EXISTS pg_trgm;
