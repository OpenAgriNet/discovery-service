# ADR-0008 — Embeddings behind an `Embedder` seam, `noop` by default

**Status:** Accepted
**Date:** 2026-08-25

## Context

Publish-time embedding costs 15–40 ms of inference on the write path, for one
retrieval mode of four, and the self-hosted inference deployment it needs does
not exist yet. The Digital Public Good mandate rules out sending catalog text
to a hosted API.

## Decision

An `Embedder` interface with four implementations: `noop` (production default),
`hashing` (deterministic, used by every test target and by CI), `fixture`, and
`ollama` for when a self-hosted deployment exists. Selected by
`EMBEDDING_PROVIDER`.

## Alternatives considered

- **OpenAI, Cohere** — a hosted API for catalog text, which the DPG mandate
  does not permit.
- **Shipping no seam at all until embeddings are wanted** — the column, index
  and dimension guard would then arrive as a migration on a populated table
  rather than as a backfill.

## Consequences

Test targets and CI pin `EMBEDDING_PROVIDER=hashing` rather than inheriting the
production default. Without that pin the entire semantic path — query
embedding, HNSW, fusion, the dimension guard, the degradation report — would go
untested from the day semantic search was deferred, which is exactly when it
stops being exercised by accident. On a fresh deployment the semantic mode is
absent, and its absence degrades rather than refuses (ADR-0013's sibling
decision, C11).
