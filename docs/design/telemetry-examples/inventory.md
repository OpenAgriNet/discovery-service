# Signal inventory — everything both components emit, in one place

The complete list of traces, logs and metrics for **discovery-service** and the
**beckn-onix adapters**, so an implementer can work from one file instead of
three. Every row is either cited to source (onix — running code) or to
`opentelemetry.md` (ours — specified, Task 23 unstarted).

**This file is a projection, not a decision.** `opentelemetry.md` decides what we
emit, `telemetry-seam.md` where the code lives, and the plan owns task shape. If
this file and one of those disagree, they win and this file is stale. It exists
because "what are we generating?" is a question you should be able to answer
without reading 1,200 lines.

Legend: **[S]** span attribute · **[E]** event attribute · **[L]** log field ·
**[M]** metric label · **[R]** Resource attribute.

---

## 1. discovery-service (specified, Task 23)

### 1.1 Resource — all three signals

`eid` is the only attribute that varies per signal: **`API`** on spans,
**`METRIC`** on metrics, **`AUDIT`** on log records. The signal is *named*
LOG/AUDIT and its `eid` is `AUDIT`, never `LOG`.

| [R] | Value | Required by | Task |
|---|---|---|---|
| `eid` | `API` / `METRIC` / `AUDIT` | spec | 23a |
| `producer` | `APP_SUBSCRIBER_ID` — an FQDN, `discovery.oan.example.org` | spec | 23a |
| `domain` | `Agriculture`. **Byte-identical across every OAN component** or grouping splits (open question 2) | spec | 23a |
| `service.name` | `discovery-service`. **Not** `producer` — this names the software, `producer` names the participant | semconv | 23a |
| `service.version` | `-ldflags -X`; unstamped reports `dev`, never `""` (OP5) | semconv | 23a |
| `network.id` | `APP_NETWORK_ID` — `mahavistar` (C8) | ours | 23a |
| `k8s.pod.name`, `k8s.namespace.name` | `OTEL_RESOURCE_ATTRIBUTES` from the chart's downward API. **No code.** This is where pod identity lives instead of a per-span `parent_id` | semconv | chart |

### 1.2 Trace — two spans, one per protocol action

Span `name` is the Beckn action: **`discover`** or **`publish`**. Kind is always
`SPAN_KIND_SERVER` — we only ever receive.

**Identity and correlation [S]** — 23b

| Attribute | Notes |
|---|---|
| `sender.id` | `context.senderId` when present. Unverified in Phase 1 |
| `sender.unidentified` | `true` when the envelope carried no `senderId`; `sender.id` then **omitted**, never invented |
| `sender.unverified` | `true` whenever `sender.id` is set — Task 6 is parked |
| `recipient.id` | `APP_SUBSCRIBER_ID`, same value as `producer`. **Ours, never the caller's `receiverId`** |
| `recipient.unidentified` | `true` when `APP_SUBSCRIBER_ID` is unset. **Never fall back to `context.receiverId`** |
| `beckn.receiverId` | `context.receiverId`. Emitted **beside** `recipient.id`: the two disagreeing is a misaddressing signal, unaskable if one field held both |
| `beckn.transactionId` + `transaction_id` | Alias pair. Load-bearing today — nothing else joins our span to the adapter's (§3) |
| `beckn.messageId` + `message_id` | Alias pair |
| `span_uuid` | Per span, from a `SpanProcessor.OnStart` |
| `observedTimeUnixNano` | Unix nanos **as a string**. Set in the middleware just before `span.End()` — `OnEnd` gets a `ReadOnlySpan` and cannot set attributes |
| *`parent_id`* | **Not emitted.** onix-local; pod identity goes on the Resource |

**HTTP [S]** — 23b

`http.method` · `http.host` · `http.route` (`r.Pattern`, method prefix stripped) ·
`http.scheme` · `http.flavor` · `http.status.code` (**string**, following the
spec's examples over its own structure) · `http.status_code` (**int**, for
ClickStack). `http.user_agent` and `http.server_name` are deliberately **not**
emitted — user agent is on the deny-list.

**Classification [S]** — 23b

