# Worked example — one transaction, both emitters

One farmer question, followed across the two codebases that emit telemetry for
it: **beckn-onix** adapters and **discovery-service**. Every attribute name and
literal below is copied from source or from `opentelemetry.md`'s own example, and
cited. Where the two repos spell the same idea differently the row is marked **⚠**
and collected in §6 — those divergences, not the payloads, are the point of this
document.

**Not binding.** `opentelemetry.md` decides what we emit, and its §The seam says
where the code lives. discovery-service emits none of this yet: Task 23 is
unstarted, so our side is the *specified* output, not observed. onix's side is
observed — it is running code.

## 1. The flow, and why it is not one trace today

A farmer asks for a mandi price. OAN is **synchronous** — every hop is a
request/response on one connection, `on_discover` is a response action returned
inline in the 200 body, and async callback dispatch is out of scope
(`beckn/actions.go`'s `Action*` constants, C3).

So this *should* be one trace. It is not, and the reason is concrete:

```
consumer node (onix)                      discovery-service
────────────────────                      ─────────────────
 SERVER span "/discover"       ── HTTP ─►  SERVER span "discover"
   ├── step:validate-sign                    (no parentSpanId — nothing
   ├── step:sign                               injected traceparent)
   └── step:route             ◄── 200 ──
```

**onix has no client span and no propagation injection.** `SpanKindClient`
appears nowhere in the repo — `grep -rn 'SpanKindClient\|WithSpanKind'` returns
exactly one line, `stdHandler.go:158`, and it is `SpanKindServer`. Inbound is
extracted (`propagator.Extract`, `stdHandler.go:155`); nothing on the outbound
leg injects. The step spans (`step_instrumentor.go:58`, named `step:<name>`) are
children of the inbound server span, not of the downstream call.

That is why the network collector rewrites `trace_id` from `transaction_id`
(`otel-collector-network/config.yaml:22-25`), and its own comment says so: it
"receives one span from each node (BAP, BPP) for the same Beckn message"
(`:16-17`) — *each node*, not a parent and a child. The rewrite is the workaround
for absent propagation, and it is the only thing currently correlating a
transaction across participants.

> **This corrects a claim in `opentelemetry.md`'s I1.** That paragraph attributes
> the rewrite to async callbacks arriving with no parent. The collector's comment
> shows the real cause is narrower and worse: nothing injects `traceparent` on
> any leg, sync or not. Fixing the cause — one `otelhttp` round-tripper, or an
> inject before the outbound request — makes the rewrite unnecessary *and* makes
> per-hop timing meaningful. Until then `beckn.transactionId` is load-bearing for
> correlation, which is a stronger argument for emitting the `transaction_id`
> alias than "operator legibility in Zipkin". Open question 4 already names the
> propagation gap; worth raising with the onix side as a defect, not a preference.

## 2. Resource — where the two differ most

Both build one Resource per signal, varying only `eid`. Values: **`API`**,
**`METRIC`**, **`AUDIT`** — the log signal is *named* LOG/AUDIT and its `eid` is
`AUDIT`, never `LOG` (`otel-specification.md:271,446,599`; onix at
`otelsetup.go:122,143,159`).

| Attribute | discovery-service (specified) | onix (observed) | |
|---|---|---|---|
| `eid` | `API` | `API` | spec-Required |
| `producer` | `discovery.oan.example.org` | `cfg.Producer`, e.g. `bap.example.com` | spec-Required. The **subscriber id**, an FQDN — from `APP_SUBSCRIBER_ID` on our side |
| `domain` | `Agriculture` | `cfg.Domain` | spec-Required. **Must be byte-identical across every OAN component** or grouping splits. Open question 2 |
| `service.name` | `discovery-service` | `cfg.ServiceName` | ours is the *software* name, deliberately **not** equal to `producer` |
| `service.version` | from `-ldflags -X` | `cfg.ServiceVersion` | OP5: unstamped reports `dev`, never `""` |
| `network.id` | `mahavistar` | *absent* | ⚠ onix's `buildBaseAttrs` carries no network id (`otelsetup.go:210-232`) |
| `environment` | *absent* | `cfg.Environment` | ⚠ onix-only |
| `device_id`, `producerType` | *absent* | set | ⚠ onix-only; neither is in the spec's Required set |
| `onix.build.commit`/`.tree_state`/`.date` | *absent* | set | ⚠ onix-only, and a good idea — it spots locally-patched deployments |
| `k8s.pod.name` | via `OTEL_RESOURCE_ATTRIBUTES` | — | how we get pod identity **without** a `parent_id` span attribute |

## 3. Span — discovery-service, the one we own

Trimmed from `opentelemetry.md` §Worked example — `discover`, `semantic`
degraded; see that section for the full payload
and its four-absences discussion. Reproduced here only to sit beside onix's:

```jsonc
"name": "discover", "kind": "SPAN_KIND_SERVER",
"status": {"code": "STATUS_CODE_OK"},
"attributes": [
  {"key":"span_uuid",           "value":{"stringValue":"3fae2d5f-…"}},
  {"key":"observedTimeUnixNano","value":{"stringValue":"1756296000148000000"}},
  {"key":"sender.id",           "value":{"stringValue":"oan-experience"}},
  {"key":"sender.unverified",   "value":{"boolValue":true}},
  {"key":"recipient.id",        "value":{"stringValue":"discovery-service"}},
  {"key":"beckn.receiverId",    "value":{"stringValue":"discovery-service"}},
  {"key":"http.method",         "value":{"stringValue":"POST"}},
  {"key":"http.route",          "value":{"stringValue":"/discover"}},
  {"key":"http.status.code",    "value":{"stringValue":"200"}},
  {"key":"http.status_code",    "value":{"intValue":200}},
  {"key":"beckn.action",        "value":{"stringValue":"discover"}},
  {"key":"beckn.transactionId", "value":{"stringValue":"b6b0f1c2-…"}},
  {"key":"beckn.messageId",     "value":{"stringValue":"e2f1a3d4-…"}}
],
"events": [
  {"name":"request_info",   "attributes":[{"key":"intent.kinds", …}]},
  {"name":"retrieval_info", "attributes":[
    {"key":"retrieval.modes_run","value":{"arrayValue":{"values":[
      {"stringValue":"lexical"},{"stringValue":"fuzzy"},{"stringValue":"spatial"}]}}},
    {"key":"retrieval.modes_degraded","value":{"arrayValue":{"values":[
      {"stringValue":"semantic"}]}}}]},
  {"name":"response_info",  "attributes":[
    {"key":"result.catalog_count","value":{"intValue":4}},
    {"key":"result.provider_ids", "value":{"arrayValue":{"values":[
      {"stringValue":"imd.gov.in"},{"stringValue":"agmarknet.gov.in"}]}}},
    {"key":"result.empty",        "value":{"boolValue":false}}]}
]
```

Three details that are easy to get wrong and are pinned by 23d:

- Event names are **`request_info` / `retrieval_info` / `response_info`** —
  underscores, not dots. Dots would read as a namespace they do not have.
- **`retrieval.modes_degraded` is an array of the mode names that degraded**, not
  a boolean. `["semantic"]` says which one; `true` says only that something did,
  and the operator still has to guess which.
- `result.provider_ids` carries **two** ids against a `catalog_count` of **four**.
  Distinct providers, not one entry per catalog — the difference between an
  identity attribute and a worse copy of the count.

## 4. Span — onix consumer adapter, for contrast

Verbatim from `setBecknAttr` (`stdHandler.go:852-886`) and the deferred block at
`:192-193`:

```jsonc
"name": "/discover",                       // = r.URL.Path (stdHandler.go:157)
"kind": "SPAN_KIND_SERVER",                // always; onix has no client span
"attributes": [
  {"key":"recipient.id",   "value":{"stringValue":"discovery.oan.example.org"}},
  {"key":"sender.id",      "value":{"stringValue":"bap.example.com"}},
  {"key":"span_uuid",      "value":{"stringValue":"…"}},
  {"key":"http.request.method","value":{"stringValue":"POST"}},   // ⚠
  {"key":"http.route",     "value":{"stringValue":"/discover"}},  // ⚠ r.URL.Path, actual
  {"key":"action",         "value":{"stringValue":"discover"}},
  {"key":"transaction_id", "value":{"stringValue":"b6b0f1c2-…"}},
  {"key":"message_id",     "value":{"stringValue":"e2f1a3d4-…"}},
  {"key":"parent_id",      "value":{"stringValue":"bap:bap.example.com:pod-7d9f"}},
  {"key":"server.address", "value":{"stringValue":"discovery.oan.example.org"}},
  {"key":"user_agent.original","value":{"stringValue":"beckn-onix/…"}},
  {"key":"http.response.status_code","value":{"intValue":200}},   // ⚠
  {"key":"http.request.error","value":{"stringValue":""}},        // ⚠ empty, not absent
  {"key":"observedTimeUnixNano","value":{"stringValue":"…"}}
]
```

`sender.id`/`recipient.id` are resolved **by direction** from a configured
`selfID` and the remote id (`resolveDirection`, `stdHandler.go:840-850`), so the
same adapter's own id is the sender on a caller hop and the recipient on a
receiver hop. **discovery-service needs none of that machinery**: we only ever
receive, so `recipient.id` is a config constant and `sender.id` is always remote.

`http.request.error` is set unconditionally from `errString(err)`
(`stdHandler.go:193`), so a successful span carries the key with an empty value.
Our convention is the opposite and deliberately so — absent means "did not
happen", and an empty string is a value that has to be filtered out of every
query that touches it.

## 5. The other two signals

**Log (`eid: AUDIT`)** — ours is already-emitted zap JSON; 23e adds the two ids:

```json
{"level":"info","ts":"2026-09-08T09:00:00.148Z","msg":"request completed",
 "request_id":"01J7Z8Q2M3","transaction_id":"b6b0f1c2-…","action":"discover",
 "status":200,"duration_ms":148,
 "trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7"}
```

`trace_id` is present because a span exists; under `OTEL_EXPORTER=none` it is
**absent, not empty**. `duration_ms` lives only here — it is the span's own
`end - start`, so an attribute copy is free to disagree with it.

onix's equivalent goes through `telemetry.EmitAuditLogs`
(`stdHandler.go:199,201`) with an `audit.direction` of `request` or `response`,
and carries **`receiver.id`** — see §6, that is a bug.

**Metric (`eid: METRIC`)** — Task 25, node-operator. A *level*, not a
restatement of any span:

```jsonc
{"name":"<named by Task 25>","unit":"1","description":"Connections checked out",
 "gauge":{"dataPoints":[{
   "asDouble": 30,
   "startTimeUnixNano":"…","endTimeUnixNano":"…",
   "attributes":[{"key":"pool","value":{"stringValue":"primary"}}]}]}}
```

The name is a placeholder on purpose. `opentelemetry.md`'s Task 25 row describes
the instruments — "pool acquire-wait and in-use", one liveness gauge — but
**names none of them**, and inventing a name here would create a second source of
truth for it. Naming them is 25's job and `Instrument.Name` is where the name
will live. `asDouble` and the two collection timestamps are not registry rows: a
table declaring them would be a table declaring the clock. The single label is
`Bounded` over a closed value set, which is what the `Signals&Label ⇒ Bounded`
check enforces. **`metric.code` is absent** — it comes from a network metrics
registry OAN does not have (open question 7), which is why `Instrument.Code`
empty is legal for a node metric and fatal for a facilitator one.

## 6. Divergences this example exposes

The recipient row is already recorded — `opentelemetry.md` §Four more, each with
a named owner outside this repo, item 4, and
onix's `OBSERVABILITY.md:352-363`. The rest are new here.

| | discovery-service | onix | Who should move |
|---|---|---|---|
| **recipient, across signals** | `recipient.id` everywhere | `recipient.id` on span (`stdHandler.go:855`) and metric (`http_metric.go:102`), **`receiver.id` on the audit log** (`:199,201`) | **onix — this is a defect.** One value, two keys, so a trace↔log join on the recipient silently returns nothing |
| HTTP method | `http.method` | `http.request.method` | **onix.** The spec's mandatory profile says `http.method`; onix is on current semconv, defensible alone but no single query spans both |
| HTTP status | `http.status.code` (string) + `http.status_code` (int) | `http.response.status_code` (int) | Neither cleanly. Ours is the spec's own self-contradiction (divergence 3); onix's is semconv |
| `http.route` | route **template** (`r.Pattern`) | `r.URL.Path`, the actual path | Nobody urgently — they coincide while paths are literal, and part the moment either side adds a path parameter, at which point onix's label is unbounded |
| Beckn action | `beckn.action` **and** `action` | `action` | Ours is the alias pair; harmless |
| error on success | key absent | `http.request.error: ""` | **onix**, cheaply — one `if err != nil` |
| `parent_id` | not emitted | `role:subscriberID:pod` | **Neither.** Ours goes on the Resource as `k8s.pod.name`; onix's is per-span and constant for the process |
| `network.id` | on the Resource | absent | **onix**, once OAN runs more than one network |
| trace continuity | will inject and extract | extracts inbound, **injects nowhere** | **onix.** §1 — the root cause of the collector's `trace_id` rewrite |

The first row is the most useful thing in this document, because it is the
argument for the seam (`opentelemetry.md` §The seam) stated as an observed fact
rather than a
prediction. onix names the recipient in three places and got two of them the
same. There is no registry: `AttrRecipientID` is a `pkg/telemetry` constant
(`pluginMetrics.go:49`) used by the span and metric paths, while the audit path
passes a bare `auditlog.String("receiver.id", …)` literal — so the one call site
that did not import the constant drifted, and nothing failed. Under the seam
design that is one `Definition` with `SpanKey`, `LogKey` and `MetricKey`, and the
completeness check turns a missing or misspelled projection into a build failure
instead of an empty query result.

The method row costs something today: an operator asking "all POSTs that failed"
needs `http.method` **or** `http.request.method` depending on which participant
emitted the span. That belongs in the vendored
`tests/testdata/cross-layer-attributes.json` fixture and is not in it — because
`http.method` was never thought of as a join key. **It should be added, together
with the recipient key.**
