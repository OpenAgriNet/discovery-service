# ADR-0013 — Protocol version coexistence, recorded but not built

**Status:** Accepted
**Date:** 2026-08-25

## Context

Beckn v2.0.0 is the only version this service accepts today. TRD §9 asks what
happens when a network runs two protocol versions at once, which is the normal
state of an ecosystem mid-migration. Answering it by building it now would be
building against one known version and one imagined one.

## Decision

The shape is recorded and deliberately not implemented:

- `SpecIndex` becomes **version-keyed** — one loaded specification per accepted
  version, indexed by `(version, action)` rather than by `action` alone.
- An **accepted-versions set** in config replaces the single pinned `2.0.0`
  envelope rule; a version outside the set is a `CTX_` fault, not a schema
  fault, so it is answered before any schema is selected.
- The **response echoes the request's version**, never the server's preferred
  one. A responder that upgrades the version in its reply has changed the
  contract mid-conversation.

Until that is built, `context.version` must equal `2.0.0` (C6), enforced by
envelope struct tags that run even when L1 validation is off.

## Alternatives considered

- **Building it now** — a version-keyed index with exactly one key is an
  abstraction with no second implementation behind it, which the rule in
  ADR-0012 refuses.
- **Recording nothing** — the first person to add a version would have to
  rediscover that echoing the request's version is not optional, and would
  likely discover it from a downstream bug report.

## Consequences

The single-version path stays simple and the migration path is not left to be
reinvented. The cost is a design in a document rather than in code, which ages
against a specification nobody has published yet — it is a starting point for
the person who builds it, not a specification of what they must build.
