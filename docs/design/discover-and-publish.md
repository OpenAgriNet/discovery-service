# OpenAgriNet Discover & Publish Service — Implementation Plan

**Goal:** A Go service exposing synchronous `POST /publish` and `POST /discover`
that ingest Beckn v2.0.0 catalogs into PostgreSQL and serve geo + lexical
discovery under 20 ms.

**Architecture:** `controller → service → repository`. Two ports live in
`src/domain/`: `CatalogRepository` (write, driven by publish) and
`SearchRepository` (read, driven by discover). Neither capability package
imports a driver, so the backend is swappable. HTTP is `net/http` + chi; SQL is
sqlc + pgx; wiring is explicit constructors, no reflection.

**Tech Stack:** Go 1.25 · chi v5 · pgx/v5 + sqlc · PostgreSQL 16 + pgvector 0.8
· uber/h3-go v4 · kin-openapi · zap · testify · testcontainers-go

> **How to read this.** Code blocks are **pseudocode and schema, not source.**
> The DDL, the SQL predicates and the wire shapes are literal — those are the
> contract. Everything in a `pseudo` block describes intent and order; the
> implementer writes idiomatic Go against the interfaces named in each task.
>
> An earlier long-form version of this plan, with full source for every step,
> existed but is superseded and has been moved out of this repository — it is
> not present here and is not a reference for implementation. This file is the
> only instruction.

---

## Contents

