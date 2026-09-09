# ADR-0012 — Which interfaces are promises and which are internal

**Status:** Accepted
**Date:** 2026-08-25

## Context

TRD §5 requires that the system not be tied to one database, and TRD §2 asks
which parts of the design are stable. An interface list alone answers neither:
a Go interface says nothing about whether a second implementation is expected,
and an abstraction nobody has implemented twice is a guess about the future.

## Decision

Four interfaces are **promises** — a second implementation may arrive behind
them, and their shape is deliberately storage-neutral:

| Promise | What may be swapped | Cost of the swap |
|---|---|---|
| `CatalogRepository` | the metadata store and its transactions | one package under `src/storage/` plus one line in `container.go` |
| `SearchRepository`, split into `Retriever` per mode + `Hydrator` | the vector store; the geo index | one `Retriever` each |
| `Embedder` | the inference backend | one file under `src/indexing/embeddings/` |
| `registry.Keyring` | the participant registry | one file under `src/platform/registry/` |

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
