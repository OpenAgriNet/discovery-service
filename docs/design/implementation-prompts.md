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

Tasks are dependency-ordered — run them in numeric order, with one hole in it:
**Task 6 is parked and Task 7 runs at half scope.** Signature verification is
not being implemented, so the Ed25519 primitives are not built and the
`Signature` middleware is not written. Task 22 is Phase 1 despite sounding
deferred — see the notes under the checklist for all three.

**Next task to implement: Task 23, OpenTelemetry Tracing & Metrics.**

Tasks 1-22 have landed, with three holes that are decisions rather than
omissions: **6** is parked, **7** shipped at half scope (`Envelope` only), and
**10** (L2 validation) is deferred. Task 23 is the last one in the plan.

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

## Next task — ready to paste

Task 23. Paste this verbatim into a fresh session.

An earlier version of this section carried Task 8's prompt and stayed there
while twelve tasks landed past it, which is the failure mode of a document that
duplicates state it does not own. The per-task template above is the thing that
does not go stale; this block is only ever the template with one number filled
in, and it is worth keeping only because it saves the paste.

```
Implement Task 23 — OpenTelemetry Tracing & Metrics from
docs/design/discover-and-publish.md, and only Task 23.

Rules:
- Follow the task's own steps literally, in TDD order (failing test first, run
  it, watch it fail, then the minimal thing that makes it pass, then run it
  again). A test you never saw fail pins nothing.
- The Global Constraints table (near the top of the doc) applies to everything
  you write; read it before starting.
- Check Spec Conflicts, Amendments and Open Items before asking a question —
  most things are already decided there, with the reasoning.
- Names from the plan are load-bearing. Do not simplify them.
- Stop when Task 23 is done. Summarize what you built, paste the real output of
  `make build`, `make lint` and `make test`, and wait for review.

What this task specifically replaces: Task 8 shipped `Trace` as a no-op
pass-through whose only effect is appending `trace` to `X-Beckn-Chain`. Task 23
puts real `otelhttp` instrumentation in its place and drops that chain entry.
The chain itself does not move — Task 20's order test reads the remaining
entries and must still pass.

Note before you start: `src/platform/telemetry/` exists but holds only a
`.gitkeep`. ADR-0011 is the accepted decision and describes the shape; the OTel
modules are currently INDIRECT dependencies in go.mod and this task is what
makes them direct.
```

## Task checklist

