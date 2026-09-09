# OpenTelemetry — discovery-service

What this service emits, what it never emits, the questions it has to answer,
and how Task 23 builds it.

Base is the Sunbird
[network-telemetry-spec](https://github.com/Sunbird-Obsrv/network-telemetry-spec);
the `beckn.*` attributes and the four events are ours. Monitoring stack is
ClickStack (ClickHouse + HyperDX + bundled OTel collector), which ingests OTLP
natively.

**Binding on the shape of a span.** Where this and `discover-and-publish.md`
disagree about a span, this wins; about anything else, the plan does.

**Companion.** This document is the *what*. `telemetry-seam.md` is the *where the
code lives* — the attribute registry that makes adding or renaming an attribute a
one-file edit that reaches the span, the log line and the metric label together,
and the tests that make that structural rather than remembered. It is subordinate
to this document on what an attribute means and to the plan on task shape.

`telemetry-examples.md` is the *worked payload* — one transaction rendered as
OTLP by both emitters side by side, and a table of the nine places
discovery-service and beckn-onix spell the same idea differently. It is
illustrative, binding on nothing, and cites the source line for every literal so
it can be checked rather than trusted. It is also where the cross-repo defects
found while writing it are recorded — the largest being that onix extracts
`traceparent` inbound and injects it nowhere, so no trace crosses a participant
boundary today.

## Why — the questions this must answer

The network's observability requirements, as a tree rooted at the **Registry**
with Seeker and Provider as its two branches. Transcribed here from the
requirements board (`ObservabilityRequirements.png`) so that image is no longer
the source: a requirement that lives only in a PNG on one person's desktop
cannot be grepped, diffed, or cited in a review.

**Scope warning, and it is the first thing to read.** The tree is rooted at the
Registry, so it spans the whole network; this service is one node in it. Several
Provider-branch questions ask about a provider's *own* serving path, which we
never observe — discover reads from our Postgres, so no synchronous call to IMD
or any other source happens on the request path. If an adapter fetches IMD, that
traffic does not pass through this binary and no span here will ever describe
it. Those questions belong to the adapter or the facilitator, not to Task 23.

### The tree

```
Registry
├── Seeker
│   ├── S1   What are they seeking?
│   ├── S2   Are they satisfied?
│   ├── S3   How much time did we take to respond?
│   │   ├── S4   … to respond to every message?
│   │   └── S5   … to respond to address the query?
│   ├── S6   What is the accuracy of our response?
│   │   └── S7   What was the source?
│   │       ├── S8   Who added the source?
│   │       └── S9   Was it relevant?
│   └── S10  How many seekers?
└── Provider
    ├── P1   How many requests are they getting?
    │   └── P2   From which channel are the requests coming in?
    │             (e.g. BV and MV use the same IMD provider)
    ├── P3   How many requests are they able to serve?
    │   ├── P4   How many failures were recorded?
    │   │   └── P5   What caused these failures?
    │   │       └── P6   Where are these failures concentrated?
    │   └── P7   What is the performance on the basic unit?
    │             (weather: location; mandi: location and crop)
    ├── P8   How much time do they take to serve?  — "this is latency"
    └── P9   How many providers?
```

The ids are ours, added so later sections and commits can cite one question
instead of paraphrasing it. Two notes were raised on the board itself and are
not yet decided:

| | Raised | Question | Status |
|---|---|---|---|
| N1 | karan-deep, on the Seeker branch | **What do we mean by a question?** | The tree draws the distinction itself, at S4 versus S5. Both ids are on the span — `beckn.messageId` and `beckn.transactionId` — so either definition is queryable today. Which one *counts* as a question is a network agreement, not a code change |
| N2 | karan-deep, on the root | **How do we define a unique user?** | Open question 1. It is what makes S10 unanswerable, and it is the same blocker as `sender.id` |

### What the design answers

Verdicts for **this service alone** — *The stack* below says which layer answers
the ones that are No here, and for four of them the answer is not "nobody".
The reasoning behind every **No** lives in *Open questions —
network level* at the foot of this document and is not restated here: a second
copy is a second thing to keep true, and it is the copy that rots.

| | Question | | By what |
|---|---|---|---|
| S1 | What are they seeking | **Partial** | `beckn.schemaContext` + `beckn.schemaType`, at capability granularity — `WeatherObservation`, `MandiPrice` — plus `intent.kinds` / `intent.filter_type` / `intent.spatial_ops` for the shape. Never the text, the crop or the location |
| S2 | Are they satisfied | **No** | Open question 12 |
| S3 | Time to respond | **Yes** | The span's own duration. No attribute — see *No duration attribute* |
| S4 | … per message | **Yes** | Span duration keyed on `beckn.messageId` |
| S5 | … to address the query | **Partial** | Our hop only. End to end needs `traceparent` forwarded by the adapter and the experience layer — open question 4 |
| S6 | Accuracy | **No** | Open question 12 |
| S7 | What was the source | **Yes** | `result.provider_ids` on `response_info` |
| S8 | Who added the source | **Yes** | `publish.provider_ids` on the publish `request_info` |
| S9 | Was it relevant | **No** | Open question 12 |
| S10 | How many seekers | **No** | `sender.id` is optional, unverified and often absent; blocked on Task 6, which is parked. `sender.unidentified` measures the size of the hole — the *unattributable request share* candidate under *Metrics* |
| P1 | Requests a provider is getting | **Partial** | Span count by `result.provider_ids` is *discovers their catalog answered*, not their inbound traffic. See the scope warning |
| P2 | From which channel | **Yes** | `network.id` on the Resource and `beckn.networkId` on the span — `mahavistar`, `bharatvistar` (C8). Crossed with `result.provider_ids` this is exactly the shared-IMD question the board asks |
| P3 | Requests they can serve | **Partial** | Non-empty share by provider id |
| P4 | How many failures | **Yes** | `status = Error`, `error_type`, and the `error` event |
| P5 | What caused them | **Yes** | The `error` event — `code`, `type`, `path` |
| P6 | Where concentrated | **Partial** | Per **provider**, yes: `error_type` grouped by `result.provider_ids` / `publish.provider_ids`. Per **place**, no — coordinates are on the never-emitted list. Open question 11 |
| P7 | Performance on the basic unit | **No — refused** | Open question 11. The most consequential refusal in this document, and the only one with a middle path already identified |
| P8 | Time they take to serve | **Partial** | Our latency, yes. An upstream provider's latency is invisible — nothing is called synchronously on the read path. `retrieval.embedding_ms` is the one external hop broken out, and it is Ollama, not a provider |
| P9 | How many providers | **No — not telemetry** | Spans count who *served*, never who *exists*: a provider matched by nothing emits no span in any window. This is a `SELECT count(DISTINCT …)`, and saying so is the answer rather than conceding a gap |

Seven of nineteen are answered outright, six partially, six not. **The six are
decisions, not oversights**: S2, S6 and S9 share one root cause, S10 is blocked
on a parked task, P7 is a policy refusal, and P9 is answerable — from the
database, not from a span. Each points at a numbered open question rather than
at a missing attribute, which is the difference between a gap we chose and one
we missed.

### What it changes for Task 23

Nothing in **23a**: it builds the package, the Resource and the exporter, and one
of the Resource attributes it pins — `network.id` — is already what answers P2.
Three items run on their own clocks.

| When | What |
|---|---|
| Now, and cheap | The literal `domain` and `producer` values (open questions 2 and 3). 23a creates the config field and the boot refusal either way, so it is not blocked — but `Agriculture` is a placeholder, and every OAN component must emit an identical string or grouping splits across the network |
| ~~Before **23d**~~ — **resolved** | P7 was going to force an H3-coarsening policy call here. It does not: *The stack* places P7 on the provider adapter, where location and commodity are already call-plan fields and no deny-list has to widen. The coarse-cell idea stays on record in open question 11 as the fallback if that layer cannot deliver |
| Not Task 23 at all | S2, S6, S9. No attribute added here substitutes for one. They belong to the **experience-layer adapter**, which already emits traces — see *The stack* and onix **U3**. An earlier draft of this row called that a new network contract; it is not, the layer exists |
## The stack — who answers what

This service is one node. The other three layers run **beckn-onix** adapters,
which already emit OTel traces, metrics and audit logs through their `otelsetup`
plugin. Any plan that treats Task 23 as the whole answer to the questions above
is wrong by construction: most of them are answered somewhere else, and three of
them cannot be answered here at all.

```
Experience layer   [onix adapter]          traces ✓  metrics ✓  audit ✓
                        │ W3C traceparent
Network layer      [onix adapter]          traces ✓  metrics ✓  audit ✓
                   [discovery-service]     traces — Task 23    metrics — Tasks 24 / 25
                        │ W3C traceparent
Provider layer     [onix adapter]          traces ✓  metrics ✓  audit ✓
                        └── upstream.go:631 ──► IMD · Agmarknet Vistaar
                                                 NOT INSTRUMENTED
```

Everything claimed about onix below is from
`beckn-onix/pkg/plugin/implementation/otelsetup/OBSERVABILITY.md` and the
collector configs under `beckn-onix/install/network-observability/`, read at
commit `076500e`. Where this document and that one disagree about a *cross-layer*
attribute, that one wins — it has three deployed adapters and two collector
configs already keyed on its spelling, and we have none.

### What onix already provides

| | |
|---|---|
| Traces | One `SpanKindServer` request span per adapter, one child span per configured step, plus key-management and cache spans. Scope `beckn-onix` v2.0.0 |
| Metrics | `onix_http_request_count`, `onix_step_execution_duration_seconds`, `onix_step_errors_total`, `onix_plugin_execution_duration_seconds`, `onix_plugin_errors_total`, `onix_cache_*`, `onix_routing_decisions_total`, `onix_plugin_info` |
| Audit logs | One structured record per request, masked per `config/audit-fields.yaml` |
| Propagation | W3C `traceparent` / `tracestate` read on every inbound request, written on every outbound one |
| Collector | Two pipelines per node — the full stream to the node operator, a filtered subset to a network-level collector |
| Build identity | `service.version` and three `onix.build.*` from `-ldflags`, on the Resource, so every signal names the build that produced it |

The last row is a gap on our side, not theirs — **OP5**.

### Question ownership

Which layer answers each question from the tree. The verdict table above asks
*can this service answer it*; this one asks *who does*, and the two are different
questions with different answers.

| | Question | Answered at | By what |
|---|---|---|---|
| S1 | What are they seeking | Experience, then here | The experience adapter holds the user's actual request; here it narrows to `beckn.schemaContext` / `beckn.schemaType` |
| S2 S6 S9 | Satisfied · accurate · relevant | **Experience adapter, and nowhere else** | Needs what the user did *after* the answer. Not a discovery-service question, and no attribute added here substitutes for one. onix **U3** |
| S3 S4 | Response time | Every layer, per hop | Span duration; the sum is the trace |
| S5 | Time to address the query | The network collector | Only once all four layers stitch — **I1**–**I3** |
| S7 | What was the source | Here | `result.provider_ids` |
| S8 | Who added the source | Here | `publish.provider_ids` |
| S10 | How many seekers | Experience adapter | It has the user. We have an optional, unverified claim |
| P1 P3 | Requests a provider gets and serves | **Provider adapter** | Its request span is the provider's actual traffic. Ours counts only the discovers their catalog answered |
| P2 | Which channel | Here, and the network adapter | `network.id` |
| P4 P5 | Failures and their cause | Every layer | Ours: the `error` event |
| P6 | Where concentrated | Per **provider** here; per **place**, provider adapter | onix **U1** |
| P7 | Performance on the basic unit | **Provider adapter** | There, location and commodity are call-plan fields. Here they are a coordinate and a JSONPath expression we refuse to emit |
| P8 | Time a provider takes to serve | **Provider adapter, at the upstream call** | Not instrumented today. onix **U1** |
| P9 | How many providers | The registry | A `SELECT`, not a span |

Two corrections this makes to the verdict table's framing, worth stating rather
than leaving a reader to notice. **S2/S6/S9 are not blocked on a new network
contract** — the experience-layer adapter is that contract, and it already emits
traces. And **P7 is not refused by the stack**, only by this service: the layer
that already holds the commodity as a first-class field can answer it without
anyone widening a deny-list.

### The interop contract

Three things must be true for one transaction to be readable across four layers.
**None is optional.** A span that fails I1 or I2 exports successfully, costs money
to store, and is invisible to the network observer — the worst of the three
available outcomes, because nothing anywhere reports an error.

**The premise all three rest on, and it is not established anywhere.** I1 and I2
bind **if and only if** our OTLP passes through the node's onix companion
collector — the `traces/network` pipeline in `node/otel-collector-bap/config.yaml`,
whose filter is I2 and whose downstream `transform/beckn_ids` stage is I1. Nothing
committed in either repository establishes that it does, and an earlier draft of
this section asserted it by omission.

**If we export direct to ClickStack and to the facilitator, I2 is moot and I1
reduces to insurance.** I2 is a property of one YAML file we would never traverse:
with no `filter/network_traces` between us and a destination we post to ourselves,
the `sender.id` admission bug drops nothing of ours, and **U2 stops being a
blocker on us** — it stays onix's bug affecting onix's adapters. I1 reduces to:
emit `transaction_id` and `message_id` anyway, because they are two attributes
written from values we already hold at a call site we are already writing, and any
consumer that ever keys on the Beckn transaction will want that spelling. It stops
being "not optional" and becomes an asymmetric bet — near-zero cost, non-zero
payoff — which is a weaker and truer claim.

**Direct export is what the committed artifacts point at, and it is not close.**
`config/common.yaml:55` defaults `otel.exporter: none` and states that a
collector-less deploy still boots. `config/instance.yaml.example:39-42` has the
OTLP block commented out and, uncommented, names `http://localhost:4317` with
nothing saying whose. *Two destinations* above names ClickStack and the OAN
facilitator and no third hop, and Decision 5 resolves the deny-list into a second
in-process exporter — a two-exporters-from-the-binary topology by construction.
Against all that, the only thing pointing at the companion collector is that onix
ships one. Note also where onix's network pipeline terminates: **Zipkin**
(`network/otel-collector-network/config.yaml:39-41,58-61`), with the `trace_id`
rewrite existing for Jaeger/Zipkin UI correlation. It is a network *operator's*
UI, not the facilitator's ingest — so I1 and I2 were never about facilitator
conformance at all. They are about being legible in onix's Zipkin, which is a real
benefit and a much smaller claim than "a span that fails either is invisible to
the network observer".

**This is decided here, not carried as an open question.** Deferring it defers
23f, which already has to know whether its second exporter targets a facilitator
endpoint or a collector, and it puts a bug in a repository we do not own on our
critical path. **Assume direct export to ClickStack and the facilitator, per
Decision 5, and emit the two I1 aliases regardless.** I2 is demoted from a
justification to a recorded consequence: if a deployment later routes us through
an onix node collector, `sender.unidentified` is not enough and that deployment
must either carry U2 or accept that the network layer drops us. The topology
itself is settled by whichever chart in `OpenAgriNet/helmcharts` deploys this
service and decides whether a collector sidecar is in the pod; record the answer
there and cite it here, because a deployment fact asserted in a design document
and contradicted by a values file is the kind of divergence nobody finds until a
span goes missing.

**I1 — join keys take onix's spelling, and there are exactly two of them.** The
network collector rewrites `trace_id` from an attribute named literally
`transaction_id` (`network/otel-collector-network/config.yaml:23-25`). We
specified `beckn.transactionId`. A span spelled our way is never stitched to
anything. **Emit `transaction_id` and `message_id` beside the `beckn.*` pair** —
that is the complete list.

**No `receiver.id` alias. It was in an earlier draft of this section and it was
an invention.** onix's *spans* carry `recipient.id` — `AttrRecipientID` at
`pkg/telemetry/pluginMetrics.go:49`, set at `core/module/handler/stdHandler.go:855`
— and `recipient.id` is the spelling we already emit. `receiver.id` appears in
onix only on **audit log** records (`stdHandler.go:199,201`); no collector
pipeline reads it off a span, so a fourth spelling would be paid for on every
span and consumed by nothing. It is also one capital letter from
`beckn.receiverId`, which this document defines as the caller's *claim* about who
it addressed — the opposite of `recipient.id`, which is who actually answered.
Two keys differing by a capital and meaning opposite things is a query someone
writes wrongly and never finds out about. The right fix for the audit-log
spelling is onix's: emit `recipient.id` there too, retaining `receiver.id` as an
alias, so both sides move together.

`message_id` is worth stating carefully for the same reason. The network
collector deliberately does **not** map it onto `span_id`
(`otel-collector-network/config.yaml:15-20` says why — several nodes emit spans
for one Beckn message, and identical span ids would corrupt the trace). It is a
searchable tag, not a join. We emit it because onix does and a network operator
filters on that spelling.

Two aliases bend the no-second-copy rule that *No duration attribute* applies,
and do so knowingly. The cases differ in the way that matters: a `duration_ms`
attribute is free to disagree with the span it duplicates, whereas both spellings
here are written from one value at one call site and cannot — which is why 23c's
pin asserts them *equal* rather than merely present. Renaming two attributes in
one service is cheaper than renaming one in three adapters and every collector
config, and that is the entire argument. It does not extend to a third attribute
nobody consumes.

**And the argument is weaker than the one above it, because OAN is
synchronous.** Every flow is a sync API call — `consumer → network node` and
`consumer → provider node` — and `on_discover` / `catalog/on_publish` are
*response actions* returned inline in the 200 body, with async callback dispatch
out of scope (`beckn/actions.go:23-24`, C3); `bapUri` and `bppUri` are
deliberately absent from `Context` for the same reason (`beckn/types.go:48`). So
every hop *could* be a nested child span on one live connection, and **W3C
`traceparent` would stitch the whole trace natively.** Nothing would need a
Beckn id to correlate.

**It does not work that way today, and the reason is a defect and not the
topology.** onix extracts `traceparent` on inbound
(`propagator.Extract`, `stdHandler.go:155`) and **injects it nowhere**:
`SpanKindClient` appears in no file in that repo, and the single
`WithSpanKind` call is `SpanKindServer` at `stdHandler.go:158`. So the outbound
leg carries no context, and every participant's span is the root of its own
trace regardless of how synchronous the call was.

That, not async callbacks, is what the collector rewrite is for. A `trace_id`
derived from `transaction_id` (`otel-collector-network/config.yaml:22-25`) is
how you recover a transaction when nothing propagated, and the config's own
comment says as much — the network collector "receives one span from each node
(BAP, BPP) for the same Beckn message" (`:16-17`). *Each node*, not a parent and
a child.

Two consequences for us. First, **the aliases carry more weight than operator
legibility**: while propagation is missing, `beckn.transactionId` is the only
thing joining our span to the adapter's, so `transaction_id` and `message_id`
are correlation keys rather than conveniences — and the earlier claim that they
exist for "legibility in onix's Zipkin" (`config.yaml:39-41,58-61`) undersells
them. Second, **once propagation is fixed the rewrite becomes actively wrong**:
it would overwrite a correct parent-linked trace id with one derived from a
caller-supplied field. So the order matters — inject first, then retire the
processor. A deployment that fixes one without the other is worse off than one
that fixes neither.

Fixing it is small on both sides — an `otelhttp` round-tripper, or an inject
before the outbound request — and it is the prerequisite for any per-hop
latency attribution across participants. Open question 4 names the gap;
`telemetry-examples.md` §1 shows the resulting trace shape. **Raise it with the
onix side as a defect, not a preference.**

**I2 — the network filter currently drops us.** `filter/network_traces` drops
every span where `attributes["sender.id"] == nil`
(`node/otel-collector-bap/config.yaml:29-33`). `senderId` is optional in this
phase and absent on most requests (divergence 1), so our spans would be dropped
*after* export.

This is **already inconsistent inside onix**, independent of us: the filter admits
on `sender.id` while the rewrite joins on `transaction_id`. A span carrying one
and not the other either passes the filter and fails to stitch, or would have
stitched perfectly and is dropped. The fix is onix's — **U2**, filter on
`transaction_id != nil`, which is what the pipeline downstream actually consumes.

Until U2 lands we could buy admission by emitting a `sender.id`. **Do not.**
Divergence 1 refuses that for a reason that has not changed: missing from a
dashboard is recoverable, and poisoning the network's only cross-participant
identity join is not.

**I3 — trace context in and out.** Already 23c's scope — inbound `traceparent`
joined rather than replaced, outbound injected on both clients. Recorded here
because 23c is what makes this service a participant in a trace rather than the
author of an orphan.

### Operator expectations — beyond the tree

The tree is the network's questions. These are what anyone *running* the thing
asks on day one, and their absence is what makes a telemetry rollout feel like it
answered the wrong questions.

| | Expectation, and where it stands | Lands in |
|---|---|---|
| OP1 | **Is the node up and serving?** Span rate is a proxy, and a poor one: a node that stopped receiving looks exactly like a network that went quiet. **But the answer is already deployed and is not ours to emit.** `router.go:97-98` serves `/healthz` and `/readyz`, kubelet already polls both, and kube-state-metrics already exports `kube_pod_status_ready` and `kube_pod_container_status_restarts_total` from them. A gauge this process emits *about itself* is strictly worse than an external prober, because a process wedged enough to stop serving can usually still set a gauge — self-reported liveness is the one kind that lies in exactly the outage it exists to catch | **Nothing here.** kubelet + kube-state-metrics. Struck from Task 25 on 2026-09-09 |
| OP2 | **Rate, errors, duration per action.** Derivable from spans the moment 23c lands. Needs aggregation, not instrumentation — the `spanmetrics` connector, no Go at all; see *How the derivation happens* under **Metrics** | **Collector config**, not Task 25 |
| OP3 | **Saturation — is it about to fall over?** Three ceilings exist and not one has a *level* anyone can see: `DATABASE_MAX_CONNS` (32), `RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` (20/40), `SERVER_MAX_REQUEST_BODY_BYTES` (10 MiB). The refusals themselves are already visible — `Trace` is index 1 in `router.go:134-141`, above both `Envelope` and `RateLimit`, so a 429 and a body-ceiling refusal each produce a span, a status off the record and, post-23d, an `error` event from the one `logNack` they both pass through. What no span can carry is the pool sitting at 30 of 32 for ten minutes while every request succeeds: a ceiling is a **level**, a span is an **event**, and the distance to a ceiling is observable only by sampling it on a clock. **Narrowed on 2026-09-09 against the layers that already emit:** a *utilisation* gauge is not the instrument this needs. `pg_stat_activity` grouped by `application_name` already shows connections per pod — which we do not get today only because `pool.go` never sets `application_name`, a one-line fix rather than an instrument — and what it shows is *established* connections, which pgxpool holds open when idle, so 32 open with 2 acquired is indistinguishable from 32 acquired. The number that actually answers *about to fall over* is not utilisation at all: it is `EmptyAcquireCount`, acquires that had to **wait** because the pool was empty, which rises before anything fails and is invisible to every layer outside this process. The other two ceilings need nothing — a 429 and a body-ceiling refusal each already produce a span, a status off the record and an `error` event from the one `logNack` they both pass through (`Trace` is index 1 in `router.go:134-141`, above both `Envelope` and `RateLimit`) | **Task 25**, as acquire-wait only. `RATE_LIMIT_*` and `SERVER_MAX_REQUEST_BODY_BYTES` get no instrument |
| OP4 | **Dependency health** — Postgres acquire-wait; Ollama when semantic is on. `retrieval.embedding_ms` covers Ollama only while it is enabled. **Query latency leaves this row:** it is the span's own duration once a repository span exists, and `postgres_exporter` plus `pg_stat_statements` answer it server-side better than a client histogram would. Acquire-wait is the half nothing else can see — it is queueing *inside this process, before any syscall*, so cAdvisor sees only the container's resource envelope and Postgres never sees a statement that was not sent. This is the one operator number in this document that no other layer can produce | **Task 25** — the whole of it |
| OP5 | **Which build is running?** onix stamps `service.version` and three `onix.build.*`. We have no version variable and no `-ldflags` in the Makefile, so *did the deploy break it* is unanswerable here | **23a** — the Resource is already being constructed; this is the cheapest moment it will ever be |
| OP6 | **Data freshness per provider.** In agriculture a stale weather catalog is worse than an absent one: it answers confidently and wrongly. Derivable from publish spans keyed on `publish.provider_ids` | **Struck** (see :385) |
| OP7 | **Is the telemetry itself working?** Trace completeness — the share of transactions carrying spans from every layer that should have handled them. Catches a layer silently dropping out, which every other dashboard renders as "traffic went down" | onix **U4**, at the network collector |
| OP8 | **Cardinality and cost.** Export here is always-on and unsampled. Defensible at Phase 1 volumes, not at network scale, and cheaper to decide before ingestion is paid for than after | **Decision 6** |
| OP9 | **An SLO, so a latency number has a verdict attached.** "p95 is 300 ms" means nothing without a target, and the plan's 20 ms retrieval budget is an internal figure rather than a served-request objective | Decision 7 |
| OP10 | **What pages a human.** An alert list falls out of OP1 (kubelet's, not ours), OP3/OP4's acquire-wait, and OP2's error rate once the connector runs. Out of nothing else | **No code anywhere.** Every input is either already deployed or is Task 25's single instrument; the list itself is a rule file with no acceptance criterion, which is why it was struck |
| OP11 | **The deny-list is testable.** Today it is a rule 23a–23e comply with and nothing enforces. A conformance test over the facilitator exporter is the only thing that turns it into a pin. It asserts over the exported **bytes**, regexing the deny-list across string *values* rather than key names — any per-row column is closed-world over keys and the risk is in values: a caller-supplied `beckn.schemaContext` URI, a `status.message`, a `zap.Error(err)` carrying wrapped driver text. That gap is also why the `Visibility` column deleted on 2026-09-09 was never going to be the mechanism on its own | **Task 26**, and it follows **23f** — corrected 2026-09-09. This row previously said 23d on the grounds that "an in-memory exporter is all it needs", which is true about the exporter *type* and misses that the thing under test is `telemetry/redact.go`, and that file is 23f's deliverable. A conformance test cannot precede the projection it asserts on |
| OP12 | **Clock discipline.** Four layers, four clocks, one stitched trace: skew shows up as a child span starting before its parent, and as negative inter-layer deltas. Cheap to require, expensive to debug once someone is charting it | onix **U4** |

### What this changes in the plan

| Task | Change |
|---|---|
| **23a** | Add build identity to the Resource — OP5. `-ldflags -X` in the Makefile and a `version` variable, matching what onix already does. This does not enlarge 23a's shape: the Resource is being built there anyway |
| **23c** | Add the I1 alias attributes beside the `beckn.*` pair. Propagation (I3) was already in scope |
| **23d** | Unchanged, and *smaller*: P7 moves to the provider adapter, so the H3-coarsening question raised against 23d is **withdrawn as a discovery-service concern**. See open question 11 |
| **Task 25** (new) | **Node-operator metrics — one instrument.** Postgres pool acquire-wait, and nothing else. Deliberately *not* Task 24: this is an `onix_*`-style operational number for the node pipeline and is **not blocked** on the metric-code registry that blocks the facilitator's METRIC signal. The scope was cut from three instruments to one on **2026-09-09**, against the test the first draft never ran — see *What earns an instrument here* below. Per-provider freshness (OP6), the alert list (OP10) and the liveness gauge (OP1) are all **struck**, and `ratelimit.go` and `envelope.go` leave the file list with them |
| **Task 26** (new) | **Deny-list conformance test** — OP11. Asserted over exported bytes, not key names. Gated on **23f**, which builds the `redact.go` it asserts against; an in-memory exporter is all it needs *beyond* that |

Four items in onix, none of which this repo can land:

| | Work | Why it is theirs |
|---|---|---|
| **U1** | Instrument `internal/upstream` — a client span and a duration metric per attempt | `upstream.go:631` is the only place an external provider is called, and that package imports no OTel at all. Answers P8, and P1/P3/P6/P7 for real providers |
| **U2** | Filter the network trace pipeline on `transaction_id`, not `sender.id` | Fixes an inconsistency that predates us, and is what admits our spans — **I2** |
| **U3** | Outcome events at the experience layer | The only place S2, S6 and S9 can be answered |
| **U4** | Trace completeness and clock skew at the network collector | OP7 and OP12 are network-level by definition |

---

## What we emit

| Signal | Emitted here? | Where |
|---|---|---|
| **TRACE** (`eid: API`) | **Yes** — one span per protocol request | This document |
| **LOG/AUDIT** (`eid: AUDIT`) | Already emitted as zap JSON; 23e adds `trace_id`/`span_id`. **The `eid` is `AUDIT`, not `LOG`** — the signal's name and its `eid` differ, which is exactly the sort of thing a second implementation guesses wrong (`otel-specification.md:599`) | 23b, 23e |
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
| `producer` | This deployment's **registered subscriber id** — an FQDN such as `discovery.oan.example.org`, from the new `APP_SUBSCRIBER_ID`. The example here used to read `discovery-service`, which is a *service* name and not a participant id; the pattern below was always FQDN-shaped, so the row contradicted itself and the example was the wrong half. This is the value the OAN registry issues and the value Task 6 resolves to fetch a public key, so it is not ours to invent — see the note under `recipient.id`. Participant-id shape — `^[a-z0-9][a-z0-9._:-]{2,252}$`, `maxLength` 253, from `docs/design/registry/schemas/ProviderSchema.json#/$defs/ParticipantId`. **Not** the `{2,63}` alternation on that file's line 18: that is a *provider* id, and open question 3 says a discovery service is not a Provider. The pattern is borrowed as a shape, not resolved from a record — there may be no record for us to resolve |
| `domain` | The **sector** — `Agriculture`. New config. Not the network, not the entity type |
| `service.name` | ClickStack's grouping column. **Not the same value as `producer`, and this row said it was.** OTel semconv `service.name` names *what software this is* — `discovery-service`, a constant carried in the struct tag. `producer` names *which participant this is* — an FQDN that differs per deployment. Collapsing them means either every deployment reports a different `service.name` and ClickStack cannot group the service, or `producer` reports a service name and the network cannot identify the participant. Two questions, two fields |
| `network.id` | `APP_NETWORK_ID` — `mahavistar`, `bharatvistar` (C8). Our key, not the spec's |

### Build identity

Four more Resource attributes name the build: `service.version`, `build.commit`,
`build.tree_state` and `build.date`. This is where their reasoning lives, because
it was restated at four sites — `main.go`, the `Makefile`, the `Dockerfile` and
the Go source — and was **wrong at all four** until it was measured on
2026-09-09. One home, cited from each.

**The standing preference is to read the toolchain's own build stamp, not to
inject.** `debug.ReadBuildInfo` gives `vcs.revision`, `vcs.modified` and
`vcs.time` for free, so `Makefile`, `Dockerfile` and CI need not agree on a flag
string for a binary to identify itself. Three of the four take that route.

**`service.version` is the one exception, and it cannot take that route at all.**
The stamp's `Main.Version` carries the **module's** version, never the release
tag: in a git checkout on go1.25 it reads a pseudo-version derived from the last
tag, and in the release image it reads `(devel)`. OP5 wants the tag, so that a
deploy which broke something can be named. Hence exactly one `-X`, which is the
smallest thing three build systems can be asked to agree on:

```
-X github.com/OpenAgriNet/discovery-service/src/platform/telemetry.version=$(VERSION)
```

It targets the **package**, so `version` may move between files inside it. `go
build` silently ignores an `-X` naming a symbol that does not exist, so a rename
would leave a green build shipping `dev` — `tests/architecture/ldflags_test.go`
asserts the `Makefile`'s and `Dockerfile`'s spellings match each other and that
the symbol exists.

**The gap is in the build that ships.** The `Dockerfile` copies `go.mod`, `cmd/`,
`src/` and `migrations/` and no `.git`, so the release image's build stage has no
repository to stamp from. There, `build.commit` and `build.tree_state` report
`unknown` and `build.date` reports `1970-01-01T00:00:00Z` — the free route is
free but it is not populated, and `service.version` is the only one of the four
that answers OP5 in production. Measured by building from `git archive HEAD`,
which reproduces the same no-`.git` condition.

`build.date` is the **commit's** timestamp, not the moment the compiler ran;
onix's `onix.build.date` is the latter. The commit time is the reproducible half
and the one that answers which change is deployed.

Absences are reported as values rather than as errors, and `service.version`
defaults to `dev` rather than `""`: an empty Resource attribute is
indistinguishable from an unset one, so a facilitator seeing nothing could not
tell whether the participant declined to answer or the attribute was dropped in
transit.

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
| `recipient.id` | `APP_SUBSCRIBER_ID`, the same value as `producer`. **Ours, never the caller's `receiverId`** — a caller can address anyone; this attribute has to say who actually answered. Constant on every span this binary emits, because we only ever receive: `on_discover` is a response action returned inline in the 200 body and async dispatch is out of scope (`beckn/actions.go:23-24`), so we are never the sender. **We therefore need none of onix's direction machinery** — no `deriveDirection`, no `selfID`/`remoteID` swap (`stdHandler.go:845-855`); that exists for an adapter that both calls and answers, and copying it here would import a case that cannot arise. **The value is configuration, not a registry lookup, and the deployment convention for it already exists** — onix's `selfID` is `h.SubscriberID`, filled from `subscriberId:` in the adapter YAML, which `helmcharts/quick-start/config/adapters/network.yaml.tmpl:71` populates from `__NETWORK_SUBSCRIBER_ID__` and `.env.example:130` sets. Three siblings are already there — `EXP_`, `NETWORK_`, `PROVIDER_SUBSCRIBER_ID` — beside `APP_NETWORK_ID`, which the quick-start README:125 records as ours. `APP_SUBSCRIBER_ID` is the fourth line in that file under the prefix this service already uses, so the name is confirmed by convention rather than chosen here. **Nothing sets it today**, which is why 23a makes it optional with no default and `recipient.unidentified` exists |
| `recipient.unidentified` | `true` when `APP_SUBSCRIBER_ID` is unset, with `recipient.id` omitted — the same shape as `sender.unidentified`, for the same reason: the spec marks it Required and we refuse to invent an identity. **Never fall back to `context.receiverId`.** Today the controllers echo it, so the fallback would make `recipient.id` and `beckn.receiverId` hold one value and the misaddressing query below would silently always return nothing — the bug staying invisible until Task 6 stops the echo |
| `span_uuid` | Generated per span, by a `SpanProcessor`'s `OnStart` |
| ~~`parent_id`~~ | **Not emitted.** onix builds one from `role + subscriberID + pod name`; two of those three do not survive here. There is no role — OAN uses `senderId`/`receiverId` and not `bapId`/`bppId`, so a participant carries no type — and `subscriberID` is already `recipient.id`. What remains is pod identity, which belongs on the **Resource**, not on every span: it is constant for the process lifetime, so a per-span copy pays thousands of times a second to say one thing. `OTEL_RESOURCE_ATTRIBUTES=k8s.pod.name=$(POD_NAME),k8s.namespace.name=$(NS)` from the chart's downward API is standard semconv, needs no code, and the SDK merges it into the Resource `Init` builds — so it lands on spans, logs and metrics at once. Not a divergence: `parent_id` is onix-local and appears nowhere in the spec |
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
(`src/discover/intent_mapper.go`), splits each entry on `#` into
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
| `result.provider_ids` | The distinct provider-node id of what was returned — **whose** data answered this query. Read off `Catalog.BppID`. **OAN does not use `bap`/`bpp` terminology, so no attribute here repeats it** — the struct field keeps the spelling only because `Catalog` (`beckn-v2.0.0.yaml:2759-2803`) closes with `additionalProperties: false`, which rejects any replacement name; the spec's own reason, backward compatibility with existing integrations (`:35`), is not ours. See the plan's note on that field — renaming it is a schema amendment, not a rename |
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
| `publish.provider_ids` | The distinct provider-node id of what is being published — **who added the source**. Same bound and same reasoning as `result.provider_ids`, and the same name stem on purpose: both read the same struct field, so one name for one concept makes the join between "who published it" and "whose data answered" obvious instead of something a reader has to work out. This attribute was `publish.bpp_ids`; OAN does not use `bap`/`bpp` terminology, and telemetry names are **ours**, so it follows OAN. The struct field it reads keeps the protocol's spelling for the schema reason given above, which is a constraint rather than an endorsement |
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

Every mandatory field above is emitted. These ten are where we knowingly differ,
collected here so a reviewer sees them in one place. It read "these six" while
rows 7 and 8 sat unlisted in the example commentary below and rows 9 and 10 were
in no document at all — a divergence noted in passing is one that gets
re-litigated as a bug.

| | Divergence | Why |
|---|---|---|
| 1 | **`sender.id` omitted when absent**, with `sender.unidentified = true` in its place — and **`sender.unverified = true` when it is present**. The spec marks it Required and treats it as an identity | This protocol phase does not require `senderId` — identity was parked with Task 6. Requiring it rejects legal requests; inventing it poisons the facilitator's only cross-participant join; and emitting it unqualified hands that join a string the caller chose. Open question 1 |
| 2 | **No `reqBody` / `resBody`**, though the spec's example carries both | Design principle 3 — the spec's own. Its example also puts `deviceid` and `useragent` in `resource`, which contradicts that principle; we follow the principle over the example |
| 3 | **`http.status.code` sent as string**, though the structure declares `Int` | The spec contradicts itself; all three examples send a string. `http.status_code` carries the int |
| 4 | **No METRIC signal from this binary** | Mandatory for the participant, but `ref-impl-design.md` places computation in micro-observability. Task 24 |
| 5 | **`traceId` / `spanId` are OTLP hex**, not the spec's UUIDs | The spec's examples use dashed UUIDs — `d4ae9294-ab00-11ee-9db4-325096b39f47` — which are **not valid OTLP**: the protocol requires 16-byte and 8-byte hex. Design principle 2 says adopt OpenTelemetry, so the examples are wrong, not the protocol. **But a facilitator validating against those examples would reject every span we send.** Open question 9 |
| 6 | **Event timestamps are `timeUnixNano`**, not the spec's `time` | Same root cause as 5, and found the same way: the spec's event shape names a field `time` carrying an ISO string, and OTLP span events carry `timeUnixNano`. This is not a field we choose — the SDK serialises it, so honouring the spec's spelling would mean rewriting the OTLP payload on the way out. Note the spec is already inconsistent with itself here in our favour: it insists on nanos for `observedTimeUnixNano` (divergence note under Span attributes). Open question 9 covers both |
| 7 | **`status` is `{"code":"STATUS_CODE_OK"}`**, not the spec's `"Ok"` | Same root cause as 5 and 6 and the same answer: `status` is a **field on the OTLP `Span` message**, not an attribute, so no `attribute.KeyValue` can name it and no registry row can reach it. The SDK serialises the enum. This is the boundary `telemetry-seam.md` §1 draws — same value under two keys is a registry alias; a structural OTLP field serialised differently is the exporter's, and 23f is the only place it can be rewritten |
| 8 | **`kind` is `SPAN_KIND_SERVER`**, not the spec's `Server` | Identical to 7 — an enum on the `Span` message. Both were already stated in the example commentary below as things "easy to get wrong by hand", which is how they escaped this table for so long: described accurately, filed as a formatting note rather than as a divergence a facilitator might reject on |
| 9 | **`http.route` carries the route template**, where the spec's prose says URL | The only one of the nine that is a **value** divergence rather than a spelling or serialisation one, and the only one a registry row can hold. `/discover` is bounded and a URL is not, so the spec's reading makes `http.route` unusable as a metric label and leaks any query string the caller wrote into an always-on, unsampled export. We keep the template and record the reason on the `Definition` itself (`Note`), where the next reader meets it before "fixing" it toward the spec |
| 10 | **`recipient.id` is self-declared**, where the spec's prose describes the addressed recipient | `otel-specification.md:299` marks it Required and glosses it "Identifier of the system that is **expected to be** the recipient of the API call" — which is the caller's claim, i.e. `context.receiverId`. onix implements the other reading: `AttrRecipientID` comes from a configured `selfID` resolved by direction, never from the envelope. The two coincide for a correctly-addressed request, which is why the disagreement has gone unnoticed, and they diverge exactly when someone addresses us wrongly — the case telemetry exists to surface. We take onix's reading, because a self-declared value is the one that is true and because the spec's own examples (`np1`, `gateway1`) read as topology rather than as an echoed payload field. Both are emitted, so nothing is lost either way: `recipient.id` self-declared, `beckn.receiverId` as claimed. **This is a judgement call between a prose spec and a reference implementation, not a settled fact** — it belongs with open question 9 to the spec owners |

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
(divergence 6), `status` is an object and `kind` an enum name (divergences 7 and
8, which is where they are now argued rather than only noted), and every
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
| 2 | `src/platform/telemetry/provider.go` | `Init(cfg)` — Resource, tracer provider, OTLP exporter, shutdown |
| 3 | A `SpanProcessor` | Stamps `span_uuid` at `OnStart`. `observedTimeUnixNano` is set by the middleware before `End()` — `OnEnd` is read-only |
| 4 | `src/platform/middlewares/trace.go` | Allocates the observation record if nothing above it has, starts the span, sets the request-side `http.*`, joins an inbound `traceparent`, and — after `next` returns — projects the status off the record before `End()` |
| 5 | `correlate()` in `envelope.go` | Names the span, sets `sender.id` / `sender.unverified` / `recipient.id` / `beckn.*` |
| 6 | The two controllers | Call `record()` at the three points above — each at the moment it happens |
| 7 | `response_writer.go` | The `error` event, from the fault it already has |
| 8 | `request_logger.go` | Adopts the record rather than allocating when `Trace` is above it, and records the status as a fact — the one thing the span cannot see for itself |

**Controllers never link the OpenTelemetry SDK.** They record timestamped facts;
a projection turns those into events. **Six of the eight existing log fields are
also span attributes** — instrumenting separately would put `error_type` in two
places, which is what C1 exists to prevent.

That sentence read "controllers never import the telemetry package" until
`telemetry-seam.md` made the wording load-bearing, and the narrowing is
deliberate rather than a softening. A controller has to name the fact it is
recording, so it names a `fact.Key` — and `src/platform/telemetry/fact/` imports
`context`, `iter` and `time` and nothing else, precisely so that naming one links
no exporter and no SDK. What the rule was always protecting is that a controller's
build does not break on an SDK release, and that survives exactly. The literal
old reading does not, so it is restated rather than left to be discovered as a
contradiction: **controllers may import `.../telemetry/fact`; they may not import
`go.opentelemetry.io/...` or `src/platform/telemetry` itself.** Unlike the old
wording, this one is checkable, and `tests/architecture/boundary_test.go` checks
it.

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

**Done, and it is the mechanism rather than the one row that matters.**
`Definition.PromoteToSpan` is a bool per row; `onTheSpan` honours it and the
event projection is untouched, so a promoted fact ships on both and the
interop contract is unchanged. `fact.Validate` refuses the bool on a row with
no `Event` or without the `Span` bit, and `TestOnlyTheDeclaredRowsArePromoted`
fails the moment a second row sets it — the "individually, not wholesale" half
of the note above is now a test rather than a sentence.

A second reason arrived after this note was written, and it is the stronger
one: the collector's `spanmetrics` connector can name a span attribute as a
metric dimension and **cannot reach an event at all**. So the promotion is what
makes unmet demand countable, not merely cheaper to query. `retrieval.modes_degraded`
is the remaining event-only fact worth having and is deliberately not promoted —
it is `KindStrings`, and a string-slice dimension is a series per distinct
combination of degraded modes. Promoting it needs a scalar first.

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

**These candidates have been worked into an actual proposal**, with a code per
measurement, the units, the labels and their value sets, and the seven questions
only the registry owner can answer: `metric-code-registry-proposal.md`. It is
addressed outward and binding on nothing. The table below stays as the source
list; the proposal is what to send.

| Candidate | Aggregates over |
|---|---|
| discover / publish call count | span count by `beckn.action` |
| response time p50 / p95 / p99 | span duration |
| failure percent, split by category | `status = Error`, `error_type` |
| **unmet demand rate** | `result.empty` — the one metric no other participant can produce |
| degraded-mode rate, per mode | `retrieval.modes_degraded` |
| catalog freshness per provider | time since that provider's last `publish` `request_info`, keyed on `publish.provider_ids` |
| publish volume per provider | `publish.resource_count` by `publish.provider_ids` |
| **provider serve share** | distinct `result.provider_ids` per span — which sources actually answer, and which never do |
| per-provider failure concentration | `error_type` grouped by `result.provider_ids` / `publish.provider_ids` |
| unattributable request share | `sender.unidentified` — the number that argues for finishing Task 6 |

Two names from the original Task 23 — `search_degraded_modes` and
`embedding_duration_ms` — were dropped as metrics by A23 and **survive as span
facts**, which is what an exporter aggregates over. Both now have a named home,
which they did not in the first draft of this document: `retrieval.modes_degraded`
and `retrieval.embedding_ms`, both on `retrieval_info`. A promise that a fact
survives, with no attribute defined to carry it, is the fact not surviving.

### How the derivation happens — `spanmetrics`, not a counter

"Derivable from the spans" above was a claim with no mechanism attached, which
is how a derivation quietly becomes a counter someone adds later. The mechanism
is the collector's **`spanmetrics` connector**: it consumes the trace stream and
emits a request count and a latency histogram, with the label set named in its
`dimensions` list, because *Rate, errors, duration* is exactly what the
connector was built to produce.

**Success versus failure needs `error_type`, and NOT the span's own status.**
An earlier draft of this section said the opposite — `beckn.action` plus the
status, "nothing else needed" — and it is wrong for this service, measurably.
`setStatus` (`middlewares/trace.go:202-207`) moves the span status only at 5xx,
on the deliberate reasoning that a 400 is the caller's mistake and counting it
as a server error reports how often this service broke when it did not. So
every 4xx refusal — the whole of C1's `CONTEXT`, `DOMAIN` and `POLICY`, and
most of `CORE` — arrives with `status_code=STATUS_CODE_UNSET`, indistinguishable
from a success. Verified against a live stack: the three `DOMAIN` refusals in
`examples/verify.sh` land as `error_type=DOMAIN, status_code=UNSET`.

A dashboard must therefore compute the error rate from `error_type != "none"`.
Deriving it from the status alone reports a **0% error rate on a service
refusing every request it receives** — which is the one failure mode a
success-versus-failure panel exists to catch. `error_type` needs a
`default: none` on the dimension, or the connector drops it on successful spans
and splits one stream in two.

**This is why OP2 leaves Task 25 and needs no Go.** Everything it requires is
already deployed:

| Needed | Already there |
|---|---|
| A collector build carrying the connector — `spanmetrics` is contrib, not core | Every collector in `beckn-onix/install/network-observability` runs `otel/opentelemetry-collector-contrib` |
| A metrics pipeline and a scrape target | `metrics/app` → `prometheus` at `:8889` in `node/otel-collector-bap/config-full.yaml`, with `prometheus-node` and `grafana-node` beside it in `docker-compose.with-telemetry.yml` |
| Spans with an action and a status on them | 23c and 23d. **The connector cannot precede them** |

So the change is a `connectors:` block, the connector added as a second exporter
on the existing `traces/app` pipeline, and one `metrics/spanmetrics` pipeline
reading from it. No new instrument, no `fact.Instrument` row, no import.

**Built. `otel/collector.yaml`, brought up by `docker-compose.telemetry.yml`.**
It is an overlay rather than a compose profile because it has to change the
service's own environment, which a profile cannot do. The streams are
`discovery_calls_total` and `discovery_duration_milliseconds`; `make telemetry`
starts it and `make telemetry-metrics` scrapes it. Read that file rather than
this section for the label set — it records three things only a live stack
teaches, including that `discovery_calls_total` legitimately reads 0 for about
a minute after startup while the histogram beside it is already correct.

Three things this pins, each of which is a way to get it wrong:

- **Declare the stream names explicitly.** The connector's defaults have been
  renamed across collector releases and every config here pins `:latest`, so a
  default-named dashboard breaks on an image pull. Set `namespace` and treat the
  resulting names as the contract.
- **It stays node-local, and that is correct.** `filter/network_metrics` drops
  every metric not named `onix_http_request_count`, so a connector-derived
  stream never reaches the facilitator. This is an **operator** dashboard, not
  the spec's `METRIC` signal — the filter is what keeps the low-code choice from
  leaking into a stream that has a registry and a `metric.code` it would fail to
  supply. Do not widen the filter to admit it.
- **Cardinality is the connector's now, and the ceiling still applies.** Each
  `dimensions` entry multiplies the series count exactly as a `Label` bit does,
  but sits in YAML where `fact`'s guards cannot see it. Every dimension must
  name a `fact.Key` that already carries `Bounded`; `sender.id` is the obvious
  tempting addition and the one that makes the series count grow with the
  participant list. Task 24's dashboard work owns keeping the two in step —
  nothing enforces it, and that is the honest status.

**Why this cannot be the `METRIC` signal, stated once so nobody tries.** The
spec permits exactly one aggregation: "only the 'sum' aggregation and
non-monotonic only" (`otel-specification.md:437`). `spanmetrics` emits a
**monotonic** counter and a **histogram**, and Task 25's one instrument is a pair
of monotonic counters — every node-local stream in this design is outside the
spec's METRIC profile on aggregation type alone, before `metric.code` is even
reached. That is not a defect in either; it is the split `ref-impl-design.md`
describes, with node-operator observability on one side and the network's
business metrics on the other. It does mean the boundary has to be *enforced*
rather than assumed, and `filter/network_metrics` is the thing enforcing it.
Anyone reading "we ship gauges but the spec says sum-only" as a bug has found
the boundary, not the bug.

### Shape, when it is unblocked

- `sum` aggregation only, **non-monotonic** — the only kind this spec version allows.
- `aggregationTemporality`: `1` delta, `2` cumulative.
- Required per data point: `metric_uuid`, `observedTimeUnixNano`, `metric.code`.
  Optional: **`metric.label`** (not `label` — an earlier draft here dropped the
  prefix), `metric.category`, `granularity`, `frequency`.
- Also Required and not previously listed here, because they sit on the metric
  rather than on the data point and so were read past: **`name`** and **`unit`**
  on the stream, and **`asDouble`**, **`startTimeUnixNano`** and
  **`endTimeUnixNano`** per data point. Only the first two are ours to declare —
  `telemetry-seam.md` puts them on `Instrument`. `asDouble` is the measurement
  itself and the two timestamps are the periodic reader's collection window; a
  table declaring either would be a table declaring the clock.
- **The same Resource as the spans, with one attribute deliberately different:
  `eid` is `METRIC` here, not `API`.** This paragraph read "the same Resource
  attributes as the spans" and that was wrong by exactly one field — the one
  field a consumer routes on. Everything else is shared, which is the reason
  `producer` and `domain` are settled once at boot rather than per signal; `eid`
  is the single exception and is therefore a registry row with a per-signal
  projection rule, not a literal in `Init`.

---

## Node-operator metrics — Task 25

Task 24 above is the facilitator's METRIC signal, blocked on a `metric.code`
registry OAN does not have. This is the other thing entirely: the numbers the
person *running this node* needs. Separating them is why the service has metrics
at all; conflating them is why it had none.

### What earns an instrument here

**Only a number invisible to the layer below.** The first draft of this task
named three instruments without running that test, and running it removes two.

| Layer | Already emits | Blind to |
|---|---|---|
| kubelet / cAdvisor / kube-state-metrics | the container's resource envelope; pod ready, restarts, OOM kills | anything inside the application |
| `postgres_exporter` / `pg_stat_statements` | the server's own state — connections, locks, replication lag, statement latency | anything that never arrived |
| this process | — | — |

Applied honestly:

- **Liveness (OP1) — struck.** `/healthz` and `/readyz` already exist and kubelet
  already polls them. A self-reported gauge is worse than an external prober at
  the one moment it matters.
- **Pool utilisation — struck.** `pg_stat_activity` grouped by `application_name`
  gives connections per pod. **`pool.go` must set `application_name`**; that is
  the whole change, and it is one line, not an instrument.
- **Rate, errors, duration (OP2) — struck.** The `spanmetrics` connector, in YAML.
- **Acquire-wait (OP3/OP4) — kept.** Queueing inside this process, before any
  syscall. No layer below can see it, and it rises *before* anything fails.

### The one instrument

Source is `pgxpool.Stat()`, which computes everything already — `container.go:200`
reads `EmptyAcquireCount()` for `/readyz` today. So this is a sampling callback
over an existing struct, not new bookkeeping.

| Reported | From |
|---|---|
| acquires that had to wait on an empty pool | `Stat().EmptyAcquireCount()` |
| cumulative time spent in that wait | `Stat().EmptyAcquireWaitTime()` |

**Two observable counters, not a histogram, and this is a constraint rather than
a preference.** pgxpool exposes only cumulative totals; a real distribution would
mean wrapping every `Acquire` call on the hot path. Two counters let the consumer
divide one rate by the other for mean wait per acquire, which answers the
saturation question without touching the request path at all.

### Tests pin

- Acquire-wait observed under a pool deliberately sized to 1, so a second
  concurrent caller must wait and the counters must move.
- Registered under our own scope, never the global meter.
- Every label names a `fact.Key` carrying the `Label` bit with a `Bounded` value
  set — the cardinality guard `fact` already enforces.
- **A count: one instrument.** A second arrives with a reviewer attached, and the
  reviewer's question is the table above — which layer is blind to it?
- **No request counters and no rejection counters.** Every refusal already
  produces a span, a status and an `error` event. A counter restating them is the
  `duration_ms` mistake one signal up.

### Not blocked

Nothing here needs the network to answer anything. Open question 7's SLO fed
OP10's alert list, and OP10 is struck — so **the SLO does not gate this task**,
which an earlier version of that row claimed.

---

## Build order

A23 split Task 23 into six. One review gate between each. **`telemetry-seam.md`
adds a seventh in front of them, 23a0**, because the attribute registry every
later sub-task reads sat in no task at all — 23a's Produces is `provider.go`,
and an implementer starting there would find the plan does not describe the work.

| | Sub-task | Files | Tests pin |
|---|---|---|---|
| **23a0** | The attribute registry | new `platform/telemetry/fact/` — `fact.go`, `registry.go`, `record.go`; `tests/architecture/boundary_test.go`; `tests/testdata/cross-layer-attributes.json` | Every `Key` has a complete `Definition`, with `Cardinality`, `Layer` and `Kind` each refusing their `Unspecified` zero (a `Visibility` column did the same until it was deleted on 2026-09-09 — it had no runtime consumer, 23f was its only one, and all 54 rows carried the same value; the build-vs-reuse audit Finding 2 is the record and 23f restores it); `Signals&Label ⇒ Bounded ∧ len(Values)>0`; `Required ⇒ Signals&Resource`; no `Definition` names an OTLP structural field (`status`, `kind`, `traceId`…); the fifteen cross-layer keys match the vendored fixture byte for byte; `fact` imports nothing outside `context`, `iter`, `time`; controllers and `src/storage` import no OTel. **Emits nothing** — it is a table and its guards. Note this makes `fact/` a node 23b, 23c and 23d all edit, which partly re-couples the six gates A23 separated; concentrating that coupling in one reviewed sub-task is the point of doing it first |
| **23a** | Foundation and Resource | new `platform/telemetry/`; `config.go` (adds `APP_SUBSCRIBER_ID`, optional, no default — see `recipient.id`; **one field beside the existing `Network string \`env:"APP_NETWORK_ID"\`` at `config.go:57`**, not a new block), `container.go`, `server.go`, `Makefile`. Plus one line in `helmcharts/quick-start/.env.example` beside the three `*_SUBSCRIBER_ID` keys already at `:129-131` — a different repo, so it is a companion PR and not a file this task edits | `OTEL_EXPORTER=none` still boots; Resource carries all five; `otlp` with `Producer`/`Domain` empty fails **at boot**. Starts no spans. **Plus OP5**: `service.version` and the build attributes from `-ldflags -X`, with a test that an unstamped build reports `dev` rather than an empty string — an empty version is indistinguishable from an unset Resource field |
| **23b** | The observation record | `middlewares/correlation.go`, `envelope.go`, `request_logger.go`, `trace.go` | Log output byte-identical before and after. **Changes no output**; acceptance is the existing suite passing with no test file edited — including `request_logger_test.go:181,207`, which mount `RequestLogger` with no `Trace` above. Also pins the adopt-or-allocate rule from both sides: `Trace` first, and `RequestLogger` alone |
| **23c** | Span lifecycle | `middlewares/trace.go`, `correlate()`, `validation/http_fetcher.go`, `embeddings/ollama.go` | Inbound `traceparent` joined not replaced; outbound injection on the two clients; scope is ours; `http.status.code` comes off the record and matches the status actually written; a recovered panic's 500 is inside the exported span; A11's behavioural pin holds. **Plus I1**: `transaction_id` and `message_id` carry the same values as their `beckn.*` counterparts, asserted as equal in one test so the pair cannot drift. Two aliases, not three — `recipient.id` is already onix's span spelling and needs none, and **no `receiver.id` is emitted**; a test asserts the exported key set does not contain it |
| **23d** | Events | `discover/controller.go`, `publish/controller.go`, `response_writer.go` | Event times strictly increasing, none equal to span end; a master publish reports `MASTER`; `error` category matches `X-Beckn-Error-Type` byte for byte; `retrieval.embedding_ms` absent — not zero — under `noop`; `result.provider_ids` is DISTINCT and bounded at 16, so a 200-catalog answer from one provider emits one id; `beckn.schemaContext` is absent rather than empty when the seeker sent no predicate |
| **23e** | Trace/log correlation | `logger/logger.go`, `trace.go` | `trace_id`/`span_id` present once a span exists, **absent not empty** when exporter is `none` |
| **23f** | Facilitator stream + redaction | `telemetry/redact.go` | **BLOCKED** on open questions 2, 3 and 9 — the plan's **O1**, **O2** and **O4**. Also where `scope_uuid` and `count` land, since both need the custom exporter this sub-task builds |

Two tasks follow 23, and neither is blocked by what blocks 23f:

| | Task | Files | Tests pin |
|---|---|---|---|
| **25** | **Node-operator metrics** — OP3 and OP4 only. **One instrument, and no more** (cut from three on 2026-09-09; OP1 went to kubelet, pool utilisation went to `pg_stat_activity` + an `application_name` line in `pool.go`). See *Node-operator metrics — Task 25* above for the layer test that removed them. OP2 also left this row: it is `spanmetrics` in the collector, not a counter here, so "conditionally" is now decided and the condition is *no*. **OP6 and OP10 are struck**, and `ratelimit.go` and `envelope.go` leave the file list with them | new `platform/telemetry/fact/instruments.go` and `telemetry/metrics.go`; `storage/postgres/` | Acquire-wait is observed under a pool deliberately sized to 1, so a second concurrent caller must wait and `EmptyAcquireCount`/`EmptyAcquireWaitTime` must both move. **Two observable counters off `pgxpool.Stat()`, not a histogram** — pgxpool exposes only cumulative totals, so a distribution would mean wrapping every `Acquire` on the hot path; the consumer divides one rate by the other. **No in-use gauge** and **no liveness gauge** — the first is `pg_stat_activity`'s once `pool.go` sets `application_name`, the second is kubelet's. Registered under our own scope, not the global meter, and every label it names is a `fact.Key` carrying the `Label` bit with a `Bounded` value set whose product is under the per-instrument ceiling. **No rejection counters, and no request counters either.** The earlier row said a 429 "is not counted today because it short-circuits above the handler"; `Trace` is index 1 in `router.go:134-141`, above both middlewares, so each refusal already produces a span, a status and an `error` event. A counter restating them is the `duration_ms` mistake one signal up. The test that pins this is a count: **one** instrument registered, so a second arrives with a reviewer attached — and the reviewer's question is the layer table, *which layer below us is blind to this number?* |
| **26** | **Deny-list conformance** — OP11. Follows **23f**, which builds the `redact.go` under test. This row said "after 23d, not after 23f" until 2026-09-09 and directly contradicted `implementation-prompts.md`; that row was the correct one | `telemetry/redact_test.go` | A span carrying `textSearch`, `filters.expression`, coordinates and a user agent leaves the facilitator exporter with none of them, asserted over the **exported payload** rather than over the code that builds it — and over its string *values*, not its keys, since the risk is a caller-supplied URI or a wrapped driver error and neither is a key any per-row column can reach. Facilitator projection only: asserting it on the ClickStack stream would forbid the local analysis the split exists to permit. Fixture-driven, so adding a denied field is a fixture line, and it carries a vacuity guard — a conformance test that passes over zero inputs is a failure this repo has already met once |

Notes that bite:

- `src/platform/telemetry/` holds a `.gitkeep`; the OTel modules are **indirect**
  in `go.mod` and the SDK and exporter modules are absent entirely. 23a adds them.
- 23c **drops the `X-Beckn-Chain: trace` header entry**, which existed only so
  Task 20's order test had something to observe. That assertion moves to the span:
  the recovered panic's 500 must be recorded *inside* it, true only if `Trace`
  wraps `Recover`. **Keep A11's behavioural pin** — one completion line at
  `status = 500` with `X-Response-Time` set — when the header pair goes.
- **`trace.go`'s `otelhttp` rejection is correct today, and 23c must keep it.**
  An earlier version of this note claimed `:22` and `:31` "still say this is the
  place Task 23 puts `otelhttp`", and instructed 23c to delete them. Both halves
  were wrong. `:22-23` names the slot — "a pass-through today, and the place Task
  23 starts the span" — and does not mention the library. `:31-37` states the
  **rejection** and its reason, which at the time read: "NOT otelhttp: the
  network telemetry spec requires scope.name/scope.version on every exported
  batch, and the instrumentation scope is fixed when the span is created, so a
  span otelhttp started would carry that package's scope for ever (A23,
  ADR-0011)." That is this document's own argument under *Scope*, written at the
  one file someone would reach for the library in. Deleting it is the failure the
  note was trying to prevent, inverted.

  **The quoted half about `requires` was itself wrong**, which the 2026-09-09
  audit caught and `trace.go` no longer says — the block is Optional and the
  collision is `scope.version`'s meaning, not a missing mandatory field. Quoted
  verbatim above because the finding was about *deleting the rejection*, and a
  quote silently improved is no longer evidence of what was there. The rejection
  stands; only its stated reason narrowed.

  What 23c **does** delete is the chain-entry machinery and nothing else: the
  `w.Header().Add(HeaderChain, chainTrace)` line, the `chainTrace` constant, and
  the sentences at `:24-30` explaining why a pass-through needed a side effect.
  All three exist only so Task 20's order test had something to observe, and the
  order assertion moves to the span. The rejection paragraph survives the edit
  verbatim, moved onto the new body, and 23c's diff should show it unchanged.

  **The temptation is live, not hypothetical.** `go.mod:74` already carries
  `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.69.0` as an
  indirect dependency, so `import ".../otelhttp"` compiles today with no `go get`
  and no new line in `go.mod` for a reviewer to notice. A rejection recorded only
  in a design document is one that gets undone by someone reading the file.
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
| 1 | **`scope.version`** — the spec repo has **no version tags** and `1.0` is the only value any example uses. Stamped on every span; a facilitator may key on it | **Ship `1.0`** and record that it is example-derived, not released. A constant in `provider.go` |
| 2 | **OTLP transport — gRPC or HTTP?** `otlptracegrpc` and `otlptracehttp` are different modules, so this is a dependency, not a config flag | **gRPC** — the OTel default for `OTEL_EXPORTER_OTLP_ENDPOINT`, which config already reads, and ClickStack's collector accepts it |
| 3 | **`App.Close()` is `func()`** — no ctx, no error — but `TracerProvider.Shutdown` needs both | Bound the flush **inside** `Close` and report failure to stderr as `Log.Sync` already does, rather than widening the signature across every caller |
| 4 | ~~**ADR-0011 contradicts A23**~~ — **DONE.** It read "traces **and metrics**" and "`otelhttp` instrumentation", both rejected by A23 | Amended in place, with an Amendments section recording what changed and why. Two committed documents disagreeing is a defect rather than a choice, so it was not left for 23a to carry |
| 5 | **Where is the deny-list enforced — in Go, or in the collector?** "Two exporters" can mean two `TracerProvider`s in-process, or one export to our local collector which fans out to the facilitator through a filter processor. The collector route is the standard OTel pattern and needs no Go code; the in-process route is the only one a Go conformance test can assert against | **Enforce in Go**, on a second exporter. A privacy rule enforced only in YAML is one a deployment can silently drop, and the doc's conformance test assumes an in-process seam. Decide before 23f — it defines 23f's scope |

Two more, raised by *The stack* rather than by the SDK. Neither blocks 23a, and
both are cheaper to answer before the thing they govern is built than after:

| | Decision | Recommendation | Needed by |
|---|---|---|---|
| 6 | **Sampling — what fraction of spans is exported?** Today's design is always-on and unsampled. That is defensible at Phase 1 volumes and indefensible at network scale, and the cost lands on whoever pays for ingestion, who is not us. **OP8** | **Set no sampler.** The default you get by writing nothing is exactly the one we want, and writing the one we want is what destroys it — see below the table | Before **23c**, and it is one line *not* written |
| 7 | **What is the served-request SLO?** The plan's 20 ms retrieval budget is an internal figure covering one phase of one path. Without an end-to-end objective a p95 is a number with no verdict attached, and OP10's alert list has nothing to fire on. **OP9** | **Do not invent one here.** It is the network's to set, and a target this document picks becomes a target someone charts. Ask for it as one number per action, at the served-response boundary, and record it beside the metrics that measure it | **Not before Task 25** — corrected 2026-09-09. The SLO fed OP10's alert list, and OP10 is struck, so Task 25's one instrument needs no target to be worth emitting. This gates dashboards and paging, which are nobody's task in this repo |

**Decision 6 in full, because the obvious implementation is self-defeating.** The
recommendation reads like a null decision and is not. Passing
`sdktrace.WithSampler(sdktrace.AlwaysSample())` — which is what "AlwaysSample in
Phase 1, behind `OTEL_TRACES_SAMPLER`" would compile to, and what an earlier draft
of this row said — **removes the knob the same sentence promises.**
`sdk@v1.44.0/trace/provider.go:399-403` is explicit:

> This option overrides the Sampler configured through the OTEL_TRACES_SAMPLER
> and OTEL_TRACES_SAMPLER_ARG environment variables. If this option is not used
> and the sampler is not configured through environment variables or the
> environment contains invalid/unsupported configuration, the TracerProvider will
> use a ParentBased(AlwaysSample) Sampler by default.

Read the second sentence: **passing nothing gives both halves at once.** Unsampled
by default, `ParentBased` by default, and the environment variables live. Passing
`AlwaysSample` gives the first half, throws away the second, and silently ignores
whatever a deployment sets — the worst outcome of the three, because the operator
sees the variable in their manifest and believes it is doing something. The bare
form additionally discards the parent's decision, producing exactly the holed
cross-layer traces this decision exists to prevent.

So: **`provider.go` passes no `WithSampler` option, and a comment at that
non-line says why.** A pin that holds only because someone remembered it is not a
pin, and this one is invisible by construction — there is no code to review.
Encode it behaviourally: 23c starts a child from an inbound `traceparent` whose
sampled flag is clear and asserts the child is not recorded, then flips the flag
and asserts it is. That test fails the moment anyone adds the option back.

**One thing the SDK's environment layer does not give us, and it is worth a
boot-time warning.** The parent-respecting samplers are the `parentbased_*`
values; plain `always_on` and `traceidratio` are **not** parent-based. A
deployment setting `OTEL_TRACES_SAMPLER=traceidratio` gets a per-node sampler that
ignores the inbound decision, and four independent per-node samplers produce
traces with holes — worse than no traces, because a hole reads as a dropped hop
rather than as a sampling artefact. Config already reads
`OTEL_EXPORTER_OTLP_ENDPOINT`, so the SDK's environment surface is already part of
ours; read `OTEL_TRACES_SAMPLER` there too and **warn at boot** when it is set to
anything without the `parentbased_` prefix. A warning, not a refusal:
`validateAuth` refuses the boot because a security control claiming to run and not
running is a lie about safety, and a sampler choice is not that. Document
`parentbased_traceidratio` as the value to set.

## Open questions — network level

| | Question |
|---|---|
| 1 | **`sender.id` has no reliable source.** Blocks **S10** and answers **N2**. The spec requires it and treats it as an identity; this phase neither requires `senderId` nor verifies it. Telemetry that names participants, or a phase that does not verify them — both is not available. We emit the claim flagged as a claim (`sender.unverified`); what the facilitator does with a flagged join is theirs to say |
| 2 | **What is the literal `domain` string?** `Agriculture` is a placeholder. Every OAN component must emit the identical value or grouping splits. Same for the `network.id` key name — our invention, so others must be told it |
| 3 | **Is `producer` = `discovery-service`, and who keeps the participant id list?** The Sunbird registry holds *Providers*, and a DS is not one. Answering this also decides whether `producer` and `service.name` stay one value |
| 4 | **Blocks S5 end to end.** Do the adapter and experience layer forward `traceparent`? If so, ClickStack shows one timeline across all three — the main thing a monitoring stack buys. We cannot do it alone |
| 5 | Should the adapter and experience layer emit any of Part 2 too, for consistent naming? `error` and the `beckn.*` attributes are the obvious shared ones |
| 6 | **Is a publish an AUDIT event?** It mutates a catalog and a `FULL` republish deletes resources. Modelling it as both API and AUDIT duplicates; picking one is a network call |
| 7 | **Who owns Task 24?** The thing that queries ClickHouse, shapes `resourceMetrics` and ships on a schedule does not exist. "ClickStack does it" is false — ClickHouse stores, HyperDX charts, neither exports a METRIC signal. `metric.code` also needs a registry that does not exist |
| 8 | **Per-mode retrieval timing** — worth emitting? Today a slow `lexical` and a slow `spatial` look the same in aggregate. `retrieval.embedding_ms` now covers the one phase that leaves the process; this question is what remains, and the merge step holds the per-mode results so it is cheap. Still more than the spec asks |
| 9 | **Which trace id format, and which event timestamp field, does the facilitator validate?** The spec's examples use dashed UUIDs and an ISO `time`; OTLP requires hex ids and `timeUnixNano`, which is what any OTel SDK emits (divergences 5 and 6). If the facilitator was built against the examples it will reject conformant spans — from every participant, not just us. **This needs answering before 23f, and it is the spec's bug to fix, not ours.** Carried into the plan's Open Items as **O4**, because a blocker recorded only in this document is one the plan's own blocker table does not know about |
| 10 | **Will the spec publish real schemas?** `schemas/` and `examples/` are empty placeholders. Until they are filled there is no conformance target, and every participant is interpreting prose independently — which is how five participants end up with five `domain` strings |
| 11 | **P6 (per place) and P7 — largely resolved by *The stack*, and left open only for this service.** The provider adapter holds location and commodity as first-class call-plan fields, so it answers both without anyone widening a deny-list; that is onix **U1**, and it is why the H3 policy call this row used to force before 23d is **withdrawn**. What remains genuinely open is only the narrower question below — whether *this* service should also carry a coarse geography, for the discovers that never reach a provider. **Performance "on the basic unit" — weather by location, mandi by location and crop — is asked for by the network and refused by this design.** Coordinates and `filters.expression` are both on the never-emitted list, so no per-location or per-crop breakdown is derivable today. There is a middle path this document does not yet take: the service already computes **H3 covers** (`src/indexing/`), so a coarse cell — resolution 3-4, roughly 100 km — would give location-grained performance without emitting a farmer's point. Crop is harder, because it lives inside the filter expression. **This is a policy decision, not an implementation one**, and it is recorded rather than taken: the deny-list exists for a reason and widening it is the network's call |
| 12 | **S2, S6 and S9.** **Relevance and accuracy cannot be answered from this service at all, and no attribute will fix it.** "Was it relevant?" needs to know what happened *after* the response — a select, a click, a farmer acting on it. This service is one synchronous hop that never learns the outcome; `result.empty` says a query went unmet, never that a non-empty answer was any good. Closing it needs the `select` leg (a different node — see `docs/design/registry/`) or an explicit feedback signal, either of which is a new network contract *for this service*. **Resolved elsewhere in the stack**: the experience-layer adapter sees the user, already emits traces, and is where the outcome event belongs — onix **U3**. So the answer is not "nobody", it is "not here". **Named here so it is not mistaken for something Task 23 forgot** |
