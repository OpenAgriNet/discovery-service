# Implementation Prompts — Discover & Publish

Copy-paste prompts for driving a fresh Claude Code session/agent through
`docs/design/discover-and-publish.md`, one task at a time. The template below
is the same for every task — only the task number/name changes.

## How to use this

1. Start a fresh session (or continue this one) with no other work in flight.
2. Paste the **Per-task prompt** below, filling in `{N}` and `{TASK_NAME}`
   from the checklist.
3. Review the diff and the pasted test output before saying "go" on the next
   task. Do not batch multiple tasks into one prompt — the whole point is a
   review gate between each one.
4. Tick the checklist as tasks land.

Tasks are dependency-ordered — run them in numeric order, no exceptions. Task 6
ships its primitive without wiring it live, and Task 22 is Phase 1 despite
sounding deferred — see the notes under the checklist for both.

---

## Per-task prompt

```
Implement Task {N} — {TASK_NAME} from docs/design/discover-and-publish.md,
and only Task {N}.

Rules:
- Follow the task's own steps literally, in TDD order (failing test/check
  first, then the minimal thing that makes it pass).
- The Global Constraints table (near the top of the doc) applies to
  everything you write, not just this task.
- Code blocks marked `pseudo` in the doc are intent, not literal source —
  write idiomatic Go against the interfaces the task names. DDL, SQL
  predicates, and wire shapes are literal contracts — copy them as-is.
- Run this task's own "Tests pin:" / "Test:" command and paste the actual
  output before claiming the task is done.
- One commit per task step marked *Commit*, Conventional Commits format.
- No TODO left on main — anything you'd defer belongs back in the doc's
  Deferred/Out of Scope section, flagged to me, not left in a comment.
- If you hit a genuine ambiguity the doc doesn't resolve (check Spec
  Conflicts, Amendments, and Open Items first — most things are already
  decided there), stop and ask me rather than guessing.
- Before you say you're done, self-review: re-read this task's own section of
  docs/design/discover-and-publish.md side-by-side with your diff. Check every
  file, type, function signature, and behavior it names is actually there,
  under the name it uses, and that nothing you wrote drifted from it or from
  the Global Constraints table. Include this as a short checklist in your
  summary (matched / deviated, with a reason for each deviation) — not a
  silent pass.

When the task's tests pass, self-review is done, and it's committed, stop and
summarize what you built — don't continue to the next task until I say go.
```

---

## Task checklist

| # | Task | Notes |
|---|---|---|
| 1 | Repository Bootstrap & Toolchain | Run first — every later `make` target depends on it |
| 2 | Configuration & Feature Flags | |
| 3 | Structured Logging | |
| 4 | Beckn Wire Types & Envelope Parsing | |
| 5 | Error Model & Response Writer | |
| 6 | Ed25519 Signature Primitives | *Deferred, but built* — implement the primitive now, but `AUTH_ENABLE_SIGNATURE_VERIFICATION` stays `false`. Don't wire it live ahead of schedule |
| 7 | Signature & Envelope Middleware | |
| 8 | Request Logger & Rate Limit Middleware | Also builds `Trace` as a **no-op pass-through** that stamps `X-Beckn-Trace-Seen: 1` — the marker exists purely so Task 20's order test can observe `Trace`'s position before Task 23 gives it a real body |
| 9 | L1 Schema Validation | |
| 10 | L2 Extended Schema Validation | |
| 11 | Domain Model & The DB-Agnostic Boundary | `purity_test.go` lives here — the import-boundary gate every later task is checked against. Also **scaffolds** `storage/memory/repository.go` (both port interfaces, no behavior) and `storage/conformance/` (fixture types, no fixtures) — Tasks 12, 15, 16 modify these as they add behavior; this task creates them |
| 12 | H3 Geospatial Indexing | Modifies `storage/memory/repository.go` — adds the spatial-matching behavior (`MatchesOp`, bounding-box stage) the memory backend needs |
| 13 | Text Derivation & Embeddings | Ships with `EMBEDDING_PROVIDER=noop` — don't turn semantic search on |
| 14 | PostgreSQL Schema, Indexes & Test Harness | Needs Postgres/pgvector reachable (`docker-compose` from Task 1) |
| 15 | PostgreSQL Catalog Repository (Write) | Modifies `storage/memory/repository.go` — adds the write-side behavior (`UpsertCatalog`, `DeleteCatalog`, `GetCatalog`) |
| 16 | PostgreSQL Search Repository (Read) | Modifies `storage/memory/repository.go` — adds `Search`; this is where the memory/Postgres conformance suite actually starts asserting parity |
| 17 | Wire-to-Domain Mapping | |
| 18 | Publish Service & Controller | Tests pin includes the `stats.itemCount`/`providerCount`/`categoryCount` values (C5/C12), not just the publish flow |
| 19 | Discover Service & Controller | |
| 20 | Container, Router & Server Lifecycle | First point the service actually boots end-to-end. Wires the full middleware chain, order-tested by observing side effects — including `Trace`'s `X-Beckn-Trace-Seen` marker surviving a `Recover`-caught panic, which is what proves `Trace` wraps `Recover` |
| 21 | End-to-End Acceptance Suite | The 35 scenarios in the doc's Scenarios section get pinned here, over real HTTP against a real Postgres — this is the integration/e2e layer, not unit tests. Covers publish and discover each in their own `_test.go` file, plus offers, validity, performance, defaults, geopath and spatial-operator groups. Runs against **Postgres only** — memory-backend parity is `storage/conformance`'s job (Tasks 11/12/15/16), not this suite's |
| 22 | Structured Attribute Filtering | *Phase 1, per the doc's Open Items table* — do not skip unless you've deliberately decided to push it to Phase 2. Validation + rebase live in `src/platform/jsonpath/subset.go` (backend-agnostic, beside `Canonicalise`); `storage/postgres/jsonpath.go` only casts/executes the already-accepted expression |
| 23 | OpenTelemetry Tracing & Metrics | Replaces Task 8's no-op `Trace` body with real `otelhttp` instrumentation and drops the `X-Beckn-Trace-Seen` marker — the chain itself doesn't move |

---

## Definition of done (whole plan)

Not a single task — run after Task 21, and again after any later task:

```
make build && make lint && make test
```

Green, with all 35 scenarios passing against Postgres (Task 21) **and** the
`storage/conformance` suite passing for the memory backend (built
incrementally by Tasks 11, 12, 15 and 16 — these are two different test
layers, not the same 35 scenarios run twice), and no `TODO` on `main`.
