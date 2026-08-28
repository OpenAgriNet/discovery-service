# discovery-service

A Beckn v2.0.0 discover and publish service for OpenAgriNet, in Go. Postgres is
the only datastore; there is no spatial extension and no separate search engine.

## The plan is the spec

`docs/design/discover-and-publish.md` is binding. It carries 23 dependency-ordered
tasks, 35 acceptance scenarios and the reasoning behind every schema decision.
Work one task at a time, in numeric order.

- **Global Constraints** (near the top) is inherited by every task. Read it once
  per session. It is not repeated here, because a second copy is a second thing
  to keep true and it is the copy that rots.
- Before asking a question, check **Spec Conflicts**, **Amendments** and
  **Open Items**. Most things are already decided there, with the why.
- Blocks marked `pseudo` are intent — write idiomatic Go against the interfaces
  the task names. DDL, SQL predicates and wire shapes are literal contracts.
- `beckn.yaml` is the protocol source of truth. The Java `beckn-discovr`
  implementation is a reference that deviates from it in places; when they
  disagree, the schema wins.

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
| `src/platform/` | Config, logging, errors, crypto, middleware, validation, plus `httpx` (envelope + response writer), `jsonpath` and `registry`. `telemetry/` and `constants/` are empty placeholders — telemetry is Task 23 |
| `config/` | `common.yaml` (committed, reviewed); `instance.yaml` is mounted per deployment |
| `tests/` | `acceptance/`, `dbtest/`, `testdata/`, and `architecture/boundary_test.go` — the import-graph guard on the TRD §5 swap boundary |
| `docs/design/`, `docs/adr/` | The plan, and the 16 ADRs behind it — 0001 is superseded by 0016 |
| `docs/registry/` | Four documents, indexed by `README.md`: `usecases.md` traces six farmer questions end to end with real payloads; `schemas.md` is the three entities field by field plus the five rules JSON Schema cannot express and the known gaps; `api.md` is the registry's own REST surface; `examples.md` the thirteen records to seed. `schemas/` holds the machine-readable draft-07 files, which are the contract, and `verify/` the four checkers that keep the pages true. `archive/` is the BV Beckn adapter's own design set — a **different system**, copied verbatim for interop context and kept diffable against its source. Binding on nothing here; `docs/design/` still wins |
