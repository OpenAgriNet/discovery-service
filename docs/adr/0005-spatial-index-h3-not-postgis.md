# ADR-0005 — H3 cell indexing instead of PostGIS

**Status:** Accepted
**Date:** 2026-08-25

## Context

Discovery is geographic: catalogs carry service areas and queries carry
constraint geometries. The obvious answer is PostGIS, and the requirement in
TRD §5 is that no part of this system be tied to one database.

## Decision

uber/h3-go v4 (Apache-2.0). Geometries are stored as sorted, deduplicated
`BIGINT[]` cell covers. No spatial extension is installed.

## Alternatives considered

- **PostGIS** — exact, and it welds the geo layer to PostgreSQL. Its predicates
  have no equivalent in Elasticsearch or Redis, so choosing it would have made
  the geo swap the most expensive of TRD §5's three rather than the cheapest.

## Consequences

Cells are plain integers: GIN-indexable, shardable, and portable to any store
with set operators. Accuracy is bounded by one cell (~1.1 km at r8), which is
right for discovery and wrong for deciding which side of a survey boundary a
plot sits on. Cadastral precision would mean adopting PostGIS after all, and is
a dependency worth taking only against a requirement that exists. How the
operators are actually answered over these covers is ADR-0014.
