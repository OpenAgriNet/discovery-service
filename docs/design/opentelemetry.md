# OpenTelemetry — discovery-service

What this service emits, what it never emits, and how Task 23 builds it.

Base is the Sunbird
[network-telemetry-spec](https://github.com/Sunbird-Obsrv/network-telemetry-spec);
the `beckn.*` attributes and the four events are ours. Monitoring stack is
ClickStack (ClickHouse + HyperDX + bundled OTel collector), which ingests OTLP
natively.

**Binding on the shape of a span.** Where this and `discover-and-publish.md`
disagree about a span, this wins; about anything else, the plan does.

## What we emit

| Signal | Emitted here? | Where |
|---|---|---|
| **TRACE** (`eid: API`) | **Yes** — one span per protocol request | This document |
| **METRIC** | Not from this binary | The spec makes metrics **mandatory for the participant** — this is an obligation OAN owes, not an optional extra. `ref-impl-design.md` places the computation in micro-observability, not the SDK ("only a lightweight library without any storage"), so the split is conformant. **Task 24 is that obligation** — see *Metrics* below for the candidate list and what blocks it. N stateless replicas each counting in memory would produce N partial counts nothing can reassemble |
| **AUDIT** (OTel LOG) | No | Spec-optional ("*in addition to the above two mandatory data points… optional data*"), so silence conforms. Open question 6 — a publish fits the `item.prevstate` → `item.state` shape well, but modelling it as both API and AUDIT duplicates |

One span per request, because this service answers synchronously — `on_discover`
is the response body, not a second call.

**Only `/discover` and `/publish` produce spans.** `/healthz` and `/readyz` run a
short middleware chain (`src/app/router.go`) that excludes tracing. Preserve
that: a probe every few seconds carrying `eid: API` would be most of the stream
and none of the signal.

### Two destinations

One collector, two exporters, different filtering:

| Destination | Gets |
|---|---|
| **ClickStack** — ours, data never leaves | Everything, including query text |
| **Facilitator** — OAN network level | API events only, deny-list enforced |

Everything below specifies the **facilitator** stream. That is the one an
external party can be harmed by.

---

## Resource — set once at boot

| Attribute | Value |
|---|---|
| `eid` | `API` |
| `producer` | This deployment's participant id, e.g. `discovery-service`. New config. Participant-id shape — `^[a-z0-9][a-z0-9._:-]{2,252}$`, `maxLength` 253, from `docs/design/registry/schemas/ProviderSchema.json#/$defs/ParticipantId`. **Not** the `{2,63}` alternation on that file's line 18: that is a *provider* id, and open question 3 says a discovery service is not a Provider. The pattern is borrowed as a shape, not resolved from a record — there may be no record for us to resolve |
| `domain` | The **sector** — `Agriculture`. New config. Not the network, not the entity type |
| `service.name` | ClickStack's grouping column. **Same value as `producer`** — they split only if one participant ever runs several services |
| `network.id` | `APP_NETWORK_ID` — `mahavistar`, `bharatvistar` (C8). Our key, not the spec's |

## Scope — per exported batch

The `scope` object itself is spec-Optional; `name` and `version` are Required
inside it. We emit it.

| Field | Value | |
|---|---|---|
| `scope.name` | `discovery_service` — the spec's own example value | Required |
| `scope.version` | The network-telemetry-spec version — **decision 1** | Required |
| `scope_uuid` | — | Optional, **deferred** — not reachable from the SDK, see below |
| `count` | — | Optional, **deferred** — same reason |
| `checksum` | — | Optional, **skip**: tamper-evidence over an unsigned transport is theatre. Revisit when the facilitator authenticates |
| `schema_url` | — | Optional, **skip**: the spec publishes no schema to point at (see below) |

**Why `scope_uuid` and `count` are deferred.** Both are *per-batch* concepts, and
OTel Go has no per-batch hook. Scope attributes are a `TracerOption` fixed when
`Tracer()` is called (`trace@v1.44.0/config.go:345`), and the provider caches
tracers by that key — so a per-batch `scope_uuid` would mean a new tracer per
batch, and `count` is unknowable at tracer-creation time. Emitting them needs a
custom exporter that rewrites the outgoing OTLP scope. Both are spec-Optional, so
omitting them conforms; **they belong to 23f, not 23a.**

Scope is immutable once a span exists, so it comes from our own tracer. This is
why `otelhttp` is rejected: a span it started carries that package's identity
permanently.

## Span attributes

**Mandatory profile** — from the spec:

| Attribute | Source |
|---|---|
| `name` | The Beckn action: `discover` / `publish` |
| `sender.id` | `context.senderId`, **when present** — one field on both paths, and unverified. See below |
| `sender.unidentified` | `true` when the envelope carried no `senderId` — see below |
| `sender.unverified` | `true` whenever `sender.id` is set in this phase — see below |
| `recipient.id` | Same value as `producer`. **Ours, never the caller's `receiverId`** — a caller can address anyone; this attribute has to say who actually answered |
| `span_uuid` | Generated per span, by a `SpanProcessor`'s `OnStart` |
| `observedTimeUnixNano` | Unix nanos as a string (the spec's prose says ISO; its field name and example say nanos — follow the name). **Set in the middleware just before `span.End()`, not in a processor** — `OnEnd` receives a `ReadOnlySpan` and cannot set attributes |
| `http.method`, `http.host` | The request |
| `http.route` | `r.Pattern` (Go 1.22+ `ServeMux`), **with the method prefix stripped** — the pattern reads `POST /discover` and `http.method` already carries the verb |
| `http.status.code` **and** `http.status_code` | Both — see the type note below. Neither is readable from where `Trace` sits; both come off the observation record, see *How the span learns the status* |
| `http.scheme`, `http.flavor` | Spec-Optional, free from the request. Emit |
| `traceId`, `spanId`, `parentSpanId`, timings, `status` | The SDK. **Hex, not UUID** — see divergence 5 |

**`http.status.code` — the spec disagrees with itself on the type.** Its
structure declares `Attribute("http.status.code", Int)`; all three of its examples
emit `{"stringValue": "200"}`. **We follow the examples and send a string**,
because a facilitator was built against those examples and `http.status_code`
already carries the int for ClickStack. If the facilitator rejects it, this is a
one-line change — the point of emitting both.

`http.user_agent` and `http.server_name` are spec-Optional and **deliberately not
emitted**; user agent is on the deny-list.

**Ours** — correlators and classification, true for the whole request:

| Attribute | Source |
|---|---|
| `beckn.action` | `discover` / `publish` |
| `beckn.version` | `context.version` |
| `beckn.networkId` | `context.networkId` |
| `beckn.transactionId` | Joins our span to the caller's and the next hop |
| `beckn.messageId` | Joins a request to its callback |
| `beckn.receiverId` | `context.receiverId`, when present. Emitted **beside** `recipient.id` rather than into it: the two disagreeing is a caller addressing a participant that is not us, which is worth a query and impossible to ask if one field holds both |
| `beckn.schemaContext` | The `@context` URIs, entry order preserved. **Bounded** — see below. On discover this is the seeker's predicate, not decoration — see below |
| `beckn.schemaType` | The `#fragment` of each — `MandiPrice`, `SeedLot`. **Parallel and same-length** with the above, `''` where an entry named no type; flattening them into two independent sets would count cross-matches no request made |
| `beckn.schemaTruncated` | `true` when either bound below was hit. Absent otherwise |
| `error_type` | The C1 category. Also on the `error` event, so a facilitator can filter a span set without unpacking events |

**On discover, `beckn.schemaContext` is what the seeker is asking for.** It is
not metadata that happens to ride along: `mapSchemaContext` reads the predicate
off the **envelope**, not the intent
(`src/discover/intent_mapper.go:113`), splits each entry on `#` into
`domain.SchemaFilter{Context, Type}`, and the repository turns it into a schema
clause in the SQL. So `beckn.schemaContext` + `beckn.schemaType` answer *what are
they seeking* at capability granularity — `WeatherObservation`, `MandiPrice` —
which is the one question about query content this design can answer without
emitting query content. Everything finer (which location, which crop, the
free-text string) stays on the never-emitted list.

**Absent and empty must stay distinguishable**, because they are different
questions. `schemaContext` is optional on discover, and absent means *no schema
predicate at all* — every capability matches. That is the "seeking anything"
bucket, and it is likely a large one; if the attribute is emitted as an empty
list when the field was missing, the bucket disappears into the same value as a
seeker who sent an empty array. Omit the attribute entirely when the field was
absent — the same absent-not-zero rule 23d pins for `retrieval.embedding_ms`.

**`beckn.schemaContext` is attacker-controlled, so it is bounded**: at most the
first **16** entries, each at most **256** characters, with `beckn.schemaTruncated`
set when either bound bites. `beckn.schemaType` is truncated *in the same step* so
the parallel-length invariant survives — truncating one alone is what turns a
correct pairing into silently wrong pairs.

The request body has a ceiling (C14), so this is not about span size alone. It is
that export is always-on and unsampled, a `@context` entry is a URI the caller
wrote, and a URI has room for arbitrary text in its query and fragment. A
deny-list checked over attribute *keys* cannot see that. Bounding the value is
what keeps the blast radius of this one attribute finite; it does not make the
content trusted.

**No duration attribute.** Response time is the span's own
`end - start`; ClickHouse stores it as a native `Duration` column and HyperDX
charts p50/p95/p99 off it. A `duration_ms` attribute would be a second copy free
to disagree with the first.

### `sender.id` — required by the spec, optional here, and unverified when present

`src/platform/validation/envelope_rules.go` requires five fields; `senderId` and
`receiverId` are deliberately excluded (`envelope_rules.go:98-104`) — participant
identity was parked with Task 6. `bapId` and `bppId` are not the fields to read:
they were dropped from `beckn.Context` entirely, and a caller that still sends
them is accepted and ignored (`src/beckn/types.go:48-66`).

So a valid request can carry no caller identity — and a request that carries one
carries a string the caller chose.

**Two flags, because there are two different failures and a facilitator has to
tell them apart:**

- **`sender.id` omitted, `sender.unidentified = true`** when the envelope named
  no `senderId`. Requiring it would reject requests the protocol permits;
  inventing one would poison the facilitator's only cross-participant join.
- **`sender.unverified = true`** whenever `sender.id` *is* set. Nothing in this
  build resolves the DID or checks a signature, so the id is a claim. `types.go:56`
  is explicit that this is "the same 'a string the caller chose' hazard the rate
  limiter refuses to key on" — and Task 8 refused to key the bucket on it for
  exactly this reason. Telemetry that carries the claim silently would let the
  facilitator key its cross-participant join on something any caller can assert
  about any other participant, which is a worse outcome than the absence
  `sender.unidentified` already reports honestly.

`sender.unverified` is `true` on **every** span carrying a sender in this phase.
That looks like a constant, and the doc's own test says a field with one value
filters nothing — but it is not constant across phases: it goes away when Task 6
lands, and until then it is the only thing standing between a claim and an
identity. Counting both flags gives the number that argues for finishing Task 6 —
open question 1.

### How the span learns the status

`http.status.code` cannot be read from where `Trace` sits, and the naive
implementation is wrong in a way no test would catch by accident.

The chain is `RequestID → Trace → RequestLogger → Recover → …`
(`src/app/router.go:134-141`). The status is only knowable from *inside*, which is
why `RequestLogger` wraps the writer in a `responseRecorder`
(`request_logger.go:35-47`). `Trace` sits **above** that wrapper: it holds the
outer `http.ResponseWriter` and never observes `WriteHeader`'s int. Nor, today,
can it read the record — `correlation` is allocated by `RequestLogger` *below*
it, and context values travel down.

Note what *is* reachable: `w.Header()` is the same map through the embedding, so
`X-Beckn-Error-Type` and `X-Response-Time` are readable from `Trace` after the
chain returns. Only the status int is stuck, because it is not a header.

**The fix is the design A23 already states — record the fact once, project it
twice.** The status is a fact; `RequestLogger` is the only thing that can know it;
the log line and the span are both projections of it. What has to change is *who
allocates the record*, and the answer is: **whichever of the two runs first, with
the other adopting it.**

```pseudo
// in both Trace and RequestLogger
record := recordFrom(ctx)
if record == nil {
    ctx, record = newRecord(ctx)
}
```

`Trace` runs first when both are mounted, so it allocates and can read the status
back after `next` returns and before `span.End()`. `RequestLogger` mounted alone
still allocates its own and behaves exactly as it does today.

That last sentence is the constraint, not a nicety. `request_logger_test.go:181`
and `:207` mount `RequestLogger(Envelope(…))` with no `Trace` above. A design that
moved allocation wholesale up to `Trace` would drop the correlators from those two
tests' completion lines — which breaks 23b's acceptance criterion that **no test
file is edited**, and would break it in the one way that reads as the refactor
having worked.

The record now carries traffic both ways, which is what it was always for. **Up**
to the span: the status, and anything else only something below `Trace` can know.
**Down** to `RequestLogger`'s completion line: 23e's `trace_id` and `span_id`,
which only `Trace` can know. One structure, one allocation, no middleware
reaching into another's wrapper.

### Spans that start before the envelope parses

Tracing sits above `Envelope`, so a malformed or oversized body produces a span
with no `beckn.*` attributes. **Still exported** — a rejected request is network
data, and dropping it hides the callers getting it wrong. Unparsed fields are
omitted, not blanked.

---

## Events

Rule: **true for the whole request → span attribute; produced at a point during
processing → event.**

Each event carries its own `time`, stamped **when it occurs**. The deltas are the
phase breakdown; batch them at handler exit and every timestamp collapses to the
same value — the total stays correct, the dashboards keep rendering, and the
breakdown silently becomes zeros.

### `discover`

**`request_info`** — fired after `correlate()` reads the envelope. The *shape* of
the question, never its content.

| Attribute | Meaning |
|---|---|
| `intent.kinds` | Which of `textSearch` / `filters` / `spatial` / `mediaSearch` were present |
| `intent.filter_type` | `Filters.Type` — the grammar named, not the expression |
| `intent.spatial_ops` | Each `SpatialConstraint.op` verbatim (`S_INTERSECTS`, `S_DWITHIN`) |
| `intent.scoped` | Whether `networkId` was supplied |

**`retrieval_info`** — fired the moment the storage call returns. This delta is
time-in-retrieval, the number that moves.

| Attribute | Meaning |
|---|---|
| `retrieval.modes_run` | Which of `lexical` / `fuzzy` / `semantic` / `spatial` / `jsonpath` ran |
| `retrieval.modes_degraded` | The `X-Beckn-Degraded` list |
| `retrieval.embedding_ms` | How long the query embedding took. **Present only when one was computed** — absent under `EMBEDDING_PROVIDER=noop`, which is every Phase 1 deployment (A5) |

`retrieval.embedding_ms` is the surviving half of A23's `embedding_duration_ms`.
It earns its place rather than being carried over out of loyalty: when semantic
search is on, the embedding is a network hop to Ollama
(`src/indexing/embeddings/ollama.go`) and is the single likeliest thing to blow
the 20 ms budget. Without it the `request_info` → `retrieval_info` delta bundles
the embedding call and the storage call into one number, and the two have nothing
to do with each other.

It does not contradict *No duration attribute* above. That rule refuses a second
copy of a duration the span already reports; this is a phase the span reports
nowhere.

**`response_info`** — fired after the response body is assembled.

| Attribute | Meaning |
|---|---|
| `result.catalog_count` | How many catalogs came back |
| `result.provider_ids` | The distinct `Catalog.bppId` of what was returned — **whose** data answered this query |
| `result.empty` | True when zero. The most valuable signal this service gives the network: somebody asked and nobody serves it |

**`result.provider_ids` is the answer to “what was the source”, and without it
four network questions have no substrate at all**: which provider served a query,
which provider's data is never served, per-provider request volume, and whether
failures concentrate on one source. The span carried a catalog *count* and no
identity, so none of them could be asked of the telemetry — only of the database,
which cannot correlate them with a request.

`bppId` is emittable where `Catalog.provider` is not. It is a participant
identifier, already on the wire in the response body, and a fact about the
published document rather than a claim about the caller — which is exactly why
**A24** removed `bppId` from `Context` and left it on `Catalog`
(`src/beckn/types.go:102`). `Catalog.provider` is `json.RawMessage`: emitting it
would put descriptor *content* on a span and break the shape-not-value rule.

**Distinct, not one per catalog**, and bounded like `beckn.schemaContext` — at
most the first 16, `result.providersTruncated` when the bound bites. A discover
answering with 200 catalogs from one provider must emit one id, or the attribute
becomes a page-size measurement wearing an identity's name.

It answers *how many providers served*, never *how many providers exist*. A
provider who publishes once and is never matched emits no span in any later
window; the population count stays a `SELECT count(DISTINCT ...)`.

### `publish`

**`request_info`** — fired **at intake, before the A1 MASTER refusal**. After it,
`publish.catalog_types` would read `REGULAR` on every span that exists. At intake
it answers *who is trying to publish master data to a network that refuses it*,
and the `error` event on the same span carries the refusal.

| Attribute | Meaning |
|---|---|
| `publish.bpp_ids` | The distinct `Catalog.bppId` being published — **who added the source**. Same bound and same reasoning as `result.provider_ids` |
| `publish.catalog_count`, `publish.resource_count`, `publish.offer_count` | Payload volume |
| `publish.update_modes` | `FULL` / `MERGE` — a `FULL` republish deletes what it does not mention |
| `publish.catalog_types` | As sent. `MASTER` appears even though refused |
| `publish.visible_to` | Which networks it was published to |
| `publish.validity_present` | Whether a validity window was set — the freshness signal |

**Judgement call:** `publish.*_count` reveals catalog size and `publish.visible_to`
reveals distribution — arguably commercial information. In, because the spec asks
for volume and OAN's providers are largely public bodies. **On a network with
competing commercial providers, drop both.**

### Both paths

**`error`** — projected from the fault `logNack` already holds, so the C1
category is decided in one place and span and log cannot disagree.

| Attribute | Source |
|---|---|
| `type` | `beckn.Error.Type` — the C1 category |
| `code` | `beckn.Error.Code`, e.g. `NET_CATALOG_SOURCE_UNAVAILABLE` |
| `msg` | `beckn.Error.Message` |
| `path` | `beckn.ErrorDetails.Path`, e.g. `$.message.publishDirectives[1]` |

---

## Never emitted

The spec's own example puts `reqBody` and `resBody` on events. **We do not.**
Never on a span or event leaving for the facilitator:

- the `textSearch` string — a farmer's query is user data
- `filters.expression` — only its declared grammar
- spatial coordinates — a query location is a location
- resource or offer content
- request/response bodies, client IP, `X-Forwarded-For`, user agent

Everything above is a **shape or a count, never a value**. That is the test for
the next attribute someone wants to add.

The cost is real: `result.empty` says unmet demand *happened*, not *what for*.
That half is recovered in ClickStack, where the query text may go because the
data does not leave. The deny-list is enforced **on the facilitator exporter
specifically** — asserting it on both would forbid the local analysis the split
exists to permit.

---

## Divergences from the spec

Every mandatory field above is emitted. These six are where we knowingly differ,
collected here so a reviewer sees them in one place.

| | Divergence | Why |
|---|---|---|
| 1 | **`sender.id` omitted when absent**, with `sender.unidentified = true` in its place — and **`sender.unverified = true` when it is present**. The spec marks it Required and treats it as an identity | This protocol phase does not require `senderId` — identity was parked with Task 6. Requiring it rejects legal requests; inventing it poisons the facilitator's only cross-participant join; and emitting it unqualified hands that join a string the caller chose. Open question 1 |
| 2 | **No `reqBody` / `resBody`**, though the spec's example carries both | Design principle 3 — the spec's own. Its example also puts `deviceid` and `useragent` in `resource`, which contradicts that principle; we follow the principle over the example |
| 3 | **`http.status.code` sent as string**, though the structure declares `Int` | The spec contradicts itself; all three examples send a string. `http.status_code` carries the int |
| 4 | **No METRIC signal from this binary** | Mandatory for the participant, but `ref-impl-design.md` places computation in micro-observability. Task 24 |
| 5 | **`traceId` / `spanId` are OTLP hex**, not the spec's UUIDs | The spec's examples use dashed UUIDs — `d4ae9294-ab00-11ee-9db4-325096b39f47` — which are **not valid OTLP**: the protocol requires 16-byte and 8-byte hex. Design principle 2 says adopt OpenTelemetry, so the examples are wrong, not the protocol. **But a facilitator validating against those examples would reject every span we send.** Open question 9 |
| 6 | **Event timestamps are `timeUnixNano`**, not the spec's `time` | Same root cause as 5, and found the same way: the spec's event shape names a field `time` carrying an ISO string, and OTLP span events carry `timeUnixNano`. This is not a field we choose — the SDK serialises it, so honouring the spec's spelling would mean rewriting the OTLP payload on the way out. Note the spec is already inconsistent with itself here in our favour: it insists on nanos for `observedTimeUnixNano` (divergence note under Span attributes). Open question 9 covers both |

### The spec is prose only

`schemas/` and `examples/` in the spec repo are **placeholders** — the README
lists API / METRIC / LOG in italics with no files, and the examples are marked
WIP. The repo carries **no version tags**. So:

- There is nothing machine-readable to validate against, and no conformance test
  can target the spec itself. Ours asserts our own profile — the table above is
  the diff a future schema would have to reconcile.
- **`scope.version` has no released value to name.** `1.0` appears in every
  example and is the only candidate; decision 1 is really "confirm `1.0` and say
  so", not "look it up".
- The examples carry copy-paste errors — the LOG example is wrapped in
  `resourceMetrics`, and `item.duration` in the structure is `item.duration.seconds`
  in the example. Treat structure over example **except** where structure and
  example conflict on something a consumer already parses, which is divergence 3.

---

## Worked example — `discover`, `semantic` degraded

The realistic default: Phase 1 ships `EMBEDDING_PROVIDER=noop` (A5), so
`semantic` is degraded on every fresh deployment.

**This is a wire-accurate OTLP/JSON envelope, not a sketch.** Ids are
illustrative, but every field name, enum spelling and timestamp is what the SDK
actually emits and what a facilitator actually parses — including the ones that
are easy to get wrong by hand: events carry `timeUnixNano` and not `time`
(divergence 6), `status` is an object and `kind` an enum name, and every
timestamp in the block is nanos on one clock so the deltas below can be read off
it. An earlier draft of this example had ISO event times a year adrift from the
span's own start, which is exactly the fault 23d's monotonic-time assertion
exists to catch — and it went unnoticed in the artifact implementers copy from.

```jsonc
{"resourceSpans": [{
  "resource": {"attributes": [
    {"key": "eid",          "value": {"stringValue": "API"}},
    {"key": "producer",     "value": {"stringValue": "discovery-service"}},
    {"key": "domain",       "value": {"stringValue": "Agriculture"}},
    {"key": "service.name", "value": {"stringValue": "discovery-service"}},
    {"key": "network.id",   "value": {"stringValue": "mahavistar"}}
  ]},
  "scopeSpans": [{
    "scope": {"name": "discovery_service", "version": "1.0"},
    "spans": [{
      "name": "discover",
      "traceId": "4bf92f3577b34da6a3ce929d0e0e4736",
      "spanId":  "00f067aa0ba902b7",
      "startTimeUnixNano": "1756296000000000000",
      "endTimeUnixNano":   "1756296000148000000",
      "kind": "SPAN_KIND_SERVER",
      "status": {"code": "STATUS_CODE_OK"},
      "attributes": [
        {"key": "span_uuid",            "value": {"stringValue": "3fae2d5f-3cfb-4e6e-b6a2-0ee5d6579832"}},
        {"key": "observedTimeUnixNano", "value": {"stringValue": "1756296000148000000"}},
        {"key": "sender.id",            "value": {"stringValue": "oan-experience"}},
        {"key": "sender.unverified",    "value": {"boolValue": true}},
        {"key": "recipient.id",         "value": {"stringValue": "discovery-service"}},
        {"key": "http.method",          "value": {"stringValue": "POST"}},
        {"key": "http.route",           "value": {"stringValue": "/discover"}},
        {"key": "http.host",            "value": {"stringValue": "discovery.oan.example"}},
        {"key": "http.scheme",          "value": {"stringValue": "https"}},
        {"key": "http.flavor",          "value": {"stringValue": "1.1"}},
        {"key": "http.status.code",     "value": {"stringValue": "200"}},
        {"key": "http.status_code",     "value": {"intValue": 200}},
        {"key": "beckn.action",         "value": {"stringValue": "discover"}},
        {"key": "beckn.version",        "value": {"stringValue": "2.0.0"}},
        {"key": "beckn.networkId",      "value": {"stringValue": "mahavistar"}},
        {"key": "beckn.transactionId",  "value": {"stringValue": "b6b0f1c2-6b1a-4c2e-9b7e-2a1f0c4d5e6f"}},
        {"key": "beckn.messageId",      "value": {"stringValue": "e2f1a3d4-77c8-4a19-bb02-9d3e5f7a1b2c"}},
        {"key": "beckn.receiverId",     "value": {"stringValue": "discovery-service"}},
        {"key": "beckn.schemaContext",  "value": {"arrayValue": {"values": [{"stringValue": "https://beckn.org/Agri"}]}}},
        {"key": "beckn.schemaType",     "value": {"arrayValue": {"values": [{"stringValue": "MandiPrice"}]}}}
      ],
      "events": [
        {"name": "request_info",   "timeUnixNano": "1756296000004000000", "attributes": [
          {"key": "intent.kinds",       "value": {"arrayValue": {"values": [{"stringValue": "textSearch"}, {"stringValue": "spatial"}]}}},
          {"key": "intent.spatial_ops", "value": {"arrayValue": {"values": [{"stringValue": "S_DWITHIN"}]}}},
          {"key": "intent.scoped",      "value": {"boolValue": true}}
        ]},
        {"name": "retrieval_info", "timeUnixNano": "1756296000141000000", "attributes": [
          {"key": "retrieval.modes_run",      "value": {"arrayValue": {"values": [{"stringValue": "lexical"}, {"stringValue": "fuzzy"}, {"stringValue": "spatial"}]}}},
          {"key": "retrieval.modes_degraded", "value": {"arrayValue": {"values": [{"stringValue": "semantic"}]}}}
        ]},
        {"name": "response_info",  "timeUnixNano": "1756296000147000000", "attributes": [
          {"key": "result.catalog_count", "value": {"intValue": 4}},
          {"key": "result.provider_ids",  "value": {"arrayValue": {"values": [{"stringValue": "imd.gov.in"}, {"stringValue": "agmarknet.gov.in"}]}}},
          {"key": "result.empty",         "value": {"boolValue": false}}
        ]}
      ]
    }]
  }]
}]}
```

A facilitator reads off this, without ever seeing the query: somebody searched
for mandi prices near a point, three modes ran, `semantic` was unavailable, four
catalogs answered — and 4 ms parsing, 137 ms retrieving, 6 ms building
(`.004`/`.141`/`.147` against an end of `.148`).

Four things are absent, each for its own reason, and none of them blanked:

- `intent.filter_type` — the request carried no `filters`.
- `retrieval.embedding_ms` — `semantic` is degraded, so no vector was computed.
  A zero here would read as a fast embedding rather than no embedding.
- `parentSpanId` — no inbound `traceparent`. This is the common Phase 1 case and
  it is open question 4: until the adapter and experience layer propagate, every
  span in the network is the root of its own trace.
- `scope_uuid` and `count` — deferred to 23f, and *not* present in the scope
  above. See *Scope*: they are per-batch values the SDK cannot reach.

Here `beckn.receiverId` agrees with `recipient.id`, which is the ordinary case.
The reason both are emitted is the case where they do not.

`result.provider_ids` carries **two** ids against a `result.catalog_count` of
four: the ids are distinct providers, not one entry per catalog. That is the
whole difference between an identity attribute and a second, worse copy of the
count.

**A rejected request** looks the same minus what never parsed:
`"status": {"code": "STATUS_CODE_ERROR"}`, `sender.unidentified: true`,
`error_type: CONTEXT`, and one `error` event carrying
`CTX_VERSION_UNSUPPORTED` at `$.context.version`. Absent `beckn.networkId` means
the request did not carry it, not that parsing failed — and `http.status.code`
reads `400`, which is only true because the status came off the record rather
than off a writer `Trace` cannot see.

---

## How it is generated

| # | Where | Does |
|---|---|---|
| 1 | `src/platform/config/config.go` | `OTel` group gains `Producer`, `Domain`. Required only when the exporter is on |
| 2 | `src/platform/telemetry/telemetry.go` | `Init(cfg)` — Resource, tracer provider, OTLP exporter, shutdown |
| 3 | A `SpanProcessor` | Stamps `span_uuid` at `OnStart`. `observedTimeUnixNano` is set by the middleware before `End()` — `OnEnd` is read-only |
| 4 | `src/platform/middlewares/trace.go` | Allocates the observation record if nothing above it has, starts the span, sets the request-side `http.*`, joins an inbound `traceparent`, and — after `next` returns — projects the status off the record before `End()` |
| 5 | `correlate()` in `envelope.go` | Names the span, sets `sender.id` / `sender.unverified` / `recipient.id` / `beckn.*` |
| 6 | The two controllers | Call `record()` at the three points above — each at the moment it happens |
| 7 | `response_writer.go` | The `error` event, from the fault it already has |
| 8 | `request_logger.go` | Adopts the record rather than allocating when `Trace` is above it, and records the status as a fact — the one thing the span cannot see for itself |

**Controllers never import the telemetry package.** They record timestamped
facts; a projection turns those into events. **Six of the eight existing log
fields are also span attributes** — instrumenting separately would put
`error_type` in two places, which is what C1 exists to prevent.

The two that are not are deliberate, and naming them is worth more than the
count. `logger.go` defines eight fields: `request_id`, `transaction_id`,
`message_id`, `action`, `error_type`, `error_code`, `status`, `duration_ms`.

- **`duration_ms`** is the span's own `end - start`. See *No duration attribute*.
- **`request_id`** has no span attribute because the join runs the other way:
  23e puts `trace_id` and `span_id` on every log line, so an operator holding a
  span reaches the logs without the span having to carry our internal id to the
  facilitator as well.

`error_code` counts because it is on the `error` event rather than the span
attributes, which is where the C1 code belongs.

### What "propagated in and out" means here

**In** is the whole of it today: an inbound `traceparent` is joined rather than
replaced, so a caller that traces gets one timeline across the hop.

**Out** has exactly two call sites in this repository, and neither runs under
Phase 1 defaults: `src/platform/validation/http_fetcher.go`, gated off by
`EXT_ALLOW_NETWORK_FETCH=false`, and `src/indexing/embeddings/ollama.go`, unused
under `EMBEDDING_PROVIDER=noop`. 23c injects the context into both anyway —
it is a propagator call on an outbound request, and an uninstrumented client is
the kind of thing that stays uninstrumented until the day someone turns the flag
on and finds the trace stops.

Because neither runs by default, the pin is a unit test over the injection, not
an end-to-end assertion. Saying so matters: an earlier reading of "in and out"
implied an outbound protocol hop this service does not make.

**Sampling: always-on.** The facilitator's mandate is all API calls; a sampled
stream under-reports every metric derived from it.

**Pinned by a conformance test:** drive a request through an in-memory exporter
and assert, against the spec's Required list: every mandatory Resource, Scope and
Span field present; event times strictly increasing with none equal to the span's
end; `beckn.schemaContext` and `beckn.schemaType` the same length after
truncation as before it; and a deny-list over exported keys that fails if a body,
query string or IP appears. The spec ships no schema (see Divergences), so this
test **is** our conformance target — it encodes the profile, and the divergence
table is the diff a future schema would reconcile.

Note what the deny-list cannot do: it is a check over *keys*, and
`beckn.schemaContext` is a caller-supplied URI that can carry text in its query
and fragment. Bounding that value (see Span attributes) is the mitigation; the
key check is not one.

### ClickStack note

Span attributes are a queryable map; event attributes are harder to aggregate in
HyperDX. If a dashboard needs one hot — `result.empty` is the candidate —
promote that one to a span attribute too. Individually, not wholesale; the events
are the interop contract.

---

## Metrics — Task 24, and blocked

**No metric list exists yet, and one cannot be invented here.** `metric.code`
comes from a **network-level metrics registry** that OAN does not have; codes
made up locally will not match the ones a facilitator later publishes, and a
stream of unrecognised codes is worse than none. That block is the whole of
open question 7.

The only names anywhere today are the spec's own **examples** — illustrations,
not a mandate: `search_api_total_count` (unit `1`), `avg_api_response_time`,
`search_api_failure_percent` (unit `%`).

### Candidates to propose to the registry

Everything below is derivable from the spans this document specifies, so
**Task 24 needs no new instrumentation in this service** — and cannot precede
Task 23.

| Candidate | Aggregates over |
|---|---|
| discover / publish call count | span count by `beckn.action` |
| response time p50 / p95 / p99 | span duration |
| failure percent, split by category | `status = Error`, `error_type` |
| **unmet demand rate** | `result.empty` — the one metric no other participant can produce |
| degraded-mode rate, per mode | `retrieval.modes_degraded` |
| catalog freshness per provider | time since that provider's last `publish` `request_info`, keyed on `publish.bpp_ids` |
| publish volume per provider | `publish.resource_count` by `publish.bpp_ids` |
| **provider serve share** | distinct `result.provider_ids` per span — which sources actually answer, and which never do |
| per-provider failure concentration | `error_type` grouped by `result.provider_ids` / `publish.bpp_ids` |
| unattributable request share | `sender.unidentified` — the number that argues for finishing Task 6 |

Two names from the original Task 23 — `search_degraded_modes` and
`embedding_duration_ms` — were dropped as metrics by A23 and **survive as span
facts**, which is what an exporter aggregates over. Both now have a named home,
which they did not in the first draft of this document: `retrieval.modes_degraded`
and `retrieval.embedding_ms`, both on `retrieval_info`. A promise that a fact
survives, with no attribute defined to carry it, is the fact not surviving.

### Shape, when it is unblocked

- `sum` aggregation only, **non-monotonic** — the only kind this spec version allows.
- `aggregationTemporality`: `1` delta, `2` cumulative.
- Required per data point: `metric_uuid`, `observedTimeUnixNano`, `metric.code`.
  Optional: `metric.category`, `label`, `granularity`, `frequency`.
- **The same Resource attributes as the spans** — another reason `producer` and
  `domain` must be settled once and shared across signals, not decided per signal.

---

## Build order

A23 split Task 23 into six. One review gate between each.

| | Sub-task | Files | Tests pin |
|---|---|---|---|
| **23a** | Foundation and Resource | new `platform/telemetry/`; `config.go`, `container.go`, `server.go` | `OTEL_EXPORTER=none` still boots; Resource carries all five; `otlp` with `Producer`/`Domain` empty fails **at boot**. Starts no spans |
| **23b** | The observation record | `middlewares/correlation.go`, `envelope.go`, `request_logger.go`, `trace.go` | Log output byte-identical before and after. **Changes no output**; acceptance is the existing suite passing with no test file edited — including `request_logger_test.go:181,207`, which mount `RequestLogger` with no `Trace` above. Also pins the adopt-or-allocate rule from both sides: `Trace` first, and `RequestLogger` alone |
| **23c** | Span lifecycle | `middlewares/trace.go`, `correlate()`, `validation/http_fetcher.go`, `embeddings/ollama.go` | Inbound `traceparent` joined not replaced; outbound injection on the two clients; scope is ours; `http.status.code` comes off the record and matches the status actually written; a recovered panic's 500 is inside the exported span; A11's behavioural pin holds |
| **23d** | Events | `discover/controller.go`, `publish/controller.go`, `response_writer.go` | Event times strictly increasing, none equal to span end; a master publish reports `MASTER`; `error` category matches `X-Beckn-Error-Type` byte for byte; `retrieval.embedding_ms` absent — not zero — under `noop`; `result.provider_ids` is DISTINCT and bounded at 16, so a 200-catalog answer from one provider emits one id; `beckn.schemaContext` is absent rather than empty when the seeker sent no predicate |
| **23e** | Trace/log correlation | `logger/logger.go`, `trace.go` | `trace_id`/`span_id` present once a span exists, **absent not empty** when exporter is `none` |
| **23f** | Facilitator stream + redaction | `telemetry/redact.go` | **BLOCKED** on open questions 2, 3 and 9 — the plan's **O1**, **O2** and **O4**. Also where `scope_uuid` and `count` land, since both need the custom exporter this sub-task builds |

Notes that bite:

- `src/platform/telemetry/` holds a `.gitkeep`; the OTel modules are **indirect**
  in `go.mod` and the SDK and exporter modules are absent entirely. 23a adds them.
- 23c **drops the `X-Beckn-Chain: trace` header entry**, which existed only so
  Task 20's order test had something to observe. That assertion moves to the span:
  the recovered panic's 500 must be recorded *inside* it, true only if `Trace`
  wraps `Recover`. **Keep A11's behavioural pin** — one completion line at
  `status = 500` with `X-Response-Time` set — when the header pair goes.
- `trace.go:22` and `:31` still say this is "the place Task 23 puts `otelhttp`".
  A23 rejected `otelhttp`, so those two comments are wrong today and 23c must
  delete them along with the header entry and the `chainTrace` constant. A stale
  comment naming a rejected library is how the rejection gets undone by someone
  reading the file instead of the plan.
- **`Trace` stays above `RequestLogger`.** The chain order does not move. What
  changes is that the observation record is allocated by whichever of the two runs
  first — see *How the span learns the status*. Moving `Trace` below
  `RequestLogger` would also give it the status, and is the wrong fix: it puts
  `RequestLogger`'s own work outside the span and leaves 23e's `trace_id` unable
  to reach the completion line.
- Until 23f lands, **the deny-list is a rule 23a–23e comply with and nothing
  enforces.** That is the honest status.

**Verification:** `make build && make lint && make test`, output pasted, after
each sub-task.

---

## Decisions needed before 23a

| | Decision | Recommendation |
|---|---|---|
| 1 | **`scope.version`** — the spec repo has **no version tags** and `1.0` is the only value any example uses. Stamped on every span; a facilitator may key on it | **Ship `1.0`** and record that it is example-derived, not released. A constant in `telemetry.go` |
| 2 | **OTLP transport — gRPC or HTTP?** `otlptracegrpc` and `otlptracehttp` are different modules, so this is a dependency, not a config flag | **gRPC** — the OTel default for `OTEL_EXPORTER_OTLP_ENDPOINT`, which config already reads, and ClickStack's collector accepts it |
| 3 | **`App.Close()` is `func()`** — no ctx, no error — but `TracerProvider.Shutdown` needs both | Bound the flush **inside** `Close` and report failure to stderr as `Log.Sync` already does, rather than widening the signature across every caller |
| 4 | ~~**ADR-0011 contradicts A23**~~ — **DONE.** It read "traces **and metrics**" and "`otelhttp` instrumentation", both rejected by A23 | Amended in place, with an Amendments section recording what changed and why. Two committed documents disagreeing is a defect rather than a choice, so it was not left for 23a to carry |
| 5 | **Where is the deny-list enforced — in Go, or in the collector?** "Two exporters" can mean two `TracerProvider`s in-process, or one export to our local collector which fans out to the facilitator through a filter processor. The collector route is the standard OTel pattern and needs no Go code; the in-process route is the only one a Go conformance test can assert against | **Enforce in Go**, on a second exporter. A privacy rule enforced only in YAML is one a deployment can silently drop, and the doc's conformance test assumes an in-process seam. Decide before 23f — it defines 23f's scope |

## Open questions — network level

| | Question |
|---|---|
| 1 | **`sender.id` has no reliable source.** The spec requires it and treats it as an identity; this phase neither requires `senderId` nor verifies it. Telemetry that names participants, or a phase that does not verify them — both is not available. We emit the claim flagged as a claim (`sender.unverified`); what the facilitator does with a flagged join is theirs to say |
| 2 | **What is the literal `domain` string?** `Agriculture` is a placeholder. Every OAN component must emit the identical value or grouping splits. Same for the `network.id` key name — our invention, so others must be told it |
| 3 | **Is `producer` = `discovery-service`, and who keeps the participant id list?** The Sunbird registry holds *Providers*, and a DS is not one. Answering this also decides whether `producer` and `service.name` stay one value |
| 4 | **Do the adapter and experience layer forward `traceparent`?** If so, ClickStack shows one timeline across all three — the main thing a monitoring stack buys. We cannot do it alone |
| 5 | Should the adapter and experience layer emit any of Part 2 too, for consistent naming? `error` and the `beckn.*` attributes are the obvious shared ones |
| 6 | **Is a publish an AUDIT event?** It mutates a catalog and a `FULL` republish deletes resources. Modelling it as both API and AUDIT duplicates; picking one is a network call |
| 7 | **Who owns Task 24?** The thing that queries ClickHouse, shapes `resourceMetrics` and ships on a schedule does not exist. "ClickStack does it" is false — ClickHouse stores, HyperDX charts, neither exports a METRIC signal. `metric.code` also needs a registry that does not exist |
| 8 | **Per-mode retrieval timing** — worth emitting? Today a slow `lexical` and a slow `spatial` look the same in aggregate. `retrieval.embedding_ms` now covers the one phase that leaves the process; this question is what remains, and the merge step holds the per-mode results so it is cheap. Still more than the spec asks |
| 9 | **Which trace id format, and which event timestamp field, does the facilitator validate?** The spec's examples use dashed UUIDs and an ISO `time`; OTLP requires hex ids and `timeUnixNano`, which is what any OTel SDK emits (divergences 5 and 6). If the facilitator was built against the examples it will reject conformant spans — from every participant, not just us. **This needs answering before 23f, and it is the spec's bug to fix, not ours.** Carried into the plan's Open Items as **O4**, because a blocker recorded only in this document is one the plan's own blocker table does not know about |
| 10 | **Will the spec publish real schemas?** `schemas/` and `examples/` are empty placeholders. Until they are filled there is no conformance target, and every participant is interpreting prose independently — which is how five participants end up with five `domain` strings |
| 11 | **Performance "on the basic unit" — weather by location, mandi by location and crop — is asked for by the network and refused by this design.** Coordinates and `filters.expression` are both on the never-emitted list, so no per-location or per-crop breakdown is derivable today. There is a middle path this document does not yet take: the service already computes **H3 covers** (`src/indexing/`), so a coarse cell — resolution 3-4, roughly 100 km — would give location-grained performance without emitting a farmer's point. Crop is harder, because it lives inside the filter expression. **This is a policy decision, not an implementation one**, and it is recorded rather than taken: the deny-list exists for a reason and widening it is the network's call |
| 12 | **Relevance and accuracy cannot be answered from this service at all, and no attribute will fix it.** "Was it relevant?" needs to know what happened *after* the response — a select, a click, a farmer acting on it. This service is one synchronous hop that never learns the outcome; `result.empty` says a query went unmet, never that a non-empty answer was any good. Closing it needs the `select` leg (a different node — see `docs/design/registry/`) or an explicit feedback signal, either of which is a new network contract. **Named here so it is not mistaken for something Task 23 forgot** |
