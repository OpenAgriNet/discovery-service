# Telemetry

What this service emits, what it deliberately does not, and where each piece is
generated. Every payload below was captured from a running local stack
(`make telemetry`), not transcribed from the design.

## What exists today

The Beckn network telemetry specification names three signals. This service is
not in the same place on all three, and the difference is the first thing worth
knowing:

| Signal | Status today | Where it goes |
|---|---|---|
| **TRACE** | Complete. One span per protocol request, `eid: API` | OTLP/gRPC to a collector |
| **METRIC** | Two node-operator instruments, plus two streams *derived* from the traces. The network METRIC signal — the one carrying `metric.code` — is **not emitted** | OTLP/gRPC, then a Prometheus scrape endpoint |
| **LOG** | Structured zap JSON on stdout, carrying `trace_id` and `span_id` | stdout. **Not** OTLP — the collector has no logs pipeline |

Nothing is exported unless `OTEL_EXPORTER=otlp`. The default is `none`, which
builds a provider that records nothing and reaches no network, so a deployment
with no collector still boots and answers requests normally.

## Traces

### One span per request, and only for the two API routes

`/healthz` and `/readyz` are excluded on purpose — their chain in
`src/app/router.go` is `RequestID → Recover` and nothing else, so a kubelet
polling every two seconds does not become the bulk of the trace stream.

Every transaction is synchronous, so one request is one span with no children of
its own. `on_discover` is the body of the same HTTP response, not a second call.

The span is **started under the route** and **renamed to the action** at End,
because the action is inside the request body and is not known when the
middleware runs. A request refused before the action is parsed therefore keeps
the route as its name — you will see both `discover` and `/discover` in a scrape,
and the second is not a bug.

### The Resource — who this process says it is

```
Resource attributes:
     -> build.commit: Str(unknown)
     -> build.date: Str(1970-01-01T00:00:00Z)
     -> build.tree_state: Str(unknown)
     -> domain: Str(Agriculture)
     -> eid: Str(API)
     -> network.id: Str(local-network)
     -> producer: Str(discovery-service.local-network.oan)
     -> service.name: Str(discovery-service)
     -> service.version: Str(dev)
InstrumentationScope discovery_service 1.0
```

`producer` / `domain` / `network.id` is the participant triple, and the boot
**refuses** to start when the exporter is `otlp` and any of them is missing or
unrecognised. A span reaching a facilitator with an empty `producer` lands under
no participant, which is worse than no span at all: an empty string in a grouping
column reads as data rather than as an absence. `domain` is checked against the
declared sector list rather than a literal, so a typo fails the boot instead of
quietly producing a second group of one.

