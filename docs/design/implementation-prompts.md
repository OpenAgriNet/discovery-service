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

**Next task to implement: Task 7, `Envelope` only.**

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

Task 8. Paste this verbatim into a fresh session.

```
Implement Task 8 — Request Logger & Rate Limit Middleware from
docs/design/discover-and-publish.md, and only Task 8.

Rules:
- Follow the task's own steps literally, in TDD order (failing test first, run
  it, watch it fail, then the minimal thing that makes it pass, then run it
  again). A test you never saw fail pins nothing.
- The Global Constraints table (near the top of the doc) applies to everything
  you write, not just this task.
- Code blocks marked `pseudo` in the doc are intent, not literal source — write
  idiomatic Go against the interfaces the task names. DDL, SQL predicates and
  wire shapes are literal contracts — copy them as-is.
- Run `make build`, `make lint` and `make test` and paste the actual output
  before claiming the task is done. `make lint` must be 0 issues.
- One commit per task step marked *Commit*, Conventional Commits format:
  `<type>: <summary in imperative mood> [#1]`. The body carries the why — what
  you chose, what you rejected, what breaks if someone changes it back.
- Never push. Committing on the working branch is yours; pushing is the
  human's.
- No TODO left on main — anything you would defer belongs back in the doc's
  Deferred / Out of Scope section, flagged to me, not left in a comment.
- If you hit a genuine ambiguity the doc does not resolve (check Spec
  Conflicts, Amendments and Open Items first — most things are already decided
  there, with the why), stop and ask me rather than guessing.
- Before you say you are done, self-review: re-read this task's own section
  side-by-side with your diff. Every file, type, function signature and
  behaviour it names, present under the name it uses, and nothing drifted from
  it or from Global Constraints. Report it as a short matched / deviated
  checklist with a reason per deviation — not a silent pass.

What already exists, so you neither rebuild it nor re-derive it:
- `logger.New`, `NewContext`, `FromContext`, `With` and the field constructors
  `RequestID`, `TransactionID`, `MessageID`, `Action`, `ErrorType`, `ErrorCode`
  (Task 3). The field *names* are spelled in that package and nowhere else —
  one key spelled two ways is two fields to whatever queries the logs.
- `httpx.WriteNack(ctx, w, cfg config.Errors, messageID string, err error)`
  (Task 5) — the single writer. It sets the status, `X-Beckn-Error-Type`,
  `Retry-After` when the fault carries a back-off, and logs the code and the
  category once. Do not assemble a rejection body anywhere else; there is a
  test that fails if you do.
- `apperrors.RateLimited(retryAfter time.Duration, message string)` — the
  constructor `RateLimit` must use. It exists precisely so the back-off cannot
  be forgotten after construction; `WriteNack` turns it into the header.
- `middlewares.Envelope(cfg config.Errors, maxBodyBytes int64)` and
  `EnvelopeFromContext` (Task 7). The body ceiling is already enforced there —
  do not add a second one anywhere in this task.
- `config.RateLimit{RPS, Burst}` (`RATE_LIMIT_RPS=20`, `RATE_LIMIT_BURST=40`)
  and `config.Errors` (Task 2). Do not re-declare either.

Four things this task must get right, because everything downstream reads them:

1. X-Beckn-Chain is built here, and it is what Task 20 tests the chain order
   with. `Trace` and `Recover` each `Header().Add` one entry — `trace` and
   `recover` — BEFORE calling the next handler. `Add` preserves insertion
   order, so `Values("X-Beckn-Chain")` reads back as the order the two links
   actually ran. A single presence marker cannot do this: both stamp before
   `Recover` writes its 500, so a presence assertion passes whichever way round
   the two are mounted. Read the paragraph in Task 8's own section; it is the
   reason `Trace` is a pass-through with a side effect rather than a
   pass-through.

2. RequestID mints; it never trusts an inbound X-Request-Id. Phase 1 is
   unauthenticated, so an inbound value is one the caller chose — honouring it
   lets a caller collide two requests' log lines or write control characters
   into a log field. It is also first in the chain for a reason: until it
   installs the request-scoped logger, `logger.FromContext` returns the no-op
   logger and `WriteNack`'s log line goes nowhere.

3. X-Response-Time has to be stamped inside the ResponseWriter wrapper's
   WriteHeader. A header set after the handler has written is a header that
   never reaches the wire, so a `RequestLogger` that sets it on the way back
   out will pass a test that only ever exercises a handler which wrote nothing.
   The wrapper is also what captures the status the completion line reports.
   `error_type` is read back off `X-Beckn-Error-Type`, which `WriteNack`
   already set — do not derive the category a second time; C1 exists to have
   exactly one place that decides it.

4. The rate limit bucket is keyed on the remote address, NOT the subscriber id
   — and this is a deliberate departure from A4's wording that the doc records
   in Deferred. `context.bapId` on an unverified request is a claim: keying on
   it lets any caller shed its own limit by rotating the field, and lets any
   caller exhaust a named third party's bucket by claiming their id. Do not
   "fix" this back. Do not read `X-Forwarded-For` either — there is no
   trusted-proxy list to make that safe. Evict idle buckets; a map keyed on
   remote address that only ever grows is a leak an unauthenticated caller
   drives.

When the task's tests pass, self-review is done and it is committed, stop and
summarize what you built — do not continue to the next task until I say go.
```

---

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
| 8 | Request Logger & Rate Limit Middleware | Also homes `RequestID` (its own file; it mints rather than trusting an inbound `X-Request-Id`, and it is first in the chain because nothing below it logs until it installs the request-scoped logger) and **departs from A4**: the bucket is keyed on the remote address, not the subscriber id, which on an unverified request is a claim anyone can make about anyone. Subscriber-id keying moved to Deferred, tied to the task that verifies the signature. Also builds `Trace` as a **no-op pass-through** that appends `trace` to `X-Beckn-Chain`, alongside `Recover` appending `recover` — the pair exists so Task 20's order test can read the two back in the order they ran. See Task 8's own section; an earlier draft of this row said `X-Beckn-Trace-Seen: 1`, a single presence marker that cannot carry order |
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
| 20 | Container, Router & Server Lifecycle | First point the service actually boots end-to-end. Wires the full middleware chain **minus `Signature`**, order-tested by observing side effects — specifically the *order of the two entries* `Trace` and `Recover` append to `X-Beckn-Chain`, since both are appended before `Recover` writes its 500 and a mere presence marker therefore survives under either nesting. Reads `SERVER_MAX_REQUEST_BODY_BYTES` once and passes it to `Envelope`; sets no second ceiling on the `http.Server` (C14) |
| 21 | End-to-End Acceptance Suite | The 35 scenarios in the doc's Scenarios section get pinned here, over real HTTP against a real Postgres — this is the integration/e2e layer, not unit tests. Covers publish and discover each in their own `_test.go` file, plus offers, validity, performance, defaults, geopath and spatial-operator groups. Runs against **Postgres only** — memory-backend parity is `storage/conformance`'s job (Tasks 11/12/15/16), not this suite's |
| 22 | Structured Attribute Filtering | *Phase 1, per the doc's Open Items table* — do not skip unless you've deliberately decided to push it to Phase 2. Validation + rebase live in `src/platform/jsonpath/subset.go` (backend-agnostic, beside `Canonicalise`); `storage/postgres/jsonpath.go` only casts/executes the already-accepted expression |
| 23 | OpenTelemetry Tracing & Metrics | Replaces Task 8's no-op `Trace` body with real `otelhttp` instrumentation and drops its `trace` entry from `X-Beckn-Chain` — the chain itself doesn't move |

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
