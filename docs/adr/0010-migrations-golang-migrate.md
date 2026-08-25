# ADR-0010 — golang-migrate for schema migrations

**Status:** Accepted
**Date:** 2026-08-25

## Context

The schema is also the input to sqlc (ADR-0002), so migrations must be plain
SQL that both PostgreSQL and a code generator can read. The deployed binary
should be able to migrate itself rather than depend on a separate job.

## Decision

`golang-migrate/v4`, plain `.up.sql` / `.down.sql` pairs in `migrations/`,
`//go:embed`-able so the binary carries them.

## Alternatives considered

- **goose, atlas** — atlas infers migrations from a declared desired state,
  which is powerful and is a second description of the schema. goose is close
  enough that the embed story decided it.

## Consequences

Migrations are readable SQL with no tooling in the way, and `make migrate`
needs only a `DATABASE_URL`. The CLI must be built with the `postgres` build
tag — without it the binary compiles and then reports every migration URL as an
unknown scheme, which is why the Makefile passes the tag explicitly.
