# ADR-0007 — PostgreSQL full-text search for the lexical mode

**Status:** Accepted
**Date:** 2026-08-25

## Context

Lexical retrieval is the mode that carries Phase 1: with embeddings deferred
(ADR-0008), it and the geo mode are the two that run on a fresh deployment.

## Decision

PostgreSQL `tsvector` with a GIN index, fused with the other modes by
reciprocal rank fusion.

## Alternatives considered

- **Elasticsearch** — better ranking and a cluster to operate. For v1 the
  ranking difference does not justify the second system; the `Retriever` seam
  is where it goes if that changes.

## Consequences

No extra infrastructure, and one transaction spanning metadata, geo and text.
Ranking quality is PostgreSQL's rather than Lucene's, and language handling is
whatever the configured text search dictionary supports.
