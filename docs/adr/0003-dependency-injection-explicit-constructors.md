# ADR-0003 — Explicit constructors for dependency injection

**Status:** Accepted
**Date:** 2026-08-25

## Context

The service has one composition root (`src/app/container.go`) assembling a
config, a pool, a logger, a clock, an embedder, several retrievers and a
hydrator into two controllers.

## Decision

Explicit constructors, wired by hand in `container.go`. Reflection-based
containers — `dig`, `wire` — are prohibited. No package-level mutable state and
no `init()` that does work: config, pool, logger, clocks and clients are built
in the container and passed down.

## Alternatives considered

- **`dig`** — a missing provider is a panic at startup. With explicit
  constructors it is a compile error, which is the earlier of the two failures
  and the only one a CI build catches without running the binary.
- **Package-level singletons** — cheaper to write and impossible to substitute
  in a test. A dependency that is injected *and* reachable through a global is
  not injected; it is a suggestion.

## Consequences

`container.go` is long and boring, and it is the one file that has to change
when a dependency is added. That is the intended trade: the wiring is visible
in one place rather than distributed across the registrations that produce it.
