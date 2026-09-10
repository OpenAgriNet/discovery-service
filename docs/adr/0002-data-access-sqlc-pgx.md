# ADR-0002 — sqlc over pgx/v5 for data access

**Status:** Accepted
**Date:** 2026-08-25

## Context

The read path runs hand-written SQL — H3 array predicates, `tsvector` ranking,
reciprocal rank fusion — that no ORM expresses. The write path runs upserts
under a row lock. Both must fail loudly when the schema moves underneath them.

## Decision

sqlc generates typed Go from `.sql` files in `src/storage/postgres/queries/`,
checked against the migrations, executed through pgx/v5. Generated code lives
in `src/storage/postgres/gen/` and is regenerated, never edited.

## Alternatives considered

- **GORM** — resolves columns by reflection at runtime, so a renamed column
  fails in production. sqlc fails at `make build`. That is the whole argument.
- **Hand-rolled `database/sql`** — the same SQL with the row scanning written
  by hand, which is the part sqlc removes and the part that rots.

## Consequences

Query authoring is a two-step loop: write SQL, run `make sqlc`. `make
sqlc-verify` fails CI when the committed layer is stale. Queries sqlc cannot
type — dynamic predicate assembly, in particular — are written directly against
pgx rather than forced through the generator, and stay parameterised either way.
