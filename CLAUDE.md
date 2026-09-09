# discovery-service

A Beckn v2.0.0 discover and publish service for OpenAgriNet, in Go. Postgres is
the only datastore; there is no spatial extension and no separate search engine.

## The plan is the spec

`docs/design/implementation-plan.md` is binding. It carries 26 dependency-ordered
tasks, 35 acceptance scenarios and the reasoning behind every schema decision.
Tasks 24-26 were added after the original 23 — 24 by A23, 25 and 26 by A25 —
so a stale "23 tasks" anywhere means that document, not this one, is behind.
Work one task at a time, in numeric order.

- **Global Constraints** (near the top) is inherited by every task. Read it once
  per session. It is not repeated here, because a second copy is a second thing
  to keep true and it is the copy that rots.
- Before asking a question, check **Spec Conflicts**, **Amendments** and
  **Open Items**. Most things are already decided there, with the why.
- Blocks marked `pseudo` are intent — write idiomatic Go against the interfaces
  the task names. DDL, SQL predicates and wire shapes are literal contracts.
- `tests/testdata/beckn-v2.0.0.yaml` is the protocol source of truth. The Java
  `beckn-discovr` implementation is a reference that deviates from it in places;
  when they disagree, the schema wins.

`docs/design/implementation-prompts.md` holds the per-task driver prompt and the
task checklist.

## Working agreement

- **One task, then stop.** Summarize what you built and wait. Do not start the
  next task unbidden — the review gate between tasks is the point.
- **TDD order.** Write the failing test, run it and watch it fail, write the
  minimal thing that passes, run it again. A test you never saw fail pins
  nothing.
- **Self-review before declaring done.** Re-read the task's own section beside
  your diff: every file, type, signature and behaviour it names, present under
  the name it uses. Report it as a short matched/deviated checklist with a reason
  per deviation — not a silent pass.
- **Never push.** Commit freely on the working branch; pushing is the human's.
- If you find a real problem outside your task, say so in your summary. Don't
  fix it.

## Verifying

```
make build      # compiles every package into bin/
make lint       # golangci-lint run + fmt --diff; must be 0 issues
make test       # go test -race ./... with EMBEDDING_PROVIDER=hashing
```

The three above are the gates. Three more need a running stack (`make run`),
and they answer different questions: `make verify` asserts the sample requests
still return the ids they returned before, `make newman` does the same through
the Postman collection, and `make audit` recomputes the expected answers from
the published catalog instead of trusting either — so it catches an answer that
is wrong rather than merely changed.

`make test` pins the embedding provider rather than inheriting it: production
defaults to `noop` (A5), so without the pin the entire semantic path — query
embedding, HNSW, RRF, the dimension guard, the degradation report — would go
untested from the day semantic search was deferred. A test must therefore never
assert against `os.Environ`; take the environment as a parameter, as
`src/platform/config` does with `load`.

Paste the real output before claiming a task is done.

## Commits

```
<type>: <summary in imperative mood> [#<issue>]
```

The issue number makes commits grep-able (`git log --grep="#9"`). `feat` is a
MINOR bump, `fix` a PATCH, `refactor`/`chore`/`test`/`docs` none; a
`BREAKING CHANGE:` footer is MAJOR regardless of type. Scopes are optional — add
one only when the repo is large enough that filtering by area is genuinely
useful, never speculatively.

| Issue | Scope |
|---|---|
| #1–#3 | Publish API — tests, core, end-to-end |
| #4 | Publish API automation |
| #5 | Publish API security scanning |
| #6–#8 | Discover API — tests, core, end-to-end and performance |
| #9 | Discover API automation & config |
| #10 | Discover API security, telemetry, observability |
| #11 | Implementation plan design (complete — do not tag new work with it) |

The body carries the **why**: what you chose, what you rejected, and what breaks
if someone changes it back. A commit message that restates the diff is wasted.

## Standing rules

- **Names from the plan are load-bearing.** `MaxCandidatesPerMode` is not
  `MaxLimit`; `FailOnUnavailableMode` is not `StrictModes`. Where the doc renamed
  something it explains what the old name failed to say, and later tasks
  reference the new one. Do not simplify them back.