| # | Task | Notes |
|---|---|---|
| 1 | Repository Bootstrap & Toolchain | Run first — every later `make` target depends on it |
| 2 | Configuration & Feature Flags | |
| 3 | Structured Logging | |
| 4 | Beckn Wire Types & Envelope Parsing | |
| 5 | Error Model & Response Writer | **Settled** — `Error`'s own description makes Level 1 codes canonical and Level 2 (`details.cause`) codes open, so the six invented codes were mapped onto members that exist (table in the task). `SPT_` is not a family; `DOM_` keeps its `error_type` row and gets no constructor. `beckn.ErrorCode` constants are pinned against the fixture's enum |
| 6 | Ed25519 Signature Primitives | **PARKED — do not implement.** Was *deferred, but built*; now not built either. Signature verification is Phase 2 and the key registry is another team's, so the primitive would ship with no caller — and a primitive with no caller is one whose first real caller finds out what it got wrong. The task section is kept as the Phase 2 starting point. What ships instead: the empty slot in the middleware order, and a boot refusal when `AUTH_ENABLE_SIGNATURE_VERIFICATION=true` |
| 7 | Signature & Envelope Middleware | **`Envelope` only** — the `Signature` half is parked with Task 6, which no longer builds the `Keyring` it needs. Do not create `signature.go` and do not stub it: a mounted middleware that does nothing is indistinguishable from a working one at exactly the call sites where it matters. Scenario 7 is now the boot refusal, not the two flag-sides. `Envelope` also carries the **request body ceiling** (C14): it is the only thing that reads a body and it runs before `RateLimit`, so a bound anywhere later is a bound after the allocation |
| 8 | Request Logger & Rate Limit Middleware | Also homes `RequestID` (its own file; it mints rather than trusting an inbound `X-Request-Id`, and it is first in the chain because nothing below it logs until it installs the request-scoped logger) and **departs from A4**: the bucket is keyed on the remote address, not the subscriber id, which on an unverified request is a claim anyone can make about anyone. Subscriber-id keying moved to Deferred, tied to the task that verifies the signature. Also builds `Trace` as a **no-op pass-through** that appends `trace` to `X-Beckn-Chain`, alongside `Recover` appending `recover` — the pair exists so Task 20's order test can read the two back in the order they ran. See Task 8's own section; an earlier draft of this row said `X-Beckn-Trace-Seen: 1`, a single presence marker that cannot carry order. **A11 landed in review of this task**: `RequestLogger` moved above `Recover`, so a panicking request is still timed and logged, and `Recover` now aborts rather than writing a second body over a committed response |
| 9 | L1 Schema Validation | |
| 10 | L2 Extended Schema Validation | |
| 11 | Domain Model & The DB-Agnostic Boundary | `purity_test.go` lives here — the import-boundary gate every later task is checked against. Also **scaffolds** `storage/memory/repository.go` (both port interfaces, no behavior) and `storage/conformance/` (fixture types, no fixtures) — Tasks 12, 15, 16 modify these as they add behavior; this task creates them |
| 12 | H3 Geospatial Indexing | Modifies `storage/memory/repository.go` — adds the spatial-matching behavior (`MatchesOp`, bounding-box stage) the memory backend needs |
| 13 | Text Derivation & Embeddings | Ships with `EMBEDDING_PROVIDER=noop` — don't turn semantic search on. Four open questions from this task sit in **Open questions** (Q1–Q4); Q1, the versioning mechanism, is the one that changes a contract elsewhere. Builds no selector and no `embedding_source_hash` — the hash is the publish path's (plan line ~1484) |
| 14 | PostgreSQL Schema, Indexes & Test Harness | Needs Postgres/pgvector reachable (`docker-compose` from Task 1) |
| 15 | PostgreSQL Catalog Repository (Write) | Modifies `storage/memory/repository.go` — adds the write-side behavior (`UpsertCatalog`, `DeleteCatalog`, `GetCatalog`) |
| 16 | PostgreSQL Search Repository (Read) | Modifies `storage/memory/repository.go` — adds `Search`; this is where the memory/Postgres conformance suite actually starts asserting parity |
| 17 | Wire-to-Domain Mapping | |
| 18 | Publish Service & Controller | Tests pin includes the `stats.itemCount`/`providerCount`/`categoryCount` values (C5/C12), not just the publish flow |
| 19 | Discover Service & Controller | The degraded list is the `X-Beckn-Degraded` **header**, never a body key (C11) — and it is set before `WriteJSON` writes the status line, pinned over a real connection because a `ResponseRecorder` accepts a header set too late to be sent. `query.NetworkID` comes from the request envelope and is **not** defaulted to `APP_NETWORK_ID` (scenario 29). Mapper faults are typed by a switch over code literals, not a conversion, so the `src/platform/errors` enum pin still sees them. Leaves **Q5** in **Open questions** — discover has no channel for a partial fault |
| 20 | Container, Router & Server Lifecycle | First point the service actually boots end-to-end. Wires the full middleware chain **minus `Signature`**, order-tested by observing side effects — specifically the *order of the two entries* `Trace` and `Recover` append to `X-Beckn-Chain`, since both are appended before `Recover` writes its 500 and a mere presence marker therefore survives under either nesting. `RequestLogger`'s position stamps no chain entry, so it is pinned behaviourally instead (A11): a panicking route must produce one completion line at `status = 500` with `X-Response-Time` set. Reads `SERVER_MAX_REQUEST_BODY_BYTES` once and passes it to `Envelope`; sets no second ceiling on the `http.Server` (C14) |
| 21 | End-to-End Acceptance Suite | The 35 scenarios in the doc's Scenarios section get pinned here, over real HTTP against a real Postgres — this is the integration/e2e layer, not unit tests. Covers publish and discover each in their own `_test.go` file, plus offers, validity, performance, defaults, geopath and spatial-operator groups. Runs against **Postgres only** — memory-backend parity is `storage/conformance`'s job (Tasks 11/12/15/16), not this suite's |
| 22 | Structured Attribute Filtering | *Phase 1, per the doc's Open Items table* — do not skip unless you've deliberately decided to push it to Phase 2. Validation + rebase live in `src/platform/jsonpath/subset.go` (backend-agnostic, beside `Canonicalise`); `storage/postgres/jsonpath.go` only casts/executes the already-accepted expression |
| 23 | OpenTelemetry Tracing & Metrics | Replaces Task 8's no-op `Trace` body with real `otelhttp` instrumentation and drops its `trace` entry from `X-Beckn-Chain` — the chain itself doesn't move |