`beckn.action` + `action` · `beckn.version` · `beckn.networkId` ·
`beckn.schemaContext` (bounded, entry order preserved) · `beckn.schemaType`
(parallel and same-length with it) · `beckn.schemaTruncated` · `error_type`.

> On discover, `beckn.schemaContext` + `beckn.schemaType` are **what the seeker
> is asking for** at capability granularity — `WeatherObservation`, `MandiPrice`.
> The one question about query content this design answers without emitting query
> content. Absent ≠ empty: absent means no schema predicate at all, which is the
> "seeking anything" bucket and probably a large one.

**Events [E]** — 23d. Each stamped **when it occurs**; batching at handler exit
collapses every timestamp and the phase breakdown silently becomes zeros.

| Event | Attributes |
|---|---|
| `request_info` (discover) | `intent.kinds` · `intent.filter_type` · `intent.spatial_ops` · `intent.scoped` |
| `retrieval_info` | `retrieval.modes_run` · `retrieval.modes_degraded` (**array of degraded mode names, not a bool**) · `retrieval.embedding_ms` (**present only when one was computed** — absent under `noop`, i.e. every Phase 1 deployment) |
| `response_info` | `result.catalog_count` · `result.provider_ids` (**distinct**, bounded at 16) · `result.providersTruncated` · `result.empty` |
| `request_info` (publish) | `publish.provider_ids` · `publish.catalog_count` · `publish.resource_count` · `publish.offer_count` · `publish.update_modes` · `publish.catalog_types` · `publish.visible_to` · `publish.validity_present`. **Fires at intake, before the A1 MASTER refusal** |
| `error` | `type` · `code` · `msg` · `path` — **unprefixed**, unlike the span's `error_type` |

Event names use **underscores**: `request_info`, `retrieval_info`,
`response_info`. `error` is bare.

