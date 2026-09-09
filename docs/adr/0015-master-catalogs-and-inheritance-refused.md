# ADR-0015 — Master catalogs and resource inheritance are refused at intake

**Status:** Accepted
**Date:** 2026-08-25

## Context

Beckn v2.0.0 has a catalog inheritance model: a `MASTER` catalog defines
resources, and a later catalog's `resourceDirectives` may carry `extends` to
inherit from them. Implementing it means resolving a reference graph at publish
time, deciding what happens when a master changes under its children, and
carrying `master_catalog_id` / `master_resource_id` / `variant` through every
read path.

The product decision for Phase 1 is REGULAR only. The question this ADR settles
is not *whether* to build inheritance — it is what a publisher who sends a
master catalog gets back, and where in the system that refusal lives.

Two things make the naive answer wrong. First, the specification **infers** the
catalog type from content when `publishDirectives` is absent: a catalog with
offers is `regular`, a catalog with only resources is `master` (C9). Honouring
that inference would reject every ordinary catalog that happens to carry no
offers — the common case, and a refusal the publisher could not have predicted
from anything they wrote. Second, publish is a batch: one request carries many
catalogs, and one unsupported catalog must not fail the nine beside it.

## Decision

**Only an explicit `catalogType: MASTER`, or a `resourceDirective` carrying a
non-empty `extends`, is refused. An absent directive is REGULAR, not inferred**
(C9, a recorded deviation from the specification).

**Refusal is per catalog, at intake, in the mapper.** The mapper returns
`REJECTED` with `SCH_TYPE_NOT_SUPPORTED` and a `details.path` JSONPath naming
the offending directive. Nine regular catalogs land and the tenth reports
`REJECTED`, in one 200 response carrying ten verdicts — the request was
well-formed, so the HTTP status stays 200 even when every catalog was rejected.

**Nothing about inheritance reaches the schema.** `catalog_type`,
`master_catalog_id`, `master_resource_id` and `variant` are not columns.
`catalog_type` could only ever hold `REGULAR` while this ADR stands, and the
other three would be NULL for ever.

**The refusal says "not approximable here", not "not yet built" in the vague
sense** — it names inheritance as unsupported so a caller can tell whether to
wait for a release or restructure their catalog.

## Alternatives considered

- **Accepting MASTER and ignoring `extends`** — stores a catalog whose
  resources silently lack everything they were declared to inherit. The
  publisher gets a 200 and a catalog that is wrong, which is the failure mode
  no downstream consumer can detect.
- **Honouring the specification's content-based inference (C9)** — turns every
  offer-less catalog into a rejection. A default that breaks the common case is
  a default nobody keeps.
- **Rejecting the whole request when any catalog is a master** — punishes nine
  well-formed catalogs for the tenth, in a protocol whose publish call is
  explicitly a batch.
- **Shipping the columns now against a later migration** — four columns that are
  NULL or constant for ever, carried through every read path, for a feature with
  no delivery date. The same argument that removed `pending_targets` (ADR-0014).

## Consequences

Refusal lives entirely in `src/publish/mapper.go`, so lifting it later is a
mapper change plus a migration, not a change to how publish is shaped. The
scenario suite pins the refusal rather than the feature —
`MasterCatalogAndInheritanceAreRefused` asserts that both a MASTER catalog and a
child carrying `extends` come back `REJECTED` / `SCH_TYPE_NOT_SUPPORTED` and
that neither is stored. It is deliberately not a test that inheritance works: it
is a test that inheritance is refused, visibly.

This is a **recorded deviation** from Beckn v2.0.0, not an omission. An
undocumented deviation is indistinguishable from a bug.
