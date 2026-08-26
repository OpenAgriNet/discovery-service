-- Reverse order, so that if a later schema ever comes to depend on one of these
-- the failure names the extension still in use rather than the one after it.
DROP EXTENSION IF EXISTS pg_trgm;
DROP EXTENSION IF EXISTS vector;
