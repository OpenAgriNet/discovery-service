# ADR-0004 — kin-openapi for L1 schema validation

**Status:** Accepted
**Date:** 2026-08-25

## Context

Every request body must be validated against the published Beckn v2.0.0
`beckn.yaml`. The specification is owned upstream and changes without this
service being consulted.

## Decision

kin-openapi (MIT) validates request bodies against the fetched specification.
The specification is loaded at boot from `VALIDATION_SPEC_URL`, cached under
`.cache/beckn/`, and indexed by `context.action` — not by URL, because the two
accepted spellings of the publish action resolve to one schema (C2).

## Alternatives considered

- **Hand-rolled validation** — a second description of a schema somebody else
  owns, guaranteed to drift from it and to drift silently.

## Consequences

The published `beckn.yaml` *is* the validator, so a specification change is
picked up by restarting rather than by writing code. Envelope rules that the
schema cannot express — `Context` declares no `required` list (C6) — are
enforced separately by struct tags and run even when L1 is switched off.
