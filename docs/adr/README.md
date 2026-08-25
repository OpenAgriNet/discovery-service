# Architecture Decision Records

One file per decision, numbered, never edited after acceptance — a decision
that turns out wrong is **superseded** by a new record, not rewritten. The
point of the log is to answer *why does this look the way it does*, and a
rewritten record cannot answer it.

`docs/design/discover-and-publish.md` is the specification and stays the
binding document. These records carry the reasoning behind it: what was
rejected, and the specific property that disqualified it.

Start from [`0000-template.md`](0000-template.md).

| # | Decision | Source |
|---|---|---|
| [0001](0001-router-chi-v5.md) | chi v5 for HTTP routing | D1 |
| [0002](0002-data-access-sqlc-pgx.md) | sqlc over pgx/v5 for data access | D2 |
| [0003](0003-dependency-injection-explicit-constructors.md) | Explicit constructors for dependency injection | D3 |
| [0004](0004-l1-validation-kin-openapi.md) | kin-openapi for L1 schema validation | D4 |
| [0005](0005-spatial-index-h3-not-postgis.md) | H3 cell indexing instead of PostGIS | D5 |
| [0006](0006-vector-store-pgvector-hnsw.md) | pgvector 0.8 with HNSW for the vector store | D6 |
| [0007](0007-lexical-search-postgresql-fts.md) | PostgreSQL full-text search for the lexical mode | D7 |
| [0008](0008-embeddings-behind-an-embedder-seam.md) | Embeddings behind an `Embedder` seam, `noop` by default | D8, A5 |
| [0009](0009-layered-configuration-env-and-yaml.md) | Four-layer configuration, environment on top | D9, T1 |
| [0010](0010-migrations-golang-migrate.md) | golang-migrate for schema migrations | D10 |
| [0011](0011-telemetry-opentelemetry.md) | OpenTelemetry for traces and metrics | D11, T2 |
| [0012](0012-which-interfaces-are-promises.md) | Which interfaces are promises and which are internal | T5 |
| [0013](0013-protocol-version-coexistence.md) | Protocol version coexistence, recorded but not built | T5 |
| [0014](0014-seams-that-ship-with-only-a-no-op.md) | `CatalogReplicator` and `Keyring`: what a seam must carry to ship | A7 |
| [0015](0015-master-catalogs-and-inheritance-refused.md) | Master catalogs and resource inheritance are refused at intake | A1 |

**Source** points at the design document: `D` rows are the Technology Decisions
table, `T` rows are TRD Alignment, `A` rows are Amendments.
