# ADR-0012 — Which interfaces are promises and which are internal

**Status:** Accepted
**Date:** 2026-08-25
**Amended:** 2026-09-07 — see Amendments

## Context

TRD §5 requires that the system not be tied to one database, and TRD §2 asks
which parts of the design are stable. An interface list alone answers neither:
a Go interface says nothing about whether a second implementation is expected,
and an abstraction nobody has implemented twice is a guess about the future.

## Decision

Three interfaces are **promises** — a second implementation may arrive behind
them, and their shape is deliberately storage-neutral:

| Promise | What may be swapped | Cost of the swap |
|---|---|---|
| `CatalogRepository` | the metadata store and its transactions | one package under `src/storage/` plus one line in `container.go` |
| `SearchRepository`, split into `Retriever` per mode + `Hydrator` | the vector store; the geo index | one `Retriever` each |
| `Embedder` | the inference backend | one file under `src/indexing/embeddings/` |

`registry.Keyring` is **not** on that list. It is a promise the parked Task 6
will make, not one this build has made — see the amendment below.

Everything else — services, controllers, mappers, the validation chain — is
**internal**. Concrete types, changed freely, no compatibility owed.

The rule that keeps this from being a wish list: **a seam ships with a
conformance test or a second implementation behind it, or it does not ship.**
Config knobs meet the same bar — a flag no scenario sets is not shipped. The
`memory` backend is the second implementation for the repository ports and the
only permitted double for them; `src/storage/conformance/` is the single suite
both backends pass, and `tests/architecture/boundary_test.go` asserts over the
import graph that no capability package reaches a driver.

## Alternatives considered

- **Declaring every interface a promise** — costless to write and unfalsifiable.
  Some of these seams will never see a second implementation, and saying so is
  more useful than a blanket guarantee nobody plans to honour.
- **Per-file mocks for the repository ports** — a mock written by the test that
  asserts on it proves only that both were written by the same person. Shared
  conformance fixtures are the one thing keeping the two backends from drifting.

## Consequences

Adding a storage backend means passing `conformance/` and nothing else; adding
one that passes it but breaks the import graph fails `boundary_test.go` rather
than review. The cost is that the memory backend is real code with real
behaviour to maintain, including the parts of the port nobody uses yet — which
is the price of the port being a promise rather than a claim.

## Amendments

**2026-09-07.** This ADR read "Four interfaces are **promises**" and the fourth
row was `registry.Keyring`. There is no such interface: `src/platform/registry/`
holds a `.gitkeep` and nothing else, and no package imports it.

The correction is not a change of mind about the registry seam. It is this ADR
applying its own rule to itself — *a seam ships with a conformance test or a
second implementation behind it, or it does not ship.* An interface that is not
declared fails that bar more completely than any of the cases the rule was
written to catch, and a promise table listing one is a table a reader cannot
check against the tree.

`Keyring` is designed, in Task 6 of `docs/design/discover-and-publish.md`, and
that task is **parked**: nothing below its heading is implemented, and it is
kept so Phase 2 restarts from a written design rather than from scratch. What
holds the line in the meantime is `validateAuth` in `src/platform/config`, which
refuses the boot when `AUTH_ENABLE_SIGNATURE_VERIFICATION=true`, because a flag
reporting a control that is not running is worse than no flag. When Task 6 is
built, `registry.Keyring` returns to the table above with the swap cost it
always had.

ADR-0014 named `Keyring` in its title on the strength of this row and is
amended in step.