The `build.*` triple and `service.version` read `dev` / `unknown` above because
this was a local `make telemetry` build. A release stamps them through
`-ldflags -X`. They are Resource attributes and never metric labels — see
[Metrics](#metrics) for why that distinction is load-bearing.

The instrumentation scope is **ours** (`discovery_service 1.0`). This is why
`otelhttp` is not used to start the span: the scope is fixed at span creation, so
a span that library started would carry that package's scope for ever, where the
network spec has repurposed `scope.version` to mean the specification's version.

### A publish span

```
    Name           : publish
    Kind           : Server
    Start time     : 2026-09-09 16:36:12.326344468 +0000 UTC
    End time       : 2026-09-09 16:36:12.346738718 +0000 UTC
    Status code    : Unset
Attributes:
     -> span_uuid: Str(6d5b21c0-5d78-45cc-bec2-02098d5bbb7e)
     -> http.method: Str(POST)
     -> http.host: Str(localhost:8080)
     -> http.route: Str(/publish)
     -> http.scheme: Str(http)
     -> http.flavor: Str(1.1)
     -> recipient.id: Str(discovery-service.local-network.oan)
     -> beckn.transactionId: Str(3f9a1c62-4d5e-4a7b-9c8d-1e2f3a4b5c6d)
     -> transaction_id: Str(3f9a1c62-4d5e-4a7b-9c8d-1e2f3a4b5c6d)
     -> beckn.messageId: Str(8b7c6d5e-4f3a-42b1-a0c9-d8e7f6a5b4c3)
     -> message_id: Str(8b7c6d5e-4f3a-42b1-a0c9-d8e7f6a5b4c3)
     -> beckn.action: Str(publish)
     -> sender.id: Str(weather.karnataka.example.org)
     -> sender.unverified: Bool(true)
     -> beckn.version: Str(2.0.0)
     -> beckn.receiverId: Str(discovery.local-network.oan)
     -> http.status_code: Int(200)
     -> http.status.code: Str(200)
     -> observedTimeUnixNano: Str(1788971772346698134)
Events:
SpanEvent #0
     -> Name: request_info
     -> Attributes::
          -> publish.catalog_count: Int(1)
          -> publish.resource_count: Int(3)
          -> publish.offer_count: Int(2)
          -> publish.validity_present: Bool(true)
          -> publish.provider_ids: Slice(["weather.karnataka.example.org"])
          -> publish.catalog_types: Slice(["REGULAR"])
          -> publish.update_modes: Slice(["MERGE"])
          -> publish.visible_to: Slice(["local-network"])
```

Things in there that are decisions rather than accidents:

- **`beckn.action` is `publish`, though the request said `catalog/publish`.**
  Telemetry folds the two spellings to one so the attribute stays bounded and
  countable. The wire response still answers in the caller's own spelling.
- **`transaction_id` duplicates `beckn.transactionId`** — and `message_id`
  duplicates `beckn.messageId`. Two aliases, deliberately: one spelling is the
  Beckn envelope's, the other is what beckn-onix emits, and a join across the two
  repos needs both present. A test asserts the pair carries equal values so they
  cannot drift.
- **`sender.unverified: true`.** The caller supplied a `senderId` and nothing has
  checked it. Signature verification is parked, so an unqualified `sender.id`
  would hand the facilitator's only cross-participant join a string the caller
  chose. Where no `senderId` arrives at all — every fixture in `examples/` for
  discover — the span instead carries `sender.unidentified: true` and no
  `sender.id`.
- **`recipient.id` is self-declared**, read from this node's own configured
  identity, not echoed from `context.receiverId`. Both are emitted:
  `recipient.id` is what is true, `beckn.receiverId` is what the caller claimed.
  They differ exactly when someone addresses us wrongly, which is the case
  telemetry exists to surface.
- **`http.status.code` is a string and `http.status_code` an int.** The spec
  contradicts itself here; both are sent so either reader works.
- **`http.route` is the route template**, never the URL. `/publish` is a bounded
  value and a URL is not, and a URL would carry any query string the caller wrote
  into an always-on export.

### A discover span

```
    Name           : discover
    Kind           : Server
    Status code    : Unset
Attributes:
     -> span_uuid: Str(08d0acdc-7cef-4165-8f2d-417d3219ce64)
     -> http.route: Str(/discover)
     -> recipient.id: Str(discovery-service.local-network.oan)
     -> beckn.transactionId: Str(a1000000-0000-4000-8000-000000000003)
     -> beckn.action: Str(discover)
     -> beckn.version: Str(2.0.0)
     -> beckn.schemaContext: Slice(["https://schemas.openagrinet.global/schema/WeatherAdvisoryCapability/v0.1/context.jsonld"])
     -> beckn.schemaType: Slice(["openagrinet:WeatherAdvisoryCapability"])
     -> result.empty: Bool(false)
     -> http.status_code: Int(200)
     -> http.status.code: Str(200)
     -> sender.unidentified: Bool(true)
Events:
SpanEvent #0
     -> Name: request_info
     -> Timestamp: ... .256624637
     -> Attributes::
          -> intent.kinds: Slice(["textSearch"])
          -> intent.scoped: Bool(false)
SpanEvent #1
     -> Name: retrieval_info
     -> Timestamp: ... .259508928
     -> Attributes::
          -> retrieval.modes_run: Slice(["lexical","fuzzy"])
          -> retrieval.modes_degraded: Slice(["semantic"])
SpanEvent #2
     -> Name: response_info
     -> Timestamp: ... .259552387
     -> Attributes::
          -> result.catalog_count: Int(1)
          -> result.provider_ids: Slice(["weather.karnataka.example.org"])
          -> result.empty: Bool(false)
```

(Correlators, `http.*` and `span_uuid` elided above where they repeat the publish
span verbatim.)

**Attribute or event?** The rule is one line: *true for the whole request → span
attribute; produced at a point during processing → event.* Each event stamps its
own time when it occurs, so the deltas between them are the phase breakdown.
Above, `request_info → retrieval_info` is 2.88 ms of retrieval and
`retrieval_info → response_info` is 43 µs of assembling the answer. Batch the
events at handler exit and every timestamp collapses to the same value: the total
stays right, the dashboard keeps rendering, and the breakdown silently becomes
zeros.

Three things worth calling out:

- **`retrieval.modes_degraded: ["semantic"]`** is the honest report that semantic
  search did not run. `EMBEDDING_PROVIDER` defaults to `noop`, so this is what
  every Phase 1 deployment looks like. The same list is on the response's
  `X-Beckn-Degraded` header. A fourth attribute, `retrieval.embedding_ms`, appears
  on this event **only when an embedding was actually computed** — absent, not
  zero, under `noop`.
- **`result.provider_ids`** is the answer to *whose data answered this query*.
  Without it, four network questions have no substrate: which provider served a
  query, which provider's data is never served, per-provider volume, and whether
  failures concentrate on one source. It is DISTINCT and bounded at the first 16,
  with `result.providersTruncated` when the bound bites — otherwise an answer of
  200 catalogs from one provider would turn an identity attribute into a
  page-size measurement.
- **`result.empty`** is the single most valuable thing this service tells the
  network: somebody asked, and nobody serves it. It is also the one attribute
  promoted from an event onto the span, purely so the metrics connector can see
  it — see below.

### Refusals

A refused request produces a span like any other, plus an `error` event:

```
     -> error_type: Str(DOMAIN)
     -> http.status_code: Int(400)
SpanEvent #1
     -> Name: error
     -> Attributes::
          -> type: Str(DOMAIN)
          -> code: Str(SCH_INVALID_JSONPATH)
          -> msg: Str(filters.expression is not a valid SQL/JSON path)
          -> path: Str($.message.intent.filters.expression)
```

The event is projected from the same fault the NACK is built from, so the span,
the log line and the response body cannot disagree about the category.

Note `Status code: Unset` on that 400. The span status moves only at 5xx, on the
reasoning that a 400 is the caller's mistake and counting it as a server error
reports how often this service broke when it did not. **This has a direct
consequence for dashboards** — see the next section.

## Metrics

Two different things arrive at `localhost:8889/metrics`, from two different
places.

### Derived from the spans — instrumented nowhere in Go

```
discovery_calls_total{beckn_action="discover",domain="Agriculture",eid="API",
  error_type="none",network_id="local-network",
  producer="discovery-service.local-network.oan",result_empty="false",
  service_name="discovery-service",span_kind="SPAN_KIND_SERVER",
  span_name="discover",status_code="STATUS_CODE_UNSET"} 45

discovery_calls_total{...,error_type="none",...,result_empty="true",...} 8
discovery_calls_total{...,error_type="DOMAIN",...,span_name="discover",...} 9
discovery_calls_total{...,error_type="none",...,span_name="publish",...} 3

discovery_duration_milliseconds_bucket{...,le="25"} ...
discovery_duration_milliseconds_count{...} 9
discovery_duration_milliseconds_sum{...} 7.899624
```

Call count and latency, the two numbers most often asked for, come from the
collector's `span_metrics` connector reading the trace stream. A span already
carries a start, an end, a status and `beckn.action`; nothing in Go needs to
count anything. That is the whole reason there is no request counter in this
codebase — a counter restating what a span already says is a second source of
truth that can be wrong.

**Compute the error rate from `error_type != "none"`, never from
`status_code`.** Because the span status moves only at 5xx, every 4xx refusal
arrives as `STATUS_CODE_UNSET`, indistinguishable from a success. A panel derived
from the status alone would report a 0% error rate on a service refusing every
request it receives — the one failure mode such a panel exists to catch. The
dimension carries `default: none` so the label is present on successful spans
too, rather than splitting one stream in two.

`result_empty` carries **no** default, and that asymmetry is intentional. Absent
is a third meaningful state: a publish has no result to be empty, and a discover
that errored before responding has none either. A query for unmet demand must say
`result_empty="true"`, not `!= "false"`.

Three labels are excluded on purpose. `collector.instance.id` is a UUID
regenerated on every collector restart, and only four Resource attributes are
promoted to labels — `producer`, `domain`, `eid`, `network.id`. Promoting the
whole Resource, which is the obvious setting, was measured here and is a
cardinality bug: it makes `build_commit`, `build_date`, `build_tree_state` and
`service_version` into labels, all four change on a deploy, and a `rate()` across
a deploy then sees a counter reset.

### Emitted by the binary — two node-operator instruments

```
pgxpool_empty_acquire_total{domain="Agriculture",eid="API",
  network_id="local-network",producer="discovery-service.local-network.oan"} 0
pgxpool_empty_acquire_wait_time_nanoseconds_total{...} 0
```

These are the entire METRIC output of the Go process, and the bar for adding a
third is stated as a question: **which layer below us is blind to this number?**

| Layer | Already emits | Blind to |
|---|---|---|
| kubelet / cAdvisor | the container's resource envelope; ready, restarts, OOM kills | anything inside the application |
| `postgres_exporter` | the server's state — connections, locks, statement latency | anything that never arrived |
| this process | — | — |

Connection-pool queueing happens inside this process, before any syscall:
Postgres never sees a statement that was not sent, and it rises *before* anything
fails. Liveness went to kubelet, pool utilisation to `pg_stat_activity`, and
rate/errors/duration to the connector above. What was left is this pair, sampled
off `pgxpool.Stat()` on a clock — not new bookkeeping on the request path.

A pair rather than a histogram, and that is a constraint rather than a
preference: pgxpool exposes only cumulative totals, so a real distribution would
mean wrapping every `Acquire` on the hot path. Divide one rate by the other for
mean wait per acquire.

### What is not a metric

`discovery_calls_total` is deliberately **not** named `discover_api_total_count`.
That shape is a `metric.code` and belongs to the network's METRIC signal, which
this is not and cannot be — the spec permits only non-monotonic sums, and this
connector emits a monotonic counter and a histogram. Naming a node-local operator
stream as though it were a registered network metric is how an unregistered code
reaches a facilitator.

## Logs

```json
{"level":"info","ts":1788971772.3465807,"caller":"middlewares/request_logger.go:121",
 "msg":"request completed","request_id":"JNFMPKMXBA6RCKWLMO4BIJGOF4",
 "trace_id":"e511b1393638969bc0da8b2fa651b088","span_id":"bf03d9acfca7ac79",
 "transaction_id":"3f9a1c62-4d5e-4a7b-9c8d-1e2f3a4b5c6d",
 "message_id":"8b7c6d5e-4f3a-42b1-a0c9-d8e7f6a5b4c3",
 "action":"publish","status":200,"duration_ms":20.148}
```

And the same line for a refusal, which adds two fields:

```json
{"level":"info","msg":"request completed","action":"discover",
 "error_code":"SCH_INVALID_JSONPATH","status":400,"duration_ms":1.118,
 "error_type":"DOMAIN", ...}
```

One line per request, at completion. `trace_id` and `span_id` are present exactly
when a span exists and **absent rather than empty** when the exporter is `none` —
an empty string in a correlation field is a join that silently matches everything
else that also failed to get one.

Logs are stdout only. Whatever collects container output collects these; the OTel
collector in this repo has no logs pipeline.

## How it is designed

### One table, three projections

Every attribute is a row in a registry (`src/platform/telemetry/fact/`) — 54 of
them today. A row says what the attribute is called on a span, what it is called
in a log field, which signals may carry it, its Go kind, whether its value set is
bounded, and why it exists.

The projections read that table. `telemetry/traces.go` projects to span
attributes and events, `logger/fields.go` projects to zap fields, and
`fact/instruments.go` names label keys from it. **Changing an attribute is one
edit.** Adding one to a signal is a bit in the row's `Signals` mask, not a change
in three files that can drift apart.

The table is also where the guards live, so that constraints are enforced rather
than remembered:

- A key used as a metric label must be `Bounded` and must have a declared value
  set — a key reaching a label without that bit has bypassed the cardinality
  review the bit exists for.
- An instrument's labels are multiplied out and checked against a ceiling. The
  accident is multiplicative: four labels can each be honestly bounded and still
  produce 800 streams.
- No row may name an OTLP structural field (`status`, `kind`, `traceId`) — those
  are fields on the protobuf message, not attributes, and no registry row can
  reach them.

The one apparent asymmetry is deliberate: the log projection lives in
`src/platform/logger/`, not in `telemetry/`. Its only caller is
`middlewares/request_logger.go`, which `tests/architecture/boundary_test.go`
deliberately keeps off the OTel SDK allow-list. A `telemetry/logs.go` would need
an exemption from the very guard that exists to prevent it.

### Shape or a count, never a value

Nothing that leaves for the facilitator carries user data:

- the `textSearch` string — a farmer's query
- `filters.expression` — only its declared grammar (`intent.filter_type`)
- spatial coordinates — a query location is a location
- resource or offer content
- request and response bodies, client IP, `X-Forwarded-For`, user agent

The spec's own example puts `reqBody` and `resBody` on events. We do not. That is
the test for the next attribute someone wants to add: is it a shape or a count,
or is it a value?

The cost is real and is accepted — `result.empty` says unmet demand *happened*,
not *what for*. That half is recoverable in a node-local backend, where the query
text may go because the data does not leave the operator's own infrastructure.
The deny-list is scoped to the facilitator stream specifically; asserting it on
both would forbid the local analysis the split exists to permit.

### Where the code is

| File | Owns |
|---|---|
| `src/platform/telemetry/provider.go` | Boots the SDK, both OTLP exporters, the propagator |
| `src/platform/telemetry/traces.go` | Spans whole — the Resource, the participant triple, span attributes, events, the `span_uuid` processor |
| `src/platform/telemetry/metrics.go` | The two instruments and their sampling callback |
| `src/platform/telemetry/fact/` | The attribute registry: `definition.go` (what a row is and the rules it must satisfy), `registry.go` (the table), `record.go` (what one request observed), `instruments.go` (the metric instruments) |
| `src/platform/middlewares/trace.go` | Starts the span, joins an inbound `traceparent`, renames to the action, sets the status |
| `src/platform/logger/fields.go` | The log projection |
| `src/discover/controller.go`, `src/publish/controller.go` | The three events each path emits |
| `otel/collector.yaml` | The connector, the dimensions, the scrape endpoint |

## Configuration

| Variable | Default | Does |
|---|---|---|
| `OTEL_EXPORTER` | `none` | `none` or `otlp`. `none` records nothing and reaches no network |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | — | Required when the exporter is `otlp` |
| `APP_SUBSCRIBER_ID` | — | Required when the exporter is `otlp`. Becomes the Resource's `producer` and every span's `recipient.id` |
| `APP_DOMAIN` | — | Required when the exporter is `otlp`, and must be a sector this build declares (`Agriculture`) |
| `APP_NETWORK_ID` | — | The Resource's `network.id`; required always, for reasons that predate telemetry |

`none` demands nothing, because a deployment with no collector must not answer
for telemetry it does not emit. `otlp` demands the whole identity, because a
stream attributed to no participant is not worth exporting. The asymmetry is the
whole of the validation.

## Trying it

```
make telemetry          # the stack plus a collector, with the service exporting
make verify             # traffic to describe
make telemetry-metrics  # the derived streams
make telemetry-logs     # the collector's view of the spans
make telemetry-down
```

`make telemetry-metrics` polls rather than scraping once, deliberately.
`discovery_calls_total` reads 0 for about a minute after the collector starts
even though the requests were served: a cumulative counter needs a second data
point before the endpoint has a total to report. The histogram's `_count` beside
it is correct immediately, which is what the target waits on.

The local stack has no trace backend, so the collector logs spans to its own
stdout instead of shipping them. A real deployment replaces one exporter line and
keeps everything else.

## Deferred

| | What | Blocked on |
|---|---|---|
| 23f | The separate facilitator stream and its redaction, plus `scope_uuid` and `count`, plus rewriting the OTLP enums the spec spells differently | Three open questions with the spec owners |
| 24 | The network METRIC signal — twelve candidate codes are written up as a proposal to send outward | OAN has no network-level metrics registry, and `metric.code` must come from one. Codes invented locally will not match what a facilitator later publishes |
| 26 | A byte-level conformance test asserting the deny-list over the exported payload rather than over the code that builds it | Follows 23f, which builds the thing under test |

Ten places where this service knowingly differs from the network telemetry
specification are collected in one table in
[`design/opentelemetry.md`](design/opentelemetry.md). Most are serialisation:
OTLP requires hex trace ids where the spec's examples show dashed UUIDs, and
`status` and `kind` are enums on the protobuf message that no attribute can
reach. One is a genuine value divergence (`http.route`), and one is a judgement
call between the spec's prose and beckn-onix's implementation (`recipient.id`).

## Where next

- [`design/opentelemetry.md`](design/opentelemetry.md) — the binding design: every
  attribute, the reasoning, the divergence table, the open questions
- [`design/telemetry-seam.md`](design/telemetry-seam.md) — the attribute registry
  and its rules, in detail
- [`design/telemetry-examples.md`](design/telemetry-examples.md) — a worked OTLP
  payload for one transaction across this service and beckn-onix, and where the
  two repos' attribute names diverge
- [`publish-and-discover.md`](publish-and-discover.md) — the two APIs these spans
  describe
