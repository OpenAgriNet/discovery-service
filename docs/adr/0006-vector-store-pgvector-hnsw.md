# ADR-0006 — pgvector 0.8 with HNSW for the vector store

**Status:** Accepted
**Date:** 2026-08-25

## Context

Semantic retrieval is one of the modes fused into the discover result. It is
deferred for Phase 1 (ADR-0008), but the column and the index ship now so that
turning it on is a backfill rather than a migration.

## Decision

pgvector 0.8 with an HNSW index and cosine distance, in the same PostgreSQL
instance as everything else. The `embedding` column is nullable, and
`embedding IS NULL` doubles as the backfill queue.

## Alternatives considered

- **Qdrant, OpenSearch** — a second datastore to deploy, back up and secure for
  one of four retrieval modes, on a single-node deployment.

## Consequences

No extra infrastructure. The vector store is swappable behind a single
`Retriever` (ADR-0012) — that is the *vector* store only, and it is the
cheapest of the three swaps TRD §5 names precisely because it hides behind one
interface with one implementation and one conformance suite.