**Error taxonomy** — `errors.TypeOf` splits on the code prefix
(`beckn_error.go`'s `TypeOf`): `CTX_`→`CONTEXT`, `AUT_`→`CORE`,
`SCH_`/`BIZ_`/`DOM_`→`DOMAIN`, `POL_`→`POLICY`, else `SYSTEM`. The event's `type`
must match the `X-Beckn-Error-Type` header byte for byte; 23d asserts it.

### 1.3 Log / AUDIT — already emitted, 23e adds two fields

Existing zap JSON: `level` · `ts` · `msg` · `request_id` · `transaction_id` ·
`message_id` · `action` · `status` · `duration_ms` · `error_type` · `error_code`.
23e adds **`trace_id`** and **`span_id`**, and nothing else.

- `trace_id` is present only when a span exists; under `OTEL_EXPORTER=none` it is
  **absent, not empty**. The logger↔registry bidirectional check makes this
  structural.
- **`duration_ms` lives only here.** On a span it would be a second, worse copy
  of `endTime - startTime`.
- `request_id` correlates one process's logs; `trace_id` correlates across
  participants. Two ids for two scopes, deliberately.
- Projection lives in `src/platform/logger/project_log.go` so package `logger`
  never imports OTel.

### 1.4 Metrics — Task 25, node-operator only

**Three instruments, and `opentelemetry.md` names none of them** — naming is 25's
job and `Instrument.Name` is where the name goes.

| What | Kind | Label | Answers |
|---|---|---|---|
| DB pool connections in use | gauge | `pool` (Bounded) | OP3 — the pool at 30 of 32 for ten minutes while every request succeeds |
| DB pool acquire-wait | histogram | `pool` | OP3 |
| Liveness | gauge | — | Held to the standard of beating `/readyz` at something |

Observed under a pool **deliberately sized to 1** in test. Registered under our
own scope, never the global meter. Every label is a `fact.Key` carrying the
`Label` bit with a `Bounded` value set whose product is under the per-instrument
ceiling.

**No rejection counters.** A 429 and a body-ceiling refusal each already produce
a span, a status and an `error` event — `Trace` is index 1 in
`router.go:134-141`, above both `RateLimit` and `Envelope`. A counter restating
them is the `duration_ms` mistake one signal up. **OP6 and OP10 are struck** as
derivable from publish spans.

**And no request counters.** Discover and publish call counts, success/failure
split and latency percentiles are *not* in this table, and the reason is a
mechanism rather than a preference: the collector's **`spanmetrics` connector**
consumes the trace stream and emits exactly those, with `dimensions` set to
`beckn.action` and the span status. That is a `connectors:` block plus one
pipeline in `node/otel-collector-bap/config-full.yaml` — **no Go, no fourth
instrument, no `fact.Instrument` row**. Every prerequisite is already deployed:
each collector in `beckn-onix/install/network-observability` runs
`otel/opentelemetry-collector-contrib` (the connector is contrib, not core), and
`config-full.yaml` already exports `metrics/app` to `prometheus` at `:8889`
with `prometheus-node` and `grafana-node` alongside it in
`docker-compose.with-telemetry.yml`. It cannot run before 23c and 23d, because
it aggregates over attributes those sub-tasks put on the span.

Three ways to get that wrong: the connector's default stream names have been
renamed across releases and every config here pins `:latest`, so set `namespace`
and treat those names as the contract; the derived stream stays **node-local**,
because `filter/network_metrics` drops every metric not named
`onix_http_request_count` — correct, since this is an operator dashboard and not
the spec's `METRIC` signal, so do not widen the filter to admit it; and each
`dimensions` entry multiplies series exactly as a `Label` bit does but sits in
YAML where `fact`'s guards cannot see it, so every dimension must name a key
already marked `Bounded`. `sender.id` is the tempting one and the one that makes
the series count grow with the participant list — §3.4 is that same mistake
already shipped.

`metric.code` is **absent** — it needs a network metrics registry OAN does not
have (open question 7). So `Instrument.Code` empty is legal for a node metric and
fatal for a facilitator one. See §3 for what onix does instead.

### 1.5 Never emitted

`reqBody` / `resBody` (the spec's own example puts them on events; we refuse) ·
query text and descriptor content · `Catalog.provider` (`json.RawMessage`) ·
coordinates (so P6 is per-provider only, not per-place — open question 11) ·
user agent · anything from `DATABASE_URL` or a secret.

Enforced by a **deny-list over exported bytes**, not over attribute keys — so a
body cannot reach a facilitator under any key name.

---

## 2. beckn-onix adapters (observed — running code)

Same three signals, one Resource per signal at `otelsetup.go:122`/`:143`/`:159`.

### 2.1 Resource — `buildBaseAttrs`, `otelsetup.go:210-232`

`eid` · `producer` · `domain` · `service.name` · `service.version` ·
`environment` · `device_id` · `producerType` · `onix.build.commit` ·
`onix.build.tree_state` · `onix.build.date`.

**No `network.id`.** The build triple is the good idea here — it identifies a
locally-patched deployment, and is worth copying rather than diverging from.

### 2.2 Trace

**Inbound handler span** — `stdHandler.go:152-158`, `852-886`, `192-193`. Name is
`r.URL.Path`, kind always `SPAN_KIND_SERVER`.

`recipient.id` · `sender.id` (both resolved **by direction** from a configured
`selfID`) · `span_uuid` · `http.request.method` · `http.route` (`r.URL.Path`,
actual) · `action` · `transaction_id` · `message_id` · `parent_id`
(`role:subscriberID:pod`) · `server.address` · `user_agent.original` ·
`http.response.status_code` · `http.request.error` (**set unconditionally, empty
on success**) · `observedTimeUnixNano`.

**Step spans** — `step_instrumentor.go:58`, `:146`; `step.go:55`, `:70`, `:172`;
`sunbirdRegistry.go:362`, `:598`. Names: `step:<name>`, `response-step:<name>`,
`keyset`, `sign`, `validate-sign`, `registry lookup`, `cache lookup`. None sets a
kind, so all are `INTERNAL`. Naming is inconsistent across the three sites.

**Outbound call — not instrumented.** See §3.1.

### 2.3 Log / AUDIT — `stdHandler.go:199`, `:201`

Two records per request, `audit.direction` of `request` or `response`. Fields:
`audit.direction` · `http.response.status_code` · `http.request.error` ·
`sender.id` · **`receiver.id`** · plus **the full request or response body** and,
on the request record, the headers.

### 2.4 Metrics

`onix_http_request_count` (counter) with labels `http.status` (**class** —
`2xx`/`4xx`/`5xx`, correctly bounded) · `action` · `role` · `sender.id` ·
`recipient.id` · `metric_code` (computed as `<action>_api_total_count`) ·
`category` (`Discovery` when the action ends `/search` or `/discovery`, else
`NetworkHealth`) — `http_metric.go:91-112`.

Plus `onix_step_execution_duration_seconds` · `onix_step_executions_total` ·
`onix_step_errors_total` · `onix_plugin_execution_duration_seconds` ·
`onix_plugin_errors_total` · `onix_plugin_info` · `onix_routing_decisions_total`.

RED-complete for the adapter's own pipeline; **nothing on saturation** — no pool
level, no queue depth, no connection count. Same gap our Task 25 fills.

### 2.5 Collector pipeline — `install/network-observability/`

Node (`otel-collector-bap`, `-bpp`), network (`otel-collector-network`), plus
all-in-one copies. Exporters: zipkin (spans), prometheus `:8890` namespace
`onix_network` (metrics), otlphttp/loki (logs).

`transform/beckn_ids` (`network/…/config.yaml:22-25`) rewrites `span.trace_id`
from `transaction_id`. It deliberately does **not** map `message_id` to `span_id`
— the config's comment at `:15-19` explains multiple nodes share one
`message_id`, so that would give distinct spans identical ids. Correct reasoning,
and the same reasoning makes the `trace_id` rewrite wrong once propagation works.

---

## 3. What is missing or misaligned, ranked

### 3.1 No trace crosses a participant boundary (largest)

onix extracts `traceparent` inbound (`stdHandler.go:155`) and **injects it
nowhere**: `SpanKindClient` appears in no file in the repo, and the single
`WithSpanKind` call is `SpanKindServer` at `:158`. So every participant's span is
the root of its own trace, and discovery-service's span will have no
`parentSpanId` regardless of what we implement.

That is what the collector's `trace_id` rewrite is really for — not async
callbacks. Two consequences: **`beckn.transactionId` is the only cross-participant
join today**, so the alias pair is a correlation key rather than a convenience;
and **once propagation is fixed the rewrite becomes actively wrong**. Order
matters — inject first, then retire the processor. Fixing one without the other
is worse than fixing neither.

**The fix is one line and the seam already exists.** `newHTTPClient`
(`stdHandler.go:75`) is the single construction point for the protocol client —
called once at `:117` — and at `:94-98` it already composes a
`definition.TransportWrapper` around the transport. So it is either
`finalTransport = otelhttp.NewTransport(finalTransport)` at `:98`, or a
`TransportWrapper` implementation with **zero** changes to core. Wrapping
`upstream.go:203`'s client the same way closes §3.2 in the same edit. Cost: two
wrapped transports plus one dependency (`otelhttp` is not yet in onix's
`go.mod`).

### 3.2 No span around the external provider call

Nothing wraps the WeatherObservation / MandiPrice HTTP call.
`onix_plugin_execution_duration_seconds` times the *plugin*, which includes the
provider call but cannot separate provider latency from plugin overhead or a
retry. The provider node adapter is the only process that can see this. onix U1;
answers P8 and P1/P3/P6/P7 for real providers.

### 3.3 `receiver.id` vs `recipient.id` inside onix

`recipient.id` on its span (`stdHandler.go:855`, via `AttrRecipientID` at
`pluginMetrics.go:49`) and metric (`http_metric.go:102`); **`receiver.id`** on its
audit log, as a bare literal. One value, two keys — a trace↔log join on the
recipient silently returns nothing.

The cause is that there is no registry: the audit call site is the one that did
not import the constant, and nothing failed. **This is the argument for
`telemetry-seam.md` as an observed fact rather than a prediction** — under that
design it is one `Definition` with `SpanKey`, `LogKey` and `MetricKey`, and a
missing projection is a build failure instead of an empty query result. Already
recorded: seam open item 4, onix `OBSERVABILITY.md:352-363`.

### 3.4 Unbounded metric label cardinality in onix

`sender.id` and `recipient.id` as labels on `onix_http_request_count` make the
label product quadratic in participant count. Our design forbids exactly this —
a `fact.Key` with the `Label` bit must be `Bounded` over a closed value set, and
the cardinality product is asserted rather than reviewed. **Do not copy this.**
It is the concrete reason the `Signals&Label ⇒ Bounded` check exists.

### 3.5 Attribute spellings that block a single query

| Idea | ours | onix | Move |
|---|---|---|---|
| HTTP method | `http.method` (spec's mandatory profile) | `http.request.method` (current semconv) | **onix** — one line in `setBecknAttr` |
| HTTP status | `http.status.code` string + `http.status_code` int | `http.response.status_code` int | Neither cleanly; ours is the spec's self-contradiction |
| `http.route` | route **template** | actual path | Coincide while paths are literal; onix's goes unbounded the moment a path parameter appears |
| error on success | key absent | `http.request.error: ""` | **onix** — one `if err != nil` |
| `network.id` | on Resource | absent | **onix**, once OAN runs >1 network |

### 3.6 Full bodies in onix's audit logs

onix's audit records carry complete request and response payloads. We refuse
them outright. **Not a bug on either side** — but a deployment shipping both to
one backend must know one stream contains farmer payloads and needs different
retention and access control from the other.

### 3.7 For the spec owners

- Is `metric_code = <action>_api_total_count` normative or onix-local? It is
  where the spec's own `search_api_total_count` example comes from. Ask alongside
  open question 7 — otherwise two participants naming the same metric differently
  both pass.
- `recipient.id` semantics: the spec's prose ("expected to be the recipient",
  `otel-specification.md:299`) describes the *addressed* recipient; neither
  implementation reads it off the envelope. **onix resolves it by direction, not
  by self-declaration** — `resolveDirection` (`stdHandler.go:843-850`) returns
  `(selfID, remoteID)` when calling and `(remoteID, selfID)` when receiving, so
  `recipient.id` is onix's own id only on the inbound leg. Ours is constant,
  which agrees with onix in the only direction we have. An earlier version of
  this line said "both implementations self-declare" — true of us, and true of
  onix only half the time. Divergence 10, with open question 9.

  Worth naming because it is the part that needs no registry: `selfID` is
  `h.SubscriberID`, a **configured** value — `subscriberId:` in the adapter YAML,
  filled from `__NETWORK_SUBSCRIBER_ID__` in
  `helmcharts/quick-start/config/adapters/network.yaml.tmpl:71` and set in
  `.env.example:130`. Identity-of-self is config; only *verifying a remote* id
  needs the registry. §3.3's drift is a naming inconsistency, not a missing
  lookup.

### 3.8 Ours to do

Add `http.method` and the recipient key to
`tests/testdata/cross-layer-attributes.json`. It holds fifteen keys and neither
is among them, because neither was thought of as a join key.

Wire the `spanmetrics` connector into `node/otel-collector-bap/config-full.yaml`
once 23d lands — §1.4. It lives in a repo we do not own, so it is a PR against
beckn-onix rather than a sub-task here, and it is the only item on this list that
buys a dashboard without a line of Go.

---

## 4. Build order

| Sub-task | Adds |
|---|---|
| **23a0** | `src/platform/telemetry/fact/` — the registry itself, which sits in no other task |
| **23a** | Foundation and Resource; `config.go` gains `APP_SUBSCRIBER_ID` (optional, no default) |
| **23b** | Span attributes — identity, HTTP, classification |
| **23c** | The sampler decision. **Adds no attribute** |
| **23d** | Events, including `error` |
| **23e** | `trace_id` / `span_id` into the log record |
| **23f** | Exporter — the only place OTLP *structural* fields (`status`, `kind`, hex vs UUID ids) can be reshaped |
| **25** | The three node-operator metrics |
| **26** | Needs an in-memory exporter, so it depends on **23d** |