---

## Open questions

Raised while implementing, not blocking. Each names the task it came from and
what was shipped in the meantime, so a decision here is a change to a known
line rather than an archaeology exercise.

| # | From | Question | What ships today |
|---|---|---|---|
| Q1 | 13 | **How is `deriveSearchText` versioned?** The task says it must be "deterministic and **versioned**" but names no mechanism. Two options, and they are not equivalent: (a) a package constant whose bump is a documented reindex step — visible, but nothing reads it, and an unread constant is a comment with a type; (b) fold the version into the hash input, so a bump invalidates every `embedding_source_hash` and the A5 branch re-embeds the corpus by itself — self-enforcing, but the publish pseudocode (line ~1484) says literally `hash ← blake2b256(resource.SearchText)`, so (b) is an edit to that contract, not an implementation detail | Neither. The function is deterministic and its godoc says a change to it lands with a reindex. No version token exists, so today a change to the derivation is caught by review and nothing else |
| Q2 | 13 | **Which JSON value types count as "attribute values"?** Strings only, or also numbers and booleans? A quantity of `50` and a grade of `"A"` are both facts a buyer might search on, but `50` and `true` in a tsvector are terms nobody queries and dilute the ones they do. If numbers should be searchable it is probably as `key=value` pairs, which contradicts stripping keys | Strings only. Numbers, booleans and nulls contribute nothing |
| Q3 | 13 | **Should URI-valued fields be indexed?** `descriptor.thumbnailImage` is `format: uri` in the schema, and `docs[]`/`mediaFile[]` are links. They are strings, so they are indexed as prose today, contributing tokens like `https` and `com` to every resource that has one — terms with no discriminating power, in the index that ranks by exactly that | Indexed as ordinary strings. No URI detection |
| Q4 | 13 | **Where does `EMBEDDING_PROVIDER` become an `Embedder`?** Task 13 names the four providers but no selector, and no composition root exists yet. Until Task 20 wires one, the config field and `make test`'s `EMBEDDING_PROVIDER=hashing` pin select nothing — the pin is inert. `fixture` is the awkward member: its vectors come from a file the config does not name, so a selector has to decide where a fixture table is loaded from | No selector. Each provider has its own constructor and the container task builds the switch |
| Q5 | 19 | **Where does a discover PARTIAL fault go?** The mapper separates fatal faults from partial ones, and the plan says a `distanceMeters` sent with a non-`S_DWITHIN` operator "is a PARTIAL naming the field rather than silence". On the publish path a partial reaches the caller in the ACK's `details`. Discover has no such channel: `OnDiscoverAction` is `additionalProperties: false` with `catalogs` as its only property (C11's whole reason), and `X-Beckn-Degraded` names retrieval MODES, not fields — putting a field path in it would make the header mean two different things and break any client parsing it as a mode list. Three options: (a) a second header, say `X-Beckn-Partial`, symmetric with the degraded one and equally invisible to a client that does not read headers; (b) widen `OnDiscoverAction` in `beckn.yaml`, which is a protocol change rather than a service one; (c) accept that the caller is not told | The log, at Warn, with the path, code and reason (`reportPartials` in `src/discover/service.go`) — option (c). The intent is still narrowed exactly as the mapper decided; only the caller's notification is missing, and every other branch of the mapper that would widen a query refuses instead of degrading, so the blind spot is one field |

---

## Carried chores

Not tied to a task — pick one up whenever its area is already open.

| Chore | Why it is still here |
|---|---|
| Reword `a4b970e`, `5646909`, `af3c84d`, `71e36c5` to carry `[#11]`; `71e36c5` also needs the colon after `fix` and cites an amendment "A18" that does not exist | The issue tag is what makes `git log --grep="#11"` answer honestly, and four plan-design commits currently fall out of it. All four are already on `origin`, so this needs `git push --force-with-lease` and is the human's call, not an agent's |

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
