# ADR-0014 — `CatalogReplicator` and `Keyring`: what a seam must carry to ship

**Status:** Accepted
**Date:** 2026-08-25

## Context

ADR-0012 states the rule: *a seam ships with a conformance test or a second
implementation behind it, or it does not ship.* Two seams appear to violate it.

`CatalogReplicator` (A7) is publish's write fan-out. Phase 1 has exactly one
store, so the only implementation is a no-op. `registry.Keyring` is the seam to
the participant registry, and the registry is another team's and does not exist
yet, so the only implementation is a static env-backed map. Neither has a second
implementation, and neither can have a conformance suite in the sense
`src/storage/conformance/` means it — there is nothing to hold two backends
against each other.

The same amendment also *removed* something for looking exactly like these two:
`pending_targets`, a column written on every resource insert and read by
nothing. If a no-op replicator is acceptable and a dead column is not, the
difference has to be stated, or the rule is applied by taste.

## Decision

**Both seams ship, and the distinction that admits them is construction and
exercise, not implementation count.** A seam may ship with a single trivial
implementation when:

1. **A task constructs it in the composition root.** `CatalogReplicator` is
   built in `container.go` (Task 20) and injected into the publish handler by
   the same explicit constructor wiring as everything else. It is one line, and
   that line is why Task 20 appears in A7's task list — a seam nothing builds
   is not a seam, it is a type declaration.
2. **A test drives it through its real call site.** `publishOne` calls
   `Replicate` after `UpsertCatalog` returns and the transaction has committed
   — never inside it, because a fan-out that runs before commit can announce a
   catalog that then rolls back. A failed replication is logged and does not
   change the verdict. That ordering is a behavioural contract, it is testable
   today against the no-op, and it is the part a second store would otherwise
   get wrong.

**Its shape is constrained so it cannot drift into a second definition of a
catalog.** `Replicate(ctx, catalogID string) error` takes an id, not a catalog.
A second store re-reads through `GetCatalog`. Passing the aggregate would make
this interface a parallel description of what a catalog is, and two descriptions
disagree eventually.

**What does not ship is the queue.** A7 named a reconciliation queue; no queue
table is created. A queue with no consumer is `pending_targets` again — write
cost on the hot path for a feature that does not exist. The table arrives with
the second store that needs one, and so does the reconciler that reads it.

`Keyring` ships on the same terms: constructed in the container, exercised by
Task 6's tests through `Verify` (unknown `keyId` fails distinguishably from a
bad signature), behind `AUTH_ENABLE_SIGNATURE_VERIFICATION=false`.

## Alternatives considered

- **Not shipping either seam until a second implementation exists** — the
  ordering constraint above (fan-out strictly after commit) would then be
  discovered by whoever adds the second store, under deadline, from a bug
  report about a catalog announced and then rolled back.
- **Shipping the queue table now** — rejected for the reason A7 removed
  `pending_targets`: unread writes on the hot table are debt recorded in the
  place it costs most.
- **`Replicate(ctx, catalog domain.Catalog)`** — saves the second store a read
  and makes the interface a second definition of the aggregate.

## Consequences

Two interfaces exist with one trivial implementation each, which is a cost paid
in indirection at every call site. What is bought is that the hard part —
*when* replication runs relative to the transaction, and how a failure affects
the verdict — is decided and pinned now rather than inferred later. The rule
this ADR draws is falsifiable: a seam that no `container.go` line constructs, or
that no test reaches through its real call site, does not meet it, and
`pending_targets` is the worked example of what failing it looks like.