- [Global Constraints](#global-constraints)
- [Spec Conflicts](#spec-conflicts)
- [Amendments](#amendments)
- [TRD Alignment](#trd-alignment)
- [Out of Scope](#out-of-scope)
- [Technology Decisions](#technology-decisions)
- [File Structure](#file-structure)
- [Data Model](#data-model)
- [Publish — How It Works](#publish--how-it-works)
- [Discover — How It Works](#discover--how-it-works)
- [Geospatial Design](#geospatial-design)
- [Scenarios](#scenarios)
- [Tasks](#tasks)
- [Open Items](#open-items)

---

## Global Constraints

Every task inherits these.

| | |
|---|---|
| Language | Go 1.25+. Module `github.com/OpenAgriNet/discovery-service` |
| Licensing | OSI-approved dependencies only. No SaaS, no closed vendor features |
| Protocol | Beckn v2.0.0. Source of truth is the published `beckn.yaml`, fetched and cached at boot. Only a pinned CI fixture is committed |
| File names | Go `snake_case`; `PascalCase` exported, `camelCase` unexported |
| Functions | Under 50 lines, early-return errors, nesting depth ≤ 3 |
| Godoc | Every exported symbol. Non-obvious maths (H3 cover, RRF, haversine) gets inline comments |
| Comments | Only the **why** — a hidden constraint, a workaround, an invariant not visible at the call site. A comment restating the code is a second thing to keep true, and it is the one that rots first. The model is the `STRICT` / `NOT STRICT` asymmetry on `geo_haversine_m` and `geo_distance_m`: the comment says PostgreSQL declines to inline a STRICT function with a non-strict body, which no reader could recover from the signature. `// returns the distance` is the anti-model |
| DRY | Envelope parsing, signature verification, error construction, response writing and timing each live in exactly one package |
| Abstraction | **A seam ships with a conformance test or a second implementation behind it, or it does not ship.** The A6/A7 rule applied generally: an unused abstraction is a guess, one a test drives is a contract. Config knobs meet the same bar — a flag no scenario sets is not shipped. This is not a ban on the seams already named (`Embedder` under A5, `CatalogReplicator` under A7, `Keyring`): each has a task that constructs it and a test that exercises it, which is exactly the difference the rule draws |
| DI | Explicit constructors only. `dig` / `wire` / reflection containers prohibited (D3) |
| No globals | No package-level mutable state, and no `init()` that does work. Config, the pool, the logger, clocks and clients are built in `container.go` and passed down; nothing reaches for them through a `var` at call time. This is the DI rule seen from the other end — a dependency injected explicitly **and** reachable globally is not injected, it is a suggestion. A6 already applies it to query scope: a value, not a hidden global |
| Logging | zap JSON, typed field constructors. Never `zap.Any` or `Sugar()` on the request path |
| Errors | Every error crossing a package boundary is `%w`-wrapped with enough context to name the failing call — `fmt.Errorf("upsert catalog %s: %w", id, err)`, never a bare `return err`. Wrapping is for Go errors only: a validation `Fault` is a value carrying its own `Path` and `Code`, is aggregated rather than returned, and is never wrapped into an error |
| SQL | Always parameterised. String-concatenated SQL prohibited. JSONPath expressions never interpolated |
| Test doubles | The memory backend is the **only** double for the repository interface. No per-file mocks, no hand-rolled stubs. Every behaviour pinned against Postgres is pinned against memory by the same `conformance/` fixtures, which is the one thing keeping the two from drifting; a mock written by the test that asserts on it proves only that both were written by the same person |
| Naming | A config key, constant or column names **what it bounds and in what unit** — not merely that it is a bound. `MaxCandidatesPerMode`, not `CandidatesPerMode`; `MaxRadiusMeters`, with the unit in the name. Two names that differ only by which side of the system they serve must say which side: `MaxQueryCoverCells` and `MaxIndexCoverCells`, `target_path` and `source_path`. A reader who has to open the table to tell a pair apart will eventually pick the wrong one |
| Flags | `VALIDATION_ENABLE_L1_SCHEMA=true`, `VALIDATION_ENABLE_L2_CONTEXT=true`, `AUTH_ENABLE_SIGNATURE_VERIFICATION=false` (deferred; seam ships) |
| Limits | `SERVER_MAX_REQUEST_BODY_BYTES=10485760` (10 MiB). Enforced in `Envelope` with `http.MaxBytesReader`, because that is the only place in the service that reads a request body and it runs **before** `RateLimit` — the limiter never sees these bytes, so a ceiling set anywhere later is a ceiling set after the allocation it exists to prevent. Over it is `POL_NP_CAPACITY_EXCEEDED` at **413** (C14) |
| Commits | Conventional commits, one per task step marked *Commit* |
| TODOs | None on `main`. Anything deferred goes in **Deferred** or **Out of Scope** in this document, where a reader deciding scope will find it — not into a source comment only the next person to open that file will ever read. Scope drift belongs in the plan, visible, not buried at the call site |

---

## Spec Conflicts

The PRD and `beckn.yaml` v2.0.0 disagree in twelve places, the spec
contradicts itself in a thirteenth, and it is silent in a fourteenth. These resolutions are binding. C8, C9 and
C10 are **deliberate deviations** — cases where this service knowingly does
something other than what the spec says, each with the reason written down,
because an undocumented deviation is indistinguishable from a bug. C13 is not a
deviation but a choice between two things the schema says at once.

| | Conflict | Resolution |
|---|---|---|
| **C1** | PRD wants `error.type`; `beckn.yaml` `Error` is `{code, message, details}` with `additionalProperties: false` | Body stays spec-conformant. The five PRD categories move to the `X-Beckn-Error-Type` header and the `error_type` log field. Mapping: `CTX_`→CONTEXT, `AUT_`→CORE, `SCH_`/`BIZ_`/`DOM_`→DOMAIN, `POL_`→POLICY, `NET_`→SYSTEM — all seven prefixes the spec names, because an unmapped `DOM_` (the prefix a downstream chain arrives with) would leave the header blank on exactly the errors that are hardest to attribute. `ERROR_INCLUDE_LEGACY_TYPE` (default `false`) re-injects `type` into the body for v1-style clients that require it |
| **C2** | PRD says `POST /publish`; spec says `POST /catalog/publish` | **One route: `POST /publish`.** No `/catalog/publish` alias — a second path onto one handler is a second thing to route, rate-limit, log and document, and it is the spec path this network's publishers do not use. `context.action` still accepts **both** spellings, because that is a field inside a body we did not write and the L1 index is keyed by action rather than by URL, so both resolve to the same schema. The response action stays `catalog/on_publish`: that is the spec's name for the callback shape C3 returns inline, not a route |
| **C3** | Spec returns `Ack` and calls back out-of-band; PRD requires results in the 200 body | 200 body is `{context, message}` where `message` is the spec's `CatalogOnPublishAction` / `OnDiscoverAction` — the callback shape, returned inline. Async callback dispatch is out of scope |
| **C4** | PRD and the reference implementation treat `@context` as an array; `Attributes` declares `'@context'` and `'@type'` as scalar `string`, and makes **both required** | Two scalar `TEXT` columns, `schema_context` and `schema_type`. No array, no normalisation pass. An array for either is a 400 from L1, not a silent first-element pick |
| **C5** | PRD requires categories; the spec has **no `category` field anywhere**. `Resource` is exactly `{id, descriptor, resourceAttributes}`, and `Intent` is `additionalProperties: false` with no category filter | No column, no index, no derivation. `stats.categoryCount` in the publish ack *is* in the spec, so it is answered as the count of distinct `@type` values in the catalog — the only grouping the schema actually has |
| **C6** | `Context` declares no `required` list, so L1 alone cannot reject a missing `transactionId` | Envelope rules are enforced separately by struct tags: `action`, `version`(=2.0.0), `messageId`(uuid4), `transactionId`(uuid4), `timestamp`(RFC3339) required. `bppId`, `bapId`, `networkId` stay optional. Runs even when L1 is off |
| **C7** | Validation produces many faults, and `Error` is singular. But `Error.details` is itself `additionalProperties: false` with exactly two keys, `path` and `cause` — a list of extra pointers cannot go there and still validate | Two answers, because the spec already supplies both. **Publish:** `CatalogProcessingResult.errors` is natively `array of Error`, so every fault is its own `Error` and there is nothing to pack. **A NACK:** one `Error`, and each remaining fault is the `details.cause` of the one before it — a chain, which is exactly what that self-referencing field is documented for. `details.path` is a JSONPath (`$.message.publishDirectives[1]`), the form the spec's own example uses. No fault is dropped silently |
| **C8** | `publishDirectives.visibleTo`: *"When omitted, the catalog is visible to all eligible subscribers"* | **Deviation.** Omitted or empty becomes `[network]` — the request's `networkId`, else `APP_NETWORK_ID` (`mahavistar`, `bharatvistar`, …). Publishing to every network by a typo is the worse failure of the two, and a publisher wanting network-wide reach can say so explicitly. **The default applies in both update modes** (A9): a republish that does not mention `visibleTo` gets `[network]`, exactly as a first publish does. A field with a declared default is never "absent" by the time the merge sees it, so MERGE and FULL cannot disagree about what a silent publisher meant. An explicit `[]` resolves to `[network]` too |
| **C9** | With no `publishDirectives`, the spec **infers** the type from content: catalogs with offers are `regular`, catalogs with only resources are `master` | **Deviation.** An absent directive is REGULAR. Honouring the inference would make A1 reject every ordinary catalog that happens to carry no offers — the common case. Only an explicit `catalogType: MASTER`, or a resource carrying `extends`, is refused |
| **C10** | `Intent.filters.expression` names no grammar normatively, and its only example is RFC 9535: `$[?(@.rating.value >= 4.0 && @.electronic.brand.name == 'Premium Tech')]` | **Deviation.** Only PostgreSQL SQL/JSON path is executed (`$ ? (@.x == "y")`). An RFC 9535 expression is a `400` / `SCH_INVALID_JSONPATH` — never attempted, because a filter that matches nothing is indistinguishable from an honest empty result |
| **C11** | `OnDiscoverAction` is `additionalProperties: false` with exactly one property, `catalogs`. There is nowhere in the response **body** to say that a retrieval mode was degraded | The list moves to the **`X-Beckn-Degraded`** response header, comma-separated, absent when nothing degraded. The same move C1 makes for `error.type`, for the same reason: a key the schema forbids is not an extension, it is a response that fails validation at the first consumer strict enough to check. `SEARCH_FAIL_ON_UNAVAILABLE_MODE=true` stays available for callers who would rather have a `400` than a header they might not read |
| **C12** | `CatalogProcessingResult.stats` gives `itemCount` as *"Number of items accepted"* but `providerCount` as *"Number of providers in the catalog"* — one request-scoped, one catalog-scoped, in adjacent fields | All three are read **request-scoped**: `itemCount` and `categoryCount` count what this request landed, and `providerCount` is 1 because a catalog has exactly one provider, so the two readings coincide and nothing is lost. A8 is why this needed deciding at all — before field-level MERGE, a payload and its catalog were the same set |
| **C13** | Every Ack-family schema requires `message.messageId`, and the family then disagrees about what it is. Seven declare `format: uuid` — `Ack`, `AckNoCallback`, `NackBadRequest`, `NackUnauthorized`, `NackTooManyRequests`, `NackDiscretionary`, `ServerError`. Three drop the format and describe the field instead: *"Echoes the messageId from the triggering request's Context, for caller correlation"* — `NackForbidden`, `NackConflict`, `NackNotFound` | **Echo, verbatim.** The two readings are not equal and this is not a coin toss: a field defined as an echo cannot assert a format its source is not required to carry, and by **C6** the source carries no guarantee at all — `Context` declares no `required` list, so the spec never establishes that a uuid was ever sent. The three that dropped `format` are the ones that followed that through. So `WriteNack` echoes `context.messageId` exactly as received, **including a value that is not a uuid** — which is precisely the request C6 rejects, and precisely the caller with the least other means of working out which request was refused. Empty **only** when the envelope yielded no messageId at all: unparseable JSON, or the key absent. Never a minted uuid — that hands the caller a correlation id for a message they never sent, which is worse than nothing because it looks like an answer. Capped at 128 bytes, longer dropped to empty: past that it is not a correlation handle a caller can use, it is a payload they chose our error body to carry. Against the seven variants declaring `format: uuid` the echoed non-uuid is non-conformant, and that is the cost of the reading — recorded here rather than left for L1 to discover **And the NACK carries no `context`.** The ten schemas are *open* — not one sets `additionalProperties: false`, unlike `Error`, which is closed and is what forced C7's chain — so a `context` beside `message` would validate. It is left out anyway: `Ack.messageId`'s own description is *"The message id of the request sent in the context, to which this acknowledgement is being returned"*, which is the spec lifting the correlation handle out of the context deliberately so a response need not carry one. Adding it back would be legal and still wrong — the NACK that most needs correlating is the one whose request context we have just refused as unreadable, so what we sent back would be part echo and part invention, which is the fault this row already refuses when it declines to mint a messageId. `transactionId` therefore has nowhere to go and is not smuggled into a header either; a header no scenario sets is not shipped. Because the schemas are open, **L1 cannot police this** — a stray key validates clean. `TestTheNackBodyCarriesNothingBeyondTheSchema` asserts the top-level and `message` key sets by hand, and it is the only thing standing between this decision and a silent drift. |
| **C14** | The `ErrorCode` enum has **no member for a payload this receiver will not accept on size**. All 76 are checked: the nearest are `AUT_RATE_LIMITED`, which is about pace and carries `Retry-After`, and `POL_NP_CAPACITY_EXCEEDED`, which `NackTooManyRequests` describes as *"a policy-governed engagement capacity limit"* at 429. There is no `SCH_` or `NET_` code for it either | **`POL_NP_CAPACITY_EXCEEDED` at HTTP 413**, and the request body is capped at `SERVER_MAX_REQUEST_BODY_BYTES` (10 MiB) in `Envelope`. The family is right and the status is not the family's: `POL_` is *a refusal this deployment's policy requires rather than one the request earned*, which is exactly what a size ceiling is — the same request succeeds unchanged against a deployment configured to accept it. 403 would send the caller to inspect their credentials for a fault that is in their payload, so `Status()` overrides it to 413 alongside the two overrides already there. **The reuse is safe only because this service runs no engagements**: the spec's other use of this code has no code path here, so within this service the code means one thing and maps to one status. A deployment that grows an engagement lifecycle must give that refusal a code of its own — the one thing it must not do is make this mapping carry two statuses, because `Status()` derives from the code precisely so that two call sites cannot disagree about what a fault is worth on the wire. The ceiling is a knob and not a constant because the largest legitimate body is a property of a deployment's catalogs, not of the protocol; zero is refused at boot, because it reads as *unlimited* and behaves as *refuse everything* |

**`networkId` is a filter, not an identity claim, and the two mappers answer
"it's absent" differently.** On **publish**, absent → scope to
`APP_NETWORK_ID` — used only to fill an empty `visibleTo` (C8). On
**discover**, absent means **no network predicate at all**: the query matches
every network's catalogs, `visible_to` included. That is deliberate, not an
oversight — `visibleTo` is how a publisher *restricts* a catalog to one or
more networks; it is not an access boundary a network-less caller is presumed
locked out of. A caller that wants isolation supplies `networkId` and gets
scoped to exactly that network, same as always. This is why it cannot be
required either way — a publish carrying only the five required fields is a
valid request, and it is what publishers send. The networks a catalog becomes
discoverable *on* come from `publishDirective.visibleTo`, which is a message
field carrying **an array of network ids** (not `PUBLIC`/`PRIVATE`).

---

## Amendments

| | What changes | Tasks |
|---|---|---|
| **A1** | An explicit `catalogType: MASTER`, and any resource carrying `extends`, are rejected at intake; inheritance ships later. **An absent directive is REGULAR, not inferred (C9).** Rejection is **per catalog** — nine regular catalogs land, the one master reports `REJECTED`. Nothing about it reaches the schema: `catalog_type` and the master columns are dropped, so refusal lives in the publish service, checked before the mapper runs | 15, 18, 21 |
| **A2** | Discover runs its retrieval modes concurrently, under one deadline | 16 |
| **A3** | One embedding config struct; the read path gets its own deadline, separate from the write path's | 2, 13, 16 |
| **A4** | Per-caller rate limiting on the protocol routes, `429` + `Retry-After` + `AUT_RATE_LIMITED` | 5, 8, 20 |
| **A5** | Semantic search is deferred. `EMBEDDING_PROVIDER=noop` by default; the column, the index and the `Embedder` seam all ship | 1, 2, 13, 14 |
| **A6** | Retrieval splits into one `Retriever` per mode plus a `Hydrator`; query scope becomes a value, not a hidden global | 1, 2, 11, 16, 20 |
| **A7** | Publish gains a write fan-out seam and a reconciliation queue. The `pending_targets` **column** is dropped — it was written on every resource and read by nothing, so it was debt recorded in the hot table. The seam survives in `Retriever`/`Hydrator` and the fan-out interface; a queue table arrives with the second store that needs one | 1, 2, 11, 15, 16, 18, 20 |
| **A8** | `updateMode` becomes a **content** rule, not only a row-set rule. **MERGE** is RFC 7396 JSON Merge Patch against the stored documents — an absent key keeps its stored value, an explicit `null` deletes it, an array replaces wholesale — with `resources` and `offers` matched by `id` rather than by array position. **FULL** replaces the catalog outright, its own columns included: omissions reset to defaults, and resources and offers the payload omits are deleted. Publish therefore becomes read-modify-write under a row lock, and every derived column is computed **after** the merge | 11, 15, 17, 18, 21 |
| **A10** | **Spatial search is answered as cell-set algebra, not as a prefilter in front of an exact stage.** Every geometry stores two H3 covers — `CONTAINMENT_FULL` (a guaranteed subset, which proves positives) and `CONTAINMENT_OVERLAPPING` (a guaranteed superset, which proves negatives) — and each CQL2 operator becomes an array predicate over the pair. **Seven of the nine operators are answered** where the previous design answered `S_DWITHIN` alone; `S_TOUCHES` and `S_CROSSES` are refused as unapproximable at any resolution rather than deferred. All seven RFC 7946 types work on **both** sides of the constraint, so the Point-only limit and the `NONE`-inversion it caused are gone. The costs are accuracy of one cell (~1.1 km at r8), oversize geometries decided by bounding box, and no path to cadastral precision without PostGIS | 11, 12, 14, 16, 17, 21 |
| **A11** | **`RequestLogger` moves above `Recover` in the fixed middleware order**, and `Recover` learns to abort rather than write a second body. Nested the other way, a recovered panic answers 500 *outside* the response wrapper, so the panicking request carries no `X-Response-Time` and produces no completion line — the failures are exactly the requests missing from the log. `RequestLogger` writes its line from a `defer`, so an unwinding panic does not take the line with it; `Recover` asks the writer below whether the response is already committed and, when it is, logs once and re-panics with `http.ErrAbortHandler` instead of appending a NACK document to a half-written body under an already-claimed 200 | 8, 20, 23 |
| **A9** | **Declared defaults are resolved before the merge runs and apply in both update modes.** The directive fields — `catalogType`, `updateMode`, `visibleTo` — are filled by `applyDirectiveDefaults` in `publishOne`, before the mapper runs; `catalog.isActive` and an offer's `resourceIds` are filled in the mapper when it builds the patch. Either way, an omitted field reads as *sent with its default* rather than as *absent*, under MERGE as much as under FULL. Only fields with **no** declared default (`provider`, `validity`, `descriptor`, `resourceAttributes`, the offer body) preserve absence and follow the A8 merge rule | 11, 15, 17, 18, 21 |
| **A12** | **L1 reconciles the two publish spellings against the document's own `const`, rather than the document being edited to admit both.** C2 says `publish` and `catalog/publish` are one request; the published schema constrains `context.action` to `const: catalog/publish`, and kin-openapi enforces `const` — so a body spelling it `publish`, which the protocol admits and this service routes, fails L1 on the very field that identified it. `SpecIndex` therefore stores, per action key, both the compiled schema and the canonical spelling **read out of that schema**, and `L1` rewrites `$.context.action` to it. The rewrite happens in the map `L1` decoded for itself, never in the buffered body, so the bytes this service signs, hashes and audits stay identical to the ones the caller sent. Hard-coding the mapping instead would put a second copy of the protocol in Go, which is exactly what D4 rejects; editing `beckn.yaml` would fork the published document | 9 |
| **A13** | **`messageId` and `transactionId` are required to be a canonical-form UUID of any version, not specifically uuid4.** C6's parenthetical says uuid4; the schema declares `format: uuid`, which the schema wins on. Narrowing to v4 would refuse a conformant v7 id — the version publishers are moving to precisely because it sorts — and refuse it with `CTX_INVALID_FIELD`, which reads as the caller's mistake and is not. The check is a 12-line predicate over the 8-4-4-4-12 hyphenated form rather than a uuid dependency, matching `request_id.go`'s existing choice of `rand.Text` over a library; `urn:uuid:`, braced and unhyphenated spellings are refused, because a service that accepts four spellings of an id cannot correlate on it | 9 |
| **A14** | **`DeriveFunc` takes the merged catalog by POINTER: `func(merged *Catalog, touched []string) []Fault`.** A8 makes derive the post-merge seam that WRITES `searchText`, `embedding`, `embeddingSourceHash`, `schemaContext`, `schemaType` and the geometry finds onto the merged catalog before the repository stores it. Taking the catalog by value gave it a copy: everything it computed was discarded, and the columns it exists to fill would have gone to the store empty while every test that only checked the returned faults passed. The `[]Fault` return stays, because a fault is a PARTIAL that travels to the response and is not a field of the catalog | 13, 14, 15, 17 |
| **A15** | **`Owners` is spent by the geometry WALK, and the repository never reads it back.** The walk already fans a shape out — `merged.Resources[k].Geometries <- found where k in Owners` — so an offer geometry covering three resources arrives on all three lists, each copy still naming all three. A repository that then wrote one row per owner per copy would turn N lists into N x N rows and violate `uq_resource_geometries (catalog_id, COALESCE(resource_id, ''), source_path)` on the FIRST publish of any offer geometry, not on some later republish. The rule is therefore positional: a shape is stored for the resource whose list it is on, and for the catalog when it is on the catalog's. The fill is still computed once per shape — memoised on `SourcePath`, which is unique per shape and is half of that same index — because the copies are identical shapes and H3 is the expensive half. This also settles staleness: an offer's shape cannot rot on an untouched resource, because `touched` follows offers, so patching the offer touches every resource it covers | 15, 17 |
| **A16** | **The pool floor is `(modes + 1) x in-flight`, and scenario 25's 20 ms budget is measured rather than enforced.** Task 1 sizes the pool as `modes x in-flight` — 32 for scenario 25's sixteen discovers — which counts only the retrieval fan-out. A discover then issues a count and four hydration queries, each taking a connection while its fifteen siblings are still inside their two-connection fan-out, so the peak is one higher per request. Measured on the corpus: 32 connections leave 137-195 acquires waiting, 48 leave none, 96 leave none no faster. `MinConns` must equal `MaxConns` for the assertion to mean anything, because `EmptyAcquireCount` counts acquires that waited for a connection to be CONSTRUCTED as well as ones that waited for a release, so a pool growing lazily from 4 charges its own warm-up to the scenario. **The latency half does not survive contact with the hardware:** one discover over ten thousand resources answers in 19-22 ms, inside the budget, but sixteen concurrent ones measure p50 160 ms / p95 260 ms against a database that saturates at 91-98 requests a second on a four-CPU container — Little's law, and the measurement agrees within 5%. 20 ms at that concurrency needs ~800 requests a second, about nine times this capacity, and it is a hardware statement rather than a query one. Task 21 therefore logs p50, p95 and throughput on every run and enforces the two machine-independent halves instead: `EmptyAcquireCount` must not move, and twenty times the corpus must cost under three times the request — which is the `count(*)` joining `catalogs` the scenario names, since that one tracks matches rather than the page | 1, 21 |

A6 and A7 exist for one requirement: *swap the text backend later, keep geo on
PG, and let publish write to two stores.* Both build **seams plus conformance
tests, not second backends** — an unused abstraction with no second
implementation is a guess; one with a conformance test is a contract.

---

## TRD Alignment

| | TRD | Resolution | Tasks |
|---|---|---|---|
| **T1** | §1 Configurability | Four config layers, lowest first: `envDefault` tags → `config/common.yaml` → `config/instance.yaml` → process environment. Environment stays on top because secrets arrive from a secret store and must beat a file. viper stays rejected — layering two YAML documents under `env.Parse` is a function, not a dependency | 1, 2 |
| **T2** | §6, §7 Observability | OpenTelemetry traces + metrics, W3C Trace Context in and out, OTLP exporter (default `none` so a collector-less deploy still boots), RED metrics per route. Dashboards are out of scope | 23, 20 |
| **T3** | §1 Schemas without redeploy | L2 schemas load through a `SchemaSource` (directory or HTTP registry) with a refresh loop, swapped behind `atomic.Pointer`. This service *consumes* schemas; owning the schema CRUD API is the registry's job. A configured registry URL is trusted; a URL from a request body is not — that distinction does not soften | 10, 20 |
| **T4** | §8 Supply chain | `govulncheck` + Trivy image scan failing on HIGH/CRITICAL in CI | 1 |
| **T5** | §2, §9 | ADR-0012 names which interfaces are promises and which are internal. ADR-0013 records the protocol-version-coexistence shape (version-keyed `SpecIndex`, accepted-versions set, response echoes request version) without building it | 1 |
| **T6** | all | An explicit statement of what this service does not own — below | — |
| **T7** | §5 Not tied to one database | Three swaps at three costs: vector store and geo index are one `Retriever` each; metadata and transactions are one package under `src/storage/` plus one line in `container.go`. Enforced by `boundary_test.go` over the import graph, admitted by `conformance/`, not by review. YugabyteDB is reachable on those terms — it is not free, and the plan names which parts are not. The geo swap is the cheapest of the three **because** the spatial layer is cell-set algebra (A10): the predicates are array overlap and containment, which Elasticsearch `terms` and Redis sets answer natively. Choosing PostGIS would have made it the most expensive | 11, 12, 14, 16, 20 |

---

## Out of Scope

Real requirements on the programme; none of them this service's.

| TRD | Requirement | Owner |
|---|---|---|
| §1 | Model selection, prompts, temperature, fallback chains | AI layer |
| §1 | The APIs through which schemas and domains are created and versioned | Registry (this service consumes — T3) |
| §2 | Guardrails, intent detection, response generation | AI layer |
| §4 | Verifiable Credentials, DIDs, credential issuance | Identity / registry |
| §4, §8 | The participant registry and its trust model | Registry (consumed through `registry.Keyring`) |
| §6 | Infrastructure as code, deployment, backup/restore | Platform repo |
| §6 | Streaming response APIs for voice and chat | AI layer |
| §7 | Dashboards and analytics over telemetry | Add-on (e.g. Obsrv) |
| §8 | The secret store itself, and key rotation | Platform |
| §8 | Consent capture and enforcement | Consent component |

Two sit on the boundary and are called out rather than dismissed:

- **Message signing** *is* this service's job, is specified in Tasks 6–7, and is
  switched off by flag. It is not out of scope; it is unfinished, and the plan
  should not pretend otherwise.
- **OIDC on admin surfaces** is only not a gap because there is no admin
  surface. The moment one is added, TRD §8 applies to it.

### Deferred — ours, postponed

| Deferred | Why not now | What ships instead |
|---|---|---|
| `CONTRIBUTING.md`, `SECURITY.md`, `CHANGELOG.md` | Contribution policy and disclosure timelines are decisions the project has not made. A placeholder `SECURITY.md` publishes a promise | `LICENSE`, `README.md` |
| Signature verification | Phase 2; the key registry is another team's. **Parked further than originally planned:** the Ed25519 primitives are no longer built ahead of use either, because a primitive with no caller is a primitive whose first real caller finds out what it got wrong | The **slot** in the middleware order and the flag, nothing behind them. `AUTH_ENABLE_SIGNATURE_VERIFICATION=true` therefore **refuses to boot** — a flag named for a security control, silently doing nothing, is the one failure mode worse than not having the flag: an operator reads it back as enabled and is wrong. Task 6 and the `Signature` half of Task 7 are parked with it |
| Rate limiting per subscriber id (A4) | It is keyed on `context.bapId`, and until a signature is verified that field is a claim, not an identity. A limiter that trusts it is one any caller sheds by rotating the field — and one that any caller can turn on a named third party by claiming *their* id, spending someone else's budget for them | A token bucket keyed on the **remote address**, with the same knobs, the same `429` / `Retry-After` / `AUT_RATE_LIMITED` answer and the same eviction. The key moves to the subscriber id in the task that verifies the signature, and not before |
| Publish-time embedding (A5) | 15–40 ms of inference on the write path for one mode of four | `noop` provider; nullable `embedding` column doubles as the backfill queue |
| **L2 extended schema validation (Task 10)** | Skipped by decision on 2026-08-26, not by a technical blocker. The whole task — `SchemaSource`, the refresh loop, `L2`, and the `schemas/<TypeName>/attributes.yaml` set — is unbuilt. **C4 therefore has no enforcer:** nothing requires `@context` and `@type` to be present scalar strings, so Task 22 filters on a field whose shape was never checked, and two publishers disagreeing about it surfaces as a discover query that matches one of them. The SSRF boundary is unaffected: nothing fetches a URL from a payload because nothing fetches at all | Nothing. **`VALIDATION_ENABLE_L2_CONTEXT` and `config/common.yaml`'s `enableL2Context` still default to `true`, and now name a control that does not exist.** Task 20 must either default them off or refuse to boot when they are true, on the same reasoning that made `AUTH_ENABLE_SIGNATURE_VERIFICATION=true` a boot refusal: a flag an operator reads back as enabled, silently doing nothing, is worse than no flag |
| Master catalogs (A1) | Product decision: REGULAR only today | Rejected at intake with `SCH_TYPE_NOT_SUPPORTED` |
| Cadastral-precision geometry | Cell algebra is accurate to one cell (~1.1 km at r8), which is right for discovery and wrong for deciding which side of a boundary a plot sits on. Closing it means PostGIS, and PostGIS is a dependency worth taking only against a requirement that exists | Seven of nine CQL2 operators over all seven RFC 7946 types, with the accuracy stated in [Geospatial Design](#geospatial-design) |
| **`Catalog.descriptor` on the discover response** | Found in Task 19's self-review, root cause older. `Catalog` in `beckn.yaml` is `required: [id, descriptor, provider]` with `additionalProperties: false`, but `domain.Catalog` has no `Descriptor` field, no `catalogs` column stores one, and the publish mapper never reads one — so `render` in `src/discover/service.go` cannot emit it and **every discover response fails its own response schema on a required field.** Closing it is a migration, a `domain.Catalog` field, a publish-mapper branch and a `render` line — four tasks' worth, in none of which it is the deliverable. It is invisible today only because nothing validates a response; Task 21 is where it starts failing | The response omits `descriptor`. `render` says so at the call site instead of claiming the four members it emits are the shape's whole story |
| **`SearchResult.Total` reaches nobody** | `OnDiscoverAction` is `additionalProperties: false` with `catalogs` as its only property (C11's own reasoning), so a carefully-computed count has nowhere on the wire to go. It is not free: `SearchRepository.Search` calls `total()` on every request, and the four skip guards do **not** fire on a full page — the common outcome, since `discover_tsquery` ORs its terms — so an ordinary request pays for an uncapped `count(*)` over the whole match set and the service discards the result. Either give it a header, the way `X-Beckn-Degraded` carries the other thing the body cannot, or stop computing it | `Total` is computed, asserted by `storage/conformance`, and dropped by `discover.Service.Discover`. No production reader exists |
| `S_TOUCHES` and `S_CROSSES` | **Not deferred — refused.** A cell decomposition has no measure-zero boundary, so no resolution answers them. Listing them as "later" would be a promise nothing in this design can keep | `400` + `SCH_TYPE_NOT_SUPPORTED`, naming the operator |

---

## Technology Decisions

Recorded as ADRs in `docs/adr/` — `documentations/` is gitignored as working
notes, not the shared record (see `.gitignore`).

| # | Layer | Choice | Rejected | Why |
|---|---|---|---|---|
| D1 | Router | chi v5 (MIT) | Fiber, Gin, Gorilla | chi is `http.Handler` all the way down, so `r.Context()` propagates natively and `httptest`/`otelhttp`/`testcontainers` need no adapters. Fiber's fasthttp forces a `UserContext()` dance |
| D2 | Data access | sqlc + pgx/v5 (MIT) | GORM | GORM resolves columns by reflection at runtime; a renamed column fails in production. sqlc fails at `make build` |
| D3 | DI | explicit constructors | `dig` | A missing provider must fail at compile time, not panic at startup |
| D4 | L1 validation | kin-openapi (MIT) | hand-rolled | The published `beckn.yaml` *is* the validator |
| D5 | Spatial index | uber/h3-go v4 (Apache-2.0) | PostGIS | H3 cells are plain `bigint`s — GIN-indexable, shardable, portable to Elasticsearch or Redis without rewriting the query. Two covers per geometry (`CONTAINMENT_FULL` and `CONTAINMENT_OVERLAPPING`) turn the CQL2 operators into **set algebra**, so seven of the nine are answered with array operators and no spatial extension is installed at all. The cost is stated rather than elided: accuracy is one cell, `S_TOUCHES` and `S_CROSSES` are refused as unapproximable, and cadastral precision would require PostGIS — see [Geospatial Design](#geospatial-design) |
| D6 | Vector store | pgvector 0.8 + HNSW, cosine | Qdrant, OpenSearch | Single-node deployment; swappable behind `Retriever` alone (A6) — that is the **vector** store only. What the other two swaps cost is in [Data Model](#data-model), because "swappable" unqualified reads as a claim about the metadata store too |
| D7 | Lexical | PostgreSQL FTS (`tsvector` + GIN) | Elasticsearch | No extra infrastructure for v1; fused with vectors by RRF |
| D8 | Embeddings | `Embedder` interface — `noop` default (A5), `hashing` in CI, `ollama` when enabled | OpenAI, Cohere | Self-hosted keeps the DPG mandate |
| D9 | Config | `caarlos0/env/v11` + `yaml.v3`, layered (amended by T1) | viper | Reviewed YAML files under the environment |
| D10 | Migrations | `golang-migrate/v4` | goose, atlas | Plain `.up.sql`/`.down.sql`, `//go:embed`-able so the binary self-migrates |
| D11 | Telemetry | OTel + `otelhttp` + Prometheus client | zap alone | Only a propagation standard makes one request followable across a network hop |

---

## File Structure

Two capabilities behind one binary. `src/domain/` is the seam between them:
publish drives the write port, discover drives the read port, and neither
imports a driver. `src/app` is the only package that imports both capabilities.

```
cmd/discovery-service/main.go       config → app.Build → app.Run

src/publish/                        SYSTEM 1
  controller.go  service.go  mapper.go  geometry.go  text.go

src/discover/                       SYSTEM 2
  controller.go  service.go  intent_mapper.go  filter_parser.go

src/domain/                         THE CONTRACT — stdlib + uuid only
  catalog.go        Catalog, Resource, Offer, GeoPoint, Geometry
  query.go          SearchQuery, SearchModes, filters, SearchResult
  validity.go       TimeOfDay, WithinDailyWindow — the Go twin of the SQL
  mergepatch.go     RFC 7396 MergePatch, MergeCatalog (A8) — pure, no storage
  errors.go         sentinel errors both ports may return
  catalog_repository.go             WRITE port
  search_repository.go              READ port
  retrieval.go      Scope, Retriever, Hydrator (A6)
  purity_test.go    walks imports; fails on anything else

src/beckn/                          wire types
  types.go  actions.go  errors.go

src/indexing/
  geo/h3.go  geo/distance.go
  embeddings/{embedder,ollama,fixture,hashing,noop}.go

src/storage/
  postgres/  pool.go  mapping.go  catalog_repository.go
             search_repository.go  retrievers.go  hydrator.go
             fusion.go  jsonpath.go  queries/  gen/
  memory/repository.go              proves the port is portable — built
                                     incrementally from Task 11 onward, not
                                     in one task; see Task 11's Files note
  conformance/                      one suite both backends must pass —
                                     scaffolded in Task 11, extended by every
                                     task that adds repository behaviour

src/platform/                       knows nothing of publish or discover
  config/  constants/  errors/  logger/  httpx/  registry/
  jsonpath/    canonical.go — jsonpath.Canonicalise, used by BOTH mappers
  crypto/signature/                 deferred, but built
  validation/  spec_index, schema_validator(L1), envelope_rules(C6),
               schema_source, schema_cache, extended_validator(L2)
  telemetry/   telemetry.go  metrics.go
  middlewares/ recover, request_logger, envelope, signature,
               ratelimit, schema_validator, trace

src/app/                            COMPOSITION ROOT
  container.go  router.go  server.go

config/  migrations/  schemas/  .cache/beckn/
tests/   acceptance/  dbtest/  testdata/
  architecture/boundary_test.go     import graph — the TRD §5 swap boundary
docs/
  adr/                               ADR-0001–0015, template, README
  design/discover-and-publish.md    this document
Makefile  Dockerfile  docker-compose.yml  sqlc.yaml  .golangci.yml
```

**Two placements are load-bearing, not filing preferences:**

`jsonpath.Canonicalise` lives in `src/platform/jsonpath/`, not in either mapper.
Both mappers call it, and `src/discover/` importing `src/publish/` to reach it
would weld the two capabilities together — which is precisely the coupling this
layout exists to prevent. The comparison it enables (`g.target_path = ANY($targets)`)
is plain SQL equality only because one function produced both sides of it.

`src/publish/geometry.go` is split out of `mapper.go` because geometry
extraction carries its own fault handling, its own per-geometry error isolation
and its own path construction. Folded into the catalog mapper it is the largest
thing in the file: a general structural walk, bounded, over the whole catalog.

**Middleware order — fixed, do not reorder:**

```
RequestID → Trace → RequestLogger → Recover → Envelope
          → RateLimit → Signature → SchemaValidator → controller
```

**`Signature` is not mounted in Phase 1** — it is parked with Task 6 and does
not exist. The slot is where it goes when Phase 2 builds it; every other link
closes up around the gap, and Task 20 wires the chain without it. The order
below is the Phase 2 order, kept whole so the reasoning survives the parking.

`Recover` is outermost of everything that touches the *request* — `Envelope`
and below — so it catches every handler and every middleware that reads, parses
or rejects. **`RequestLogger` sits above it, not below** (A11): the 500 a
recovered panic produces has to go out through `RequestLogger`'s response
wrapper, or the one request an operator most needs timed is the one request that
logs nothing at all and any count of requests by status silently under-counts
exactly the failures. The cost is that `RequestLogger` is itself outside
`Recover`, so a panic *in it* is uncaught — the same exposure `RequestID` and
`Trace` already carry, and the right one: a panic in our own logging middleware
is a bug in this repo rather than a request-shaped fault, and `net/http`'s own
recovery is where a bug in this repo belongs. `RequestLogger` also starts the
timer before auth, so rejected requests still report latency. `Envelope`
precedes `Signature` because a NACK for a signature failure
needs the `messageId` to echo (C13), and only `Envelope` has read the body it is
in. **Not `transactionId`** — no member of the Ack family declares one; all ten
are exactly `{message: {status, messageId, error}}`, with no `context` key, so
`messageId` is the whole of what a NACK carries. `RateLimit`
sheds load before the signature check it exists to protect, and after `Envelope`
because `Envelope` is what bounds the body (C14): shedding first would leave the
one unbounded allocation on the path in front of the thing that sheds. `Trace` sits above `Recover` so a panic
lands inside a span, and above `Envelope` so the span covers body reading.

---

## Data Model

Two layers, separated because conflating them is what makes storage
unswappable:

**A — the swap boundary.** A repository interface alone does not achieve
swappability; it is defeated in three ways, each forbidden explicitly:

| Leak | Symptom | Rule |
|---|---|---|
| Type | `pgtype.UUID`, `pgvector.Vector` in a domain signature | `src/domain/` imports stdlib + `google/uuid` only, enforced by `purity_test.go` |
| Dialect | The service composes SQL fragments or passes a `WHERE` string down | Queries are **data** — a `SearchQuery` the backend interprets. No caller names a column |
| Capability | A weaker backend silently returns worse results | Backends declare `Capabilities`; unsupported modes fail loudly or degrade **observably** |
| Grammar | The accepted wire grammar is whatever the engine happens to parse, so changing the engine changes what callers may send | The filter subset is validated in `src/platform/jsonpath/` **before any store sees it** (C10). A backend that cannot execute it declares the capability missing and degrades; the accepted grammar does not move |

**Three swaps, three costs (TRD §5).** The TRD names PostgreSQL for a small
instance, and YugabyteDB for metadata with Qdrant for vectors at scale.
"Swappable" covers three different amounts of work, and collapsing them into one
word is how it stops being something anyone can act on:

| Swap | What it costs | Why that little |
|---|---|---|
| **Vector store** — pgvector → Qdrant | One new `Retriever` | A6 split retrieval into one `Retriever` per mode for exactly this. No other mode learns it changed |
| **Geo index** — PostgreSQL → Elasticsearch, Redis | One new `Retriever` | D5: H3 cells are plain `BIGINT`s computed in Go, and no PostGIS is installed. Nothing spatial is being ported — only an integer set-membership test |
| **Metadata and transactions** — PostgreSQL → YugabyteDB | One new package under `src/storage/`, plus one line in `container.go` | Everything engine-specific already lives under `src/storage/postgres/`. What makes that a fact rather than an intention is the test below |

**`boundary_test.go` — the twin of `purity_test.go`, one level out.**
`purity_test.go` guards `src/domain/`, and says nothing about the packages that
*consume* the ports. `boundary_test.go` walks the whole module's import graph and
fails the build if any package other than `src/storage/postgres/**` and
`src/app/container.go` imports `pgx`, `pgvector`, `sqlc`-generated code, or
`src/storage/postgres` itself.

That one allowance for `container.go` is what turns "minimal code changes" into a
number: a second engine is **a new directory under `src/storage/` and one
constructor call in the composition root.** Without the test the claim decays on
the first afternoon somebody reaches for `pgx.ErrNoRows` in
`src/publish/service.go` — a one-line change that compiles, passes every existing
test, and moves the swap boundary a layer outward in silence.

**What stays PostgreSQL-shaped, and why that is not a portability failure.**
`tsvector` + GIN (D7), GIN `fastupdate = off`, the expression-based unique index
on `COALESCE(resource_id, '')`, `pgx.Batch` pipelining, and `sqlc` compiling
`.sql` against a live server at build time. All five are engine-specific, all
five sit inside `src/storage/postgres/`, and none of them crosses the port. A
second engine reimplements them, or declares the capability missing and degrades
observably — it does not inherit them. `src/storage/conformance/` is what admits
it: a backend is accepted by **passing the suite both existing backends pass**,
not by review.

**B — the PostgreSQL realisation: four tables.** Shaped by the query modes, not
by the Beckn object graph. Beckn's aggregate boundary is the *catalog*, so
normalising below it buys nothing and costs a join on every read.

| Table | Grain | Why |
|---|---|---|
| `catalogs` | one per published catalog | The publish/merge unit and the transaction boundary. Holds the provider document — stored **once**, not per resource |
| `resources` | one per resource per catalog | **The only table discover scans.** Search vector, embedding, schema pair, attributes *and the scope gate* all on the row — nothing it holds is a copy of something else on the same row except `name`, which exists to carry a trigram index |
| `resource_geometries` | one per geometry, per catalog **or** per resource | Keyed by the JSONPath it was found at, because the path is what a spatial constraint names |
| `offers` | one per offer per catalog | A row, not a JSONB array, because discover returns the offers attached to the resources it matched — which is an overlap query against `resource_ids` |

### The scope gate lives on `resources`, not on `catalogs`

Every discover query answers "is this row visible to me, right now". Putting
those columns only on `catalogs` means every query joins, including the `count(*)`
that computes `Total` — and that count runs over *every* match, not just the
page. Five thousand text hits meant five thousand probes into a second table.

So the gate columns are **copied onto `resources`** and the join disappears.
`provider` is *not* copied: it is 1–3 KB, and duplicating it across forty
resource rows is forty times the write on every republish for no read benefit.

That copy is only safe because of one rule, stated here and enforced in
`UpsertCatalog`: **every publish rewrites the gate on every resource of the
catalog, unconditionally** — not only the resources present in the payload.
Without it a publisher who changes `visibleTo` while sending no resources
updates the catalog and nothing else, and the change silently does nothing.

Everything PostgreSQL-specific is confined to three groups of files, so a
reviewer can audit the entire swap surface by reading them:

| Mechanism | Confined to |
|---|---|
| H3 cell computation and array overlap | `indexing/geo/h3.go`, `postgres/search_repository.go` |
| `tsvector`, `websearch_to_tsquery`, pgvector `<=>` | `migrations/*.sql`, `postgres/queries/discover.sql` |
| SQL/JSON path rendering | `postgres/jsonpath.go` (Task 22) |

### Migration 001 — extensions

```sql
-- Two extensions, and no PostGIS: H3 cells are plain BIGINTs computed in Go,
-- full-text search and SQL/JSON path are core PostgreSQL.
CREATE EXTENSION IF NOT EXISTS vector;
-- Trigram similarity for the misspellings stemming cannot recover ("tracter").
CREATE EXTENSION IF NOT EXISTS pg_trgm;
```

### Migration 002 — `catalogs`

Deliberately small. Discover never reads this table; it holds the provider
document, the publish bookkeeping, and the write-side source of truth for the
gate that `resources` carries a copy of.

```sql
CREATE TABLE catalogs (
    -- `CHECK (id <> '')` on every id column in this schema, for one reason
    -- that only surfaces two migrations later: `uq_resource_geometries` keys on
    -- `COALESCE(resource_id, '')`, so an empty-string resource id and a
    -- catalog-level geometry become the same key. The constraint lives on all
    -- four id columns rather than only that one, because "ids are never empty"
    -- is the invariant, and enforcing it in the one place that currently
    -- depends on it is how it stops being true somewhere else.
    id           TEXT PRIMARY KEY CHECK (id <> ''),

    -- Verbatim, and stored exactly once per catalog. A providers table would
    -- add a join to every read to save nothing: no query reaches a provider
    -- except through its catalog.
    -- DEFAULT so the lock-and-load INSERT that opens every publish can name
    -- `id` alone. See `updateMode` — MERGE and FULL.
    provider     JSONB       NOT NULL DEFAULT '{}'::jsonb,

    -- publishDirective.visibleTo: the network ids this catalog is discoverable
    -- from. An array because that is what the directive carries — a publisher
    -- naming two networks publishes into both from one call.
    --
    -- DEFAULT '{}' is a fail-safe, not a valid state: the writer fills an empty
    -- list with the request's network first, because a catalog visible to
    -- nobody is findable by nobody while reporting success.
    visible_to   TEXT[]      NOT NULL DEFAULT '{}',

    -- The publisher's own off switch (catalog.isActive). Withdrawing is not the
    -- same as narrowing.
    active       BOOLEAN     NOT NULL DEFAULT TRUE,

    -- catalog.validity is a TimePeriod, and a TimePeriod carries TWO windows
    -- that the spec's anyOf lets appear separately or together:
    --   startDate/endDate  a one-off calendar range   ("live Jan -> Mar")
    --   startTime/endTime  a window that REPEATS DAILY ("open 09:00 -> 17:00")
    -- They are independent, so they are two independent column pairs, and a
    -- row must satisfy both to be live. NULL means unbounded on that axis.
    valid_from      TIMESTAMPTZ,
    valid_to        TIMESTAMPTZ,
    valid_time_from TIME,
    valid_time_to   TIME,

    -- published_at is set on first publish and never moves; updated_at moves on
    -- every republish. The upsert must set updated_at explicitly, because
    -- DEFAULT now() only fires on INSERT.
    published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**The gate columns above are written and never read, and that is on purpose.**
Discover issues exactly one query against this table — `select id, provider
from catalogs where id = any(...)` at hydration — so `visible_to`, `active` and
the four validity columns are write-side state only. They stay for two reasons
the dropped columns below did not have: this table has **one row per catalog**,
not one per resource, so the storage argument that removes a column from
`resources` does not apply here; and they are the record of what the publisher
actually asked for, which is the only thing that can adjudicate a "why is my
catalog invisible" report against a `resources` copy that a bug could have
written wrong. A copy is not a source of truth unless the original survives.

**No index on this table, and no `catalog_type`, `provider_id` or `network_id`
column.** Each was written and read by nothing:

- `provider_id` was `provider->>'id'` copied out, and the only index on it
  served no query in this plan — `CatalogRepository` has no list-by-provider.
- `network_id` was pure audit. The writer uses the request's network at publish
  time to fill an empty `visible_to`; that happens in Go, before the insert.
  If the audit trail matters it belongs in the log line, not on every row.
- `catalog_type` could only ever hold `REGULAR`, because A1 refuses MASTER at
  intake. Adding it back when inheritance lands is `ALTER TABLE ADD COLUMN`
  with a default — instant in PostgreSQL 11+, not a real migration.

### Migration 003 — `resources`

```sql
CREATE TABLE resources (
    id          TEXT NOT NULL CHECK (id <> ''),
    catalog_id  TEXT NOT NULL REFERENCES catalogs (id) ON DELETE CASCADE,

    -- ---- the scope gate, copied from the catalog -------------------------
    -- Written by UpsertCatalog in the same transaction as the catalog row, for
    -- EVERY resource of the catalog on EVERY publish. This is what removes the
    -- join from the read path; the unconditional rewrite is what keeps the two
    -- copies from drifting.
    visible_to      TEXT[]      NOT NULL DEFAULT '{}',
    active          BOOLEAN     NOT NULL DEFAULT TRUE,
    valid_from      TIMESTAMPTZ,
    valid_to        TIMESTAMPTZ,
    valid_time_from TIME,
    valid_time_to   TIME,
    -- `Resource` in the spec has NEITHER `isActive` NOR `validity` — only
    -- `{id, descriptor, resourceAttributes}`. Every column in this block is
    -- therefore a derived copy of the catalog's, never publisher-supplied,
    -- which is exactly why the unconditional rewrite below is safe.
    -- ----------------------------------------------------------------------

    -- A duplicate of descriptor->>'name', and knowingly so: fuzzy search needs
    -- GIN (name gin_trgm_ops), and a trigram index over a JSONB extraction is
    -- worse to build, to read and to explain. A duplicate paid for by an index.
    name        TEXT  NOT NULL DEFAULT '',
    descriptor  JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- JSON-LD domain attributes, already validated by L2.
    attributes  JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- resourceAttributes.@context and .@type, both scalar `string` and both
    -- REQUIRED by the Attributes schema (C4). Two plain columns, because the
    -- filter that reads them must match them as a PAIR — see below. Default ''
    -- covers the resource that carries no resourceAttributes at all, which the
    -- Resource schema permits (only `id` is required).
    schema_context TEXT NOT NULL DEFAULT '',
    schema_type    TEXT NOT NULL DEFAULT '',

    -- No geometry columns. Every geometry lives in resource_geometries, one row
    -- per geometry, keyed by its path — see that table for why.

    -- Derived in Go at publish (stripping JSON-LD keywords and attribute keys)
    -- and passed in as a parameter, so the Go function stays the one source of
    -- truth for what is searchable. Only the tsvector is kept.
    --
    -- There is no `search_text` column. It would be the concatenation
    -- of `name`, `descriptor` and every value in `attributes` — the largest
    -- text on the widest, hottest table, holding a second copy of bytes already
    -- in three columns of the same row. Its only reader was the Phase 2
    -- embedding backfill, which already loads those three columns and can call
    -- `deriveSearchText` on them for far less than the cost of storing the
    -- answer on every row for ever.
    search_tsv  TSVECTOR NOT NULL DEFAULT ''::tsvector,

    -- Nullable: a publish must succeed when the embedding service is down, and
    -- the resource stays discoverable lexically and geospatially. NULL for
    -- every row in Phase 1 (A5) — which makes `embedding IS NULL` the backfill
    -- queue, and is why no outbox table exists.
    --
    -- That queue gets no index HERE and wants one THEN. In Phase 1 every row
    -- is NULL, so a sequential scan is the optimal plan and a partial index
    -- would be a second copy of the whole table. During the Phase 2 backfill
    -- the remainder shrinks while the table does not, so the same scan gets
    -- steadily more wasteful with each batch. `CREATE INDEX … ON resources
    -- (catalog_id, id) WHERE embedding IS NULL` is the Phase 2 migration, and
    -- it self-empties as the backfill drains it. Written down here because the
    -- absence is deliberate now and a bug later, which is not something the
    -- schema can say on its own.
    embedding   VECTOR(768),

    -- blake2b-256 of the derived text `embedding` was (or will be) computed
    -- from. WRITTEN FROM DAY ONE, including under the noop embedder, because it
    -- is a hash of the TEXT and not of the vector: it is well defined whether
    -- or not an embedding exists, and it is the only thing that can answer "did
    -- the searchable content of this resource actually change?" on a republish.
    --
    -- Leaving it NULL in Phase 1 would break two things quietly. The Phase 2
    -- backfill would have no baseline and would re-embed the entire corpus on
    -- first run; and the A8 test that asserts an untouched resource keeps its
    -- hash would be comparing NULL to NULL, passing while proving nothing —
    -- which is the regression it exists to catch. 32 bytes is not what makes a
    -- row wide.
    -- It is what makes that queue correct rather than approximate: without it a
    -- republished resource whose text changed keeps its old vector for ever,
    -- and a stale vector is worse than a missing one — a missing one degrades
    -- visibly, a stale one returns confident nonsense silently.
    embedding_source_hash BYTEA,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A resource id is unique within its catalog, not globally: two providers
    -- may both publish "r1".
    PRIMARY KEY (catalog_id, id)
);

-- The variable half of the gate. Bitmap-ANDed with whichever search index the
-- query mode uses — both on this table, so neither costs a join.
--
-- fastupdate = off, deliberately, and the same on the two cell indexes in
-- Migration 004. With GIN's default fastupdate = on, inserts land in a pending
-- list, and PostgreSQL's own warning is that "searches must scan the list of
-- pending entries in addition to searching the regular index, so a large list
-- of pending entries will slow searches significantly". A search does not clean
-- that list — only VACUUM, autoanalyze, `gin_clean_pending_list()`, or an insert
-- that pushes it past `gin_pending_list_limit` does. So the cost is not one
-- unlucky discover paying for a flush; it is EVERY discover degrading in
-- proportion to how much publishing has happened since the last one, which is
-- exactly the shape a p95 SLA cannot absorb. Turning it off moves the work to
-- the writer, where there is no SLA to miss.
CREATE INDEX idx_resources_visible_to ON resources USING GIN (visible_to)
    WITH (fastupdate = off);

-- The constant half of the gate goes INSIDE each search index, so a withdrawn
-- catalog's resources are not merely skipped, they are not in the index at all.
-- Validity cannot join them: now() is not IMMUTABLE, so it stays a filter.
CREATE INDEX idx_resources_search_tsv ON resources USING GIN (search_tsv)
    WHERE active;
CREATE INDEX idx_resources_name_trgm  ON resources USING GIN (name gin_trgm_ops)
    WHERE active;
-- Composite, in this order, and NOT two separate indexes. Every schemaContext
-- entry constrains `schema_context`; only some also constrain `schema_type`. A
-- btree leading with schema_context serves both shapes from one structure.
CREATE INDEX idx_resources_schema ON resources (schema_context, schema_type)
    WHERE active;
-- NO `idx_resources_catalog_id`. `PRIMARY KEY (catalog_id, id)` builds a btree
-- leading with catalog_id, which serves every catalog_id-prefix lookup this
-- plan issues — the per-catalog rewrite, the FULL-republish delete, and the
-- cascade probe from resource_geometries — at no extra write cost. A second
-- index on the same leading column is a duplicate that only the write path
-- pays for.

-- jsonb_path_ops, not the default jsonb_ops: a third the size and faster for
-- the path-exists queries this service issues (Task 22). It cannot serve
-- key-existence (?) queries, which nothing here needs.
CREATE INDEX idx_resources_attributes ON resources USING GIN (attributes jsonb_path_ops);

-- HNSW, not IVFFlat: no training pass, so it works from the first row. Not
-- partial: a partial HNSW would have to be rebuilt when `active` flips.
--
-- It ships EMPTY, because Phase 1 writes no vectors, and an empty HNSW costs
-- nothing to carry. The Phase 2 backfill should DROP and recreate it rather
-- than insert through it: building a graph incrementally, one row at a time,
-- over a corpus that is already there is markedly slower than one bulk build
-- at the end, and the index is useless until the backfill completes anyway.
-- Noted here beside the "not partial" decision because both are choices about
-- when this index is built, and only one of them was written down.
CREATE INDEX idx_resources_embedding ON resources
    USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);
```

**Dropped, with the reason each was dead weight:** `catalog_type`,
`master_catalog_id`, `master_resource_id` and `variant` are NULL or constant
for ever while A1 stands. `pending_targets` (A7) was written on every resource
insert and **read by nothing** — there is no second backend and no reconciler
in this plan, so it was write cost for a feature that does not exist. A7's
*seam* survives where it belongs, in the `Retriever`/`Hydrator` interfaces; the
column returns with the reconciler that reads it.

### Migration 004 — `resource_geometries`

One row per published geometry, keyed by the JSONPath it was found at.

**The path is the key because the path is what a request names.** A spatial
constraint arrives carrying
`targets: "$.catalogs[*].provider.availableAt[*].geo"` and asks about the
geometry *there* — and the publish walker finds geometry at **any** path, so
`targets` naming `$.catalogs[*].resources[*].resourceAttributes.serviceArea`
is an equally answerable question against the same table. Union every geometry on a resource into one cell array and
that question stops being answerable: a provider's shopfront and a delivery
polygon buried in `resourceAttributes` share the array, so a query naming either
matches both.

**`resource_id` is nullable, and that is what stops a 40× duplication.** The
geometry in `provider.availableAt` belongs to the *catalog*, not to any one
resource. Keying every geometry to a resource meant a catalog of 40 resources
with 3 provider locations stored **120 rows for 3 distinct shapes** — and, worse,
ran 120 H3 polygon fills at publish time instead of 3. NULL means "this geometry
belongs to the whole catalog, and therefore to every resource in it".

```sql
CREATE TABLE resource_geometries (
    catalog_id    TEXT NOT NULL,

    -- NULL = catalog-level: found on the catalog's provider, shared by every
    -- resource in it, stored once. Non-NULL = found on that one resource.
    --
    -- NULL and '' must stay distinguishable here: the unique index below folds
    -- them together with COALESCE, so an empty-string resource id would key
    -- identically to a catalog-level row and one would silently upsert over the
    -- other. The FK to `resources` already makes '' unstorable — `resources.id`
    -- carries the same CHECK — and this one states the dependency where the
    -- index that needs it can be read beside it.
    resource_id   TEXT CHECK (resource_id IS NULL OR resource_id <> ''),

    -- Wildcard form, byte-identical to what a caller sends in `targets`, and
    -- the ONLY column a spatial constraint is compared against — which is what
    -- the name says, where a bare `path` beside a `source_path` did not:
    --   $.catalogs[*].provider.availableAt[*].geo
    target_path   TEXT NOT NULL,

    -- The same path with concrete indices:
    --   $.catalogs[0].provider.availableAt[2].geo
    -- In the key instead of the reference implementation's positional `seq`.
    -- It is NOT stable under array reordering — it is positional too — but it
    -- names its own source, which a bare ordinal cannot, and it has no SMALLINT
    -- ceiling. Reordering is handled by the writer, which deletes a catalog's
    -- geometry rows and re-inserts them rather than trying to match them up.
    source_path TEXT NOT NULL,

    -- No `geom_type` column. It held `geojson->>'type'` copied out, and the
    -- one place the type is tested — `geo_distance_m` — reads it from `geojson`
    -- directly. A future "polygons only" filter reads it the same way, or gets
    -- an expression index; neither needs a stored copy kept in step by hand.

    -- Verbatim. The reference implementation stores only a parsed form, which
    -- is how it drops five of the seven types and every polygon hole — a donut
    -- service area becomes a filled disc and S_CONTAINS starts answering true
    -- for addresses in the hole. Keeping the original costs one JSONB column.
    geojson       JSONB NOT NULL,

    -- The two covers the CQL2 operator set is answered from, both at
    -- `ResolutionCells`. See [Geospatial Design](#geospatial-design) — the
    -- invariant is `cells_full ⊆ the true geometry ⊆ cells_cover`, and it is
    -- the reason there are two columns rather than one.
    --
    -- ContainmentFull: cells lying ENTIRELY inside the geometry. A guaranteed
    -- SUBSET, and therefore the only column that can prove a positive. Empty
    -- for every Point and LineString and for any polygon smaller than a cell —
    -- correctly, since none of them contains a cell.
    cells_full    BIGINT[],

    -- ContainmentOverlapping: cells touching the geometry at all. A guaranteed
    -- SUPERSET, and therefore the only column that can prove a negative.
    cells_cover   BIGINT[],

    -- Both arrays are stored ASCENDING and DEDUPLICATED, and the writer is the
    -- only place that can guarantee it. `&&` and `<@` do not care, but
    -- `S_EQUALS` compares with array `=`, which in PostgreSQL is element-wise
    -- IN ORDER: two identical cell sets emitted by H3 in different orders
    -- compare unequal. That is a false negative on the one operator whose
    -- stated property is that it has none. Query covers are sorted the same
    -- way, in `geo.CoverQuery`, for the same reason.

    -- Both nullable together, and NULL is load-bearing: it means "over
    -- geo.MaxIndexCoverCells", not "covers nothing". Such a row is decided by
    -- the bounding box alone and is always MAYBE inside it. A truncated array
    -- instead would make a state-sized service polygon undiscoverable outside
    -- whichever corner the fill happened to reach.
    --
    -- They are NULL as a pair, never one without the other: a row holding a
    -- `cells_full` with no `cells_cover` would prove positives it cannot
    -- bound, and the predicate has no branch for that state. Stated as a
    -- CHECK below rather than left to the writer, on the same argument the
    -- `CHECK (id <> '')` block on `catalogs` makes: enforcing an invariant
    -- only where something currently depends on it is how it stops being true
    -- somewhere else.

    -- NOT NULL: a row exists only because a geometry parsed, and a geometry
    -- that parsed has a box. Load-bearing in a way it was not when the box was
    -- merely a second filtering stage — for an oversize row (both cell columns
    -- NULL) this box is the ENTIRE predicate.
    min_lat DOUBLE PRECISION NOT NULL,
    max_lat DOUBLE PRECISION NOT NULL,
    min_lon DOUBLE PRECISION NOT NULL,
    max_lon DOUBLE PRECISION NOT NULL,

    -- ---- three invariants the predicate's correctness rests on -----------
    -- Both cell columns NULL or neither. The predicate short-circuits on
    -- `cells_cover IS NULL` alone and never re-tests `cells_full`, so a
    -- half-NULL row would reach the operator CASE with a NULL operand, and a
    -- NULL inside EXISTS is a miss.
    CHECK ((cells_full IS NULL) = (cells_cover IS NULL)),

    -- A stored cover is never the EMPTY array. `cells_full` legitimately is —
    -- a Point contains no cell — but `cells_cover` cannot be, because every
    -- geometry that parsed touches at least one cell. The constraint is
    -- load-bearing rather than tidy: `S_WITHIN`, `S_CONTAINS` and `S_DISJOINT`
    -- are all refuted through `cells_cover <@ …`, and `'{}' <@ anything` is
    -- TRUE in PostgreSQL, so an empty cover would silently answer those three
    -- operators with "cannot refute" for the row it belongs to. It is also the
    -- backstop for a geometry whose `coordinates` is a well-formed but EMPTY
    -- array — `looksLikeGeoJSON` accepts the shape, and the parser must fault
    -- it rather than emit a row with nothing in it.
    CHECK (cells_cover IS NULL OR cardinality(cells_cover) > 0),

    -- The box is well-ordered. A geometry crossing the antimeridian gets
    -- min_lon = -179, max_lon = 179 — the whole globe, which is over-inclusive
    -- and therefore safe under the superset rule, and matters only for an
    -- oversize row where the box is the entire predicate. This CHECK exists to
    -- stop the well-meant "fix" that stores [179, -179] instead: `max_lon >=
    -- $min AND min_lon <= $max` is then false for every query, and the
    -- geometry becomes undiscoverable rather than over-discoverable, which is
    -- the one direction this design never moves in.
    CHECK (min_lat <= max_lat AND min_lon <= max_lon),

    -- Catalog-level rows hang off the catalog directly, which is also what
    -- cascades them when it is deleted.
    FOREIGN KEY (catalog_id) REFERENCES catalogs (id) ON DELETE CASCADE,

    -- Resource-level rows additionally hang off their resource. Under MATCH
    -- SIMPLE a NULL resource_id makes this constraint pass trivially, which is
    -- exactly the behaviour a catalog-level row needs.
    FOREIGN KEY (catalog_id, resource_id)
        REFERENCES resources (catalog_id, id) ON DELETE CASCADE
);

-- The key, as a unique index rather than a PRIMARY KEY, because a PK may not
-- contain an expression and NULL resource_ids must still collide on duplicates.
--
-- COALESCE picks '' as the sentinel for "catalog-level", which is only safe
-- because no resource id can BE '': `resources.id` and this table's
-- `resource_id` both carry a CHECK saying so. Without that pair the sentinel is
-- a value in the domain it is standing outside of, and a resource published
-- with `"id": ""` would share a key with the catalog's own geometry at the same
-- source path — one upserting over the other, in silence, at publish time.
CREATE UNIQUE INDEX uq_resource_geometries
    ON resource_geometries (catalog_id, COALESCE(resource_id, ''), source_path);

-- The operator predicates are array overlap (`&&`) and containment (`<@`,
-- `@>`), which is precisely what GIN's array_ops answers.
--
-- fastupdate = off for the reason spelled out on idx_resources_visible_to in
-- Migration 003: a pending list is scanned by every search and flushed by none
-- of them. These two carry it worst — a cover is up to MaxIndexCoverCells
-- entries for ONE geometry, so a single republish can bloat the list far past
-- anything a scalar column would.
CREATE INDEX idx_rg_cells_full ON resource_geometries USING GIN (cells_full)
    WITH (fastupdate = off);
CREATE INDEX idx_rg_cells_cover ON resource_geometries USING GIN (cells_cover)
    WITH (fastupdate = off);

-- A note on `<@` for the reader who checks: GIN supports contained-by, but
-- PostgreSQL estimates its selectivity poorly, and `S_WITHIN`/`S_CONTAINS` are
-- built on it. Both predicates are correlated inside an EXISTS already scoped
-- to one catalog_id, so the row count reaching them is small whatever the
-- planner believes. Task 16's EXPLAIN assertions cover the operators, not just
-- the overlap case, for exactly this reason.

-- The cascade delete and the per-resource rewrite.
CREATE INDEX idx_rg_catalog_resource ON resource_geometries (catalog_id, resource_id);

-- `targets` is an equality filter on `target_path`, and the walker finds geometry
-- anywhere in the document, so this column now holds many distinct values
-- rather than one. It is part of the same predicate as the cell overlap, so it
-- rides the composite rather than getting an index of its own.
CREATE INDEX idx_rg_catalog_target_path
    ON resource_geometries (catalog_id, target_path);

-- NO index on the bounding box:
--
-- A bounding-box overlap is `max_lat >= $1 AND min_lat <= $2` — two open-ended
-- ranges. A btree leading with min_lat can only range-scan on the first column,
-- so it reads up to half the table before max_lat can help; btrees do not do
-- overlap, which is what GiST exists for. The box is evaluated inside an EXISTS
-- already correlated to one catalog_id, so it is a cheap FILTER over that
-- catalog's geometry rows and wants no index of its own — including for the
-- oversize rows where it is the only predicate, because there are few of them
-- and they are reached through the same correlation.
```

### Migration 005 — SQL functions

The exact geo filter **must run in SQL, not Go.** PostgreSQL computes `count(*)`
for `Total` and applies `LIMIT`/`OFFSET`. A filter applied in Go after the rows
return corrects neither: `Total` counts rows the caller never sees, page 3
overlaps page 2, and each retrieval mode sees a different candidate set.

```sql
-- IMMUTABLE so the function stays eligible for an expression index later, and
-- so PostgreSQL MAY inline this body into the calling query. Not constant
-- folding: the arguments are columns, so there is nothing to fold. PARALLEL
-- SAFE so a parallel seq scan may evaluate it.
--
-- STRICT so a NULL coordinate propagates instead of reading as zero — and
-- here that is not the usual argument, because without STRICT this body does
-- something worse than return zero. `least()` IGNORES NULLs: with a NULL
-- latitude the haversine term is NULL, `least(1, NULL)` is 1, `asin(1)` is
-- pi/2, and the function confidently returns ~20,015 km — half the Earth's
-- circumference — for a coordinate it could not read. STRICT is what makes it
-- return NULL instead, which the call site is written to expect.
--
-- STRICT and inlining are in tension here, and the plan states which side it
-- takes rather than assuming the question away. `least()` is non-strict, so
-- this body is non-strict, and PostgreSQL declines to inline a STRICT SQL
-- function whose body is non-strict — the same rule cited two functions down
-- as the reason `geo_distance_m` is NOT marked STRICT. The clamp cannot simply
-- go: floating-point overshoot puts the argument at 1 + 1e-16 for antipodal
-- points and `asin()` then raises "input is out of range", a hard error on a
-- live query. So correctness wins and this one is expected NOT to inline,
-- costing a function call per candidate row on the Point-to-Point refinement
-- path only. Task 14 asserts the outcome with `EXPLAIN (VERBOSE)` rather than
-- leaving the reader to assume it was checked — if a later PostgreSQL inlines
-- it after all, the test is what tells us.
CREATE OR REPLACE FUNCTION geo_haversine_m(
    lat1 DOUBLE PRECISION, lon1 DOUBLE PRECISION,
    lat2 DOUBLE PRECISION, lon2 DOUBLE PRECISION
) RETURNS DOUBLE PRECISION
LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS $$
    SELECT 2 * 6371008.8 * asin(sqrt(least(1,
        power(sin(radians(lat2 - lat1) / 2), 2) +
        cos(radians(lat1)) * cos(radians(lat2)) *
        power(sin(radians(lon2 - lon1) / 2), 2)
    )));
$$;

-- Distance to one stored Point, and ONLY a stored Point. Every geometry type
-- including this one is decided by the cell algebra in
-- [Geospatial Design](#geospatial-design); this function exists to SHARPEN the
-- single commonest case — `S_DWITHIN` from a Point to a stored Point — from
-- cell accuracy (~1.1 km at r8) to exact.
--
-- It returns NULL for the other six types, and the call site guards on
-- `geom->>'type' = 'Point'` so that NULL is never compared. The guard is not
-- belt-and-braces: an unguarded `NULL <= radius` is UNKNOWN, which fails inside
-- EXISTS and SUCCEEDS inside NOT EXISTS, and that asymmetry is what previously
-- returned a Polygon lying inside the radius from a "nowhere near here" query.
-- The function keeps returning NULL because that is honest; the predicate is
-- what must never ask.
--
-- GeoJSON is [longitude, latitude]: index 0 is lon, index 1 is lat, the reverse
-- of every argument list in this file. Swapping them puts Bengaluru (12.97,
-- 77.64) in Somalia, and both values stay in range so nothing rejects it. This
-- cast is the one place the order is decided.
--
-- NOT STRICT, unlike geo_haversine_m above, and the asymmetry is deliberate. It
-- would be redundant — a NULL geom makes `geom->>'type'` NULL, the CASE falls
-- through, and the result is NULL anyway — and it would cost something:
-- PostgreSQL declines to inline a STRICT SQL function whose body is non-strict,
-- and a CASE body is non-strict. Marking it STRICT buys nothing and adds a
-- function call per candidate row.
CREATE OR REPLACE FUNCTION geo_distance_m(
    geom JSONB, lat DOUBLE PRECISION, lon DOUBLE PRECISION
) RETURNS DOUBLE PRECISION
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE
        WHEN geom->>'type' = 'Point'
         AND jsonb_typeof(geom->'coordinates') = 'array'
         AND jsonb_array_length(geom->'coordinates') >= 2
         AND jsonb_typeof(geom->'coordinates'->0) = 'number'
         AND jsonb_typeof(geom->'coordinates'->1) = 'number'
        THEN geo_haversine_m(lat, lon,
                             (geom->'coordinates'->>1)::DOUBLE PRECISION,
                             (geom->'coordinates'->>0)::DOUBLE PRECISION)
    END;
$$;

-- websearch_to_tsquery joins terms with AND, the wrong default for discovery:
-- "wheat seeds for sale" matches nothing because no listing has all four words.
-- OR semantics return every wheat and every seed listing and let ts_rank_cd
-- float the ones matching more of the query. Precision is RRF's job, not the
-- retrieval predicate's.
--
-- The rewrite is applied to websearch_to_tsquery's OUTPUT, which is what makes
-- it safe: PostgreSQL has already parsed and escaped the caller's text, so no
-- amount of punctuation produces a tsquery the caller wrote.
--
-- A query containing an exclusion keeps AND. Under a disjunction a negated term
-- stops excluding and starts matching everything that lacks it, so
-- "tractor -diesel" would return the whole catalogue.
CREATE OR REPLACE FUNCTION discover_tsquery(query TEXT) RETURNS tsquery
LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS $$
    SELECT CASE
        WHEN websearch_to_tsquery('simple', query)::text LIKE '%!%'
            THEN websearch_to_tsquery('simple', query)
        ELSE replace(websearch_to_tsquery('simple', query)::text, ' & ', ' | ')::tsquery
    END;
$$;

-- The daily half of validity. A separate function because the wrap-around case
-- is the one every hand-written BETWEEN gets wrong: a shop open 22:00 -> 02:00
-- has from > to, and `t BETWEEN from AND to` is then false for every t. Every
-- retriever, the counter, the hydrator and the offer join carry it, so it is
-- one definition and one place to be right — plus one Go twin,
-- domain.WithinDailyWindow, for the backends that are not this one.
--
-- Takes the instant as an argument rather than calling now(). A function that
-- called now() could not honestly be IMMUTABLE, and IMMUTABLE is what lets
-- PostgreSQL inline this body into the calling query rather than call it per
-- row. Nothing here is folded at plan time: `(now() AT TIME ZONE 'UTC')::time`
-- is STABLE, evaluated once per execution.
--
-- NOT STRICT, deliberately. The NULL branch IS the answer for "no daily window
-- set". STRICT would return NULL there instead, the gate clause would fail, and
-- every catalog that never set opening hours would vanish from discover.
CREATE OR REPLACE FUNCTION within_daily_window(
    from_t TIME, to_t TIME, at_utc TIME
) RETURNS BOOLEAN
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE
        WHEN from_t IS NULL OR to_t IS NULL THEN TRUE   -- no daily window set
        WHEN from_t <= to_t THEN at_utc >= from_t AND at_utc <= to_t
        ELSE                     at_utc >= from_t OR  at_utc <= to_t   -- wraps
    END;
$$;
```

### Migration 006 — `offers`

Discover returns the offers attached to the resources it matched, so this table
is on the read path and its shape is driven by that query, not only by publish.

```sql
CREATE TABLE offers (
    id           TEXT NOT NULL CHECK (id <> ''),
    catalog_id   TEXT NOT NULL REFERENCES catalogs (id) ON DELETE CASCADE,

    -- Which resources this offer applies to. EMPTY MEANS CATALOG-WIDE — an
    -- offer on everything the provider sells — so it is a meaningful state and
    -- not a default to be pruned into. See the FULL-republish rule below.
    resource_ids TEXT[]      NOT NULL DEFAULT '{}',

    -- The offer document, verbatim, exactly as `provider` is on catalogs and
    -- `geojson` is on geometries. It is what an attribute filter rooted at the
    -- offer path is evaluated against, and the only form that survives a spec
    -- adding a field this schema never named.
    --
    -- There are no `descriptor` and `price` columns beside it. They would be
    -- `offer->'descriptor'` and `offer->'price'` copied out — the same bytes a
    -- second time, on a column already read in full by the one query that
    -- touches this table, indexed by nothing and filtered on by nothing. Unlike
    -- `resources.name`, which exists to carry a trigram index, they would pay
    -- for themselves nowhere and would drift the moment a republish updated
    -- `offer` and missed a projection.
    offer        JSONB       NOT NULL DEFAULT '{}'::jsonb,

    -- An expired offer must not be returned. The catalog's own validity does
    -- not cover this: a live catalog routinely carries last month's offer.
    -- Offer.validity is the same TimePeriod, so it gets the same two pairs —
    -- a lunch special is a daily window and nothing else.
    valid_from      TIMESTAMPTZ,
    valid_to        TIMESTAMPTZ,
    valid_time_from TIME,
    valid_time_to   TIME,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (catalog_id, id)
);

-- NO `idx_offers_catalog_id`, for the same reason: `PRIMARY KEY (catalog_id,
-- id)` already leads with catalog_id, and the delete-then-prune pair above and
-- the hydration fetch are both catalog_id-prefix scans.

-- Hydration asks "which offers touch any of these resource ids" — an array
-- overlap (&&), which is a GIN scan.
CREATE INDEX idx_offers_resource_ids ON offers USING GIN (resource_ids);

-- For attribute filters rooted at the offer path (Task 22).
CREATE INDEX idx_offers_offer ON offers USING GIN (offer jsonb_path_ops);
```

**`resource_ids` is an array with no foreign key, because PostgreSQL cannot
declare one into an array.** There is therefore no constraint to catch drift,
which makes this the one relationship in the schema whose correctness rests
entirely on the writer running the right statements in the right order. It is
defended in three places rather than one, because a single unenforced rule is
one edit away from not being a rule:

1. **At write time,** every offer the patch names has its `resourceIds` checked
   against the merged catalog, and a dangling id is a **named PARTIAL fault**,
   not a silent prune (see [Inside `UpsertCatalog`](#inside-upsertcatalog)).
   This is the only one of the three that can catch a reference to a resource
   that never existed — a `resourceIds` typo on a first publish, which the two
   statements below cannot distinguish from a correct array.
2. **On a FULL republish that removes resources,** the delete-then-prune pair
   below, which catches the offers the patch did not name and so could not
   check.
3. **In the acceptance suite,** as an invariant asserted after *every* scenario
   rather than in a test of its own: no row of `offers.resource_ids` names a
   `(catalog_id, resource_id)` that `resources` does not have. That is the
   assertion a periodic reconciliation job would be a slower, later version of
   — a job discovers the drift in production, this discovers it in the commit
   that introduced it.

A FULL republish that removes resources must do two things in this order:

```sql
-- 1. An offer that referenced resources and now references none is dead. This
--    runs FIRST, on the pre-pruned arrays, because after pruning it would be
--    indistinguishable from a catalog-wide offer.
DELETE FROM offers o
 WHERE o.catalog_id = $1
   AND cardinality(o.resource_ids) > 0
   AND NOT EXISTS (SELECT 1 FROM resources r
                    WHERE r.catalog_id = o.catalog_id AND r.id = ANY (o.resource_ids));

-- 2. Survivors keep only the ids that still exist, so no response ever carries
--    a reference to a resource the caller cannot fetch.
UPDATE offers o
   SET resource_ids = ARRAY(SELECT unnest(o.resource_ids)
                            INTERSECT
                            SELECT r.id FROM resources r WHERE r.catalog_id = o.catalog_id)
 WHERE o.catalog_id = $1 AND cardinality(o.resource_ids) > 0;
```

### The query modes and their indexes

| Mode | Predicate | Index | Phase |
|---|---|---|---|
| Geospatial | bbox → CQL2 operator as cell-set algebra (+ exact haversine for Point↔Point `S_DWITHIN`) | `GIN(cells_full)`, `GIN(cells_cover)` | 1 |
| Lexical | `search_tsv @@ discover_tsquery($q)` | `GIN(search_tsv) WHERE active` | 1 |
| Fuzzy | `name % $q` | `GIN(name gin_trgm_ops) WHERE active` | 1 |
| Semantic | `embedding <=> $vec` | `HNSW(embedding vector_cosine_ops)` | 2 (A5) — the one mode still deferred |
| Structured | `attributes @? $path` (the operator — the function form is not indexed) | `GIN(attributes jsonb_path_ops)` | 1 |
| Schema | `(schema_context, schema_type)` pairs, OR-ed | `btree(schema_context, schema_type) WHERE active` | 1 |

The ranked modes are fused by Reciprocal Rank Fusion. **Geo is a pure
prefilter** — it narrows candidates before ranking and never contributes score.

**One scope gate no query may omit** — every retriever, the counter and the
hydrator (A6) all carry it. Applying it in the hydrator alone would let a
retriever rank out-of-scope rows into the page and make `Total` disagree with
what comes back:

```sql
(sqlc.narg('network_id')::text IS NULL
 OR r.visible_to @> ARRAY[sqlc.narg('network_id')::text])
  AND r.active
  AND (r.valid_from IS NULL OR r.valid_from <= now())
  AND (r.valid_to   IS NULL OR r.valid_to   >= now())
  AND within_daily_window(r.valid_time_from, r.valid_time_to,
                          (now() AT TIME ZONE 'UTC')::time)
```

**`network_id` is `sqlc.narg`, not `@`, and that is the whole mechanism.** A
`NULL` short-circuits the `OR` to true for every row, which is what makes an
omitted `networkId` search every network rather than none — the identical
pattern `target_paths` and `schema_contexts` already use above, applied to the
one clause in this block that used to be unconditional. A caller who supplies
`networkId` still gets exactly that network's rows; nothing about the scoped
case changes.

**`plan_cache_mode = force_custom_plan`, and it is this pattern that requires
it.** `$1 IS NULL OR r.visible_to @> ARRAY[$1]` is only index-usable when
PostgreSQL knows `$1`: with the value in hand it folds the first arm to FALSE
and is left with a sargable `@>`. With the value unknown it must keep both arms,
and an `OR` whose first arm does not mention the column cannot be answered by an
index on that column — so the GIN index is dropped and the clause becomes a
sequential-scan filter. pgx speaks the extended protocol, and PostgreSQL builds
a **custom** plan for the first five executions of a prepared statement and may
switch to a **generic** one thereafter. So the fast plan is the one a cold
connection gets, and the slow plan is the one a warm connection settles into —
the exact opposite of how a performance problem is usually shaped, and
invisible to any `EXPLAIN` run once.

Every nullable clause in this plan has the property: `network_id` here,
`target_paths`, `spatial_op` and the four box bounds in the spatial `EXISTS`,
and `schema_contexts` above. That is the whole read path, so the setting is not
a tuning knob but the condition under which the query shapes in this document
mean what they say. It is applied in `pool.go`'s on-acquire block beside
`hnsw.ef_search` and `statement_timeout`, and Task 14's `EXPLAIN` assertions run
their query **six times before asserting**, because a test that only ever sees
the custom plan is a test that cannot observe this at all.

**Every column here is on `resources`.** No query in the read path joins
`catalogs` — the provider document is fetched once per catalog on the page, at
hydration, after the page is already decided. Task 16 has a test that reads the
SQL file and proves no query omits the gate — the nullable `network_id` clause
included, so a query that hard-codes `visible_to @> ARRAY[$1]` instead of the
nullable form is caught as surely as one that drops the clause outright.

### Validity — two independent windows

`Catalog.validity` and `Offer.validity` are both a `TimePeriod`, and a
`TimePeriod` is not one range. Its `anyOf` admits three shapes, and **both
forms are supported**:

| Publisher sends | Means | Columns |
|---|---|---|
| `startDate` / `endDate` | a one-off calendar range, "live 1 Jan → 31 Mar" | `valid_from` / `valid_to` |
| `startTime` / `endTime` | a window that **repeats every day**, "open 09:00 → 17:00" | `valid_time_from` / `valid_time_to` |
| both | "live Jan→Mar, and 09:00→17:00 on each of those days" | all four |

They are orthogonal, so they are separate columns and separate predicates,
ANDed. Storing the clock form in `TIMESTAMPTZ` is not possible — there is no
date to put in it — and reading it as NULL would report a closed shop as open
every night, which is the failure that stays invisible because nobody complains
about a result they never saw.

**Three details that are easy to get wrong, so they are pinned:**

- **Midnight wrap.** `22:00 → 02:00` has `from > to`, and `BETWEEN` is false for
  every instant. `within_daily_window` handles it; nothing else may open-code
  the comparison (scenario 24).
- **Whose clock.** RFC 3339 `full-time` carries an offset, so `09:00:00+05:30`
  is unambiguous and is normalised to UTC at publish. A bare `09:00:00` is
  interpreted in `APP_DEFAULT_TIMEZONE` (`Asia/Kolkata`) and normalised the
  same way. Comparison is then UTC-to-UTC, with no per-row timezone lookup.
- **The DST caveat, stated rather than discovered.** A UTC-normalised `TIME` is
  correct only where the offset is fixed. India has no DST, so this holds for
  `mahavistar` and `bharatvistar`. A network in a DST zone would need
  `(TIME, tz name)` and a comparison performed in that zone; that is an
  `ALTER TABLE ADD COLUMN`, and it is written here because the failure mode is
  an hour of silent wrongness twice a year rather than an error.

`Resource` has neither `isActive` nor `validity`. All four validity columns on
`resources` are copies of the catalog's, rewritten unconditionally on every
publish.

**One implementation per language, exactly as with haversine.** The wrap-around
rule exists in SQL as `within_daily_window` and in Go as
`domain.WithinDailyWindow(from, to, at *TimeOfDay) bool`, which is what the
memory backend calls; nothing else open-codes the comparison. Task 16's
conformance suite runs the same fixtures — forward, wrapping, one bound NULL,
both NULL — through both, for the reason the haversine agreement test exists:
two copies of a rule that quietly disagree produce a result neither side can
explain.

### Schema filtering — `context.schemaContext`

`schemaContext` is a **`Context` field, not an `Intent` field** — it sits beside
`ttl` and `timestamp` in the envelope, and `Intent` is `additionalProperties:
false`, so it cannot legally appear there. Each entry is a JSON-LD context URI
whose optional `#fragment` names the type:

```
"https://beckn.org/Mobility#RideService"   → @context AND @type
"https://beckn.org/Mobility"               → @context, any @type
```

Entries are OR-ed; within an entry the two halves are AND-ed. **The pairing is
the whole point**: a request for
`["https://schema.org#GroceryItem", "https://beckn.org/Mobility#RideService"]`
must not match a resource that is `schema.org` + `RideService`. A pair of
independent `IN` lists would return exactly that cross-match, which is why these
are two columns compared together rather than one array containing both.

The filter therefore takes the **same shape as the offer join**: parallel 1-D
arrays, one element per entry, compared as a pair.

```sql
-- One STATIC clause, whatever the entry count. `schema_contexts` and
-- `schema_types` are parallel text[]s of equal length; an entry with no
-- #fragment contributes '' — never NULL, because `s.typ = ''` on a NULL is
-- NULL, and a filter that evaluates to NULL is a filter that matches nothing.
AND (   sqlc.narg('schema_contexts')::text[] IS NULL
     OR cardinality(sqlc.narg('schema_contexts')::text[]) = 0
     OR (    r.schema_context = ANY(sqlc.narg('schema_contexts')::text[])
         AND EXISTS (SELECT 1
                       FROM unnest(sqlc.narg('schema_contexts')::text[],
                                   sqlc.narg('schema_types')::text[]) AS s(ctx, typ)
                      WHERE r.schema_context = s.ctx
                        AND (s.typ = '' OR r.schema_type = s.typ))))
```

**The `= ANY` is redundant and deliberate.** Every row the `EXISTS` admits
already satisfies it, so it changes no answer — it exists to give the planner
something `idx_resources_schema` can drive. `EXISTS (… unnest … WHERE
r.schema_context = s.ctx)` is correlated to `r`, which makes it a per-row
subplan filter over whatever scan was chosen for other reasons; the btree on
`(schema_context, schema_type)` cannot be the access path for a predicate
phrased that way. `r.schema_context = ANY($1)` is a plain sargable
array-membership test on the index's leading column, so the pair is an index
scan with a cheap filter rather than a sequential scan with an expensive one.
The pairing semantics stay entirely inside the `EXISTS`, which is the only
clause that may reject a cross-match, so the redundant conjunct cannot loosen
the filter even if someone later widens it.

**Static, because `sqlc` compiles `.sql` files at build time.** A clause list
assembled in Go — one `AND (… OR …)` per entry — has no compiled form, so it
would have to be concatenated at run time, which the SQL rule in Global
Constraints forbids outright. Both guards are needed and neither is redundant:
`IS NULL` is what an absent filter arrives as through `sqlc.narg`, and
`cardinality(NULL)` is **NULL, not 0**, so a `cardinality(…) = 0` test alone
leaves the whole `AND (…)` NULL and filters out every row — exactly the
"silently empty every response" failure this section already warns about,
reintroduced by the fix for it.

The fragment convention is **not** in `beckn.yaml`, which describes only "an
array of JSON-LD context urls". It is the network's established reading, and it
is the only one that can filter on `@type` at all given the field is a flat list
of URIs.

---

## Publish — How It Works

### The wire shape

```jsonc
POST /publish            // the only publish route (C2)
{
  "context": {
    "action": "publish", "version": "2.0.0",
    "messageId": "<uuid4>", "transactionId": "<uuid4>",
    "timestamp": "<RFC3339>",
    "networkId": "mahavistar"        // OPTIONAL → defaults to APP_NETWORK_ID
  },
  "message": {
    "catalogs": [ { "id": "cat-1", "provider": { "id": "p-1", ... },
                    "isActive": true,           // OPTIONAL, default true
                    "validity": {               // OPTIONAL TimePeriod; the two
                      "startDate": "2026-01-01T00:00:00Z",   // halves are
                      "endDate":   "2026-03-31T23:59:59Z",   // independent and
                      "startTime": "09:00:00+05:30",         // may appear
                      "endTime":   "17:00:00+05:30" },       // separately
                    "resources": [ ... ], "offers": [ ... ] } ],
    "publishDirectives": [{
      "catalogId":   "cat-1",
      "catalogType": "REGULAR",       // MASTER → REJECTED (A1)
      "updateMode":  "MERGE",         // omitted → MERGE; RFC 7396 patch (A8)
      "visibleTo":   ["mahavistar", "oan"],   // ARRAY OF NETWORK IDS
      "resourceDirectives": [ ... ]   // any `extends` → REJECTED (A1)
    }]
  }
}
```

Response, per C3, is the callback shape returned inline:

```jsonc
200 { "context": { "action": "catalog/on_publish", ... },
      "message": { "results": [
        { "catalogId": "cat-1", "status": "ACCEPTED",
          "stats": { "itemCount": 42, "providerCount": 1, "categoryCount": 3 } },
        { "catalogId": "cat-2", "status": "REJECTED",
          "errors": [{ "code": "SCH_TYPE_NOT_SUPPORTED", "message": "...",
                       "details": { "path": "$.message.publishDirectives[1]" } }] },
        // Landed, minus one unreadable geometry. `errors` is the spec's own
        // array on CatalogProcessingResult — nothing is packed (C7).
        { "catalogId": "cat-3", "status": "PARTIAL",
          "stats": { "itemCount": 8, "providerCount": 1, "categoryCount": 1 },
          "errors": [{ "code": "SCH_INVALID_FORMAT", "message": "...",
                       "details": { "path": "$.message.catalogs[2]" } }] }
      ] } }
```

### The flow

```pseudo
controller.Publish(request):
    envelope ← from context          # middleware already parsed and validated it
    action   ← decode envelope.message as CatalogPublishAction
    results  ← service.Publish(ctx, envelope.context, action)
    respond 200 with {responseContext("catalog/on_publish"), {results}}
    # 200 even when every catalog was REJECTED. The request was well-formed;
    # the per-catalog verdicts are the payload, not the transport status.
    # A transport-level NACK is reserved for a request that could not be read.
```

```pseudo
service.Publish(ctx, reqContext, action):
    network ← reqContext.networkId or config.App.Network       # C6 note
    results ← []

    seen ← {}
    for each catalog in action.catalogs:                       # sequential
        if catalog.id ∈ seen:
            results += REJECTED(SCH_SCHEMA_VALIDATION_FAILED, jsonPath(catalog))
            continue
            # One transaction per catalog means two entries with one id are two
            # transactions against one row, and the second wins: its merge
            # overwrites the first's document and its geometry delete wipes the
            # first's rows. Under FULL the first entry's resources are deleted
            # outright. That is a publisher's own mistake either way, but it is
            # currently reported as two ACCEPTEDs — one of which did not
            # survive the request that returned it. A map lookup is the whole
            # cost of saying so. `SCH_`, because the fault is in the shape of
            # the request rather than in what it asks for, and that is the
            # family Task 5 gives to a body this service will not accept as
            # written. The verdict is per-catalog, not a NACK: the other
            # catalogs in the request are unaffected and land normally, which
            # is the same rule A1's MASTER refusal follows.
        seen += catalog.id
        directive ← action.DirectiveFor(catalog.id)
        results += publishOne(ctx, network, catalog, directive)

    return results
```

```pseudo
publishOne(ctx, network, catalog, directive):
    # A catalog may arrive with no directive at all — publishDirectives is
    # optional. Absent means REGULAR, MERGE, and visibleTo = [network], which
    # is the ordinary single-network publish and must not need boilerplate.
    directive ← applyDirectiveDefaults(directive, catalog.id, network)
        # FIELD-WISE, not all-or-nothing (A9). A directive that is absent
        # entirely and a directive that names only `catalogId` must come out
        # the same, because the publisher meant the same thing by both:
        #   catalogId    ← catalog.id
        #   catalogType  ← REGULAR   if empty
        #   updateMode   ← MERGE     if empty
        #   visibleTo    ← [network] if empty        # in BOTH modes
        #   resourceDirectives ← []  if nil
        # Spelled out rather than implied, because UpsertCatalog branches on
        # `updateMode == FULL` and FULL deletes the resources the payload did
        # not mention. A zero value that read as FULL would turn every
        # directive-less republish into a partial wipe. The `updateMode` enum
        # itself is L1's job (`beckn.yaml` declares it `enum: [FULL, MERGE]`),
        # so an unrecognised spelling is a 400 and never reaches this default.

    # ---- A1 intake refusal, before any work -------------------------------
    if directive.catalogType == MASTER:
        return REJECTED(SCH_TYPE_NOT_SUPPORTED, jsonPath(directive))
    if any resourceDirective has non-empty extends:
        return REJECTED(SCH_TYPE_NOT_SUPPORTED, jsonPath(that resourceDirective))
        # jsonPath renders the directive's real index —
        # `$.message.publishDirectives[1]` — because `details.path` is a
        # JSONPath in the spec's own example (C7). A literal `i` in a response
        # is a placeholder that shipped.

    # ---- map wire → PATCH, not a finished domain object --------------------
    patch, fatal, partial ← mapper.MapCatalog(catalog, directive, network)
    if fatal is non-empty:
        return REJECTED(fatal)           # nothing is written
    # A *patch*, because absence has to survive this step (A8). A struct whose
    # defaults are already filled in cannot tell "the publisher omitted
    # isActive" from "the publisher sent true", and MERGE turns that ambiguity
    # into data loss.

    # `partial` holds per-geometry faults: the geometry was dropped, the
    # resource stands. They ride back on a PARTIAL result rather than vanishing
    # — a publisher whose polygon was unreadable has to be told, and told in the
    # status as well as in the array.

    # ---- persist: ONE TRANSACTION PER CATALOG ------------------------------
    # Nothing is derived out here any more. Under MERGE the searchable text,
    # the embedding and the geometry cover are all functions of the MERGED
    # document, which does not exist until the transaction has read the stored
    # one. `derive` is the seam: the repository owns the lock and the merge,
    # this service owns the embedder and the text rule, and they meet exactly
    # once, inside the transaction.
    derived, err ← repo.UpsertCatalog(ctx, patch, directive.updateMode, derive)
    if err: return REJECTED(mapStorageError(err))
    partial ← partial + derived     # geometry faults are raised in there now

    status ← PARTIAL if partial is non-empty else ACCEPTED
    # The spec's enum is [ACCEPTED, REJECTED, PARTIAL], and `errors` is
    # documented as "present when REJECTED or PARTIAL". A catalog that landed
    # with three of its geometries dropped IS partial. Returning ACCEPTED with
    # a non-empty `errors` tells a publisher whose tooling branches on `status`
    # — which is the field the spec made an enum — the opposite of what
    # happened, and the array it never reads holds the correction.

    return status with errors ← partial      # empty in the ordinary case
           and stats{itemCount:    resources in THIS REQUEST that landed,
                     providerCount: 1,
                     categoryCount: distinct @type among those}      # C5, C12
    # REQUEST-scoped, not catalog-scoped, and A8 is what made the two differ:
    # a MERGE patch carrying one resource into a forty-resource catalog reports
    # itemCount 1. The spec decides it — `itemCount` is documented as "Number of
    # items ACCEPTED" — and it is also the only number publishOne can honestly
    # produce, since it holds the patch and UpsertCatalog returns only faults.
```

```pseudo
derive — a domain.DeriveFunc (merged domain.Catalog, touched []string)
                                                          → []domain.Fault:
    # A closure built inside publishOne, which is how it reaches `i` (the
    # catalog's index in the request, for fault paths) and the embedder without
    # either becoming a parameter of the repository port.
    #
    # Inside the transaction. Returns faults rather than raising: an unreadable
    # geometry is a PARTIAL, and the rest of the catalog still lands.
    # Walked over the WHOLE merged catalog, so a geometry is found wherever a
    # publisher put it and `targets` can name any of them. The walk returns
    # both kinds at once: catalog-level finds (resource_id nil, stored once)
    # and resource-level finds, which are split onto their resources below.
    # Run on the MERGE RESULT — a patch that never mentioned a geo field still
    # re-derives the same rows, which is what makes the unconditional geometry
    # replacement in UpsertCatalog idempotent.
    found, faults ← ExtractGeometries(i, merged)
    merged.Geometries                 ← found where Owners is empty
    merged.Resources[k].Geometries    ← found where k ∈ Owners
    # `∈`, not `==`: one offer geometry covering three resources lands on all
    # three, as three rows. The cover is computed once per geometry below and
    # the cells are copied, because H3 fills are the expensive half and the
    # answer does not depend on who owns it.

    for each resource in merged.Resources where id ∈ touched:
        resource.SearchText ← deriveSearchText(resource)            # Task 13
        hash ← blake2b256(resource.SearchText)
        if hash ≠ resource.EmbeddingSourceHash:                     # A5
            resource.Embedding           ← embedder.Embed(...)  # noop in P1
        resource.EmbeddingSourceHash ← hash
        # The hash is written UNCONDITIONALLY, outside the branch. It records
        # what the derived text currently is, which is true whether or not an
        # embedder ran — and writing it only when the vector changed would
        # leave every Phase 1 row NULL, so the Phase 2 backfill could not tell
        # a stale embedding from a missing one and would redo all of them.
    # `touched` only. A forty-resource catalog patched in one attribute must
    # not re-embed thirty-nine untouched resources, and the ones the patch
    # never named are byte-identical to what is already stored.

    return faults
```

**Two fault classes, and the difference is which one is recoverable.** A
*fatal* fault means the request cannot be honoured as written — a resource with
no `id`, a `catalogType` this phase refuses — so nothing is stored and the
verdict is `REJECTED` (scenarios 4 and 5). A *partial* fault means one geometry
out of many could not be read; the rest of the catalog is perfectly good, so it
lands and the verdict is `PARTIAL` with the fault in `errors` — the third value
of the spec's own status enum, which exists for exactly this and would otherwise
never be returned. Nothing is ever dropped without being named.

**One transaction per catalog, not per request.** A request carrying nine
regular catalogs and one master lands the nine and reports one `REJECTED`
(A1, scenario 6). Wrapping the request would make one publisher's mistake
another publisher's outage.

### `updateMode` — MERGE and FULL (A8)

`updateMode` decides two independent things. This plan used to answer only the
first.

| | Rows the payload omits | An entity the payload names | A defaulted field the payload omits |
|---|---|---|---|
| **MERGE** (default) | untouched | RFC 7396 patch applied to the stored one | reset to its default (A9) |
| **FULL** | deleted | replaced outright, omitted fields reset | reset to its default (A9) |

**MERGE is RFC 7396 JSON Merge Patch and nothing more inventive.** An absent key
keeps its stored value, an explicit `null` deletes the key, an array replaces
wholesale rather than merging element-wise. It is a published standard, so the
publisher contract is already written down somewhere other than here, and every
edge case already has a name.

```jsonc
stored  { "grade": "A", "moisture": 12, "origin": "Hubli", "tags": ["a","b"] }
patch   {               "moisture": 14, "origin": null,    "tags": ["c"]     }
result  { "grade": "A", "moisture": 14,                    "tags": ["c"]     }
```

**Collections are keyed by `id`, and that is the one place the RFC does not
apply.** Read literally, "an array replaces wholesale" would mean a MERGE
carrying one resource deletes the other thirty-nine — the exact data loss MERGE
exists to prevent. So the rule has two levels, and they must not be confused:

- `catalogs`, `resources` and `offers` are **keyed collections**, merged by
  identity. Named → patched. Not named → untouched under MERGE, deleted under
  FULL.
- Everything *inside* one entity — `provider`, `resourceAttributes`,
  `descriptor`, the offer body — is a **document**, merged per RFC 7396. Arrays
  nested in a document (`provider.availableAt`, an offer's `resourceIds`) are
  document content, so they replace wholesale.

**A publisher cannot delete a resource under MERGE.** `null` deletes a key,
never a row. A resource is removed by a FULL republish that omits it and by
nothing else, because "remove this resource" and "clear this field" are one
typo apart and only one of them is recoverable.

**Declared defaults are resolved first, and they do not care which mode this
is (A9).** Before anything is merged, every field the spec gives a default is
filled — the directive fields (`catalogType`, `updateMode`, `visibleTo`) in
`publishOne`, `isActive` and an offer's `resourceIds` in the mapper. The
publisher contract is then one sentence — *a default means the default,
always* — rather than a sentence that has to name the update mode to be true.

| Field | Default | Absent under MERGE means |
|---|---|---|
| `publishDirective.catalogType` | `REGULAR` | `REGULAR` |
| `publishDirective.updateMode` | `MERGE` | `MERGE` |
| `publishDirective.visibleTo` | `[network]` | `[network]` — **not** the stored list |
| `catalog.isActive` | `true` | `true` — **not** the stored flag |
| `offer.resourceIds` | `[]` (catalog-wide) | `[]` |
| everything else | — | the stored value (RFC 7396) |

**The consequence, stated rather than discovered.** A publisher who withdrew a
catalog with `isActive: false` and later sends a one-attribute patch **without**
`isActive` republishes it live, and a publisher who narrowed `visibleTo` and
later patches without it goes back to `[network]`. That is the deliberate cost
of a defaulting rule that does not branch on mode: the two fields that decide
whether a catalog is visible at all are the two a publisher must keep sending.
It is stated in the publisher-facing docs, and scenario 26 asserts it, because
the alternative — defaults that mean different things in different modes — is a
rule nobody remembers correctly at 3 a.m.

**The rule stands; the silence does not.** When a MERGE republish resolves
`isActive` or `visibleTo` from its default and the resolved value *differs from
what is stored*, `UpsertCatalog` emits a WARN carrying the catalog id, the
field, the stored value and the resolved one. It changes no semantics: the
default still wins, in both modes, exactly as the table above says. What it
changes is that the day a publisher's offer-only republish makes their catalog
visible network-wide again, there is a log line naming the catalog and the
field, instead of a support ticket that begins "our catalog started appearing
somewhere it should not have and nothing changed". A deliberate cost that
nobody can attribute afterwards is indistinguishable from a bug.

**Absence still has to survive the mapper for everything else.** This is the bug
class MERGE introduces, and the reason `MapCatalog` returns a `CatalogPatch`
rather than a `Catalog`: for `provider`, `validity`, `descriptor` and
`resourceAttributes` there is no default to fall back on, so *omitted* and *sent
empty* are different instructions and the type has to carry the difference.

**Everything derived is derived after the merge.** `name`, `descriptor`,
`schema_context`, `schema_type`, `search_tsv`, `embedding` and the H3 covers are
functions of the *result*, not of the patch. A merge that touched one attribute
and then rebuilt `search_tsv` from the patch alone would leave that resource
findable by one word. This is why publish is read-modify-write, and it is where
`embedding_source_hash` earns its keep: the embedder re-runs only when the
derived text actually changed, so a patch to a non-searchable field costs no
inference at all.

**One lock per catalog, taken by the write itself.**

```sql
INSERT INTO catalogs (id) VALUES (@id)
ON CONFLICT (id) DO UPDATE SET updated_at = now()
RETURNING *;
```

`DO UPDATE`, not `DO NOTHING`. `DO NOTHING` returns **zero rows** on conflict,
which reads as "no such catalog" and would make every republish merge against an
empty document — the whole feature, silently inverted, on exactly the path it
exists for. `DO UPDATE` takes the row lock and returns the current state in one
statement, on the first publish and the thousandth alike, so two concurrent
republishes of one catalog serialise instead of one losing its patch.

### Inside `UpsertCatalog`

```pseudo
repo.UpsertCatalog(ctx, patch, updateMode, derive):
    BEGIN

    # ---- lock and load, in one statement -----------------------------------
    stored  ← INSERT INTO catalogs (id) VALUES (patch.ID)
              ON CONFLICT (id) DO UPDATE SET updated_at = now() RETURNING *
    existing ← load this catalog's resources and offers   # empty on first publish

    # ---- resolve the patch against what is stored --------------------------
    if updateMode == FULL:
        merged, touched ← patch materialised with defaults   # omissions RESET
    else:
        merged, touched ← domain.MergeCatalog(stored+existing, patch)
        # Documents by RFC 7396, `resources` and `offers` by id. A stored
        # entity the patch does not name comes through untouched.

    if merged.VisibleTo is empty: merged.VisibleTo ← [merged.NetworkID]
        # Belt and braces. The mapper already applied this default (A9), in
        # both modes, so this line should never fire. It stays because a
        # catalog visible to no network is findable by nobody while reporting
        # success, and that is not a failure worth reaching production to
        # discover. The DEFAULT '{}' on the column is the same fail-safe one
        # layer down: a state the writer must never store, not a valid one.

    faults ← derive(merged, touched)   # text, embeddings, geometry — POST-merge
        # Faults, not an abort. A geometry that cannot be read is a PARTIAL and
        # the transaction still commits; only a storage error rolls back.

    gate ← merged.{visibleTo, active, validFrom, validTo,
                   validTimeFrom, validTimeTo}
        # All six travel together, and all six are read from the MERGE RESULT,
        # never from the patch. A republish that carried no `validity` must
        # propagate the validity the catalog already had, not NULL it.

    update catalogs row (provider, gate…, updated_at = now())

    if updateMode == FULL:
        delete resources where catalog_id = id and id not in patch
        delete offers    where catalog_id = id and id not in patch
        # MERGE deletes nothing — this is the whole difference between an
        # update and a silent data loss (scenarios 2 and 3).

    # ---- catalog-level geometry: covered ONCE, not once per resource -------
    # "Catalog-level" is now a property of where the walker found it — outside
    # any resource — rather than of which field it came from.
    delete from resource_geometries
          where catalog_id = ? and resource_id IS NULL
    for each geometry in merged.Geometries:          # the MERGED provider
        cover ← geo.CoverGeometry(geometry)           # over budget → NULL cells
        insert resource_geometries (catalog_id, resource_id ← NULL,
               target_path, source_path, geojson,
               cells_full, cells_cover, bbox…)
        # Two H3 fills per geometry, not three: `cells_full` is the
        # ContainmentFull cover and `cells_cover` the ContainmentOverlapping
        # one. There is no ContainmentCenter fill, because there is no column
        # to write it to and no operator that could read it.

    for each resource in merged.Resources where id ∈ touched:
        # `touched` includes resources named only by a patched offer — see
        # MergeCatalog. This loop is where an offer's geometry gets written.
        write the WHOLE row, INCLUDING the gate columns
        # Whole-row in both modes: the merge already happened, up in `merged`.
        # A second, partial UPDATE here would be a second implementation of
        # RFC 7396, in SQL, disagreeing with the Go one by next quarter.
        # `touched` only, because a resource the patch never named is already
        # byte-identical to what is stored.

        delete from resource_geometries
              where catalog_id = ? and resource_id = ?
        for each geometry in resource.Geometries:     # live: the walker
                                                      # finds these now
            cover ← geo.CoverGeometry(geometry)
            insert resource_geometries (…, resource_id ← resource.id, …)

    # ---- propagate the gate to EVERY resource, not just the payload's -----
    # Without this a publisher who changes visibleTo while sending no resources
    # updates the catalog and nothing else, and the change silently does
    # nothing, because discover reads the copy on `resources`.
    update resources
       set visible_to      = gate.visibleTo,
           active          = gate.active,
           valid_from      = gate.validFrom,
           valid_to        = gate.validTo,
           valid_time_from = gate.validTimeFrom,
           valid_time_to   = gate.validTimeTo
     where catalog_id = ?
       and id != ALL(@touched)          # the UNTOUCHED ones only
       and (visible_to, active, valid_from, valid_to,
            valid_time_from, valid_time_to)
             IS DISTINCT FROM (gate.visibleTo, gate.active,
                               gate.validFrom, gate.validTo,
                               gate.validTimeFrom, gate.validTimeTo)
    # All SIX, spelled out rather than elided. A column left off this list
    # keeps its previous value forever, because `Resource` has no validity of
    # its own for a later publish to correct it with.
    #
    # `!= ALL(@touched)` because the loop above already wrote the gate into
    # every touched row. Without it each touched resource is written TWICE in
    # one transaction: two row versions, two WAL records, a dead tuple, and two
    # insertions into the `visible_to` GIN index for a value that did not
    # change. An empty `@touched` — a catalog-only publish, which is the case
    # this statement exists for — makes `!= ALL('{}')` true for every row, so
    # the propagate still reaches all of them.
    #
    # `IS DISTINCT FROM` makes the ordinary republish free without weakening
    # the rule. The rule is that every resource ENDS this transaction carrying
    # the catalog's gate, not that every resource is rewritten; a row already
    # holding all six values reaches the same end state by not being touched.
    # It matters because `visible_to` is GIN-indexed with `fastupdate = off`,
    # so there is no HOT path: an unconditional statement is N dead tuples and
    # N posting-tree updates on every publish of an N-resource catalog, for
    # values that change on the rare publish that alters `visibleTo` or
    # `validity` and on no other. Row-comparison rather than six ANDed tests
    # because NULL validity is the common case and `=` on NULL is NULL.
    #
    # The unconditional-rewrite guarantee is unchanged and still the thing that
    # keeps the denormalised copy honest — scenario 3's assertion is that a
    # publisher who changes `visibleTo` while sending no resources moves every
    # resource, and a changed gate is exactly what this clause lets through.

    for each offer in merged.Offers where the patch named it:
        dangling ← offer.ResourceIDs naming no resource in merged.Resources
        if dangling is not empty:
            fault("offer references a resource this catalog does not have",
                  path → that offer's resourceIds)      # PARTIAL, and NAMED
            offer.ResourceIDs ← offer.ResourceIDs minus dangling
            if offer.ResourceIDs is now empty: skip this offer, do not write it
        # Checked against the MERGED catalog, so an offer may legitimately name
        # a resource an earlier publish stored. NAMED rather than pruned in
        # silence: `resource_ids` has no foreign key, so a typo would otherwise
        # store an offer attached to nothing and report success. And an offer
        # pruned to EMPTY must not be written at all — empty means CATALOG-WIDE,
        # so writing it would promote a one-resource offer to the provider's
        # entire inventory, which is the same trap the FULL prune below avoids.
        write the whole row (offer merged, resource_ids, valid_from, valid_to,
                             valid_time_from, valid_time_to)

    if updateMode == FULL:
        delete then prune offers whose resource_ids point at deleted resources
        # in that order — see Migration 006. Still needed alongside the check
        # above, which only sees the offers the PATCH named: a FULL republish
        # that drops a resource orphans the offers it did NOT name.

    COMMIT
    return faults
```

Three rules this encodes:

**Geometry rows are replaced, not merged — and replaced from the merge
result.** A geometry has no id, so there is nothing to key an identity merge on;
the merge happens one level up, on `provider`, and the rows are then rebuilt
from whatever `provider.availableAt` says afterwards. A patch that never
mentioned `availableAt` therefore rewrites the rows it already had — idempotent,
and one code path for both modes. Catalog-level and resource-level rows are
replaced separately, so neither wipes the other.

**The gate is rewritten on every resource, every publish.** It is the single
statement that makes the denormalised copy safe, it runs whether or not the
payload carried resources — and under MERGE it carries the *post-merge* gate, so
a patch that said nothing about validity propagates the validity the catalog
already had rather than clearing it.

**Provider geometry is covered once.** The locations in `provider.availableAt`
belong to the catalog. Attaching them to each resource meant 3 shapes becoming
120 rows and 120 H3 fills for a 40-resource catalog.

**Every loop above is ONE round trip, not N — `pgx.Batch`.** Each `for each` is
a loop in Go over statements, not a licence to send them one at a time. A
50-resource catalog with a geometry apiece is ~165 statements, and every one of
them is paid for while holding the `catalogs` row that the lock-and-load upsert
took — so the serialisation that makes concurrent republishes safe becomes the
thing that makes them slow, and the lock is held for a time linear in catalog
size. Queue the resource writes, the geometry deletes, the geometry inserts and
the offer writes into a `pgx.Batch` and send each group once; the transaction
then costs a handful of round trips regardless of how many resources it carries.

**Not `unnest` for these.** The obvious alternative — one multi-row
`INSERT … SELECT * FROM unnest($1::text[], $2::jsonb[], …) ON CONFLICT DO
UPDATE` — cannot carry this table's array columns. PostgreSQL has no true
array-of-arrays, so `unnest` on a `text[][]` flattens it completely rather than
yielding one `text[]` per row: `visible_to` would arrive as one undifferentiated
stream of network ids, silently, on the column whose wrong value is the fail-safe
two paragraphs up. `resource_geometries.cells_full` and `cells_cover` have the
same shape. `unnest` would work only via a `jsonb_to_recordset` detour, which is more
machinery than `pgx.Batch` for the same number of round trips and one more place
for a type to drift.

### Geometry extraction — how the mapper finds geometry

**Called from `derive`, inside the transaction, on the merged provider (A8).**
It used to run in the mapper on the payload. It cannot any more: under MERGE a
patch that never mentioned `availableAt` must still produce the geometry rows
the stored provider implies, and a patch that replaced it must produce the new
ones. Running it on the result answers both with one call. The consequence is
that its faults are raised inside the transaction rather than before it, so
`derive` returns them and they join the *partial* list that decides the verdict.
A provider that carries an unreadable geometry re-reports it on every republish,
which is correct: the document still contains it, and nothing has fixed it.

The reference implementation walks the document looking for three key names
(`gps`, `geo`, `polygon`), parses only `Point` and `Polygon`, reads ring 0 only,
and wraps the whole extraction in one `catch (Exception) { return empty }`. That
is four defects: an unknown key name is invisible, five RFC 7946 types are
dropped, every polygon hole is lost, and one malformed geometry silently voids
every geometry on the resource.

This mapper is **structural, not name-based**:

**An offer's geometry belongs to the offer's `resourceIds`, not to the whole
catalog.** `Offer.resourceIds` is "references (IDs) to resources covered by this
offer", and an offer's `provider.availableAt` is where *those* resources are
sold. A shopfront attached to one SKU's offer is not a statement about the other
thirty-nine resources in the catalog, so it must not be stored as one. Emitting
one `resource_geometries` row per referenced id costs nothing the unique index
does not already allow — it keys on `(catalog_id, COALESCE(resource_id, ''),
source_path)`, so the same `source_path` under two owners is two legal rows —
and it makes the geometry disappear from resources the offer never covered.

**An offer with no `resourceIds`, or an empty one, is catalog-level.** That is
not a special case invented here: `offers.resource_ids` already means
CATALOG-WIDE when empty, in the hydration query and in Migration 006, and an
offer that covers the provider's whole inventory should have its location
searchable against all of it. One rule, two tables.

Dangling ids need no rule of their own, and the reason is worth stating because
the ordering looks wrong at first glance. Extraction runs *before* the check
that faults an offer naming a resource this catalog does not have, so a geometry
can be attributed to an id that resolves to nothing. It is then written nowhere:
the insert loop iterates `merged.Resources`, and an id naming no resource is an
iteration that never happens. The typo is still reported — as the named PARTIAL
that check raises — and the geometry is dropped rather than stored against a
resource that does not exist. Attributing to a bad id costs a row that is never
inserted, not a row that is wrong.

**A geo filter must work on ANY geo path, so the walker finds geometry
anywhere in the document — not at one well-known field.** `targets` is a
JSONPath, and a JSONPath that can only ever name one location is a constant with
extra syntax. A publisher who puts a service polygon in
`resourceAttributes.serviceArea` and a shopfront in `provider.availableAt` must
be able to have a caller ask about either one, separately, and get different
answers.

```pseudo
ExtractGeometries(catalogIndex, merged) → []domain.Geometry, []domain.Fault:
    out, faults ← [], []

    walk(merged, path: "$.catalogs[{catalogIndex}]", depth: 0, owners: nil)
    return out, faults


walk(node, path, depth, owners):
    if depth > MaxCatalogWalkDepth: return          # a bound, not a hope — see below

    # ---- is this node itself a geometry? ---------------------------------
    if node is an object and looksLikeGeoJSON(node):
        parsed, err ← parseGeoJSON(node)
        if err:
            faults += fault(jsonPath(path), "malformed geometry", node)
        else if len(out) >= MaxGeometriesPerCatalog:
            faults += fault(jsonPath(path), "geometry budget exceeded")
        else:
            out += Geometry{
                TargetPath: jsonpath.Canonicalise(wildcard(path)),
                SourcePath: jsonpath.Canonicalise(wildcardCatalogIndex(path)),
                Owners:       owners,       # empty ⇒ catalog-level
                Type:         node.type,
                GeoJSON:      node verbatim,
            }
        return          # do NOT descend. A GeometryCollection's `geometries`
                        # are PART of this geometry, not separate finds.

    # ---- otherwise, descend ----------------------------------------------
    # Ownership is decided at the array ELEMENT and then carried down
    # unchanged. Both of the catalog's keyed collections name their resources
    # there and nowhere else, so this is the only place that has to know.
    if node is an object:
        for key, child in node:
            walk(child, path + "['{key}']", depth+1, owners)

    if node is an array:
        for i, child in node:
            next ← owners
            if this array IS catalog.resources:  next ← [child.id]
            if this array IS catalog.offers:     next ← child.resourceIds
            walk(child, path + "[{i}]", depth+1, next)


looksLikeGeoJSON(node) → bool:
    # BOTH conditions, and the second is what keeps a general walk safe.
    # `type` alone would make any object that happens to carry
    # `"type": "Point"` a geometry — and then a MISSING `coordinates` would be
    # reported as a malformed geometry, turning an unrelated document node into
    # a publish PARTIAL. Requiring the member the type mandates means an object
    # that is not a geometry is simply not recognised, silently and correctly,
    # while an object that IS one and is broken still faults.
    return node.type ∈ the 7 RFC 7946 type names
       and (node.type == "GeometryCollection" ? node.geometries is an array
                                              : node.coordinates is an array)
```

`wildcard(path)` replaces every concrete index with `[*]`, which is the form a
caller writes. The pair is what the table stores, and each column is named for
its job: `target_path` is what a constraint's `targets` is matched against,
`source_path` names where in the document this geometry was found.

**`wildcardCatalogIndex` replaces the FIRST index and no other**, so a stored
`source_path` reads `$['catalogs'][*]['provider']['availableAt'][2]['geo']` —
concrete everywhere the disambiguation needs it, wildcard at the one position
that is a property of the request rather than of the catalog. The walk itself
keeps the concrete root, because a *fault* path must name the offending node in
the payload the publisher actually sent; it is only the stored column that
normalises.

Without it the same geometry stores different bytes depending on where its
catalog sat in the `catalogs` array — `[0]` when published alone, `[1]` when
republished behind another catalog. Nothing breaks, because rows are deleted and
re-inserted per catalog and `uq_resource_geometries` is `catalog_id`-scoped, so
the varying component never has to collide with anything. But it makes a stored
value depend on an unrelated property of the request that produced it, which is
the kind of thing that is discovered by a test that passes on Tuesday.

**`target_path` is a total function of `source_path`**, and two columns where
one derives the other are a drift surface. It stays two columns rather than a
`GENERATED ALWAYS AS` expression, because the wildcard rule would then exist in
SQL as well as in Go — the second implementation this plan refuses everywhere
else, as with haversine and `within_daily_window`. What replaces the constraint
is an assertion, in Task 14: for every row in `resource_geometries`,
`wildcard(source_path) = target_path`. That catches the walker change that
updates one and forgets the other, which is the only way the two can disagree.

Five rules that fall out of it:

1. **Error isolation is per geometry.** One bad geometry costs one geometry.
2. **All seven types are stored and cell-indexed**, whether or not Phase 1 can
   measure an exact distance to them.
3. **`TargetPath` is byte-identical to the canonical `targets` string**, because both
   sides go through `jsonpath.Canonicalise` — note the calls above, on both
   fields. This is not decoration. Storing the dot form while discover sends the
   bracket form makes `g.target_path = ANY($targets)` match nothing, and the caller
   gets `200` with an empty list: exactly how the reference implementation
   answers a spec-conformant `$['availableAt'][*]['geo']` with zero results and
   no error.
4. **Ownership is decided by the path, not by a field name.** A geometry found
   under `$.catalogs[i].resources[j]…` carries `resources[j].id`; anything found
   outside a resource is catalog-level with `resource_id NULL` and is stored
   **once** for the whole catalog. That is what still stops three provider
   locations across forty resources from becoming 120 rows and 120 H3 fills.
5. **The walk is bounded, and the bound is reported.** `MaxCatalogWalkDepth` (32) and
   `MaxGeometriesPerCatalog` (256) exist because this now reads publisher-shaped
   documents rather than one known field. Hitting the geometry budget is a
   *partial* fault naming the path that was dropped, never a silent truncation —
   a publisher whose 257th polygon vanished has to be told.

---

## Discover — How It Works

### The wire shape

```jsonc
POST /discover
{
  "context": { "action": "discover", "version": "2.0.0",
               "messageId": "...", "transactionId": "...",
               "timestamp": "...", "networkId": "mahavistar",
               // OPTIONAL → omitted searches EVERY network. NOT publish's
               // default (line above in Publish); different field, different
               // question.
               // A Context field, not an Intent one. `#fragment` is @type.
               "schemaContext": ["https://beckn.org/Agri#SeedLot"] },
  "message": { "intent": {
      "textSearch": "wheat seeds",
      "spatial": [{
        "op": "S_DWITHIN",
        "targets": "$.catalogs[*].provider.availableAt[*].geo",   // or an array
        "geometry": { "type": "Point", "coordinates": [77.5946, 12.9716] },
        "distanceMeters": 10000,
        "quantifier": "ANY",          // ANY | ALL | NONE; omitted → ANY
        "srid": "EPSG:4326"           // omitted → WGS84
      }],
      // Rooted at the response document; rebased onto the stored column
      // before it runs. PostgreSQL SQL/JSON path only — RFC 9535 is a 400
      // (C10). See "Attribute filters" below.
      "filters": {
        "type": "jsonpath",
        "expression": "$.catalogs[*].resources[*] ? (@.resourceAttributes.packagedGoodsDeclaration.manufacturerOrPacker.name == \"Hindustan Unilever Limited\")"
      }
  } }
}
```

```jsonc
200 { "context": { "action": "on_discover", ... },
      "message": {
        "catalogs": [ {
          "id": "cat-1",
          "provider":  { ... },          // fetched once per catalog on the page
          "resources": [ ... ],          // only the ones that matched
          "offers":    [ ... ]           // only those touching a matched
        } ] } }                          // resource, plus catalog-wide ones
```

```http
X-Beckn-Degraded: structured
```

The degraded list is a **header, not a body field** (C11). `OnDiscoverAction`
declares `additionalProperties: false` with `catalogs` as its only property, so
a `degraded` key inside `message` is not an extension — it is a response that
fails its own schema. The header is absent when nothing degraded.

### The flow

```pseudo
controller.Discover(request):
    envelope ← from context
    action   ← decode as DiscoverAction
    catalogs, degraded ← service.Discover(ctx, envelope.context, action.intent)
    if degraded is non-empty:
        set header "X-Beckn-Degraded" ← join(degraded, ",")        # C11
    respond 200 with {responseContext("on_discover"), {catalogs}}
```

```pseudo
service.Discover(ctx, reqContext, intent):
    query, faults ← intentMapper.Map(intent, reqContext, config)
    if faults: return NACK 400          # never silently widen — see below

    query.NetworkID ← reqContext.networkId
        # NOT defaulted to config.App.Network. Empty means EVERY network: the
        # repository emits no network predicate at all, the same way an empty
        # schemaContext emits no schema predicate (see mapSchemaContext).
        # config.App.Network is publish's default for an empty visibleTo
        # (C8) — a different field answering a different question — and
        # reusing it here would quietly put discover back to single-network
        # scoping under a name that suggests it is unscoped.

    modes, degraded ← negotiate(query, repo.Capabilities())
    result ← repo.Search(ctx, query, modes)
    return render(result), degraded + result.Degraded
```

```pseudo
negotiate(query, capabilities) → modes, degraded:
    # Degrade-and-report, or refuse — never silently ignore.
    wanted  ← modes the intent asks for
    missing ← wanted \ capabilities
    if missing is empty:          return wanted, []
    if config.Search.FailOnUnavailableMode: fail NET_CATALOG_SOURCE_UNAVAILABLE naming missing
    return wanted ∩ capabilities, missing
```

A caller who filtered for one manufacturer and got every manufacturer has been
actively misled. Silence is the one option that is never taken.

### Intent → SearchQuery

```pseudo
mapSchemaContext(reqContext) → []SchemaFilter, []domain.Fault:
    # From the ENVELOPE, not the intent. Absent or empty → nil, and the
    # repository then emits no clause. Returning an empty non-nil slice here
    # would be the bug that empties every response.
    if reqContext.schemaContext is empty: return nil, nil

    for each uri in reqContext.schemaContext:
        base, fragment ← split uri on the FIRST '#'
        if base is empty:
            fault("schemaContext entry has no context URI"); continue
            # `continue`, not fall-through. Emitting SchemaFilter{Context: ""}
            # after faulting appends a predicate that matches nothing — harmless
            # only for as long as the fault stays fatal, and silently emptying
            # every response the day someone softens it to a warning.
        emit SchemaFilter{Context: base, Type: fragment}   # fragment may be ""
```

```pseudo
mapSpatial(constraints, config) → *SpatialFilter, []string targets, []domain.Fault:
    if len(constraints) == 0:  return nil, nil, nil
    if len(constraints) > 1:   fault("multiple spatial constraints unsupported")

    c ← constraints[0]

    # Seven of the nine CQL2 operators. S_TOUCHES and S_CROSSES are refused
    # with SCH_TYPE_NOT_SUPPORTED — not "not yet", but "not approximable by
    # a cell decomposition at any resolution" (see Geospatial Design). The
    # message says which, because a caller deciding whether to wait for a later
    # release needs to know it will never arrive.
    validate c.op ∈ {S_INTERSECTS, S_DISJOINT, S_WITHIN, S_CONTAINS,
                     S_OVERLAPS, S_EQUALS, S_DWITHIN}
    validate c.srid ∈ {"", "EPSG:4326", "urn:ogc:def:crs:OGC::CRS84", "CRS84"}
                                          else fault  (never ignore an SRID)
    validate c.quantifier ∈ {"", "ANY", "ALL", "NONE"}
    validate c.geometry parses as any of the seven RFC 7946 types
    if c.op == S_DWITHIN:
        validate c.distanceMeters > 0 and <= config.Search.MaxRadiusMeters
    else:
        # `beckn.yaml` says distanceMeters is "Ignored for other ops". Ignored
        # is what we do — but a caller who sent one believes it is filtering,
        # so it is a PARTIAL naming the field rather than silence.
        if c.distanceMeters is present: partial("distanceMeters ignored for " + c.op)

    targets ← jsonpath.Canonicalise(c.targets)      # the publish mapper's own
    if targets is empty after canonicalisation: fault("unrecognised targets")

    full, cover ← geo.CoverQuery(c.geometry, c.op, c.distanceMeters)
    bounds      ← geo.BoundsFor(c.geometry, c.op, c.distanceMeters)

    return SpatialFilter{Op:        c.op,
                         CellsFull: full, CellsCover: cover,
                         Bounds:    bounds,
                         # Populated only for Point↔Point S_DWITHIN, the one
                         # shape the haversine refinement applies to.
                         Center:    from geometry if Point and S_DWITHIN,
                         RadiusM:   c.distanceMeters,
                         Quantifier: c.quantifier},
           targets, nil
```

**Quantifiers, all three.** `ANY` (the default) means *at least one* targeted
geometry matches; `NONE` means *not one does*; `ALL` means *every one does*, and
compiles to `NOT EXISTS (a targeted geometry that provably fails)`. `ALL` was a
fault under the previous design and is answerable now for one reason: every
geometry type is decidable, so "this one fails" is a claim the index can
actually support. When a type could be proven neither way, `ALL` would have
quietly answered a weaker question than it was asked — which is why it was
refused rather than approximated.

Every branch above is a **fault, not a skip**. The reference implementation
skips constraints it cannot handle, which widens the result set: a caller who
asked for "within 5 km" gets the whole country and a 200.

### Retrieval — concurrent, one deadline (A2, A6)

```pseudo
SearchRepository.Search(ctx, query, modes):
    ctx ← WithTimeout(ctx, config.Search.ReadDeadline)     # A3
    scope ← Scope{NetworkID: query.NetworkID, Now: time.Now().UTC()}
    # A6: a value, and the instant is captured ONCE here so that every mode
    # below agrees on "now". Postgres ignores it and calls now() itself.

    run concurrently, one goroutine per enabled mode:
        lexical   → Retriever.Retrieve(ctx, query, scope) → ranked ids, capped
        fuzzy     → …
        semantic  → …                    (Phase 2)
    barrier: wait for all; a mode that errors is recorded in Degraded,
             not fatal — three modes returning is a better answer than none

    fused  ← RRF(rankedLists)                # 1/(k + rank), k = 60
    page   ← fused[offset : offset+limit]       # offset+limit <= the cap; the
                                                # mapper already faulted if not

    if offset == 0 and len(page) < limit and no mode degraded and no mode capped:
        total ← len(fused)                    # already exact — see below
    else:
        total ← Hydrator.Count(ctx, query, scope)

    rows   ← Hydrator.Hydrate(ctx, page, scope)   # resources + provider + offers
    return SearchResult{rows, total, degraded}
```

**Every retriever carries `LIMIT config.Search.MaxCandidatesPerMode`** (default
500, ~25 pages deep). Without it a mode returns every matching id in the corpus
for RRF to rank in Go — and the broad query is the common one, not the
pathological one, because `discover_tsquery` ORs its terms: *"wheat seeds"*
matches everything carrying `wheat` **or** `seed`. At 10k resources that is tens
of thousands of ids crossing the wire to be sorted and thrown away. A mode that
returns exactly its cap reports itself **capped**, because that is the one state
in which the fused list is a truncation rather than the answer.

**The cap is also the pagination depth, and that is a fault, not an empty
page.** `fused` holds at most `MaxCandidatesPerMode` ids, so `offset + limit`
beyond it can only slice past the end — while `Total` correctly reports
thousands, because the count query has no cap. A caller walking pages would get
a full page 25 and an empty page 26 that is indistinguishable from having
reached the end of the results, which is the same silent-empty failure the
schema filter and the geometry paths each have their own paragraph about. So
the discover mapper **rejects `offset + limit > config.Search.MaxCandidatesPerMode`
outright**, naming the boundary, rather than letting the slice answer it. It is
the one clamp this service does not perform quietly: `limit` above `MaxPageSize` is
clamped because the caller still gets the results they asked about, and a page
past the retrieval depth is not.

The geo predicate is **not** one of the ranked modes. It is part of the WHERE
clause every retriever and the counter share.

**`Total` counts the union, because the page is a union.** The gate, geo,
schema and attribute predicates are shared, but the *text* predicates are not —
lexical is `search_tsv @@ q`, fuzzy is a trigram similarity on `name`, and the
page is the RRF fusion of both. A counter carrying only one of them would return
a number that no page can be paginated out of: fewer results than page 1
already showed, or more than exist. So the counter's text clause is the **OR of
every enabled mode's**, which makes `Total` exactly the size of the set the
fusion draws from.

**And that set is large, because `discover_tsquery` ORs its terms.** *"wheat
seeds for sale"* matches every listing carrying any one of those words, so
`Total` reports thousands where the caller can reach twenty-five pages, and the
count query — which carries no `LIMIT`, deliberately — scans all of them
whenever the four skip guards do not fire. Both are consequences of a decision
made two sections up and neither is a defect: `Total` is honestly the size of
the candidate set, and the alternative reading, "results worth showing you", is
a number no `count(*)` can produce. It is recorded here because a caller
rendering `Total` as "3,412 results" for a query with five relevant answers will
read it as a bug in this service, and the answer is that precision is RRF's job
and the ranking already did it — the count describes the pool, not the page.

The count query has **no** `LIMIT`, so a capped mode does not make `Total`
wrong — capping truncates the *page's* candidate pool, never the count. That is
also what makes the skip above sound: with `offset == 0`, no mode degraded and
no mode capped, every id in the union is in `fused`, so `len(fused)` **is** the
count and issuing the query would spend 3–6 ms re-deriving a number already in
hand. All four guards are load-bearing — a degraded mode makes the list short
because a retriever died, and a capped one because it stopped early; either
alone would under-report `Total` with no way for the caller to tell.

### Hydration — three queries, all keyed by the page

The page is decided before hydration runs, so all three read a fixed, small
handful of ids — twenty resources and their catalogs, never the whole match.

```pseudo
Hydrator.Hydrate(ctx, page, scope):
    rows      ← select resources where (catalog_id, id) in page
                  AND <scope gate>                          # A6, again
                  # Re-applied deliberately. It is the same gate the retrievers
                  # ran, and on twenty rows by primary key it costs nothing. It
                  # is the last line between a retriever bug and a leak.
    providers ← select id, provider from catalogs
                 where id in distinct(page.catalogIds)      # ~20 rows
    offers    ← select o.* from offers o
                 where o.catalog_id = any(@catalog_ids)
                   and (cardinality(o.resource_ids) = 0        # catalog-wide
                        or exists (                            # linked
                          select 1
                            from unnest(@matched_catalog_ids,
                                        @matched_resource_ids)
                                 as p(catalog_id, resource_id)
                           where p.catalog_id = o.catalog_id
                             and p.resource_id = any(o.resource_ids)))
                   and (o.valid_from is null or o.valid_from <= now())
                   and (o.valid_to   is null or o.valid_to   >= now())
                   and within_daily_window(o.valid_time_from, o.valid_time_to,
                                           (now() at time zone 'UTC')::time)
                 # The page, FLATTENED: two 1-D `text[]`s of equal length, one
                 # (catalog_id, resource_id) pair per element. The pairing is
                 # what matters — a flat `resource_ids && all_matched_ids` would
                 # let catalog A's offer match because catalog B has a resource
                 # of the same id — and the pair survives flattening.
                 #
                 # NOT one row per catalog carrying that catalog's matched ids.
                 # That shape is a ragged array, and PostgreSQL has no such
                 # thing: a `text[][]` is rectangular by definition, so a page
                 # holding 3 matches in one catalog and 1 in another is rejected
                 # outright with `multidimensional arrays must have matching
                 # slice dimensions`. Same constraint that rules `unnest` out of
                 # the publish batch — a variable-length collection reaches SQL
                 # as parallel 1-D arrays or not at all.
    assemble catalogs, each carrying only the resources on this page
             and only the offers that touch them
```

**Offers are scoped to the matched resources.** A caller who searched for wheat
gets the offers on the wheat, plus any catalog-wide offer, and not the other
thirty-eight offers in that catalog. An empty `resource_ids` is catalog-wide and
therefore always applies; it is never treated as "no resources".

**Offer validity is checked here and nowhere else.** The catalog's own validity
does not cover it — a live catalog routinely carries last month's offer.

### The spatial predicate, as it appears in every query

One `EXISTS` serves all seven answered operators. `spatial_op` selects the
branch, so the operators cannot drift apart in seven copies of a query.

```sql
AND (
  sqlc.narg('spatial_op')::text IS NULL          -- no spatial in the intent
  -- `<>` on booleans is XOR: geo_negate=false gives EXISTS, true gives NOT
  -- EXISTS. With @match_negate below, one query text serves all three
  -- quantifiers.
  OR @geo_negate::boolean <> EXISTS (
    SELECT 1 FROM resource_geometries g
     WHERE g.catalog_id = r.catalog_id

       -- A NULL resource_id is a catalog-level geometry — the provider's own
       -- locations, stored once and belonging to every resource it sells.
       AND (g.resource_id IS NULL OR g.resource_id = r.id)

       -- `targets`. Empty means "any geometry this resource can be found
       -- by" — its own, plus its catalog's.
       AND (sqlc.narg('target_paths')::text[] IS NULL
            OR g.target_path = ANY(sqlc.narg('target_paths')))

       -- Everything below is ONE boolean — "this geometry matches" — and
       -- `@match_negate` XORs it. Three quantifiers out of two flags:
       --   ANY  → geo_negate f, match_negate f →     EXISTS(matches)
       --   NONE → geo_negate t, match_negate f → NOT EXISTS(matches)
       --   ALL  → geo_negate t, match_negate t → NOT EXISTS(NOT matches)
       -- ALL needs the INNER negation because "every targeted geometry
       -- matches" is "not one of them provably fails" — a different question
       -- from "not one of them matches", which is NONE. Two flags rather than
       -- a three-valued parameter because each flag is one XOR at one place;
       -- an enum would be a second CASE wrapping the first.
       --
       -- Every clause inside returns TRUE or FALSE and never NULL — which is
       -- what makes the inner negation safe. Under `NOT`, a NULL stays NULL,
       -- and a NULL is a miss under EXISTS: an operator that returned unknown
       -- would silently satisfy ALL rather than failing it.
       AND @match_negate::boolean <> (

              -- The bounding box. A cheap pre-rejection for indexed rows, and
              -- the ONLY predicate for oversize ones. Skipped when the query
              -- geometry declined a box (antimeridian, pole) — and skipped for
              -- S_DISJOINT, the one operator a box cannot pre-reject for. Two
              -- shapes whose boxes miss entirely ARE disjoint, so for that
              -- operator non-overlap is the strongest possible match; ANDing
              -- the box in would answer `S_DISJOINT` with only the rows near
              -- the query, which is the exact complement of the truth.
              (sqlc.narg('min_lat')::double precision IS NULL
               OR sqlc.narg('spatial_op')::text = 'S_DISJOINT'
               OR (    g.max_lat >= sqlc.narg('min_lat')::double precision
                   AND g.min_lat <= sqlc.narg('max_lat')::double precision
                   AND g.max_lon >= sqlc.narg('min_lon')::double precision
                   AND g.min_lon <= sqlc.narg('max_lon')::double precision))

              -- The operator, as set algebra. Each branch is the NEGATION of
              -- that operator's "provably FALSE" column in Geospatial Design:
              -- a row matches unless it can be proven not to. MAYBE therefore
              -- matches, and the result set is a superset of the exact answer.
              --
              -- `cells_cover IS NULL` is the oversize row, already decided by
              -- the box above. It short-circuits to TRUE here rather than
              -- being compared, because a NULL array makes every operator
              -- below return NULL, and a NULL inside EXISTS is a miss — which
              -- would make the largest service areas in the catalogue the only
              -- ones nobody can find.
              AND (g.cells_cover IS NULL
                   OR sqlc.narg('q_cover')::bigint[] IS NULL
                   OR CASE sqlc.narg('spatial_op')::text
                        WHEN 'S_INTERSECTS' THEN g.cells_cover && sqlc.narg('q_cover')
                        -- Three refutations, not one, and all three are
                        -- needed because a Point's `cells_full` is EMPTY and
                        -- `NOT ('{}' && anything)` is TRUE — a single-disjunct
                        -- branch answers `S_DISJOINT` with every Point in the
                        -- corpus, which is the commonest stored type there is.
                        -- The box cannot rescue it here: it is deliberately
                        -- skipped for this operator. See the FALSE column in
                        -- Geospatial Design for why each disjunct is sound.
                        WHEN 'S_DISJOINT'   THEN NOT (g.cells_full && sqlc.narg('q_full'))
                                            AND NOT (g.cells_cover
                                                    <@ sqlc.narg('q_full'))
                                            AND NOT (sqlc.narg('q_cover')::bigint[]
                                                    <@ g.cells_full)
                        -- `cover <@ cover`, NOT `full <@ cover`. `A ⊆ Q` implies
                        -- every cell touching A touches Q, so this is sound —
                        -- and it is strictly tighter, because `cells_full ⊆
                        -- cells_cover`. The `full` form additionally reads TRUE
                        -- for every Point and LineString, whose `cells_full` is
                        -- empty and whose `'{}' <@ anything` is TRUE, leaving
                        -- the bounding box as the only surviving predicate.
                        WHEN 'S_WITHIN'     THEN g.cells_cover <@ sqlc.narg('q_cover')
                        WHEN 'S_CONTAINS'   THEN sqlc.narg('q_cover')::bigint[]
                                                    <@ g.cells_cover
                        WHEN 'S_DWITHIN'    THEN g.cells_cover && sqlc.narg('q_cover')
                        WHEN 'S_OVERLAPS'   THEN g.cells_cover && sqlc.narg('q_cover')
                                            AND NOT (g.cells_cover <@ sqlc.narg('q_full'))
                                            AND NOT (sqlc.narg('q_cover')::bigint[]
                                                         <@ g.cells_full)
                        WHEN 'S_EQUALS'     THEN g.cells_cover = sqlc.narg('q_cover')
                                            AND g.cells_full  = sqlc.narg('q_full')
                        -- Unreachable: the mapper rejects anything else with a
                        -- 400. TRUE rather than absent so the CASE cannot
                        -- return NULL — see the note on the inner negation.
                        ELSE TRUE
                      END)

              -- The one refinement: Point-to-Point S_DWITHIN is answered
              -- exactly rather than to the nearest cell. It can only REMOVE
              -- rows the algebra admitted, so the superset guarantee survives
              -- it. Guarded on `type` so the NULL this returns for the other
              -- six is never compared — see Migration 005.
              AND (sqlc.narg('center_lat')::double precision IS NULL
                   OR g.geojson->>'type' <> 'Point'
                   OR geo_distance_m(g.geojson,
                                     sqlc.narg('center_lat')::double precision,
                                     sqlc.narg('center_lon')::double precision)
                       <= sqlc.narg('radius_m')::double precision))
  )
)
```

**Every spatial parameter is `sqlc.narg`, and none of them is `@` except the two
that are always set.** sqlc types `@x` as a non-null Go value and
`sqlc.narg('x')` as a nullable one, so naming one parameter both ways is not an
inconsistency of style — it is two incompatible declarations of one argument,
and sqlc generates from whichever it reads. `@geo_negate` and `@match_negate`
stay `@` because the mapper always sets them — a `Quantifier` of `Any` is still
a value, and the two flags are derived from it in one place. Everything else,
`spatial_op` included, is absent from an intent carrying no spatial constraint —
which is precisely what the `IS NULL` short-circuit on the first line tests, and
why `spatial_op` cannot be one of the two.

**`S_DWITHIN` and `S_INTERSECTS` share a branch, and that is not a copy-paste
error.** Once the query geometry has been dilated by `k` rings (or replaced by
its circumscribed circle for a Point), "within `d` of Q" *is* "intersects the
buffered Q". The distance lives in the cover, not in the predicate. The two
branches stay written out separately because `S_DWITHIN` additionally admits the
haversine refinement below and `S_INTERSECTS` does not.

`EXISTS`, not a join. A join needs `DISTINCT` to collapse a resource with three
geometries back to one row, and `DISTINCT` forces a sort or hash over the whole
JSONB projection. `EXISTS` stops at the first matching geometry — and negating
it is exactly `NONE`, which a join cannot express without a second pass.

**Under `NONE`, a resource with no geometry at all matches.** `NOT EXISTS` is
satisfied by the absence of rows, and a catalog whose provider published no
`availableAt` gives its resources no geometry to be near. This is the correct
reading — "not within 5 km of here" is true of something with no location — but
it is surprising enough to be worth stating, because it means `ANY` and `NONE`
over the same constraint do **not** partition the catalogue: the geometry-less
rows appear only in the `NONE` half. Scenario 19 asserts exactly that split.

**`ALL` is the same subquery with both flags set.** It asks that *every*
targeted geometry match, which is `NOT EXISTS (a targeted geometry that provably
fails)`: `@geo_negate` true for the `NOT EXISTS`, and `@match_negate` true to
turn the body from "matches" into "provably fails". `NONE` sets only the first,
and the difference between them is the whole distinction between "not one of
them matches" and "not one of them fails" — two flags because the two negations
are at different depths, not two spellings of one.

It is answerable now only because every geometry type is decidable; under the
previous design a resource whose only geometry was a non-Point could be proven
neither way, which is why `ALL` was a fault.

### Attribute filters — what PostgreSQL can and cannot do

`filters` carries an expression like:

```jsonc
"filters": {
  "type": "jsonpath",
  "expression": "$.catalogs[*].resources[*] ? (@.resourceAttributes.packagedGoodsDeclaration.manufacturerOrPacker.name == \"Hindustan Unilever Limited\")"
}
```

**PostgreSQL evaluates this natively — the grammar above *is* PostgreSQL's.**
`? (@.x == "y")` is SQL/JSON path syntax, and it is **the only grammar this
service accepts** (C10). RFC 9535 spells the same filter `[?(@.x == 'y')]`,
with the predicate in brackets and single-quoted strings; it is close enough to
look identical and different enough to fail silently, so it is refused at the
edge with `SCH_INVALID_JSONPATH` rather than attempted. The distinction matters
because a translator that gets one construct wrong returns an empty page, and an
empty page from a wrong filter looks exactly like an empty page from a correct
one.

Three things have to happen before the expression can run:

**1. Rebase the root.** The caller's path is rooted at the whole response
document; the column is rooted at one resource's `resourceAttributes`. So
`$.catalogs[*].resources[*] ? (@.resourceAttributes.A.B == "x")` becomes
`$ ? (@.A.B == "x")` evaluated against `resources.attributes`. Offer-path
filters rebase the same way onto `offers.offer`, which is why that column is
stored verbatim.

**2. Use the operator, not the function.** This is the difference between a
millisecond and a sequential scan:

```sql
-- INDEXED: the @? operator is what GIN jsonb_path_ops supports.
WHERE r.attributes @? @filter::jsonpath

-- NOT INDEXED: the function form is never index-accelerated, even though it
-- computes the identical answer.
WHERE jsonb_path_exists(r.attributes, @filter::jsonpath)
```

**3. Know which predicates the index can actually serve.** GIN extracts clauses
of the form *accessor chain* `==` *constant*. Accessors may be `.key`, `[*]` and
`[index]`.

| Expression | Indexed? |
|---|---|
| `$ ? (@.manufacturerOrPacker.name == "Hindustan Unilever Limited")` | **yes** — chain + equality |
| `$ ? (@.certifications[*].id == "FSSAI-123")` | **yes** — `[*]` is an accessor |
| `$ ? (@.rating >= 4)` | no — inequality. Correct answer, full scan |
| `$ ? (@.name like_regex "wheat.*")` | no — full scan |
| `$ ? (@.a == "x" && @.b == "y")` | partially — the equalities are extracted, then rechecked |

Equality filters — the example above, and most real catalogue filters — ride
the index. Range and regex filters return the **correct** answer and read every
gated row to do it. The posture is the same as `MaxRadiusMeters`: Task 22
refuses a range filter that arrives with no other narrowing predicate, rather
than serving an unbounded scan on request.

**Never interpolate.** The expression is bound as a parameter and cast
(`@filter::jsonpath`), so a malformed path is a cast error caught at the edge,
not a query that runs. The four-argument `jsonb_path_exists(target, path, vars,
silent => true)` form is used only where a structural mismatch must not raise.

**All of this ships in Phase 1 (Task 22).** Validating the expression, rebasing
it, and refusing the constructs that cannot be served are the whole of the work;
no migration is involved, because both columns and both GIN indexes already land
in Task 14.

`Degraded` does not go away with it. It stops describing *this phase* and starts
describing *this backend*: a store whose `Capabilities` omit `jsonpath` — the
memory backend is one — still reports `structured` in `X-Beckn-Degraded` rather
than answering a filter it cannot evaluate. The rule that outlives the phase is
the one underneath: **a filter the store cannot run must narrow nothing**,
because a wrongly narrowed page and a correctly narrowed one are the same page
at the caller.

> **Settled (C10).** `beckn.yaml` names no grammar normatively; its one
> `Intent.filters.expression` example is RFC 9535-shaped, and the network's own
> examples are PG-shaped. This service executes **PostgreSQL SQL/JSON path
> only**, and an expression it cannot parse is a `400` / `SCH_INVALID_JSONPATH`.
> Task 22 is therefore rebasing and validation, not translation.
>
> **The grammar is this service's, and it coincides with PostgreSQL's — the two
> are not the same statement.** Task 22 validates the expression against the
> stated subset in `src/platform/jsonpath/`, beside `Canonicalise`, **before any
> store sees it**; the store is handed an already-accepted expression. A backend
> that cannot execute the subset declares `jsonpath` missing in its
> `Capabilities`, and `filters` reports `structured` in `Degraded` — which is
> now a statement about that BACKEND, not about the phase. What must never happen is the
> accepted grammar changing because a deployment changed a backing service: that
> is a protocol break shipped as an infrastructure decision (TRD §5).

---

## Geospatial Design

### A geometry is a set of cells

H3 exposes no geometric predicate. It has no point-in-polygon, no
intersects, no distance-to-a-shape — its only distance function,
`greatCircleDistance`, takes two points. Reading that as "H3 cannot answer
spatial queries" is the wrong conclusion, and this section exists because the
plan drew it once already.

**Represent a geometry as a set of cells and the CQL2 operators stop being
geometry problems.** They become set algebra, which every database does natively
on an indexed array of integers, and which needs no spatial extension in any of
them.

Each stored geometry gets **two** covers at one fixed resolution:

| Column | H3 containment mode | Meaning |
|---|---|---|
| `cells_full` | `CONTAINMENT_FULL` | cells lying **entirely inside** the geometry |
| `cells_cover` | `CONTAINMENT_OVERLAPPING` | cells touching the geometry **at all** |

which gives the invariant the whole design rests on:

```
cells_full  ⊆  the true geometry  ⊆  cells_cover
```

**Two covers, because one proves positives and the other proves negatives.**
A single cover can only ever say "maybe". `cells_full` is a guaranteed subset,
so anything it asserts about the interior is true of the real shape;
`cells_cover` is a guaranteed superset, so anything it *rules out* is really
ruled out. An earlier revision of this plan stored only the cover and argued
explicitly against adding the `CONTAINMENT_FULL` subset — correct while
`S_DWITHIN` was the only operator, and exactly the missing half for the rest of
CQL2.

### Every operator is set algebra

`A` is a stored geometry, `Q` the constraint geometry, both covered the same
way. `&&` is array overlap, `<@` is contained-by, `@>` contains — all
PostgreSQL array operators, all GIN-indexable on `BIGINT[]`.

| `op` | Provably TRUE | Provably FALSE |
|---|---|---|
| `S_INTERSECTS` | `A.full && Q.full` | `NOT (A.cover && Q.cover)` |
| `S_DISJOINT` | `NOT (A.cover && Q.cover)` | `A.full && Q.full`, or `A.cover <@ Q.full`, or `Q.cover <@ A.full` |
| `S_WITHIN` | `A.cover <@ Q.full` | `NOT (A.cover <@ Q.cover)` |
| `S_CONTAINS` | `Q.cover <@ A.full` | `NOT (Q.cover <@ A.cover)` |
| `S_DWITHIN` | `A.full && dilate(Q.full, k)` | `NOT (A.cover && dilate(Q.cover, k))` |
| `S_OVERLAPS` | intersects ∧ ¬within ∧ ¬contains | `NOT (A.cover && Q.cover)`, or either containment proves |
| `S_EQUALS` | *never* | `A.cover <> Q.cover OR A.full <> Q.full` |
| `S_TOUCHES` | *never* | *never* |
| `S_CROSSES` | *never* | *never* |

`S_OVERLAPS` is the one row whose FALSE column has three disjuncts rather than
one, because overlap is a conjunction of three conditions and refuting any one
of them refutes the whole. Two come from containment — a shape contained in the
other does not overlap it — and the third is plain disjointness, which is sound
here for the same reason it is sound for `S_INTERSECTS`: a true overlap implies
the true geometries meet, and covers are supersets, so covers that do not meet
prove geometries that do not either. All three appear in the `CASE` arm above.

**Refute with `cover`; prove with `full`. The two columns are not
interchangeable and the direction is what decides which one is sound.** To
*prove* `A ⊆ Q` you need every cell of `A.cover` to be a cell PostgreSQL knows
lies entirely inside Q — `A.cover <@ Q.full`, the TRUE column. To *refute* it
you need a necessary condition, and the tightest available one is
`A.cover <@ Q.cover`: if `A ⊆ Q` then every cell touching A touches Q, so a
cell of A's cover that is absent from Q's cover proves `A ⊄ Q`. `S_CONTAINS`
is the same statement with the arguments swapped. `S_DISJOINT` gains two
refutations beside `A.full && Q.full` by the same reasoning in reverse:
`A.cover <@ Q.full` means `A ⊆ Q`, and a non-empty A inside Q meets it.

**This is not a micro-optimisation, and the reason is degenerate `cells_full`.**
A Point and a LineString contain no cell, so their `cells_full` is the empty
array — correctly, and permanently. PostgreSQL's `'{}' <@ anything` is **TRUE**
and `'{}' && anything` is **FALSE**, so a refutation phrased over `full` on the
degenerate side does not merely lose precision, it stops being a predicate:
`S_WITHIN` and `S_CONTAINS` fall back to the bounding box, and `S_DISJOINT` —
which skips the box on purpose — returns every Point in the corpus. Since a
shopfront is a Point, that is the commonest row in the table. Phrasing all three
over `cover` restores them to the resolution's precision, which is the accuracy
this design claims everywhere else.

The three `cover`-side arrays it now rests on are guaranteed non-empty by
`CHECK (cells_cover IS NULL OR cardinality(cells_cover) > 0)` in Migration 004.
That constraint is load-bearing here rather than tidy: an empty `cells_cover`
would make `A.cover <@ anything` trivially TRUE and quietly reintroduce the
failure this paragraph exists to remove, one column over.

Between the two columns is a band where neither proof fires. That band is the
boundary, it is one cell wide, and what happens in it is a decision this plan
has to make rather than discover.

### MAYBE resolves as a match, and the result set is a superset

**The rule: a geometry that cannot be proven to fail is returned.** Every
operator therefore over-includes and never under-includes, and the guarantee
that falls out is worth stating as an invariant, because it is the thing a
reviewer should check any future operator against:

> For quantifier `ANY`, the result set is always a **superset** of the exact
> answer. Never a subset.

This is the correct bias for *discovery*. The word means candidates: a search
for suppliers within 1 km that also returns one at 1.3 km has done its job, and
the transaction that follows decides precisely. A search that **omits** a
supplier at 900 m has failed silently, and nothing downstream can repair it.

**Under `NONE` the direction inverts, and that is the safe way round.**
`NOT EXISTS` over an over-inclusive predicate yields a **subset** of the exact
answer — a "not near here" query may drop something that is genuinely far away,
and can never return something that is close. Compare the arrangement this
replaces, where a Polygon inside the radius came back from a "nowhere near here"
query because its distance was NULL. The asymmetry has not gone away; it has
been moved to the harmless side and stated as a rule rather than emerging from
one type's missing implementation.

`ALL` becomes answerable for the first time, for the same reason: every
geometry is now decidable, so "every targeted geometry matches" is
`NOT EXISTS (a targeted geometry that provably fails)`. It was a fault under the
old design only because a non-Point could not be proven either way.

### The two operators no resolution can reach

**`S_TOUCHES` and `S_CROSSES` are not imprecise. They are undecidable.**

`S_TOUCHES` means *the interiors are disjoint and the boundaries intersect*. A
cell decomposition has no boundary — the boundary becomes a band of cells with
area. Two polygons that touch, two that overlap by a millimetre, and two
separated by a millimetre all produce the identical signature: shared `cover`
cells, no shared `full` cells. Refining the resolution does not converge on an
answer, because touching is a measure-zero condition and cells have measure.
`S_CROSSES` fails the same way.

They are a **400**, `SCH_TYPE_NOT_SUPPORTED`, naming the operator and saying
that it is not approximable rather than not yet built — the distinction matters
to a caller deciding whether to wait for a later release. They are also the two
operators nothing in agricultural discovery asks for: they are cartographic
predicates, for deciding whether two parcels in an editor share an edge.

`S_EQUALS` is answered but **over-inclusively**, and it is the one operator
whose limit is worth a caller's attention: identical geometries always produce
identical cell sets, so there are no false negatives, but two *different*
shapes occupying the same cells are indistinguishable. "No false negatives"
holds only because both covers are stored **ascending and deduplicated** —
PostgreSQL's array `=` is element-wise in order, so an unsorted pair of
identical sets would compare unequal. It matches, in other
words, at the resolution's precision and no better.

### The MAYBE band, in metres

The band is one cell wide, so the resolution is the accuracy knob:

| Resolution | Avg edge | Avg area | MAYBE band | Cells per 1,000 km² |
|---|---|---|---|---|
| r7 | 1,406 m | 5.16 km² | ~2.8 km | ~194 |
| **r8** | **531 m** | **0.737 km²** | **~1.1 km** | **~1,357** |
| r9 | 201 m | 0.105 km² | ~400 m | ~9,494 |
| r10 | 76 m | 0.015 km² | ~150 m | ~66,459 |

Two columns fight each other and there is no resolution that wins both. **The
default is `ResolutionCells = 8`**, which holds a district-scale service area
inside the cell budget while keeping the boundary uncertainty near a kilometre —
the right trade when the query is "farms near this town" and the wrong one if it
ever becomes "is this plot inside this survey boundary". It is a config key, not
a constant, because the right answer is a property of a deployment's data.

### Oversize geometries, and the bounding box that catches them

At r8 the 8,192-cell budget is about 6,000 km². A state-sized polygon is past
it, and a cover truncated to fit would make a shape discoverable only in
whichever corner the fill reached — the failure mode that made the previous
design store NULL rather than truncate.

**A geometry over budget stores `cells_full = NULL`, `cells_cover = NULL`, and
is decided by its bounding box alone.** It is *always MAYBE* inside that box,
which under the superset rule means it matches any query whose box overlaps it.
That is the honest answer for a shape the index declined to represent, and the
bounding box keeps it from being the answer to every query on Earth.

This is why the four bbox columns survive a redesign that deleted the stage they
were introduced for. They are no longer a general second stage; they are the
**entire** predicate for oversize rows, and a cheap pre-rejection for the rest.

**With one exception, and it is the kind that is easy to write and hard to
notice.** The box pre-rejects only for operators that need the two shapes to
meet — six of the seven. `S_DISJOINT` is the seventh and it inverts: two shapes
whose boxes miss entirely *are* disjoint, so for that operator non-overlap is
the strongest possible match. ANDing the box in would answer `S_DISJOINT` with
exactly the rows near the query — the complement of the truth, returned with no
error. The predicate therefore skips the box when `op = 'S_DISJOINT'`, and an
oversize geometry (both cell columns NULL, box skipped) matches it
unconditionally, which is the superset-correct answer for a shape the index
declined to represent.

### Covering a query geometry

The constraint geometry is covered exactly like a stored one — same function,
same resolution, both modes — with one addition for `S_DWITHIN`, which is the
only operator carrying a distance.

```pseudo
CoverQuery(geometry, op, distanceMeters) → full, cover cells:
    if geometry is a Point and op is S_DWITHIN:
        # The circle case, and worth special-casing because it is most of the
        # traffic. An INSCRIBED n-gon sags to R·cos(π/n) between vertices and
        # would miss a sliver near the boundary; scaling every vertex by
        # 1/cos(π/n) makes the polygon CONTAIN the circle. At n=64, 1.0012.
        shape ← circumscribed 64-gon around (center, distanceMeters)
        return fill(shape, Full), fill(shape, Overlapping)

    full, cover ← fill(geometry, Full), fill(geometry, Overlapping)

    if op is S_DWITHIN:
        # "within 500 m of this canal" — a buffered LineString or Polygon.
        # There is no geometry engine here to buffer with, so dilate on the
        # grid instead: k rings of gridDisk around every cell.
        k ← ceil(distanceMeters / minCenterDistanceM(ResolutionCells))
        full, cover ← dilate(full, k), dilate(cover, k)

    return full, cover
```

`minCenterDistanceM` is deliberately not the average. Centre-to-centre spacing
is `√3 × edge`, H3 cell areas vary by up to ~1.99× within a resolution, so the
minimum edge is ≥ 0.71× the average — giving `1.23 × avgEdge` as the
conservative step. Sizing `k` from the *average* would under-dilate wherever
cells run small, and under-dilation is the one error direction this design does
not permit.

**Dilation is bounded like everything else.** `gridDisk(c, k)` is
`3k² + 3k + 1` cells per seed, so a 500-cell river cover at k=5 is up to 45,500
before deduplication. Past `MaxQueryCoverCells` the dilated cover is dropped and
the constraint falls back to bounding box alone — wider, never narrower.

**The antimeridian and the poles have no cell cover.** A circle crossing ±180°
or containing a pole makes the 64-gon's vertices wrap, and the polygon H3
receives is not the one that was asked for, so `CoverQuery` declines and returns
nil. Nil disables the cell predicate only: the bounding box still runs, so the
answer stays a superset and the query degrades to a scan of the scope-gated set.
`BoundsFor` declines on the same input for the same reason. For an India
deployment this is unreachable; it is written down because "it worked in
testing" and "the cover silently stopped narrowing" look identical from
outside, and because the day this service indexes Fiji the fix is a split cover
rather than a schema change.

### Covering a stored geometry

```pseudo
CoverGeometry(geometry) → Cover:
    parts ← decode(geometry)     # points, lines, polygons; recurses into
                                 # GeometryCollection to depth 8

    Cover.Bounds     ← bbox over every vertex
    Cover.CellsFull  ← fill(parts, ResolutionCells, ContainmentFull)
    Cover.CellsCover ← fill(parts, ResolutionCells, ContainmentOverlapping)

    if len(CellsCover) > MaxIndexCoverCells:
        Cover.CellsFull, Cover.CellsCover ← nil, nil   # oversize; bbox decides
```

Two facts about `CONTAINMENT_FULL` that a reader will otherwise rediscover
through a failing test:

- **A Point and a LineString have an empty `full` set**, always. Neither has
  interior area, so no cell lies entirely inside one. This is correct, not a
  bug: it means a Point can never *prove* `S_INTERSECTS` and is decided in the
  MAYBE band — which, under the superset rule, matches. `S_DWITHIN` against a
  Point is the case worth having exact, and it is handled below.
- **A polygon smaller than one cell has an empty `full` set too**, and a
  `cover` of one or two cells. It behaves like a Point, correctly.

Lines are **densified**, not sampled at their vertices — a segment can cross
cells between its endpoints:

```pseudo
walk(a, b, stepM):
    # RFC 7946 §3.1.1: a segment is a straight line IN THE CRS, so interpolate
    # linearly in lon/lat and size the sample count by a Manhattan bound
    # (lat span + lon span at the widest parallel), never by haversine.
    n ← ceil(manhattanLengthM(a, b) / stepM)
    emit a + (b-a)·i/n for i in 0..n
```

`stepM` is a quarter of the resolution's average edge (130 m at r8). The
minimum inradius is ≥ 0.61× the average edge, so a quarter-edge step cannot
jump a cell.

### Point-to-point stays exact

Cells are the whole predicate for every shape but one. **When `S_DWITHIN` is
asked about a Point constraint against a stored Point, haversine decides**, and
the cell cover is only a prefilter:

```sql
AND (g.geojson->>'type' <> 'Point'
     OR geo_distance_m(g.geojson, @center_lat, @center_lon) <= @radius_m)
```

Point-to-point is the majority of real traffic — "suppliers near me" against
providers with `availableAt` coordinates — it is the one case where the exact
answer costs six lines of arithmetic and no extension, and a ±530 m band on a
1 km search is a difference callers notice. The clause is a **refinement, never
a widening**: it can only remove rows the cell predicate admitted, so the
superset guarantee holds, and it applies to exactly one `type` so no other
geometry's answer moves.

This is not the old three-stage funnel returning under a new name. The funnel
made an exact scalar the *decider* for every operator and every type, which is
what left six of seven types answering NULL. Here the cell algebra decides
everything, and one geometry type gets one operator sharpened.

The Go side must agree with the SQL exactly. Haversine is written **twice and
only twice** — `geo.HaversineM` in Go, `geo_haversine_m` in SQL — because SQL
cannot import Go, and that is the only reason a second copy is tolerated. The
memory backend imports `geo.HaversineM` rather than keeping a private one, and
Task 16 pins the two against a fixed table of coordinate pairs, since a
disagreement reaches the caller as a result 10.1 km from a 10 km search.

### Constants

| Name | Value | Why |
|---|---|---|
| `ResolutionCells` | 8 | ~0.74 km², ~531 m average edge, ~1.1 km MAYBE band. **Config, not a constant** — the accuracy/storage trade is a property of a deployment's data |
| `MaxIndexCoverCells` | 8,192 | Cells one **publish** cover may produce (~6,000 km² at r8). Over it, both cell columns are NULL and the bounding box decides |
| `MaxQueryCoverCells` | 4,096 | Cells one **discover** cover may produce, enforced by H3 via `maxNumCellsReturn`, and the ceiling on a dilated cover |
| `queryCircleVertices` | 64 | Vertices in the polygon approximating an `S_DWITHIN` radius. Circumscribing scale 1.0012 |
| `MaxCatalogWalkDepth` | 32 | The publish walker reads publisher-shaped documents. A cyclic or pathological nesting must cost a bounded walk, not a stack |
| `MaxGeometriesPerCatalog` | 256 | Publish budget for the general walk. Over it, the extra finds are *partial* faults naming their paths — never a silent drop |

### Stated limits

Three, and they are properties of the representation rather than of this
phase — none of them closes by writing more code against the same design:

1. **`S_TOUCHES` and `S_CROSSES` are refused**, because a cell decomposition
   cannot express a measure-zero boundary relation at any resolution.
2. **Everything else is accurate to one cell** (~1.1 km at r8), biased to
   over-inclusion under `ANY` and to under-inclusion under `NONE`.
3. **Geometries over ~6,000 km² are decided by bounding box**, and match more
   widely than they should within it.

If OAN ever needs *cadastral* precision — plot boundaries for subsidy
eligibility, exact survey-number containment, a dispute resolved by which side
of a line a well sits on — this design cannot deliver it and PostGIS becomes
necessary. That is a fine trade to make deliberately for a discovery service,
whose job is to return candidates. It is a bad one to discover from a support
ticket, which is why it is a numbered limit and not a caveat in a paragraph.

---

## Scenarios

Thirty-five end-to-end scenarios in `tests/acceptance/`, run against the
assembled service: real router, real PostgreSQL, real migrations, over HTTP.
Only the embedder is stubbed.

Each row says what it *pins*, including the two that pin less than their name
suggests. A scenario index that overstates its coverage hides the gap nobody
goes looking for.

### Publish — `publish_test.go`

| # | Scenario | Pins |
|---|---|---|
| 1 | `PublishNewCatalog` | A catalog lands and is immediately discoverable: `ACCEPTED`, then a `/discover` finds it. Indexing happened inside the write |
| 2 | `UpdateExistingCatalogMerges` | A8, end to end and at field level. Publish a resource whose `resourceAttributes` are `{grade, moisture, origin}`, then republish carrying **only** `{"moisture": 14, "origin": null}`: `grade` survives untouched, `moisture` is 14, `origin` is gone, and the `descriptor` the patch never mentioned is byte-identical. Re-publishing one resource of two still leaves the other alone. The MERGE default — the difference between an update and a silent data loss |
| 3 | `FullUpdateReplacesTheCatalog` | `updateMode: FULL` removes resources the new payload omits **and resets the catalog row itself**: a FULL republish omitting `validity` clears all four validity columns, where MERGE would have kept them. The dangerous half of the same feature, asserted so a mis-wired directive cannot pass as MERGE |
| 4 | `InvalidPayloadIsRejected` | A resource with no `id` NACKs with a `SCH_` code and a JSON pointer, **and nothing is stored** — asserted by searching afterwards and finding nothing. Run twice: once with the key absent, once with `"id": ""`, because the schema requires the key and says nothing about its length, so a presence check alone admits the one value `uq_resource_geometries` cannot key |
| 5 | `MasterCatalogAndInheritanceAreRefused` | A1: both a MASTER catalog and a child carrying `extends` come back `REJECTED` / `SCH_TYPE_NOT_SUPPORTED`, neither stored. Not "inheritance works" — "inheritance is refused, visibly" |
| 6 | `ARejectedMasterDoesNotBlockTheRegularCatalogsBesideIt` | One request, two catalogs, two verdicts in one results array. The per-catalog transaction boundary, end to end |
| 6a | `TheSameCatalogIdTwiceInOneRequestIsRefused` | One request carrying the same `catalog.id` twice. The first is `ACCEPTED`, the second `REJECTED` / `SCH_SCHEMA_VALIDATION_FAILED`, and the catalog that is stored is the **first** one — asserted on a field the two entries disagree about. Without the check both are `ACCEPTED` and the stored catalog is the second, so one of the two success verdicts describes a document that no longer exists; under `FULL` the second entry additionally deletes the first's resources. The pin is on the stored document rather than on the status array, because two `ACCEPTED`s is exactly what the bug looks like from outside |
| 7 | `SignatureVerificationRefusesToBoot` | `AUTH_ENABLE_SIGNATURE_VERIFICATION=true` fails startup, naming the flag. This replaces the original pair (`MissingSignatureIsUnauthorized` / `UnsignedRequestSucceedsWhenVerificationIsOff`), which asserted both sides of a flag that now has nothing behind either side. What made the deferral honest was never the flag itself but the impossibility of believing it was on when it wasn't — with the crypto parked, a boot refusal is the only thing that still carries that |
| 8 | `UnsignedRequestSucceeds` | With the flag off — the only supported setting in Phase 1 — an unsigned publish is processed normally. Pins that nothing in the chain has quietly started requiring a signature |
| 8a | `AnOversizedBodyIsRefusedWithA413` | C14, end to end: a publish body over `SERVER_MAX_REQUEST_BODY_BYTES` comes back `413` / `POL_NP_CAPACITY_EXCEEDED`, nothing is stored, and the service is still serving afterwards. Set the knob low for this scenario rather than sending 10 MiB. It sits beside scenario 9 for the same reason 9 exists — it pins that the ceiling is *mounted*, and a ceiling only `Envelope`'s own unit tests exercise is one that a re-wiring in Task 20 can drop without a single test going red |
| 9 | `ACallerOverItsRateGetsA429` | Burst+1 requests: the last is `429` / `AUT_RATE_LIMITED` with `Retry-After`. Also pins that the limiter is *mounted* — an unmounted middleware is invisible to every other test |
| 10 | `ChangingVisibleToWithNoResourcesInThePayloadTakesEffect` | Publish a catalog with resources, then republish the same catalog with `visibleTo` narrowed and **no resources at all**. The resources must stop being discoverable. The gate lives on `resources`, so without the unconditional `UPDATE resources` the catalog row changes and discover ignores it — a visibility change that reports success and does nothing |
| 10a | `ChangingTheGateRewritesOnlyTheRowsItChanges` | Publish a forty-resource catalog, then republish the catalog document with no `visibleTo` change and no resources. Every resource still carries the catalog's gate — that is scenario 10's guarantee and it does not move — but `xmin` on the untouched rows is unchanged, so no row was rewritten. Then narrow `visibleTo` and republish: now every row moves. The `IS DISTINCT FROM` guard on the propagate is what separates the two, and it is worth a scenario because the failure it prevents is invisible in every response — forty dead tuples and forty `fastupdate = off` GIN insertions per publish, paid on the write path and observed as a slow one |
| 11 | `AFullRepublishPrunesOrphanedOffers` | A FULL republish that drops a resource must delete the offer that pointed only at it, and shorten the offer that pointed at it plus a survivor. Pins that a pruned-to-empty offer is deleted rather than silently promoted to catalog-wide |

### Discover — `discover_test.go`

| # | Scenario | Pins |
|---|---|---|
| 12 | `GeoSearchFindsNearbyResources` | A radius query returns what is inside it. Cover, bbox and haversine agree |
| 13 | `GeoSearchOutsideTheRadiusReturnsNothing` | The negative half. Without it, a cover that returns everything passes #12 |
| 14 | `TheRadiusBoundaryIsExact` | A point just inside and one just outside 10 km, 306 m apart. Cell algebra answers this to ~1.1 km and would return both; the Point↔Point haversine refinement is what separates them. The scenario that justifies the refinement existing, and the reason it runs in SQL rather than Go |
| 15 | `CatalogGeometryIsCoveredOnceAndSharedByEveryResource` | A catalog with three `availableAt` locations and forty resources stores **three** geometry rows, not 120 — and a radius query around the third location still returns all forty. Pins the nullable `resource_id` in both directions: the storage saving, and the `g.resource_id IS NULL` half of the predicate without which every geo search returns nothing |
| 16 | `ANonPointGeometryIsMatched` | A stored Polygon containing the search point comes back from an `S_DWITHIN` — **and does not come back from the same radius under `quantifier: NONE`.** Both halves, because this pair is the regression test for the design this section replaced, where a Polygon inside the radius was missing from `ANY` and present in `NONE`. A Point and a Polygon in one catalog assert that the two types now answer the same question the same way |
| 17 | `HybridSpatialAndTextualSearch` | Text and proximity in one intent, both applied. Where a refactor quietly drops one constraint |
| 18 | `AStructuredFilterNarrowsTheResult` | Two resources differing only in `resourceAttributes.packagedGoodsDeclaration.manufacturerOrPacker.name`, filtered with `$.catalogs[*].resources[*] ? (@.resourceAttributes.packagedGoodsDeclaration.manufacturerOrPacker.name == "Hindustan Unilever Limited")`. One comes back. The same filter in RFC 9535 spelling is a `400` / `SCH_INVALID_JSONPATH` in the same scenario, because the thing being defended against is the two spellings looking alike while one of them silently returns nothing. Then the same expression against a backend declaring no `jsonpath` capability returns results **plus** `X-Beckn-Degraded: structured` (C11), with a `message` that still validates against `OnDiscoverAction` — a `degraded` key in the body would not. **Deferred out of Task 21 to Task 22.** `MapIntent` never reads `intent.Filters` — nothing parses a filter yet, so the scenario would assert that an unimplemented feature returns both resources. It is written in the task that builds the parser, against the parser |
| 19 | `QuantifierNoneInvertsTheMatch` | The same radius as #12 with `quantifier: NONE` returns everything #12 did **not** — *plus* a third resource whose catalog published no location at all, because `NOT EXISTS` is satisfied by absence. Both halves are asserted: the inversion, and the geometry-less row that belongs only to this half. Pins the `geo_negate` XOR, one character away from silently inverting every geo search. Under `NONE` the result set is a **subset** of the exact answer rather than a superset — the inversion of the guarantee, and the safe direction |

### Offers on the read path — `offers_test.go`

| # | Scenario | Pins |
|---|---|---|
| 20 | `OffersOnMatchedResourcesAreReturned` | A catalog with two resources and one offer naming only the first. A search that matches the first returns that offer; a search that matches only the second does not. The array-overlap join, and that offers are scoped to the page rather than dumped per catalog |
| 21 | `ACatalogWideOfferIsReturnedWithEveryResource` | An offer with an empty `resource_ids` comes back whichever resource matched. Empty means *catalog-wide*, and the one place that distinction can be silently lost is a writer that treats `'{}'` as "no resources yet" |
| 22 | `AnExpiredOfferIsNotReturned` | An offer whose `valid_to` has passed is absent from a response whose catalog and resources are all live. Offer validity is checked at hydration; nothing upstream covers it |

### Validity — `validity_test.go`

| # | Scenario | Pins |
|---|---|---|
| 23 | `ADailyWindowClosesTheCatalogOutOfHours` | Two catalogs, neither with a `startDate`. One carries a `startTime`/`endTime` pair straddling the current instant, the other a pair excluding it; one search returns the first and not the second. **Built relative to now rather than to a fixed hour** — the SQL gate calls `now()` and does not read `Scope.Now`, so a scenario phrased as "returned at 10:00 IST" is a test that only passes before lunch. Pins that the clock-only form is stored rather than read as always-valid — the failure nobody reports, because an absent result raises no complaint |
| 24 | `AnOvernightWindowWrapsPastMidnight` | `[now+1h, now-1h]` — `from > to`, current instant in the gap — hides the catalog; `[now-1h, now-2h]` — also `from > to`, instant inside the wrap — returns it. Relative for the same reason #23 is, **and with the same limit stated rather than hidden: within two hours of midnight the arithmetic stops wrapping and this pair degenerates into two ordinary forward windows.** So the scenario pins the end-to-end plumbing, and the branch itself belongs to Task 14's SQL unit test, which passes the instant to `within_daily_window` as an argument and can therefore cover forward, wrapping, `from == to` and either bound NULL at an hour it chooses. That split exists precisely because the gate calls `now()` |

### Performance — `performance_test.go`

| # | Scenario | Pins |
|---|---|---|
| 25 | `TenThousandResourcesStayUnderTwentyMilliseconds` | 10k resources, 16 concurrent discovers, p95 < 20 ms. Also the only test that exercises the pool under real concurrency, so it asserts **`pgxpool.Stat().EmptyAcquireCount() == 0`** as well as the latency: without that, an undersized pool fails this scenario as a slow *query*, and the fix goes looking in the SQL. The single-table scope gate is what the latency depends on: a `count(*)` that joined `catalogs` would probe once per match, not once per page |

### Defaults — `defaults_test.go`

| # | Scenario | Pins |
|---|---|---|
| 26 | `AnOmittedDefaultResetsInBothModes` | A9, on the two fields where it costs something. Publish a catalog with `isActive: false` and `visibleTo: ["mahavistar","oan"]`; republish it under **MERGE** with a one-attribute patch and no `publishDirectives` at all; the catalog is live again and visible only to `mahavistar`. Then the same pair under FULL, with the same result. This scenario exists to make the surprising half of A9 *deliberate*: it is the test that fails the day someone "fixes" MERGE to preserve an omitted default, which would make the two modes disagree about what silence means |

### Geometry paths — `geopath_test.go`

| # | Scenario | Pins |
|---|---|---|
| 27 | `TargetsSelectTheRightGeometry` | The reason `targets` is a JSONPath and not a constant. One catalog, one resource, **two** geometries at different paths: a shopfront at `provider.availableAt[0].geo` in Bengaluru, and a service polygon at `resourceAttributes.serviceArea` 300 km away. A search near Bengaluru targeting the provider path returns the resource; the same search targeting the `serviceArea` path does not; a search near the polygon inverts both answers; and a search with **no** `targets` returns it from either location. Before the walker generalised, no publish payload could build this fixture — the extractor emitted one path, so the predicate could only ever be tested with hand-written rows |
| 28 | `AGeometryAnywhereIsFoundAndCanonicalised` | A geometry nested somewhere the plan never names — `resourceAttributes.pickup.point` — is indexed, discoverable, and its stored `target_path` is byte-identical to the canonical form of what a caller sends. The second half is the one that matters: a search sending the **bracket** form `$['catalogs'][*]…` and a search sending the **dot** form must return the same row, because both sides go through `jsonpath.Canonicalise`. A mismatch here is a `200` with an empty list, which no other test in this suite would notice |

`SemanticSearchFindsAParaphrase` is deliberately **not** in this list. CI runs
the `hashing` embedder, which is not semantic, so an acceptance test of that
name would assert something it cannot. The vector path is pinned by Task 16's
repository tests instead.

### Network scope — `discover_test.go`

| # | Scenario | Pins |
|---|---|---|
| 29 | `OmittedNetworkIdSearchesEveryNetwork` | Two catalogs published to non-overlapping networks — `visibleTo: ["mahavistar"]` and `visibleTo: ["bharatvistar"]`. A discover with **no** `networkId` returns both. The same intent with `networkId: "mahavistar"` returns only the first. `visibleTo` restricts which networks a publisher chose to expose a catalog on; it is not an access boundary a network-less caller is presumed locked out of — a caller wanting isolation supplies `networkId` |
### Spatial operators — `spatial_test.go`

| # | Scenario | Pins |
|---|---|---|
| 30 | `TheOperatorSetIsAnsweredAsSetAlgebra` | One stored Polygon (a district service area) and one query Polygon overlapping half of it. `S_INTERSECTS` matches, `S_DISJOINT` does not, `S_WITHIN` does not, `S_CONTAINS` does not, `S_OVERLAPS` does. Then a query Polygon strictly inside the stored one: `S_CONTAINS` matches and `S_OVERLAPS` no longer does. Five operators in two fixtures, because each is one `CASE` arm and an arm that is never executed is an arm that only compiles |
| 30a | `DegenerateFullCoversDoNotDisableTheOperator` | The same three operators against a stored **Point** — a shopfront, the commonest geometry in the corpus and the one whose `cells_full` is empty. A Point inside the query Polygon: `S_WITHIN` matches, `S_DISJOINT` does not. A Point 400 km outside it: `S_WITHIN` does **not** match, and `S_DISJOINT` does. Then a query **Point** against a stored Polygon containing it: `S_CONTAINS` matches, and against a Polygon 400 km away it does not. Scenario 30 cannot catch any of this — Polygon-vs-Polygon is exactly the case where both `cells_full` are non-empty and a `full`-phrased refutation still behaves. Phrased over `full`, every one of the negative assertions here fails: `'{}' <@ anything` is TRUE, `NOT ('{}' && anything)` is TRUE, and the operators degrade to the bounding box or, for `S_DISJOINT`, to nothing at all |
| 31 | `DisjointIsNotBoundingBoxFiltered` | A third fixture 400 km away, queried with `S_DISJOINT`. It matches — and would not if the bounding box were ANDed in for every operator, which is the one place a box pre-rejection is not merely conservative but inverted. Split out from #30 because it is the only scenario whose failure looks like an empty result rather than a wrong one, and an empty result reads as "no data" |
| 32 | `TouchesAndCrossesAreRefused` | `S_TOUCHES` and `S_CROSSES` NACK with `SCH_TYPE_NOT_SUPPORTED` and a `400`. Asserted rather than left to the schema, because both are valid `beckn.yaml` enum values — L1 validation passes them, and the only thing standing between a caller and a silently wrong answer is this refusal |
| 33 | `AnOversizeGeometryIsFoundByItsBoundingBox` | A polygon over `MaxIndexCoverCells` stores NULL in both cell columns and is still returned by a search inside its bounding box. Pins the `cells_cover IS NULL` short-circuit — without it a NULL array makes the operator branch NULL, NULL is a miss inside `EXISTS`, and the largest service areas in the catalogue become the only ones nobody can find |
| 34 | `QuantifierAllRequiresEveryTargetedGeometry` | A catalog with two `availableAt` locations, one inside the radius and one outside. `ANY` returns it, `ALL` does not; a second catalog with both locations inside is returned by both. `ALL` is answerable only because every geometry type is now decidable, so this scenario is also the assertion that it stopped being a fault |
| 35 | `AnOfferGeometryFindsOnlyThatOffersResources` | A catalog with three resources and one offer whose `resourceIds` names only the second, its `provider.availableAt` in Koramangala. A search targeting `$.catalogs[*].offers[*].provider.availableAt[*].geo` within 1 km returns **that resource only**, with that offer hydrated onto it — not the other two. Then republish the same offer with `resourceIds` emptied: the search now returns all three, because empty means catalog-wide in `resource_geometries` exactly as it already does in `offers.resource_ids`. The republish half is the half that matters: it patches the OFFER and names no resource, so it fails unless `touched` follows `resourceIds`. **A third leg, and the one an assertion on the response cannot make:** republish the offer again with `resourceIds` naming only the *third* resource, then assert both directions — the search returns the third resource and not the second, **and** `resource_geometries` holds no row for the second. Only the direct table assertion catches a relocation, because the stale row it leaves behind is a row too many rather than a row too few, and a search that returns it looks like a search that worked. This is what pins `touched` to the union of the offer's pre- and post-merge ids rather than to the merged ones |

### Not covered, deliberately

Multi-tenant isolation, `on_publish` callback delivery, real-model semantic
ranking. Each is out of phase or covered closer to the code, and none has a
passing scenario standing in for it.

### Worked example — scenario 14, the boundary

```jsonc
// setup: two resources in a catalog whose provider.availableAt[0].geo is
//   near: [77.6600, 13.0350]                    ~9,995 m from centre
//   far:  [77.6620, 13.0370]                   ~10,306 m from centre
//
// Both sit NORTH-EAST of the centre, and that is the whole construction. A
// point placed due north at 10,010 m is 0.0900 deg away while the bounding
// box reaches 0.0899 — the box would remove it, and the scenario would pass
// while proving nothing about the exactness it exists to assert.

POST /discover  { "message": { "intent": { "spatial": [{
  "op": "S_DWITHIN",
  "targets": "$.catalogs[*].provider.availableAt[*].geo",
  "geometry": { "type": "Point", "coordinates": [77.5946, 12.9716] },
  "distanceMeters": 10000 }] } } }

// → 200, exactly one catalog, containing only `near`.
// On a diagonal bearing the box corner is 1.41x the radius away, so `far` is
// comfortably inside it (0.0654 / 0.0899 lat, 0.0674 / 0.0923 lon) and its r8
// cell still touches the cover 306 m away. Cells and box both admit it; the
// Point-to-Point haversine refinement is what removes it.
//
// This is THE scenario justifying that refinement. Cell algebra alone answers
// this query to ~1.1 km, so `far` would match and 306 m of error would reach a
// caller who asked for 10 km. It is also why the refinement runs in SQL: a
// geometry Go never sees cannot be measured in Go.
```

---

## Tasks

Twenty-three tasks, dependency-ordered. Each is one reviewable unit ending in a
testable deliverable, and each follows the same cycle:

> **write the failing test → run it, see it fail → implement → run it, see it
> pass → commit**

Steps below describe *what* each task builds and *what its tests pin*. The
implementer writes the source against the interfaces named in **Produces**.

---

### Task 1 — Repository Bootstrap & Toolchain

**Files:** `go.mod`, `Makefile`, `Dockerfile`, `docker-compose.yml`,
`.golangci.yml`, `.github/workflows/ci.yml`, `LICENSE`, `README.md`,
`config/common.yaml`, `config/instance.yaml.example`, `docs/adr/`

**Produces:** `make build|test|lint|sqlc|migrate`; a CI pipeline.

- Directory skeleton per [File Structure](#file-structure); `.cache/` and
  `config/instance.yaml` gitignored (derived and deployment-local).
- `.golangci.yml`: `gosec`, `errcheck`, `govet`, `revive`, `staticcheck`.
- Multi-stage `Dockerfile`: dependencies layer first; `-trimpath` so two
  machines building one commit produce the same bytes; copy `schemas/`,
  `migrations/`, `config/`. **Do not** bake `beckn.yaml` — it is fetched at boot
  (`VALIDATION_SPEC_URL`), and a baked copy is a second source of truth that
  ages with the image. Air-gapped deploys mount a cache file at
  `VALIDATION_SPEC_CACHE_PATH`.
- CI jobs: `lint`, `test`, `security` (`govulncheck` + Trivy, fail on
  HIGH/CRITICAL — T4).
- Test targets pin `EMBEDDING_PROVIDER=hashing` rather than inheriting it.
  Production defaults to `noop` (A5), so without the pin the entire semantic
  path — query embedding, HNSW, RRF, the dimension guard, the degradation
  report — would go untested from the day semantic search was deferred.
- ADRs 0001–0015: **0001–0011** are the Technology Decisions table (D1–D11),
  **0012** (which interfaces are promises) and **0013** (protocol version
  coexistence) come from T5, **0014** is the seam-ships-with-a-test rule
  applied to `CatalogReplicator`/`Keyring` (A7), and **0015** is the
  master-catalog-and-inheritance rejection (A1).

**Test:** `make build && make lint && make test` on a clean checkout.

---

### Task 2 — Configuration & Feature Flags

**Files:** `src/platform/config/config.go`; Modify: `config/common.yaml`

**Produces:** `config.Config`, `config.Load() (Config, error)`, `config.Defaults()`

Four layers, lowest precedence first (T1):

```pseudo
Load():
    cfg ← zero Config                     # envDefault tags supply the floor
    overlay(cfg, "config/common.yaml")    # reviewed repo defaults
    overlay(cfg, "config/instance.yaml")  # deployment overrides, optional
    env.Parse(&cfg)                       # secrets, on top, always wins
    validate(cfg)                         # fail startup, never warn-and-continue
```

- YAML keys match `Config` fields case-insensitively. **A key matching no field
  fails startup** — a typo must not silently do nothing.
- Secrets (`DATABASE_URL` above all) never appear in either file. That is why
  the environment sits on top (TRD §8).
- **A blank is not a value, at any layer.** `env.Parse` reads a present-but-blank
  variable as absent, so applying one would erase the reviewed value it
  overwrote and put the `envDefault` tag back in its place — neither the file's
  answer nor the operator's. A blank variable is therefore **ignored**, which is
  what `value: ""` in a pod spec and a blank key arriving through `envFrom` mean
  in practice; a blank value in either YAML file is a deliberate keystroke and
  **fails startup naming the key** instead. One rule, two enforcements, and
  refusing costs only a spelling indistinguishable from omitting the key — which
  is already how a layer defers to the one below. **An explicit empty sequence
  is not a blank**: `key: ""` reads as both "no value" and "the empty string"
  and so is refused, but `[]` has one meaning, and for a slice field empty is a
  value rather than an absence — `Replication.Targets` spells the no-op
  replicator that way. It is also the only way any layer can clear what a lower
  one set, since a blank cannot; without it a deployment that must replicate to
  nothing would have no spelling for that once the reviewed layer named a
  target. A slice field therefore carries no `envDefault`, which would come back
  in place of the cleared value.
- Groups: `App` (`Network`, `DefaultTimezone` = `Asia/Kolkata`, validated by
  `time.LoadLocation` at startup so a typo fails the boot rather than silently
  shifting every daily window), `Server`, `Database`, `Search` (`DefaultPageSize`,
  `MaxPageSize`, `MaxRadiusMeters` = 200000, `ReadDeadline`,
  `FailOnUnavailableMode` = false, `MaxCandidatesPerMode` = 500), `Embeddings` (one
  struct — A3), `RateLimit` (`RPS`, `Burst`), `Log`, `Validation`, `Auth`,
  `OTel`, `Replication` (A7), `Errors` (`IncludeLegacyType` = false — C1),
  `Ext` (`AllowNetworkFetch` = false — the SSRF boundary under Task 10), `Geo`
  (`ResolutionCells` = 8).

Three of those `Search` names used to be guessable only from this table.
`DefaultPageSize` and `MaxPageSize` clamp the request's `limit`: they bound a
**page**. `MaxCandidatesPerMode` bounds how many ids **one retrieval mode** may
return into fusion — a different and much larger number, and the old `MaxLimit`
sat next to it saying only "max". `FailOnUnavailableMode` says what happens when
a requested mode is missing, a `400` rather than a degraded header, where
`StrictModes` said only that something somewhere was strict.

**The last three groups are defined here because groups are defined only here.**
`Errors.IncludeLegacyType` is read by Task 5, `Ext.AllowNetworkFetch` by Task 10
and `Geo.ResolutionCells` by Task 12; a task that only *reads* a knob must not
have to reopen `config.go` to invent the group it lives in, so all three arrive
with the rest of the schema. Each gets its own group rather than a home under an
existing one for the reason the `Search` names were renamed — an
`AllowNetworkFetch` sitting beside `Validation.SpecURL` would read as though it
gated the L1 spec fetch, which always happens, when what it actually forbids is
dereferencing a `@context` URL that arrived in a request body. `ResolutionCells`
appears in the Constants table but is marked *config, not a constant*, so 8 is a
default here and not a `const`; `validate` rejects anything outside H3's 0–15,
since an out-of-range resolution would otherwise fail deep inside a cover rather
than at startup.

**`Database.MaxConns` is sized by the concurrency model, not guessed.** Discover
runs its retrieval modes concurrently (A2), so ONE in-flight discover holds as
many connections as it has enabled modes — two in Phase 1, three once semantic
lands. The floor is therefore

    MaxConns ≥ (enabled modes) × (expected in-flight discovers)

bounded above by the server's own `max_connections` minus whatever else shares
that server. Scenario 25's 16 concurrent discovers want 32 connections in Phase
1 and 48 in Phase 2 — **amended by A16: measured, the floor is `(modes + 1) x
in-flight`, so 48 in Phase 1 and 64 in Phase 2, with `MinConns` equal to it**; `pgxpool`'s default is `max(4, numCPU)`, so on an 8-core
box 24 of those goroutines would queue in `pool.Acquire()` and the p95 the
scenario asserts would be measuring the queue rather than the query. `MinConns`
stays small — it is a warm-start knob, and pinning twenty idle backends costs
the server memory to save a connection handshake.

**Tests pin:** layer precedence in both directions; an unknown YAML key fails;
a missing `instance.yaml` is not an error; `validate` rejects
`MaxPageSize < DefaultPageSize`, and rejects `MaxCandidatesPerMode < MaxPageSize` — a
candidate pool smaller than one page cannot fill it, and since the pool is also
the reachable pagination depth, that ratio is how many pages deep a caller may
go; an unloadable `DefaultTimezone` fails startup; a blank environment variable
leaves the layer below standing, a blank value in a file fails startup naming the
key, an explicit empty list clears what the layer below set, and a list where
the field takes a value fails startup — empty or not, since empty would be that
same erasure under a different spelling; `IncludeLegacyType` and
`AllowNetworkFetch` default to `false` and `ResolutionCells` to 8, since all
three are security- or output-shaping defaults that must not drift silently.

---

### Task 3 — Structured Logging

**Files:** `src/platform/logger/logger.go`, `.golangci.yml`

**Produces:** `logger.New(cfg.Log)`, `logger.NewContext(ctx, log)`,
`logger.FromContext(ctx)`, `logger.With(ctx, fields...)`, and the four field
constructors `RequestID`, `TransactionID`, `MessageID`, `Action` — the field
names are spelled there and nowhere else, because one key spelled two ways is
two fields to whatever queries the logs.

- zap production JSON. Request-scoped logger carried in `context.Context`,
  pre-populated with `request_id`, `transaction_id`, `message_id`, `action`.
  **This task builds the field constructors; two later tasks fill them** —
  `request_id` in `RequestID` (Task 8), the other three in `Envelope` (Task 7),
  which is the first point at which they are known. Naming both here because a
  constructor with no caller is a promise the plan makes and no task keeps, and
  the four fields are the whole reason a log line is searchable.
  `NewContext` installs it; `With` derives a context whose logger carries more
  fields, so a middleware adds to what the one above it set rather than
  replacing it, and a sibling request cannot inherit them.
- `New` takes `cfg.Log`, not the whole `Config`. `Database.URL` carries a
  password, and the one component whose job is writing things down should not be
  handed it.
- `LOG_LEVEL` is the single constrained field `validate` does not check. The
  level table belongs to zap and a copy in config would be a second one to keep
  true; a level nobody can spell fails the boot in `New`, naming the value. Do
  not "fix" this by adding a list that can drift.
- `FromContext` on a bare context returns a no-op logger, never nil.

**Tests pin:** fields survive context propagation; `With` leaves the parent
context unchanged; a level that will not parse fails the boot and names it;
sampling is off, since every request line shares one message and production
sampling would drop most of the request log at exactly the load worth reading it
at. No `Sugar()`, `zap.Any`, `zap.S()` or `zap.L()` anywhere — a lint rule,
asserted by `make lint`, with no carve-out for `_test.go`.

---

### Task 4 — Beckn Wire Types & Envelope Parsing

**Files:** `src/beckn/types.go`, `actions.go`, `errors.go`,
`src/platform/httpx/envelope.go`, `tests/testdata/beckn-v2.0.0.yaml`

**Produces:** `beckn.Context`, `Catalog`, `Resource`, `Offer`,
`GeoJSONGeometry`, `CatalogPublishAction`, `PublishDirective`,
`CatalogOnPublishAction`, `DiscoverAction`, `Intent`, `SpatialConstraint`,
`Targets`, `OnDiscoverAction`, `httpx.ParseEnvelope[T]`

- Shapes are as printed in [Publish](#publish--how-it-works) and
  [Discover](#discover--how-it-works).
- `Targets` unmarshals **both** the scalar and the array form — `beckn.yaml`
  declares a `oneOf` and real senders use both.
- `beckn.Context` carries `SchemaContext []string`. It is a Context field in the
  spec; the reference implementation moved it to `message.intent`, which
  `Intent`'s `additionalProperties: false` forbids. This plan follows the spec.
- `Attributes` requires scalar `@context` and `@type` (C4). The struct types
  them as `string`, so an array payload fails L1 rather than silently taking
  element zero.
- **`OnDiscoverAction` is exactly `{Catalogs []Catalog}` and must not grow a
  `Degraded` field.** The v2.0.0 schema declares `additionalProperties: false`
  with `catalogs` as its only property, so an extra key there is not an
  extension — `omitempty` would hide it on the ordinary path and ship an
  invalid response on precisely the path that matters. The degraded list
  travels as the `X-Beckn-Degraded` header (C11).
- **Struct↔schema conformance test.** A test walks the pinned
  `tests/testdata/beckn-v2.0.0.yaml` and asserts every wire struct's JSON tags
  match the schema's properties, with an explicit allowlist for the documented
  deviations — C1, C4 and C5, and nothing else. `Degraded` is deliberately not
  on that allowlist: it is not in the body at all. Without this test, `beckn/`
  drifts from the spec silently between spec bumps.

**Tests pin:** scalar and array `targets` both parse; an unknown field does not
fail parsing (the spec allows it); the conformance walk passes with exactly the
allowlisted deviations and no others.

---

### Task 5 — Error Model & Response Writer

**Files:** `src/platform/errors/app_errors.go`, `beckn_error.go`,
`src/platform/httpx/response_writer.go`

**Produces:** `errors.AppError`, one constructor per code family
(`CTX_`, `AUT_`, `SCH_`, `NET_`, `BIZ_`, `POL_`), `httpx.WriteJSON`,
`httpx.WriteNack`

- C1: body stays spec-conformant; `error_type` goes on the
  `X-Beckn-Error-Type` header and in every log line.
- C7: `details` is a closed object — `path` and `cause`, nothing else. A NACK
  carrying several faults is therefore **one chain**, not one error with a list
  bolted onto it: the first fault is the `Error`, and each remaining fault
  becomes the `details.cause` of the one before. `details.path` is a JSONPath.
  The publish path never uses the chain — `CatalogProcessingResult.errors` is
  already an array, and a second encoding of "many faults" is a second thing to
  keep right.
- **The single writer.** A second place that serialises a NACK is a review
  rejection.
- A4: `AUT_RATE_LIMITED` → `429` + `Retry-After`.
- **Settled: the enum is closed at Level 1 and open at Level 2, so the six
  invented codes are mapped onto members that exist.** This section was written
  assuming `SPT_` and `DOM_` are families the spec admits. Verified against the
  pinned fixture, neither is: `ErrorCode` has 76 members across exactly `CTX_`,
  `NET_`, `AUT_`, `SCH_`, `POL_` and `BIZ_`. `SPT_` appears nowhere in the
  document; `DOM_` appears only in the prose of `Error.code`'s own description,
  with zero enum members behind it.

  Nothing fails loudly either way — `Error.code` is `type: string`, **not**
  `$ref: ErrorCode`, so L1 will not reject an invented code and Task 4's
  conformance walk compares property names, not values. What decides it is the
  one normative sentence in `Error`'s own description, and it distinguishes the
  two levels: *"The topmost (Level 1) Error in any payload MUST carry a code
  from the canonical Beckn error code enum. The self-referencing details.cause
  property (Level 2) MAY carry domain-specific or non-canonical error codes
  from downstream systems."* A `MUST` in the document that `beckn.yaml` could
  not express in JSON Schema is still a `MUST`, and this plan's own rule is
  that the schema wins.

  So: **every code this service mints is an enum member**, and the six that
  were not become the nearest member that is, with the precision they carried
  moving into `message` and `details.path` — which is where a human reads it
  anyway. The chain is where the openness lives: a `details.cause` arriving
  from downstream is Level 2 and is passed through verbatim, `DOM_` prefix and
  all, which is exactly the case C1's `DOM_`→DOMAIN row exists for.

  | Invented | Becomes | Why that member |
  |---|---|---|
  | `CTX_INVALID_ENVELOPE` | `SCH_INVALID_JSON` | Every way `Envelope` fails — empty body, `null`, an array, trailing content, a decode error — is "this is not a readable JSON object", which is the member's whole meaning. The fault is below the context, not in it: there is no `context` yet to be invalid |
  | `CTX_UNKNOWN_ACTION` | `CTX_ACTION_MISMATCH` | The only action-specific member, and an action this service indexes no schema for *is* a mismatch between what the envelope declares and what the receiver serves |
  | `SCH_DUPLICATE_CATALOG_ID` | `SCH_SCHEMA_VALIDATION_FAILED` | Stays in `SCH_` for the reason the publish flow already gives — the fault is in the shape of the request, not in what it asks for. No member names duplication, so the generic one carries it and the message names the id |
  | `SCH_INVALID_GEOMETRY` | `SCH_INVALID_FORMAT` | An unreadable geometry is a value whose format is wrong, which is the member's meaning exactly. `details.path` still points at the geometry |
  | `SPT_UNSUPPORTED_OPERATOR` | `SCH_TYPE_NOT_SUPPORTED` | `S_TOUCHES` and `S_CROSSES` are valid `beckn.yaml` enum values, so `SCH_INVALID_ENUM` would be a lie — the value is fine and the receiver refuses it. That is the same species of refusal as A1's `MASTER`, which already spends this member |
  | `BIZ_CAPABILITY_UNAVAILABLE` | `NET_CATALOG_SOURCE_UNAVAILABLE` | A retrieval mode is a source of catalogs, and it is unavailable. `NET_` also attributes the fault correctly: the caller's request is valid and the identical request succeeds on a deployment that configured the mode, so this is the deployment's gap and `error_type` should read `SYSTEM`, not `DOMAIN` |

  **`beckn.ErrorCode` is a named type with a constant per code this service
  mints, and a test asserts every one of them is a member of the fixture's
  enum.** That is what keeps the decision true after this paragraph is
  forgotten: the `MUST` the schema could not encode is encoded here instead.
  `DOM_` keeps its `error_type` row and gets no constructor — this service
  never mints one, it only relays one.

**Tests pin:** every code prefix maps to the right `error_type`, `DOM_`
included, and every one of the fixture's 76 enum members lands on a type rather
than on a blank header; every `beckn.ErrorCode` constant is a member of that
enum; three faults produce a `cause` chain three levels deep and lose none; the
serialised NACK validates against the spec's own `Error` schema — the assertion
a list in `details` would have failed; `ERROR_INCLUDE_LEGACY_TYPE=true`
re-injects `type` and `false` omits it.

---

### Task 6 — Ed25519 Signature Primitives  *(PARKED — do not implement)*

> **Parked.** Originally *deferred, but built*: ship the primitive now, wire it in
> Phase 2. That is no longer the plan — nothing below is implemented today, and
> the task is kept only so Phase 2 restarts from a written design rather than
> from scratch. Building it early buys a primitive with no caller, and a
> primitive with no caller is one whose first real caller discovers what it got
> wrong. See the **Deferred** table for what ships instead, including the boot
> refusal that keeps the flag from lying.
>
> **The next task to implement is Task 7, `Envelope` half only.**

**Files:** `src/platform/crypto/signature/signature.go`, `verifier.go`,
`src/platform/registry/keyring.go`

**Produces:** `signature.Digest`, `BuildSigningString`, `Sign`, `Verify`,
`registry.Keyring`

- Beckn HTTP Signature: BLAKE2b-512 digest, `keyId` of
  `subscriber_id|unique_key_id|algorithm`, the `(created)(expires)digest`
  signing base.
- `Verify` owns the rules: skew window, expiry, unknown `keyId`.
- `Keyring` is the seam to the registry; Phase 1 ships a static env-backed
  implementation.

**Tests pin:** a signature this code produces verifies; a tampered body fails;
a clock outside the skew window fails; an unknown `keyId` fails distinguishably
from a bad signature.

---

### Task 7 — Signature & Envelope Middleware  *(`Envelope` only)*

> **Scope today: `Envelope`.** The `Signature` half is parked with Task 6 — it
> needs the `Keyring` that task no longer builds. Do not create `signature.go`,
> and do not stub it: an empty middleware mounted in the chain is
> indistinguishable from a working one at every call site that matters.

**Files:** `src/platform/middlewares/envelope.go` ~~`signature.go`~~ *(parked)*

**Produces:** `middlewares.Envelope(cfg config.Errors, maxBodyBytes int64)`.
~~`middlewares.Signature(keyring, cfg)`~~ *(parked)*

```pseudo
Envelope(next):
    body ← read and buffer, ceiling SERVER_MAX_REQUEST_BODY_BYTES
    if over the ceiling: NACK POL_NP_CAPACITY_EXCEEDED at 413   # C14
    envelope ← parse {context, message}
    if parse fails: NACK SCH_INVALID_JSON
    stash envelope + raw body in ctx; restore r.Body for downstream
    add transaction_id, message_id, action to the request-scoped logger
    record the same three for RequestLogger's completion line
    next
```

**The ceiling is this task's, not Task 20's (C14).** `Envelope` is the only
thing in the service that reads a request body, and it runs *before*
`RateLimit` — so an unauthenticated caller's bytes are buffered by this
middleware and by nothing else, and a bound placed on the server Task 20 builds
would arrive fourteen tasks after the allocation it was meant to prevent. The
two faults stay distinct: over the ceiling the body may be perfectly
well-formed, and reporting `SCH_INVALID_JSON` would send the caller to inspect
a document that is fine. The bytes read before the refusal are still handed to
the salvage, so a message id at the front of an oversized body is still echoed.

**`Envelope` is where three of Task 3's four log fields get on** —
`transaction_id`, `message_id` and `action`, added with `logger.With` because
this is the first point in the chain at which they are known. Without them a
`request_id` correlates this service's own lines to each other and to nothing
else; the transaction is what joins them to the caller's logs and to the other
hops, which is the join an operator starts a debugging session from. A
correlator the envelope did not carry is **left off, not logged empty** —
`transactionId` is optional, and a field that is blank on every request omitting
it is a field nothing can filter by.

**The same three travel back *up* to `RequestLogger`.** That is the one piece of
request state that cannot ride a derived context: `RequestLogger` sits above
`Envelope`, so it has already run by the time the transaction is known, and it
writes the one line per request that carries the status and the latency. Left
alone, a transaction id would reach every line about a request except the one
that says how it ended, and "how long did transaction X take" would be
unanswerable from the logs. `RequestLogger` therefore allocates a per-request
`correlation` (`src/platform/middlewares/correlation.go`), puts a pointer to it
in the context, and reads it back after the handler returns; `Envelope` fills it
in passing. **Unsynchronised on purpose**: the write is in `Envelope`, the read
is after everything below `Envelope` has returned, both on net/http's goroutine
for that request. The type and its context key are unexported, so no handler can
reach it and no spawned goroutine can race it. A middleware mounted with no
`RequestLogger` above it finds nothing and records nothing — `record` tolerates
a nil receiver rather than making every caller ask first.

**Parked, for Phase 2:** `Signature` no-ops when
`AUTH_ENABLE_SIGNATURE_VERIFICATION=false`, which is the Phase 1 default, and
is mounted regardless. Neither happens today — the flag's only behaviour in
Phase 1 is that `true` refuses to boot (scenario 7), which is the check that
replaces mounting an inert middleware as the thing making the deferral honest.

**Tests pin:** a handler below `Envelope` logs `transaction_id`, `message_id`
and `action` off the parsed envelope, and an absent `transactionId` leaves the
field **absent** rather than empty; mounted under `RequestLogger`, the
completion line carries the same three, and a body that never parsed still
produces a completion line — with no correlators on it, since there were none to
learn. The body is re-readable downstream after `Envelope` has consumed
it; a malformed body NACKs `SCH_INVALID_JSON`; the message id reaches the NACK
**echoed verbatim, before it is judged** (C13) — including a value that is not
a uuid, and empty only when the body yielded nothing to echo; a body **truncated
after the id still echoes it**, which is the shape the salvage exists for and
the one a whole-value decode cannot serve; a `messageId` at any other path is
**not** echoed; the ceiling is exact at the boundary — a body of exactly
`maxBodyBytes` passes and one byte more is `413` / `POL_NP_CAPACITY_EXCEEDED`
(C14) — and an oversized body is **refused without being buffered**, asserted by
counting the bytes the request reader was actually asked for rather than by
observing the status, because a limit enforced after buffering produces the same
status and none of the protection.

*(Parked with `Signature`: the pin that a signature-failure NACK carries the
message id, which is the original reason `Envelope` precedes `Signature` in the
chain. That ordering constraint stands for Phase 2 and should not be relitigated
then — the reason it exists is recorded here.)*

---

### Task 8 — Request Logger & Rate Limit Middleware

**Files:** `src/platform/middlewares/request_id.go`, `request_logger.go`,
`recover.go`, `ratelimit.go`, `trace.go`

**Produces:** `middlewares.RequestID(log *zap.Logger)`, `Recover`,
`RequestLogger`, `RateLimit(cfg config.RateLimit, errs config.Errors)`, `Trace`

- `RequestID` is first in the chain because it is what makes everything below
  it loggable: it mints an id, derives the request-scoped logger with
  `logger.NewContext(ctx, log.With(logger.RequestID(id)))`, and echoes the id
  as `X-Request-Id`. Until it has run, `logger.FromContext` returns the no-op
  logger, so `httpx.WriteNack`'s one log line goes nowhere — which is why the
  chain starts here rather than at `Trace`. It **mints rather than trusts**: an
  inbound `X-Request-Id` on a Phase 1 endpoint is a value an unauthenticated
  caller chose, and honouring it lets that caller collide two requests' log
  lines or put control characters in a log field. Propagating a gateway's id is
  a Phase 2 decision, and it needs a trusted-proxy list before it is one.
- `RequestLogger` starts its timer before auth, writes `X-Response-Time`, and
  logs one completion line with status, duration and `error_type`. The header
  is the awkward half: a header set after the handler has written is a header
  that never reaches the wire, so the elapsed time is stamped inside the
  `ResponseWriter` wrapper's own `WriteHeader` — the last moment it can still
  be added — and that wrapper is also what captures the status the line
  reports. `error_type` is read back off `X-Beckn-Error-Type`, which
  `httpx.WriteNack` has already set (C1); deriving it a second time here would
  be a second place that decides a fault's category, and having exactly one is
  the whole of C1. The line is written from a **`defer`**, not after
  `next.ServeHTTP` returns: `Recover` re-panics by design on the committed-
  response path, and a request that ended by having its connection dropped is
  not one to leave unaccounted for.
- `Recover` turns a panic into a 500 and **never leaks a stack trace** to the
  caller; the trace goes to the log. It sits *below* `RequestLogger` (A11) so
  that 500 is written through the response wrapper and lands in the completion
  line. When the response is already committed there is no second response to
  write: `Recover` asks the writer below it — a small `committer` interface the
  wrapper satisfies — and on a yes it logs the fault once and re-panics with
  `http.ErrAbortHandler`, which is how `net/http` is told to drop the connection
  without printing a stack of its own. Writing anyway would append a NACK
  document to a half-written body under whatever status was already claimed: a
  200 carrying two documents, neither valid, and a caller with no way to tell.
  A dropped connection is the honest answer, because a truncated body served as
  a clean 200 is the failure the caller cannot detect.
- `RateLimit` (A4): per-caller token bucket, answering `429` + `Retry-After` +
  `AUT_RATE_LIMITED` through `apperrors.RateLimited` and `httpx.WriteNack` —
  which is why it takes `config.Errors` beside its own knobs, like every other
  middleware that rejects. It sits below `Envelope`, so the `messageId` it
  echoes is the parsed one rather than C13's salvage. Evicts idle buckets so
  the map is not a leak.
- **The bucket is keyed on the remote address, not the subscriber id.** This is
  the one place this task departs from A4 as written, and it departs because
  signature verification is parked: `context.bapId` on an unverified request is
  a string the caller chose. Keying on it would let any caller shed its own
  limit by rotating the field, and — the worse half — exhaust a *named third
  party's* bucket by claiming their id, which turns the protection into the
  attack. Subscriber-id keying is in **Deferred**, tied to the task that
  verifies the signature, because that is the point at which the id stops being
  a claim and becomes an identity. A deployment behind a proxy must hand the
  service the real peer address: `X-Forwarded-For` is not read, for the same
  reason `X-Request-Id` is not.
- `Trace` is a **no-op pass-through here**, but not side-effect-free: before
  calling the next handler it appends `trace` to a response header,
  `X-Beckn-Chain`, purely so Task 20's chain-order test has something to
  observe at `Trace`'s slot — a pass-through with no marker would be the one
  link in the chain no order test could place. **`Recover` appends `recover`
  to the same header**, also before calling next, and the pair is what makes
  the order *testable* rather than merely *visible*: `Header().Add` preserves
  insertion order, so `Values("X-Beckn-Chain")` reads back as the order the
  two links actually ran. A single presence marker could not do this. Both
  middlewares run before anything calls `WriteHeader` — `Recover` writes the
  500 only after catching — so a lone presence marker survives a recovered
  panic under **either** nesting, and an assertion that it is present passes
  whichever way round the two are mounted. Task 23 replaces the
  inside of `Trace` with `otelhttp` and drops its `trace` entry; `Trace`'s
  exported signature does not change, so nothing built against the chain in
  Task 20 needs to change when Task 23 lands.

**Tests pin:** `RequestID` mints an id, echoes it as `X-Request-Id`, ignores an
inbound one, and leaves a logger below it that carries `request_id` — pinned by
observing a `WriteNack` from below land on it, since a request id in a field
nothing writes to is not an id anyone can search. A panic below `Recover`
yields 500 with no stack in the body, and — mounted under `RequestLogger`, the
way Task 20 wires it — that 500 still carries `X-Response-Time` and still
produces one completion line reporting `status = 500`; a panic raised *after*
the handler has written leaves the half-written body untouched and re-panics
with `http.ErrAbortHandler`, logging the fault exactly once. `X-Response-Time` is present on a
response the handler wrote itself, which is exactly the case a header stamped
after `WriteHeader` would miss, and the completion line carries the status and,
on a rejection, the `error_type` the header named — plus, when `Envelope` is
mounted below, the three correlators it recorded (Task 7). Burst+1 requests from one
address yields `429` with `Retry-After` and `AUT_RATE_LIMITED`; two addresses
do not share a bucket; a bucket idle past its horizon is evicted. `Trace`
passes the request through unmodified except for its `X-Beckn-Chain: trace`
entry, and `Recover` adds `recover` on every request and not only on the ones
it catches (no other behaviour to test yet — Task 23 pins the real span).

---

### Task 9 — L1 Schema Validation

**Files:** `src/platform/validation/spec_index.go`, `schema_validator.go`,
`envelope_rules.go`, `src/platform/middlewares/schema_validator.go`

**Produces:** `validation.SpecIndex`, `validation.ValidateEnvelope` (C6),
`validation.L1`

```pseudo
boot:
    spec ← fetch VALIDATION_SPEC_URL, cache to VALIDATION_SPEC_CACHE_PATH
           on failure, fall back to the cache; if neither, fail to start
    index ← map action → request schema        # C2: keyed by context.action,
                                               # not by URL. One route, but both
                                               # action spellings resolve here

request:
    ValidateEnvelope(ctx)      # C6 — runs even when L1 is disabled, because a
                               # response context cannot be built without it
    if L1 enabled:
        schema ← index[ctx.action]  else NACK CTX_ACTION_MISMATCH
        faults ← validate whole envelope against schema
        if faults: NACK 400, faults chained through details.cause  # C7
```

Action-indexing is what makes T5's version coexistence cheap later: a second
`SpecIndex` for a second version is additive.

**Envelope rules read the message id before they judge it (C13).** Whatever
`context.messageId` holds is lifted out and handed to `WriteNack` as-is, and
only then is it validated — because the NACK that reports a malformed message
id is the one NACK the caller cannot correlate any other way. A rejection for
`"messageId": "not-a-uuid"` therefore echoes `not-a-uuid`. Extraction is
best-effort by construction: it runs against an envelope already known to be
invalid, so it yields empty rather than failing, and empty is what a body too
broken to parse produces.

**Tests pin:** a missing `transactionId` is rejected with L1 **off**; an unknown
action NACKs rather than 500s; a fetch failure falls back to cache; a
multi-fault envelope produces a `cause` chain that preserves every path; a
rejected non-uuid `messageId` comes back echoed in the NACK rather than blanked
(C13), and an unparseable body echoes empty rather than a minted uuid.

---

### Task 10 — L2 Extended Schema Validation

**Files:** `src/platform/validation/schema_source.go`, `schema_cache.go`,
`extended_validator.go`, `schemas/<TypeName>/attributes.yaml`

**Produces:** `validation.SchemaSource` (directory + HTTP registry),
`validation.L2`

- **C4: `@context` and `@type` are scalar strings, and both are REQUIRED.** An
  array for either is a `400` from here — not a first-element pick, not a
  normalisation pass. `Attributes` declares them `string`, and quietly widening
  that lets two publishers disagree about the shape of the field discover
  filters on, which surfaces as a schema query that matches one of them.
- T3: schemas load through `SchemaSource`, refresh on a timer, and swap behind
  `atomic.Pointer` so an in-flight request never sees a half-loaded set.
- **SSRF boundary, unchanged:** the registry URL is *configured by an operator*.
  A URL that arrived in a request body is never fetched
  (`EXT_ALLOW_NETWORK_FETCH=false`).
- Unknown `@type` → pass (open-world JSON-LD), not reject.

**Tests pin:** both `@context` forms validate; a refresh swaps atomically under
concurrent reads; a `@context` URL in a payload is never fetched; a known type
with a bad attribute is rejected with a pointer into `resourceAttributes`.

---

### Task 11 — Domain Model & The DB-Agnostic Boundary

**Files:** `src/domain/catalog.go`, `query.go`, `validity.go`, `mergepatch.go`,
`errors.go`, `catalog_repository.go`, `search_repository.go`, `retrieval.go`,
`purity_test.go`, `src/storage/memory/repository.go`, `src/storage/conformance/`

**Produces:**

```pseudo
Catalog{ID, NetworkID, Provider, ValidFrom, ValidTo,
        ValidTimeFrom, ValidTimeTo *TimeOfDay,     # nil = no daily window
        VisibleTo []string, Active, Resources, Offers,
        Geometries []Geometry}
    # Geometries here are the PROVIDER's locations. They belong to the catalog,
    # not to any one resource, and are stored once with a NULL resource_id.
    # NetworkID is the publisher's network, used only to default an empty
    # VisibleTo. It is not stored — nothing reads it back.

Resource{ID, CatalogID, Name, Descriptor, Attributes,
         SchemaContext, SchemaType string, Geometries []Geometry,
         SearchText string, Embedding []float32,
         EmbeddingSourceHash []byte,               # blake2b-256 of SearchText
         VisibleTo []string, Active, ValidFrom, ValidTo,
         ValidTimeFrom, ValidTimeTo *TimeOfDay}    # derived from the catalog
    # The last line is the scope gate, copied down from the catalog. Discover
    # reads it here and never joins `catalogs`. `Geometries` holds the finds
    # the walker made INSIDE this resource; the catalog's own finds live on
    # Catalog.Geometries and are shared by every resource in it.
    # SearchText is an insert PARAMETER, not a stored column — only the
    # tsvector built from it is kept. EmbeddingSourceHash is the A5 re-embed
    # decision, and it is a field of the DOMAIN rather than a detail of the
    # store because `derive` compares it and then writes it (see Publish). A
    # struct that omitted it would force the repository to carry that
    # comparison — the one thing the derive seam exists to keep out of it.

CatalogPatch{ID, NetworkID string,
             Provider  json.RawMessage,     # nil = absent, `null` = delete
             Validity  *TimePeriodPatch,    # nil = absent
             Active    bool,                # NOT a pointer — defaulted (A9)
             VisibleTo []string,            # NOT nilable — defaulted (A9)
             Resources []ResourcePatch, Offers []OfferPatch}
    # What MapCatalog returns (A8). The fields with no declared default carry
    # ABSENT as a distinct state, which is the entire reason this type exists
    # rather than reusing Catalog: encoding/json gives nil for a key that was
    # not sent and a non-nil zero for one that was, and MERGE turns that
    # distinction into the difference between keeping a publisher's data and
    # deleting it.
    #
    # Active and VisibleTo are deliberately NOT pointers (A9). Active's default
    # is resolved here, in the mapper; VisibleTo arrives already resolved from
    # publishOne's applyDirectiveDefaults, and the mapper only copies it.
    # Either way, by the time the merge runs there is no absence left to
    # represent — and a pointer that can never be nil is an invitation to
    # write the branch that makes it nil.

ResourcePatch{ID string,
              Descriptor json.RawMessage,     # nil = absent, `null` = delete
              Attributes json.RawMessage}     # nil = absent, `null` = delete
    # The wire `Resource` is exactly {id, descriptor, resourceAttributes} (C5)
    # and only `id` is required, so every field but the id preserves absence and
    # NONE of them has a declared default. This is the pure A8 half, with no A9
    # half at all — which is why it is three fields and not eight.
    #
    # No gate columns and no SchemaContext/SchemaType, though `Resource` has
    # both. The gate is copied down from the merged CATALOG, and the schema pair
    # is read out of the merged Attributes — both after the merge, by `derive`.
    # A patch carrying either would be a second place they could disagree with
    # the document they describe.
    #
    # ID is the merge KEY, not a patchable field: `resources` merges by id (A8),
    # so a ResourcePatch naming no stored resource is an insert and one naming a
    # stored resource is a patch against it. There is no delete — under MERGE
    # `null` deletes a key, never a row.

OfferPatch{ID string,
           Document    json.RawMessage,       # nil = absent, `null` = delete
           ResourceIDs []string,              # NOT nilable — defaulted (A9)
           Validity    *TimePeriodPatch}      # nil = absent
    # The same split as CatalogPatch and for the same reason. `resourceIds` has
    # a declared default of `[]` — CATALOG-WIDE, not "none" — so the mapper
    # resolves it and it cannot be absent by merge time. The offer body and
    # `validity` have no default, so they carry absence.
    # Document is the WHOLE verbatim offer, merged by RFC 7396 against the
    # stored one, which is why it is a RawMessage and not a parsed shape: the
    # `offer` JSONB column keeps what the publisher sent.

TimePeriodPatch{StartDate, EndDate Nullable[time.Time],
                StartTime, EndTime Nullable[TimeOfDay]}
    # `validity` expands into FOUR independent columns (see Validity), so it
    # needs four independent tri-states. A `*TimePeriod` can say "no validity
    # sent" but cannot say "clear the end date and keep the start date" — a
    # patch RFC 7396 permits and two independent column pairs make meaningful.

Nullable[T]{Value T, Set, Null bool}
    # Set == false   → ABSENT: keep whatever is stored.
    # Set && Null    → an explicit JSON null: clear the column.
    # Set && !Null   → Value.
    # One type rather than `**T`, which has four states of which three mean
    # anything — and the fourth is the one a reader dereferences by accident.
    # This is the only generic in the domain; it exists because absence,
    # deletion and value are three answers and Go's zero value is one.

MergePatch(target, patch json.RawMessage) json.RawMessage        # RFC 7396
MergeCatalog(stored Catalog, patch CatalogPatch) (Catalog, touched []string)
    # The two-level rule from `updateMode`: MergePatch for documents,
    # identity-keyed merge for the Resources and Offers collections. Pure
    # functions on values — no context, no storage, no clock — which is what
    # makes the exhaustive merge table a table-driven unit test rather than a
    # database fixture.
    #
    # `touched` is every resource the patch named, PLUS — for every offer the
    # patch named — the UNION of that offer's `resourceIds` BEFORE and AFTER
    # the merge. The second half is not decoration: an offer's geometry is
    # written in its resources' loop iteration, so a patch that moves a
    # shopfront and mentions no resource at all would otherwise re-derive the
    # geometry and then write it nowhere.
    #
    # The union, rather than the merged ids alone, is what makes a RELOCATION
    # correct. An offer whose `resourceIds` goes from ["r1"] to ["r2"] touches
    # only r2 under the merged reading, so r1 is never visited, its
    # `resource_geometries` row is never deleted, and a spatial search on the
    # offer path keeps returning a resource that offer no longer covers —
    # for ever, since no later publish has any reason to name r1 either. The
    # stored ids are the only record of where the geometry currently IS, and
    # deleting a row requires visiting its owner.
    #
    # A resource is touched when something that is stored ON it changed —
    # which, since offer geometry is stored on it, includes both the offers
    # that cover it now and the ones that just stopped.

Offer{ID, CatalogID, ResourceIDs []string, Document json.RawMessage,
      ValidFrom, ValidTo,
      ValidTimeFrom, ValidTimeTo *TimeOfDay}
    # An empty ResourceIDs means CATALOG-WIDE, not "none".
    # Document is the verbatim offer, for the same reason Provider and GeoJSON
    # are — it maps to the `offer` JSONB column. Named Document rather than
    # Offer because `offer.Offer` reads as a mistake at every call site.
    # There is no Descriptor or Price field: the response renders Document, and
    # a projection the storage layer does not keep is one the domain must not
    # pretend to have.

Geometry{TargetPath, SourcePath string, Owners []string,
         Type string, GeoJSON json.RawMessage}
    # Type is read out of GeoJSON on the way in and on the way back; it is a
    # field of the value, not a column of the table.
    # TargetPath is the wildcard form, byte-identical to a constraint's
    # `targets` — it is the only one a query compares against.
    # SourcePath carries concrete indices, which is what makes two geometries
    # under one wildcard distinguishable — EXCEPT the catalog's own index,
    # which is wildcarded because it is a property of the request rather than
    # of the catalog. It is positional in every other position, so it is NOT
    # stable across a republish that reorders an array — which is why
    # geometries are deleted and reinserted rather than merged.
    # Owners is empty for a catalog-level geometry, and carries one id per
    # resource that owns it — one for a geometry found inside a resource, N for
    # one found inside an offer covering N of them. `[]string` rather than
    # `*string` because the offer case is genuinely plural, and a pointer that
    # has to become a slice later is a migration of every call site. Each entry
    # becomes one `resource_geometries` row; the row itself still has the
    # single `resource_id` column, which is what the discover join needs.
    # GeoJSON is kept VERBATIM — parsing at publish time is how the reference
    # implementation loses five of seven types and every polygon hole.

TimeOfDay{Hour, Minute, Second int}     # always UTC, already normalised
    # A wall-clock instant with no date, which is what TimePeriod's
    # startTime/endTime are. It is a plain value type, NOT time.Time: a
    # time.Time would carry a date nobody supplied, and the zero value would
    # read as midnight rather than as "no window". That is why every field
    # holding one is a POINTER — nil is the absence, 00:00:00 is a real bound.
    # The domain never stores an offset: normalisation to UTC happens in the
    # publish mapper, so nothing downstream has to know a timezone.

GeoPoint{Lat, Lon}
BBox{MinLat, MaxLat, MinLon, MaxLon float64}

SpatialFilter{Op SpatialOp,
              CellsFull, CellsCover []uint64,   # nil ⇒ no cell predicate
              Bounds *BBox,                     # nil ⇒ declined (antimeridian)
              Center *GeoPoint, RadiusM float64,
              Quantifier Quantifier}
    # ONE type for all seven answered operators, because they differ only in
    # which set relation the repository applies — see the predicate in
    # [Discover](#the-spatial-predicate-as-it-appears-in-every-query). Seven
    # filter types would be seven places for the quantifier handling to drift.
    #
    # CellsFull and CellsCover are the query geometry's two covers, and they are
    # nil TOGETHER: a cover that declined (antimeridian, over budget) disables
    # the cell predicate entirely and leaves Bounds to decide. One without the
    # other is a state the repository has no branch for, because CoverQuery
    # cannot produce it.
    #
    # Center and RadiusM are populated ONLY for Point-to-Point S_DWITHIN — the
    # single case the exact haversine refinement applies to. A non-nil Center on
    # any other operator would silently narrow that operator's answer.

Quantifier ∈ {Any, All, None}
    # Not a bool. `Negate bool` held two of the three and there is no honest
    # value for ALL, which is NOT EXISTS over the negated predicate rather than
    # over the predicate. A third state added to a bool becomes a second bool,
    # and then a pair with an unrepresentable-but-constructible combination.

SearchQuery{Text, NetworkID, Schemas []SchemaFilter,
            Spatial *SpatialFilter, TargetPaths []string,
            Filters []AttributeFilter, Limit, Offset}
    # TargetPaths is the spatial constraint's `targets`, already canonicalised.
    # Empty means every geometry the resource can be found by — its own, plus
    # its catalog's.

SchemaFilter{Context string, Type string}
    # Type == "" means "any type under this context". Empty Schemas means no
    # schema predicate at all — NOT a predicate that matches nothing.

AttributeFilter{Root string, Expression string}       # Task 22
    # Root names the column the expression is rebased onto: `attributes` on a
    # resource, `offer` on an offer. Expression is PostgreSQL SQL/JSON path
    # (C10), ALREADY validated and ALREADY rebased — the store is handed an
    # expression it may cast and run, never one it must interpret.
    # A store that cannot run it must narrow NOTHING and say so in `Degraded`,
    # because a wrongly narrowed page is indistinguishable from a correctly
    # narrowed one at the caller.

SearchResult{Catalogs, Total, Degraded []string}

Scope{NetworkID string, Now time.Time}                    # A6: a value
    # One instant, captured once per request, so every mode in a concurrent
    # search agrees on "now". Postgres does not read it — its gate calls now(),
    # the transaction's own instant. It exists for the backends that have no
    # now(): the memory store, and the tests, which must be able to ask what
    # was live at 23:00 without waiting until 23:00.
    # NetworkID == "" means UNSCOPED — every backend must read it as "emit no
    # network predicate", never as a literal network id that matches nothing
    # and never by falling back to APP_NETWORK_ID. That fallback belongs to
    # publish's visibleTo default (C8), a different field answering a
    # different question; the memory backend's conformance suite runs the
    # empty-NetworkID case for exactly this reason.

Retriever{Retrieve(ctx, query, scope) → ranked ids}       # one per mode
Hydrator{ScopeFilter, Hydrate, Count}
Fault{Path, Code, Message string}
    # ONE fault type, and it lives in the domain because `UpsertCatalog` returns
    # faults ACROSS the port and this package may import neither `beckn` nor
    # `platform/errors` — `purity_test.go` fails the build if it tries.
    # Path is a JSONPath into the request, Code is the `DOM_`/`BIZ_` string.
    # The domain NAMES a fault; exactly one place turns it into wire bytes,
    # which is the DRY rule on error construction, held rather than restated.

DeriveFunc = func(merged Catalog, touched []string) []Fault
    # The post-merge seam (A8), NAMED rather than passed as an untyped
    # parameter: Task 15 must accept it and Task 18 must construct it, and the
    # two are written by different people who never read each other's task.
    # It returns faults and not `error` because an unreadable geometry is a
    # PARTIAL — the catalog still commits — so a signature returning `error`
    # would make the caller re-invent that distinction.
    # `touched` is the id set `MergeCatalog` returned, passed through, not
    # re-derived: a second computation of "which resources did the patch name"
    # is a second chance to re-embed a catalog nobody patched.

CatalogReplicator{Replicate(ctx, catalogID string) error}          # A7
    # The write fan-out seam. It takes an ID and not a catalog, so a second
    # store re-reads through `GetCatalog` and this interface never becomes a
    # second definition of what a catalog is. Phase 1 ships the no-op; a queue
    # table arrives with the second store that needs one, because a queue with
    # no consumer is the `pending_targets` column again — the debt A7 removed.

CatalogRepository{UpsertCatalog(ctx, CatalogPatch, updateMode, DeriveFunc)
                      → partial []Fault, error,
                  DeleteCatalog, GetCatalog, ListCatalogResources}
    # The port takes `DeriveFunc` as a parameter rather than the repository
    # holding an embedder, because the domain must not know that an embedder
    # exists and the repository must not own one.
SearchRepository{Search, Capabilities}
```

Helpers `PointGeometryAt(i, point)` and `PointGeometries(points…)` build the
provider-path Point geometries that fixtures need, so "somewhere" is spelled one
way across every test. They leave `Owners` empty, because a *catalog's* provider
location is catalog-level. An offer's provider location is not, and the fixtures
that need one set `Owners` to the ids that offer covers.

This task also **scaffolds, but does not finish, the memory backend**:
`storage/memory` gets a minimal in-process implementation of
`CatalogRepository` and `SearchRepository` — plain maps, no spatial or search
behaviour yet, just enough to satisfy both interfaces — plus a
`storage/conformance` package holding the fixture types (not yet the
fixtures themselves) that later tasks add cases to. This is why the seam is
named here and not deferred to whichever task happens to need it first: a
memory backend built piecemeal, one undocumented task at a time, is the
`pending_targets` mistake again — debt nobody owns because no task's Files
list says so. **Tasks 12, 15 and 16 each modify `storage/memory/repository.go`
and add fixtures to `storage/conformance/`** as they give the Postgres side
the behaviour the memory side must match; none of them create the package.

**Tests pin:** `purity_test.go` walks the package's imports and fails on
anything outside stdlib + `google/uuid`. This is the swap boundary, enforced
rather than requested. The memory backend's skeleton passes both interfaces'
method sets at compile time — no behavioural test yet, since it has no
behaviour yet.

Its twin `tests/architecture/boundary_test.go` is written **here**, in the task
that names the boundary, and walks every package in the module: an import of
`pgx`, `pgvector`, `sqlc` output or `src/storage/postgres` from anywhere but
`src/storage/postgres/**` and `src/app/container.go` fails the build. It passes
trivially today, because no adapter exists yet — which is the point. A guard
written after the thing it guards is written against code somebody already has a
reason to keep. `purity_test.go` protects the contract; this protects everything
that consumes it, which is where the leak actually happens (TRD §5, T7).

`MergePatch` gets the exhaustive table it deserves, because it is a pure
function and there is no excuse not to: key absent → kept; key present →
replaced; key `null` → deleted; nested object → recursed; nested array →
replaced whole, **not** element-merged; `null` on a key that does not exist →
no-op, not an insert of `null`; a scalar target patched with an object →
replaced. `MergeCatalog` adds the level the RFC does not cover: a patch naming
one of two resources leaves the other identical **and** returns exactly one id
in `touched`, which is what stops a one-attribute patch from re-embedding a
forty-resource catalog.

---

### Task 12 — H3 Geospatial Indexing

**Files:** `src/indexing/geo/h3.go`, `distance.go`;
Modify: `src/storage/memory/repository.go`, `src/storage/conformance/`

**Produces:** `geo.CoverGeometry(Geometry) → Cover`,
`geo.CoverQuery(geometry, op, distanceMeters) → full, cover []uint64`,
`geo.BoundsFor`, `geo.HaversineM`, `geo.MatchesOp`, `geo.NearestGeometryM`,
and the constants table in [Geospatial Design](#geospatial-design).

This package knows about cells, boxes and metres. It knows nothing about
JSONPath: the provider path is constructed by the mapper and normalised by
`jsonpath.Canonicalise`, so `geo` never names a document location. It knows
nothing about SQL either — `MatchesOp` is the **memory backend's twin** of the
`CASE spatial_op` block, and the two are written from the same table in
Geospatial Design rather than from each other.

`MatchesOp(op, aFull, aCover, qFull, qCover) → bool` implements the seven
operators as Go set operations over sorted `[]uint64`. **Sorted is a
precondition, not a convenience**: `S_EQUALS` compares the slices element-wise,
exactly as PostgreSQL's array `=` does, so the two backends agree only if both
sides sort. `CoverGeometry` and `CoverQuery` are the two places that guarantee
it.

It takes no bounding box, and the memory backend must therefore apply the box
itself — **skipping it for `S_DISJOINT`**, as the SQL does. Passing bounds into
`MatchesOp` would have hidden that asymmetry inside a function whose name
promises only the operator; leaving it out puts it in the backend, where the
SQL's own version of the decision is visible next to it. It is one function
rather than seven, and it takes the operator as a parameter rather than being
dispatched by the caller, for the reason the SQL uses one `CASE`: the
quantifier and NULL handling wrap all seven identically, and seven call sites
is seven chances for one of them to forget the oversize short-circuit.

`NearestGeometryM(center, geometries) → metres, ok` is the fold the memory
backend runs for the **Point-to-Point `S_DWITHIN` refinement only**. `ok` is
false when no geometry in the set is a Point — meaning "no refinement applies",
not "no match" — and the caller must then fall back to the cell answer rather
than treating false as a miss. That inversion is the one this design most
recently removed, so the test below asserts it directly.

Algorithms are specified in that section — the two containment modes, the
circumscribed 64-gon, `gridDisk` dilation sized from the *minimum* centre
spacing, line densification, and the budget rules that produce a nil cover
rather than a truncated one.

**Tests pin (≈28):**

- A point's cover contains its own cell; **longitude is read first** (a swap
  puts Bengaluru in Somalia and nothing rejects it).
- **The sandwich invariant**, as a property test over generated polygons:
  `cells_full ⊆ cells_cover`, always. Everything in this design rests on it, so
  it is asserted directly rather than inferred from the operators that use it.
- **A Point and a LineString produce an EMPTY `cells_full`** — the behaviour
  that looks like a bug on first reading and is the reason `S_INTERSECTS`
  against a Point is answered in the MAYBE band.
- **And a NON-EMPTY `cells_cover`, always.** Asserted as a property over every
  generated geometry of every type, because three of `MatchesOp`'s branches
  refute through `aCover ⊆ qCover` and the empty set is a subset of everything:
  an empty cover does not lose precision, it silently stops refuting. The
  companion case is a geometry whose `coordinates` is a well-formed but EMPTY
  array — `looksLikeGeoJSON` accepts the shape, so `CoverGeometry` must **fault
  it** rather than return a cover with nothing in it. Postgres carries the same
  rule as `CHECK (cells_cover IS NULL OR cardinality(cells_cover) > 0)`; this
  is the half of it that runs before a row exists.
- **`MatchesOp` degrades no operator on a degenerate `cells_full`.** The same
  fixtures as scenario 30a, run against the Go twin: a stored Point inside a
  query Polygon is `S_WITHIN` and not `S_DISJOINT`; one 400 km outside is
  neither `S_WITHIN` nor anything but `S_DISJOINT`. Phrased over `full`, as the
  table in Geospatial Design once was, every negative assertion here passes
  vacuously — which is precisely why the conformance suite could not have
  caught it: both backends would have been wrong in the same direction, and
  agreeing.
- A polygon's cover contains interior sample points; **holes are honoured**; a
  polygon smaller than one cell yields an empty `full` and a non-empty `cover`.
- A LineString's cover includes cells between vertices, not just at them.
- All seven RFC 7946 types are accepted, as **stored and as query** geometry.
- A geometry that cannot be **parsed** is refused rather than returning
  silently empty. An over-budget one is a different case: both covers come back
  nil, never truncated, and the row stays discoverable through its box.
- An antimeridian circle declines rather than wraps.
- **`MatchesOp` against the truth table**, one case per operator per relation
  (disjoint / touching / overlapping / contained / containing / equal) — 42
  assertions from one fixture pair, because each is a distinct arm.
- **`S_EQUALS` on the same cell set built in two different orders** returns
  true. It is the assertion that catches a sort dropped from either cover
  function, and the only operator that can see the difference.
- **`S_DISJOINT` against a far-away geometry** returns true — asserted in the
  memory backend *including* its bounding-box stage, since the box is where the
  operator inverts and `MatchesOp` alone would pass while the backend failed.
- **Dilation is a superset**: for a sample of points at distance `d` from a
  seed geometry, every one lands inside `dilate(cover, k)`. Sized from the
  minimum centre spacing, so this must hold where cells run small — the test
  seeds near an icosahedron vertex deliberately.
- `NearestGeometryM` over two Points returns the nearer, and over a resource
  holding only a Polygon returns `ok = false` — asserted **together with** the
  caller's fallback, since `ok = false` meaning "no match" instead of "no
  refinement" is precisely the regression this design corrected.

---

### Task 13 — Text Derivation & Embeddings

**Files:** `src/indexing/embeddings/{embedder,noop,hashing,ollama,fixture}.go`,
`src/publish/text.go`

**Produces:** `embeddings.Embedder{Embed, Dimensions}`, `deriveSearchText`

- `deriveSearchText` concatenates name, descriptor text and attribute
  **values**, stripping JSON-LD keywords and attribute **keys**. It is the one
  source of truth for what is searchable — which is why its *output* is not
  stored: `search_tsv` is built from it at insert, and the Phase 2 backfill
  calls it again over `name`, `descriptor` and `attributes` rather than reading
  a stale copy. It must therefore be **deterministic and versioned**: a change
  to it changes what matches, so it lands with a reindex, not silently.
- Providers: `noop` (default, A5 — returns nil, publish still succeeds),
  `hashing` (deterministic, CI), `ollama` (`nomic-embed-text`, 768-dim),
  `fixture` (committed vectors).
- A dimension guard rejects a vector whose length is not 768 rather than letting
  pgvector fail at insert time.

**Tests pin:** derivation strips keys and keeps values; `noop` never fails a
publish; a wrong-dimension vector is rejected with a clear error; `hashing` is
stable across runs.

---

### Task 14 — PostgreSQL Schema, Indexes & Test Harness

**Files:** `migrations/00000{1..6}_*.{up,down}.sql`, `tests/dbtest/postgres.go`

**Produces:** the schema in [Data Model](#data-model); `dbtest.NewPostgres(t)`

- Migrations are exactly the DDL printed above. Every down migration is the
  reverse of its up, dropping functions after the tables that use them.
- `dbtest.NewPostgres` starts one testcontainer per package, applies migrations,
  and hands out a pool; each test truncates rather than re-migrating.

**Tests pin:** an **index inventory test** asserting the exact set of indexes on
each table by name — an index silently dropped in a migration is the kind of
regression that shows up as a latency page, not a test failure. It asserts the
absence list too: `catalogs` carries **nothing beyond `catalogs_pkey`** — the
index PostgreSQL builds for the primary key, which is not optional and is not a
choice this plan made — and `resource_geometries` has **no bounding box
index**, for the reason spelled out in Migration 004. It no longer asserts the
absence of a path index: `idx_rg_catalog_target_path` exists, and it exists
because the walker made `target_path` a column of many distinct values rather
than the single constant it held when that absence was written down. A
well-meant re-add of the bbox index then comes back as a test failure with a
reason attached, rather than as a slow write path nobody attributes. The
absence list also names `idx_resources_catalog_id` and `idx_offers_catalog_id`,
which are **not** dropped indexes but indexes that were never justified: both
tables are keyed `(catalog_id, id)`, so the primary key's own btree already
leads with `catalog_id` and serves every prefix scan either one would have.

**The three `resource_geometries` CHECK constraints get a test each**, because a
constraint nobody has watched reject anything is a constraint that may have been
written wrong: a half-NULL cell pair, an empty `cells_cover`, and a box with
`min_lon > max_lon` are each rejected by the database. The second is the one
that matters most — it is what the `cover <@ cover` refutations in the operator
`CASE` rest on, since `'{}' <@ anything` is TRUE.

**`wildcard(source_path) = target_path`, asserted over every row** after each
publish scenario. The two columns are one string and its wildcarding, and
nothing in the schema can hold them in step; this assertion is what catches the
walker change that updates one and forgets the other. It runs alongside the
`offers.resource_ids` invariant in Migration 006, for the same reason and in the
same place.

**`geo_haversine_m` is asserted NOT to inline**, with `EXPLAIN (VERBOSE)` on a
query that calls it, because the plan claims it as a consequence of `STRICT`
over a `least()` body rather than as something anybody measured. If a later
PostgreSQL inlines it after all, this test is what reports the good news.

Two more: `EXPLAIN` tests asserting the cell predicate uses
`idx_rg_cells_cover` **and `idx_rg_cells_full`**, that the scope gate uses
`idx_resources_visible_to`, and that the schema filter uses
`idx_resources_schema`; and up-then-down-then-up leaving no residue.

**Every `EXPLAIN` assertion runs its query six times before asserting**, on one
connection, through the extended protocol. Five is where PostgreSQL may abandon
the custom plan for a generic one, and a generic plan cannot fold
`$1 IS NULL OR …` — so the index every one of these tests is looking for
disappears at exactly the execution the test never reached. The assertion is
that `plan_cache_mode = force_custom_plan` on the pool holds the plan steady;
without the sixth execution the test cannot tell that setting from its absence. Both
cell indexes are named because the operators split across them — `S_DISJOINT`
and `S_WITHIN` read `cells_full` where `S_INTERSECTS` reads `cells_cover`, so an
`EXPLAIN` asserting only the overlap case leaves half the operator set
unproven, and `<@` is the half whose selectivity the planner estimates worst.

`within_daily_window` gets a table-driven test of its own against a live
database — forward window, wrap-around window, both bounds NULL, one bound NULL,
and `from == to` — because it is the only gate clause whose wrong answer is a
row that is never returned and never logged.

---

### Task 15 — PostgreSQL Catalog Repository (Write)

**Files:** `src/storage/postgres/pool.go`, `mapping.go`,
`catalog_repository.go`, `queries/publish.sql`, `sqlc.yaml`;
Modify: `src/storage/memory/repository.go`, `src/storage/conformance/`

**Produces:** `postgres.NewCatalogRepository(pool) → domain.CatalogRepository`

- `UpsertCatalog` is the pseudocode in
  [Publish](#publish--how-it-works) — one transaction opened by the lock-and-load
  upsert, `visible_to` fail-safe, the FULL/MERGE branch of A8, `derive` called
  **after** the merge, catalog-level geometry covered once, the unconditional
  gate propagate, and the two-step offer prune.
- The merge itself lives in `domain` (Task 11) and is called from here. This
  package supplies the two things the domain cannot have: the row lock, and the
  stored document to merge against.
- `mapping.go` is the one place a row becomes a domain object, shared with the
  read side.
- Resource writes, geometry deletes, geometry inserts and offer writes each go
  out as **one `pgx.Batch`**, not a statement per entity — the loops in that
  pseudocode run under the catalog row lock, so their cost is lock hold time.

**Tests pin:** MERGE keeps an omitted resource, FULL removes it; an empty
`visibleTo` becomes `{network_id}`; a mid-transaction failure leaves no partial
catalog; republishing replaces a geometry rather than accumulating it; a cascade
delete removes geometries and offers. Two more on the write path itself:

- **A touched resource is written once, not twice.** Publish, then republish
  naming one resource, and read `xmin` on it: one new row version for that
  publish, not two. The gate propagate covers the untouched rows only, and
  without that clause every touched row carries a redundant second write, a dead
  tuple and a second `visible_to` GIN insertion — invisible in every functional
  test and visible in bloat a quarter later.
- **The statement count does not grow with the catalog.** Publish a 5-resource
  and a 50-resource catalog through a connection that counts round trips: the
  second must not cost ten times the first. It is the only assertion that keeps
  a `pgx.Batch` from decaying into a loop the next time someone edits it.

Then five for A8, which is the part of this task a unit test in `domain` cannot
reach:

- **field-level MERGE survives the round trip.** Publish a resource with
  `{grade, moisture, origin}`, republish with only `{moisture, origin: null}`,
  read the row back: `grade` stands, `moisture` is new, `origin` is gone. The
  domain test proves the function; this proves the function is what the column
  ends up holding.
- **the lock-and-load upsert returns the stored row on conflict.** A second
  publish must see the first one's `provider`. `ON CONFLICT DO NOTHING` returns
  zero rows here, which would silently make every republish a merge against an
  empty document and pass every MERGE test that only publishes once.
- **derivation runs after the merge, not before.** A patch that touches only
  `resourceAttributes.moisture` leaves `search_tsv` still matching a term that
  came from the *stored* `descriptor` — proof the tsvector was built from the
  merged document rather than the patch.
- **only `touched` resources are rewritten.** Republish one resource of forty
  and assert the other thirty-nine keep their `updated_at` **and** their
  `embedding_source_hash`. This is the test that catches a re-embed of the whole
  catalog on a one-field patch, which is a cost bug, not a correctness bug, and
  therefore invisible everywhere else.
- **FULL resets the catalog row itself.** Publish with a `validity`, republish
  FULL without one, assert all four validity columns are NULL — omission under
  FULL is a reset, not a carry-forward.

Three more that guard the denormalised gate and the shared geometry:

- **the gate reaches every resource**, including on a republish whose payload
  carries no resources at all — the `UPDATE resources` is the only thing that
  makes copying the gate down safe, and it is invisible in every test whose
  payload happens to include all of them. It asserts **all six** gate columns,
  the two daily-window ones included: `Resource` carries no validity of its own,
  so a column this `UPDATE` forgets keeps yesterday's value forever;
- **three provider locations across forty resources produce three geometry
  rows**, each with a NULL `resource_id`, not 120;
- **a FULL republish deletes an offer pruned to empty** rather than leaving it
  to read as catalog-wide, and the delete runs before the prune.
- **an offer naming a resource that does not exist is a PARTIAL, not a silent
  prune.** Publish an offer with `resourceIds: ["r1", "typo"]` into a catalog
  holding only `r1`: the catalog lands, the offer is stored against `r1` alone,
  and `errors` names the offer and the missing id. Then the degenerate case —
  `resourceIds: ["typo"]` only — where the offer must NOT be written, because an
  empty array means catalog-wide and a typo would otherwise attach the offer to
  the provider's entire inventory. There is no FK to catch either one.

And one for concurrency: two republishes of the same catalog issued at once,
each patching a different attribute, end with **both** attributes present. The
row lock is what makes that true; without it the second read-modify-write
overwrites the first with a document that never contained it.

---

### Task 16 — PostgreSQL Search Repository (Read)

**Files:** `src/storage/postgres/search_repository.go`, `retrievers.go`,
`hydrator.go`, `fusion.go`, `queries/discover.sql`;
Modify: `src/storage/memory/repository.go`, `src/storage/conformance/`

**Produces:** `postgres.NewSearchRepository(...) → domain.SearchRepository`,
one `Retriever` per mode, `Hydrator`, `RRF`

- Composition and concurrency are the pseudocode in
  [Discover](#discover--how-it-works). RRF is `1/(k + rank)`, `k = 60`.
- Every query embeds the scope gate and the geo `EXISTS` block printed there.
- Per-connection tuning (`hnsw.ef_search`, `statement_timeout`,
  **`plan_cache_mode = force_custom_plan`**) is applied by `pool.go` on
  connection acquire, not per query. The third is not tuning: every nullable
  predicate in `discover.sql` — the network gate, `target_paths`, `spatial_op`,
  the box bounds, `schema_contexts` — is index-usable only when the planner can
  see the parameter and fold the `IS NULL` arm away, which a generic plan
  cannot. It is the setting under which the query shapes in this plan mean what
  they say.

**Tests pin:**
- A **SQL source test** that reads `discover.sql` and asserts every query
  contains the scope gate — the nullable `network_id` clause, `active`, both
  date bounds **and** `within_daily_window` — plus the `geo_distance_m`
  predicate. A mode that drops the clause entirely leaks another network's
  catalog to a caller who scoped their request; a mode that hard-codes
  `visible_to @> ARRAY[$1]` instead of the nullable form silently reintroduces
  single-network scoping as the default.
- **An omitted `networkId` returns catalogs from every network,** including
  one whose `visibleTo` names a single network the caller never mentioned —
  and the same intent with `networkId` set still returns only that network's
  rows. Pinned as one test with two assertions, because a fix aimed at one
  direction is a regression in the other.
- **Schema filtering** matches as a pair: two resources, one `schema.org` +
  `GroceryItem` and one `beckn.org/Mobility` + `RideService`, queried with both
  fragments at once, return both — and a query for
  `["https://schema.org#RideService"]` returns **neither**. Run at one, two and
  three entries against the one static query, because the failure this replaced
  was a clause count that varied with the request. The cross-match is
  the failure two independent `IN` lists produce and a paired comparison does
  not. A third case: an empty `schemaContext` returns everything, because an
  empty list must emit no predicate rather than one matching nothing.
- A **haversine agreement test** comparing `geo_haversine_m` against
  `geo.HaversineM` across a spread of coordinate pairs, to a tight epsilon.
- `Total` equals the count of rows the predicate admits, and page 2 does not
  overlap page 1 — where "the predicate" includes the **OR of every enabled
  mode's text clause**. The pin is a corpus where lexical and fuzzy match
  *different* resources: `Total` must be the size of the union, because a
  counter carrying one mode's clause returns a number the pages cannot be
  paginated out of.
- **The count skip agrees with the count.** The same query run with the skip
  forced on and forced off returns the same `Total`. Then the three guards, one
  case each: a capped mode, a degraded mode, and `offset > 0` each force the
  query and each would under-report if the skip fired.
- **A page past the retrieval depth is a fault, not an empty list.** A corpus
  larger than `MaxCandidatesPerMode`, queried at `offset = MaxCandidatesPerMode`:
  the response names the boundary. Asserted against the empty `catalogs` array
  it would otherwise return, which reads exactly like the end of the results.
- **A retriever never returns more than `MaxCandidatesPerMode`,** and one that
  returns exactly that many reports itself capped. Pinned against a corpus wide
  enough to exceed it, because the query that finds everything is the ordinary
  one — `discover_tsquery` ORs its terms.
- A mode that errors is reported in `Degraded` and does not fail the search.
- `targets` selects between two geometries on one resource, **published
  through the walker rather than hand-written**: it emits a distinct path per
  find now, so the fixture is a real payload and scenario 27 covers the same
  ground end to end. The pin that matters here is that the stored `target_path` is
  byte-identical to a canonicalised `targets` — `path = ANY($1)` is plain
  equality, so a dot-form row against a bracket-form filter is a 200 with an
  empty list and no error anywhere to explain it.
- A catalog-level geometry (NULL `resource_id`) matches every resource in its
  catalog, and a resource-level one matches only its own.
- **Offers:** hydration returns the offers overlapping the matched resource ids
  plus the catalog-wide ones, excludes an expired offer, and does not return an
  offer belonging to a resource that is not on this page.
- Conformance: the memory backend and postgres return the same answers for the
  same fixtures — **including the validity fixtures**, where the memory backend
  must call `domain.WithinDailyWindow` and postgres `within_daily_window`, and
  the wrap-around case is the one that separates them.

---

### Task 17 — Wire-to-Domain Mapping

**Files:** `src/publish/mapper.go`, `geometry.go`,
`src/discover/intent_mapper.go`, `src/platform/jsonpath/canonical.go`

**Produces:** `publish.MapCatalog(catalog, directive, network) →
domain.CatalogPatch, fatal, partial` (the two fault kinds separately),
`publish.ExtractGeometries(catalogIndex, merged domain.Catalog) →
[]domain.Geometry, []domain.Fault`, `discover.MapIntent`,
`jsonpath.Canonicalise`

- **`MapCatalog` returns a `CatalogPatch`, not a `Catalog` (A8).** A `Catalog`
  is defaults-filled, and a defaults-filled struct cannot say whether the
  publisher sent `active: false` or sent nothing at all — which is exactly the
  distinction MERGE turns on. Absence has to survive the mapper to reach the
  merge, so every optional field is a pointer, a nil slice, or a nil
  `json.RawMessage`.
- Geometry extraction is the pseudocode in
  [Publish](#publish--how-it-works): a **general structural walk** over the
  whole catalog, recognising GeoJSON by shape rather than by field name, so a
  `targets` expression can name any geo path a publisher used. Per-geometry
  error isolation, all seven types, bounded by `MaxCatalogWalkDepth` and
  `MaxGeometriesPerCatalog`. It lives in its own file because it owns its own
  fault handling. It takes the **merged catalog** and is called from `derive`
  inside the write transaction — post-merge, because under MERGE the document
  that must be covered is the merged one, and a patch that never mentioned a
  geo field must not erase the geometries the stored document still implies.
- **Both path fields go through `jsonpath.Canonicalise`.** Not a style point:
  the discover mapper canonicalises `targets`, and `g.target_path = ANY($targets)` is
  plain equality, so a publish side that stores the raw dot form makes every
  geo search return `200` with an empty list.
- `jsonpath.Canonicalise` is called by **both** mappers, which is why it sits in
  `src/platform/` — `src/discover/` importing `src/publish/` to reach it would
  weld the two capabilities together. Because one function produced both sides,
  the SQL comparison is plain equality. Bracket form
  (`$['availableAt'][*]['geo']`) and dot form normalise to the same string.
- `MapIntent` rejects rather than skips: an unsupported op (`S_TOUCHES`,
  `S_CROSSES`), a bad SRID, a query geometry that is not one of the seven
  RFC 7946 types, a radius over `MaxRadiusMeters`, or unrecognised `targets`
  each produce a 400. It also reads `context.schemaContext` — an
  envelope field, not an intent one — splitting each entry on its first `#`
  into a `SchemaFilter{Context, Type}`.
- `MapCatalog` reads `validity` as a `TimePeriod` and fills **two** independent
  column pairs. `startDate`/`endDate` are RFC 3339 instants. `startTime`/
  `endTime` are a daily window: the offset is honoured when present, and a bare
  clock time is resolved in `App.DefaultTimezone` before being normalised to
  UTC. A `TimePeriod` carrying one half of the clock pair without the other is
  a *fatal* fault — the spec's `anyOf` requires both, and guessing the missing
  bound invents a window the publisher never stated.

**Tests pin:** both path forms canonicalise identically, **and the walker's
stored `TargetPath` equals the mapper's canonicalised `targets` byte for byte** — the
one assertion that catches the empty-200; a geometry nested under
`resourceAttributes` is found, wildcarded and owned by its resource while one on
the catalog's own provider is owned by the catalog (`Owners` empty), and one
under `offers[*].provider.availableAt[*].geo` is owned by exactly that offer's
`resourceIds` — with an offer carrying none of them falling back to
catalog-level; an object carrying
`"type": "Point"` but no `coordinates` is **not** recognised and raises **no**
fault, while one carrying both but with a malformed ring does; a
`GeometryCollection` is one find, not one per member; a document nested past
`MaxCatalogWalkDepth` terminates; the 257th geometry is a named *partial* fault rather
than a silent drop; one malformed geometry
costs one geometry and names the offending value, and comes back as a *partial*
fault on a `PARTIAL` verdict rather than sinking the catalog; a resource with no
`id` is a *fatal* fault and stores nothing; a non-Point geometry is **indexed**
(not skipped — the old behaviour, now wrong); an unknown SRID is a 400, not an
ignore; an over-max radius is a 400; each of `ANY`, `NONE` and `ALL` sets its
own pair of `Quantifier`-derived flags, and an unrecognised quantifier is a 400
rather than a silent downgrade to `ANY`; `S_TOUCHES` and `S_CROSSES` are 400s
carrying `SCH_TYPE_NOT_SUPPORTED`;
`"https://beckn.org/Agri#SeedLot"` splits into context and type while
`"https://beckn.org/Agri"` leaves the type empty, and a URI whose fragment
contains a second `#` splits only on the first; a validity with `startTime` and
no `endTime` is a fatal fault; `09:00:00+05:30` and a bare `09:00:00` under
`Asia/Kolkata` both normalise to `03:30:00` UTC.

Then the pair that separates A9 from A8, because they pull in opposite
directions and one mapper has to do both. **Defaulted:** a payload omitting
`isActive` yields `Active == true` and one omitting `visibleTo` yields
`[network]`, in MERGE as much as in FULL — the mapper resolves them, so no
absence reaches the merge. **Not defaulted:** a payload omitting `validity`
yields `Validity == nil` while `"validity": null` yields the explicit delete,
and the same for `provider`, `descriptor` and `resourceAttributes`. These look
pedantic and are the whole of A8 and A9 — a mapper that flattens the second
group to a zero value makes MERGE unimplementable downstream, silently, and a
mapper that leaves the first group nilable invites the branch A9 exists to
delete.

---

### Task 18 — Publish Service & Controller

**Files:** `src/publish/service.go`, `controller.go`

**Produces:** `publish.NewService(...)`, `publish.NewController(...)`

Flow is the pseudocode in [Publish](#publish--how-it-works).

- `publishOne` builds the `domain.DeriveFunc` closure and hands it to
  `UpsertCatalog` (A8). The closure is where the embedder and the catalog's
  index in the request are reached without either becoming a parameter of the
  repository port.
- **`CatalogReplicator.Replicate` is called here, after `UpsertCatalog` returns
  and the transaction has committed (A7)** — never inside it, because a fan-out
  that runs before commit can announce a catalog that then rolls back. A failed
  replication is logged and does **not** change the verdict: the catalog is
  stored, and a Phase 1 no-op cannot fail anyway.

**Tests pin:** a MASTER catalog beside a REGULAR one produces two verdicts in
one response and lands the regular one; the HTTP status is 200 even when every
catalog is REJECTED; a validation failure stores nothing; a directive-less
catalog is treated as **MERGE**, not FULL — `applyDirectiveDefaults` is the only thing
standing between an omitted `publishDirectives` and a republish that deletes
every resource the payload did not mention (A8). One route test: `POST /publish`
is the mount, and `POST /catalog/publish` is a `404` — the action lives in the
body, not the path. One ordering test: a publish whose transaction rolls back
does **not** call the replicator, asserted with a recording stub, because
"announced then rolled back" is the failure the after-commit rule exists for.
One stats test: `stats.itemCount`/`providerCount`/`categoryCount` on a
multi-resource, multi-`@type` catalog match the request-scoped reading C5/C12
decided — `itemCount` counts what *this request* landed, not what the catalog
now holds, which is the case a re-publish of one resource would get wrong if
the count were read back from the row set instead of carried through from the
merge.

---

### Task 19 — Discover Service & Controller

**Files:** `src/discover/service.go`, `controller.go`

**Produces:** `discover.NewService(...)`, `discover.NewController(...)`

Flow, `negotiate` and the mapper gates are the pseudocode in
[Discover](#discover--how-it-works).

**Tests pin:** a `textSearch` under `EMBEDDING_PROVIDER=noop` yields results
plus an `X-Beckn-Degraded: semantic` header, and a body carrying no `degraded`
key; with `SEARCH_FAIL_ON_UNAVAILABLE_MODE=true` the same request is
`NET_CATALOG_SOURCE_UNAVAILABLE`. Semantic rather than `structured`, because
`filters` is evaluated in Phase 1 and semantic is the mode a default Phase 1
deployment actually lacks — a degradation pin whose trigger no longer degrades
tests nothing; `networkId` absent searches EVERY network — the repository
emits no network predicate, and no fallback to `APP_NETWORK_ID` happens, which
is scenario 29 asserted from the other side; limit is clamped to `MaxPageSize`
rather than rejected; a
rendered catalog carries its offers, and carries no offer whose resources are
all off this page.

---

### Task 20 — Container, Router & Server Lifecycle

**Files:** `src/app/container.go`, `router.go`, `server.go`,
`cmd/discovery-service/main.go`

**Produces:** `app.Build(cfg) → *App`, `app.NewRouter(*App)`, `app.Run`

- Explicit constructor wiring, no reflection (D3).
- Middleware order exactly as fixed in [File Structure](#file-structure),
  **minus `Signature`**, which is parked with Task 6 and does not exist. Do not
  mount a placeholder in its slot — the chain-order test observes side effects,
  so a link with no side effect is a link the test cannot place, and a link that
  stamps a marker while doing nothing is worse: it makes an absent security
  control observable as a present one.
- Routes: `POST /publish`, `POST /discover`, `GET /healthz` (liveness, no
  dependencies), `GET /readyz` (pings the pool). **No `/catalog/publish`** (C2);
  a test asserts it 404s, so the alias cannot reappear by accident.
- **`CatalogReplicator` is constructed here (A7)** — the no-op
  implementation, injected into the publish handler by the same explicit
  constructor wiring as everything else, and called by `publishOne` **after**
  the transaction commits, never inside it. One line today, and it is why
  Task 20 appears in A7's list: a seam nothing builds is not a seam.
- Graceful shutdown drains in-flight requests on SIGTERM.
- **The request body ceiling is already enforced, in `Envelope` (C14).** Do not
  set a second one on the `http.Server` — two bounds on one quantity is two
  values to keep equal, and the one that fires first decides what the caller is
  told. `SERVER_MAX_REQUEST_BODY_BYTES` is read once, in `Build`, and passed to
  `Envelope`.
- `DATABASE_AUTO_MIGRATE` applies embedded migrations at boot.

**Tests pin:** the middleware chain is in the specified order (asserted by
observing side effects, not by reading a slice — including `Trace`'s
position: on a route that panics, `Values("X-Beckn-Chain")` is exactly
`["trace", "recover"]`, and the reverse nesting yields `["recover", "trace"]`.
It is the *order of the two entries* that carries the proof, not the presence
of either: both are appended before `Recover` writes the 500, so a test
asserting only that a marker survived the panic passes under both
arrangements); a panicking route produces exactly one completion line carrying
`status = 500` and a response bearing `X-Response-Time` — the observable that
places `RequestLogger` **above** `Recover` (A11), which the chain header cannot
show because `RequestLogger` stamps no entry in it; `/healthz` answers with the
database down and `/readyz` does not; shutdown completes an in-flight request.

---

### Task 21 — End-to-End Acceptance Suite

**Files:**
`tests/acceptance/{suite,publish,discover,offers,validity,performance,
                  defaults,geopath,spatial}_test.go`

The thirty-five scenarios above, over HTTP against the assembled service with
a real database. Only the embedder is stubbed. One file per scenario group, so
the file a failure names is the section of this plan it came from.

**One invariant runs after every scenario, in `suite_test.go`'s teardown**, not
as a scenario of its own: **no row of `offers.resource_ids` names a
`(catalog_id, resource_id)` that `resources` does not have.** `resource_ids` is
the one relationship PostgreSQL cannot constrain — it is an array, and there is
no foreign key into an array — so it is the one place a reordered or skipped
statement in `UpsertCatalog` produces drift that no `INSERT` will ever refuse.
Asserting it once per scenario costs a single `NOT EXISTS` query and fails in
the commit that introduced the drift, which is what a periodic reconciliation
job would be a slower and much later version of.

---

### Task 22 — Structured Attribute Filtering

**Files:** `src/platform/jsonpath/subset.go`, `src/discover/filter_parser.go`,
`src/storage/postgres/jsonpath.go`

`src/platform/jsonpath/subset.go` validates the caller's expression against the
accepted grammar and **rebases** it from the response-document root onto the
stored column — backend-agnostic, beside `Canonicalise`, because the accepted
grammar must not move if the backend does ([Data Model](#data-model), Grammar
leak). `src/discover/filter_parser.go` calls it to turn the wire expression
into a `SearchQuery` filter value; `src/storage/postgres/jsonpath.go` only
casts and evaluates the already-validated, already-rebased expression with
`@filter::jsonpath` and the `@?` operator. The mechanics, and which predicates
the GIN index can actually serve, are in
[Attribute filters](#attribute-filters--what-postgresql-can-and-cannot-do).

**This is not a translation (C10).** Only PostgreSQL SQL/JSON path is executed
(`? (@.x == "y")`). An RFC 9535 expression (`[?(@.x == 'y')]`) is rejected with
`400` / `SCH_INVALID_JSONPATH` naming the position that failed to parse. The
expression is cast with `@filter::jsonpath` — PostgreSQL's own parser, not a
hand-written one — and **never** interpolated into SQL text.

Both the resource path (`resources.attributes`) and the offer path
(`offers.offer`) are supported, with their `jsonb_path_ops` GIN indexes. Both
columns and both indexes already ship in Task 14, so this needs **no
migration** — which is what makes it Phase 1 work at all: the expensive half
landed eight tasks earlier, and this one is a parser, a rebase and a `WHERE`
clause.

A backend that cannot execute the subset still declares `jsonpath` missing and
degrades — or fails under `SEARCH_FAIL_ON_UNAVAILABLE_MODE`. What never happens
is silent ignoring.

**Tests pin:** an RFC 9535 expression is `400` / `SCH_INVALID_JSONPATH` rather
than an empty page; rebasing (`$.catalogs[*].resources[*] ? (@.resourceAttributes.A
== "x")` → `$ ? (@.A == "x")`) and the offer-path equivalent; an `EXPLAIN`
proving the equality form uses the GIN index and the inequality form is
knowingly a scan; and the expressions it **refuses** — a half-correct rebase
is worse than none, so refusing must be a tested path. A malformed
expression is a `400` from the cast, never an interpolated query.

---

### Task 23 — OpenTelemetry Tracing & Metrics

**Files:** `src/platform/telemetry/telemetry.go`, `metrics.go`;
Modify: `src/platform/middlewares/trace.go`

**Produces:** `telemetry.Init(cfg)`, RED metrics, retrieval-mode counters

- `otelhttp` on the server, W3C Trace Context propagated **in and out** — a
  correlation id that stops at the process boundary satisfies the letter of
  "logged" and none of the point (TRD §6). This replaces Task 8's no-op
  `Trace` middleware body — including dropping the `X-Beckn-Chain: trace`
  entry Task 8 added only so Task 20's order test had something to observe —
  and the chain Task 20 wired and tested does not move. **Task 20's order
  assertion moves with it**, from the header pair to the span: the 500 a
  recovered panic produces must be recorded *inside* the exported span, which
  is true only if `Trace` wraps `Recover`. `Recover` keeps its `recover`
  entry, but on its own it is a single value and a single value orders
  nothing — after this task the chain header proves no pair. What still places
  `Recover` is the behavioural pin: a panicking route logs one completion line
  at `status = 500` with `X-Response-Time` set, which holds only while
  `RequestLogger` wraps `Recover` (A11). Keep that assertion when the header
  pair goes.
- OTLP exporter configured by `OTEL_*`, defaulting to `none` so a deployment
  with no collector still starts.
- `search_degraded_modes` and `embedding_duration_ms` become metrics rather than
  log fields nobody aggregates.

**Tests pin:** an inbound `traceparent` is joined rather than replaced; the
exporter defaulting to `none` does not fail startup; a degraded search
increments the counter.

---

## Open Items

**None.** Every question this plan raised has an owner decision behind it.

That is a statement with a short shelf life — implementation will raise more —
so what matters is the table below, not this line. **The decisions are recorded
rather than dropped**, because the next reader's first question is why the
schema looks the way it does, and an undocumented decision is indistinguishable
from an accident:

| | Was open | Decided |
|---|---|---|
| Daily-window validity | Whether a `TimePeriod` carrying only `startTime`/`endTime` is supported | **Both forms supported.** Date range and daily clock window are independent column pairs, both nullable, both ANDed into the gate. See [Validity](#validity--two-independent-windows) |
| `category` | Whether category filtering ships | **Removed entirely.** The spec has no `category` field: `Resource` is `{id, descriptor, resourceAttributes}` and `Intent` cannot carry a category filter. C5 |
| `schemaContext` | Whether schema filtering is real | **Real, and it ships in Phase 1.** From `context.schemaContext`, matched as `@context` + `@type` pairs. C4 |
| `validFrom`/`validTo` | Whether publishers send them | **Not under those names.** The wire field is `validity: TimePeriod` on `Catalog` and `Offer`, and it expands into four columns, not two. `Resource` has neither `validity` nor `isActive`, so every gate column on `resources` is a derived copy |
| JSONPath grammar | Which grammar the spec mandates | **PostgreSQL SQL/JSON path only.** The spec names none normatively and its one example is RFC 9535, so this is a recorded deviation — C10 |
| Offer-level geometry | Which resource a geometry under `offers[*].provider.availableAt[*].geo` belongs to | **The offer's `resourceIds`**, one `resource_geometries` row per id, falling back to catalog-level when that array is absent or empty — the same meaning `offers.resource_ids` already carries. `Geometry.ResourceID *string` became `Owners []string` and `touched` had to start following offers; no migration, the column was always there |
| Unavailable retrieval mode | Whether a mode the backend cannot run should degrade or refuse | **Degrade by default; refuse on request.** `SEARCH_FAIL_ON_UNAVAILABLE_MODE` defaults to `false`: the modes that work run, results come back, and the missing one is named in `X-Beckn-Degraded` (C11). Setting it `true` makes the same request a `400` / `NET_CATALOG_SOURCE_UNAVAILABLE`. The default points at degrade for one reason: Phase 1 ships `EMBEDDING_PROVIDER=noop`, so `semantic` is missing on every fresh deployment, and defaulting to refuse would make every `textSearch` a `400` until an Ollama deployment exists — a default that breaks the common case is a default nobody keeps. Silently ignoring the mode remains the option that is never taken |
| Structured filtering | Whether JSONPath attribute filtering ships in Phase 1 or waits | **Phase 1.** `Intent.filters` is evaluated, not degraded — PostgreSQL SQL/JSON path only (C10), rebased onto `resources.attributes` or `offers.offer`, run through the `@?` operator. Both columns and both `jsonb_path_ops` indexes were already landing in Task 14, so promoting it costs a parser and a rebase and no migration. Scenario 18 changed from asserting the degradation to asserting the filter |
| Semantic search (A5) | When embeddings are turned on | **Not in Phase 1 — confirmed.** `EMBEDDING_PROVIDER=noop` ships; the column, the HNSW index and the `Embedder` seam ship with it, and `embedding IS NULL` is the backfill queue for whenever an Ollama deployment exists. Deferred deliberately, not undecided |
| `networkId` default (discover) | Whether an omitted `networkId` scopes to `APP_NETWORK_ID` or searches every network | **Searches every network.** `visibleTo` is how a publisher restricts a catalog to specific networks; it is not an access boundary a network-less discover caller is presumed locked out of by default. A caller wanting isolation supplies `networkId`. Publish's own default — `APP_NETWORK_ID`, to fill an empty `visibleTo` — is unchanged (C8); the two fields answer different questions and no longer share a fallback |