- **A pin that holds only because you remembered it is not a pin.** Encode it —
  a lint rule, a reflection assertion over the struct, a conformance fixture.
  When the plan says a constraint is "asserted by `make lint`", it is telling you
  to add the rule to `.golangci.yml`, not merely to comply with it.
- **Secrets never touch a config file or a log field.** `DATABASE_URL` and its
  kind arrive from the environment; that is precisely why the environment layer
  sits on top of both YAML files (TRD §8). `config/common.yaml` is reviewed and
  committed, so anything in it is public.
- **SQL is always parameterised.** String-concatenated SQL is prohibited and
  JSONPath expressions are never interpolated.
- **A configured registry URL is trusted; a URL from a request body is not.**
  That is the whole meaning of `EXT_ALLOW_NETWORK_FETCH=false`.
- **No `TODO` on main.** Deferred work goes to the plan's Deferred / Out of Scope
  section, where someone deciding scope will find it — not into a comment only
  the next person to open that file will read.
- **Phase 1 accepts regular resources only.** A publish naming master resources
  is rejected, not partially handled.

## Layout

| Path | Holds |
|---|---|
| `cmd/discovery-service/` | The binary's `main` |
| `src/app/` | Composition root — the container that builds and wires everything |
| `src/beckn/` | Protocol types, actions, error codes |
| `src/domain/` | Catalog, query, validity, merge-patch — no I/O |
| `src/publish/`, `src/discover/` | The two request paths |
| `src/indexing/` | H3 geometry covers and the embedding seam |
| `src/storage/` | `postgres/` and `memory/`, plus the `conformance/` suite both must pass |
| `src/platform/` | Config, logging, errors, middleware, validation, plus `httpx` (envelope + response writer) and `jsonpath`. `telemetry/` is built — 15 Go files, one per signal, and its layout is meant to be read off `ls`, with `provider.go` carrying both the package doc and the file map in prose: `provider.go` boots the SDK and owns both OTLP exporters and the propagator, `traces.go` owns spans whole (who this process says it is — the producer/domain/network triple, the Resource, the build stamp — plus span attributes, span events and the `spanUUID` processor), `metrics.go` owns metrics whole, and `fact/` is the attribute registry (`definition.go` what a fact is **and** the rules a row must satisfy, `registry.go` the table, `record.go` what one request observed, `instruments.go` the metric instruments). One test file per source file; `provider_internal_test.go` is the exception and cannot merge into `provider_test.go` — it is `package telemetry` where the other is `package telemetry_test`. There is no `doc.go` and no `logs.go`. The third signal's projection is `src/platform/logger/fields.go` and **cannot** move here: its one caller is `middlewares/request_logger.go`, which `tests/architecture/boundary_test.go` deliberately keeps off the SDK allow-list, so a `telemetry/logs.go` would need an exemption the guard's own comment argues is the thing it exists to prevent. `fields.go`'s doc comment says the same. Do not "complete the symmetry" by moving it. **Three** directories are still empty placeholders holding a `.gitkeep` and no Go: `constants/`, and `crypto/signature/` and `registry/` — Task 6 is parked, so nothing declares `registry.Keyring` and `validateAuth` refuses the boot rather than let `AUTH_ENABLE_SIGNATURE_VERIFICATION` claim otherwise. This row said "four" and named `telemetry/` among them until 2026-09-09; Task 23 has since landed 23a–23e |
| `config/` | `common.yaml` (committed, reviewed); `instance.yaml` is mounted per deployment |
| `tests/` | `acceptance/`, `dbtest/`, `testdata/`, and `architecture/boundary_test.go` — the import-graph guard on the TRD §5 swap boundary |
| `docs/` | `README.md` is the index and the way in. Four short documents describe the system **as it runs**: `quickstart.md` (Docker Compose, publish, discover back), `publish-and-discover.md` (the two endpoints, worked requests, the configuration), `telemetry.md` (the span, the derived metrics, the log line, the attribute registry all three read) and `registry.md` (the three-table capability registry, merged from five files on 2026-09-09). They are a **reading**, not a contract: where one and `design/implementation-plan.md` disagree, the plan wins and the document is wrong. Every payload in them was captured from a running stack rather than transcribed from the plan — that is what caught the claim that Task 25 was unshipped. Keep them short; the plan is where length belongs |
| `docs/api/` | `openapi.yaml` — OpenAPI 3.0.3 over **three** surfaces (this service, the registry, a provider node's `select`), each path carrying its own `servers`. Permissive on `Catalog`/`Provider`/`Resource`/`Offer` **on purpose**: `tests/testdata/beckn-v2.0.0.yaml` is what the service validates against, so where the two disagree the file is wrong, not the runtime. Beside it, `openagrinet.postman_collection.json` is the hand-kept getting-started collection — **not** the one `make newman` runs, which is generated into `examples/` by `build-postman.py` and asserts exact ids. Both moved here from `docs/open-api/` and `docs/api-collection/` on 2026-09-09 |
| `docs/design/` | The plan (`implementation-plan.md`) and the telemetry design (`opentelemetry.md`), which now carries both the *what* and — under §The seam — the *where the code lives*: the attribute registry that makes an attribute change one edit across span, log and metric. `telemetry-examples.md` is the worked OTLP payload for one transaction across both this service and beckn-onix, illustrative and binding on nothing, kept because it is where the cross-repo attribute divergences are recorded. Beside it, `telemetry-examples/` is the same deliverable in machine-readable form — `discover-service.json` is our specified output, `adapter.json` is onix's **observed** output cited to its source lines, and `inventory.md` is the flat signal list for both. Also binding on nothing, but do not delete it as a duplicate of the `.md`: the onix side is the only record here of what the other repo actually emits, and it is what a divergence is checked against. Plus the driver prompts (`implementation-prompts.md`), and `registry/schemas/`. `opentelemetry.md` §Proposed codes — the outward proposal is addressed OUTWARD and binding on nothing here: it is what to send to whoever owns the OAN metrics registry, and Task 24 stays blocked until they answer its seven questions — do not implement its twelve codes as though ratified. **Five documents were drained and deleted on 2026-09-09; they are in git history and are not to be restored.** `stripped-rationale.md` was the godoc sweep's residue — its 13 rationale entries are now the plan's **Appendix**, entry 7 is applied as Task 15's corrected `NewCatalogRepository` arity and entry 13 as Task 8's note that `Trace`'s `X-Beckn-Chain` marker is transient. `schema-revision-proposal.md` held the MEASUREMENTS behind A18 and A19; every one of them is already in the plan — A18's 20-shape GIN result in its own amendment row, A19's 1.5 ms / 150.6 ms in its row and again in Deferred, and the geometry 70 ms / 1.1 ms table under the `resource_geometries` schema. `telemetry-build-vs-reuse-audit.md` described a `telemetry/` file layout that no longer exists (`fact/fact.go`, `project_span.go`, `telemetry.go`); both its findings were applied — Finding 2 by **deleting** `fact.Visibility`, which 23f reintroduces and whose declaration the plan's Deferred row for it now keeps, and Finding 1 by correcting the `scope` misquote in place, so do not "restore" the requires-on-every-batch claim (`opentelemetry.md` §Scope — per exported batch is the accurate sentence). Its two surviving live facts were folded out first: the `otelpgx` spike is now a Deferred row in the plan, and the reason `otelhttp` is a legitimate `// indirect` (`go mod why` → testcontainers → moby) sits beside the rejection in `opentelemetry.md`. `telemetry-seam.md` and `metric-code-registry-proposal.md` were the last two, drained on the same day for the same reason — the seam into `opentelemetry.md` §The seam (with its `fact.Visibility` declaration into the plan and its four owned-elsewhere open items into §Four more, each with a named owner outside this repo), the proposal into §Proposed codes — the outward proposal. Everything binding lives here |
| `docs/design/registry/schemas/` | The three machine-readable draft-07 files, **which are the contract** — when the prose and the JSON disagree, the JSON wins, and nothing checks that they agree: the `verify/` checkers and the BV adapter's `archive/` were both removed when this folder moved. Both are in git history. The prose is `docs/registry.md`; the five markdown files that used to sit beside these schemas (`README`, `schemas`, `api`, `usecases`, `examples`) were merged into it on 2026-09-09 and are in git history. Binding on nothing here; `implementation-plan.md` still wins |
| `docs/adr/` | The 16 ADRs behind the plan — 0001 is superseded by 0016; 0011 is amended by A23 and A25, and 0012 and 0014 by the 2026-09-07 audit that found `registry.Keyring` was never built |
