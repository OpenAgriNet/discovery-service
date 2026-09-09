# `metric.code` registry — a proposal

**Status: PROPOSAL. Binding on nothing in this repo.** It is addressed to
whoever owns the OAN network metrics registry, and it exists because Task 24 is
blocked on that registry and the block has no owner (open question 7). It
proposes twelve codes, states what each is computed from, and asks seven
questions that only the registry owner can answer.

Nothing here may be implemented as a `Scope: Network` instrument until the
registry answers. `fact.checkCodeMatchesScope` (`instruments.go:198-212`) refuses
a `Scope: Network` row with an empty `Code`, and that refusal is the point: a
code invented locally will not match the one a facilitator later publishes, and
a stream of unrecognised codes is worse than no stream. This document is the
input to the conversation, not permission to skip it.

`implementation-plan.md` remains the binding plan. Where it and this disagree,
it wins.

---

## 1. What is already settled: the shape

A `metric.code` names **one measurement**, and the action is one component of
it. The shape is:

```
<action>_api_<measurement>
```

This was in question, and the question is worth recording because the wrong
answer is attractive. The tempting answer is that `metric.code` is *just the
action* — `discover`. It is not, and the reason is not theoretical:

- **beckn-onix carries both, side by side.** `onix_http_request_count` has an
  `action` label *and* a `metric_code` label on the same counter
  (`http_metric.go:91-112`, recorded at
  `telemetry-examples/inventory.md:218-222`). Two fields on one stream is a
  positive statement that they are not the same field.
- **In onix the code happens to be redundant** — it is computed as
  `<action>_api_total_count`, a pure function of `action`, because onix has
  exactly *one* measurement. So the "it's just the action" intuition describes
  onix's stream accurately. It stops describing it the moment a second
  measurement exists.
- **The spec's own examples already break it.** `search_api_total_count` and
  `search_api_failure_percent` are the same action and two codes
  (`opentelemetry.md:1017-1019`). If the code were the action, those two would
  arrive indistinguishable — and so would every one of the three per-action
  measurements proposed below.

**Dimensions do not go in the code.** `sender.id`, `error_type` and provider
identity are `metric.label`, which is spec Optional and exists for exactly this
(`opentelemetry.md:1107-1109`). A code is what the facilitator *routes* on; a
label is what it slices by. Encoding a dimension into the code multiplies the
registry by the value set.

**`discover`, never `search`.** `search` is Beckn v1's name for this action;
v2.0.0 renamed it and this service serves v2 only. The spec's examples and the
Java `beckn-discovr` reference both carry v1 vocabulary — they are v1 artifacts
in a v2 document, not a naming mandate.

---

## 2. The constraint that shapes everything, and where the spec strains

The spec permits **one** aggregation: *"only the 'sum' aggregation and
non-monotonic only"* (`otel-specification.md:437`, quoted at
`opentelemetry.md:1091-1092`).

Every proposal below is therefore a **non-monotonic sum**, `Delta` temporality —
a per-window count or a per-window ratio, reset each window. That is a real fit
for counts and percentages: a windowed count is a number that can go down.

It is **not** a fit for two things anyone actually wants, and this is the first
thing the registry owner has to decide rather than something this document can
route around:

- **Latency percentiles cannot be expressed at all.** A p95 is not a sum of
  anything; it is a quantile over a distribution the METRIC signal has no way to
  carry. There is no histogram and no summary in the permitted set.
- **The spec's own third example is not a sum either.** `avg_api_response_time`
  is a mean. A mean of means is not a mean, so declaring one as `sum`
  aggregation is arithmetically wrong the moment two data points are combined.

So the aggregation field, as specified, admits the spec's own examples only by
not being enforced. Question 1 below is the ask. Until it is answered this
document proposes an average and marks it as the one entry that is inconsistent
with the constraint **by the spec's own precedent**, not by our choice.

**Everything else about the wire shape is already pinned** and is not re-litigated
here: `metric_uuid`, `observedTimeUnixNano`, `metric.code` per data point; `name`
and `unit` on the stream; `asDouble`, `startTimeUnixNano`, `endTimeUnixNano` per
data point; and the same Resource as the spans **except `eid`, which is `METRIC`
and not `API`** (`opentelemetry.md:1103-1122`). That last one is a single field
and it is the field a consumer routes on.

---

## 3. Where the numbers come from

Two facts about provenance decide what is cheap and what is not.

**Everything proposed is derivable from the spans Task 23 already emits.** No
new instrumentation in `src/` — which is why Task 24 cannot precede Task 23 and
why it is "probably not this repo": a stateless service behind N replicas emits
N partial counts nobody can reassemble, so the aggregation belongs in the tier
with storage (A23, `ref-impl-design.md` §Micro Observability).

**Some facts sit on span events rather than span attributes**, and the
distinction matters in exactly one direction:

| Fact | Placement | Verified |
|---|---|---|
| `beckn.action`, `error_type`, `http.route`, `sender.unidentified` | span attribute | `registry.go` — `Event` unset |
| `result.empty` | event `ResponseInfo` **and span attribute** | `registry.go:646` — the one `PromoteToSpan` row |
| `result.provider_ids` | event `ResponseInfo` | `registry.go:676` |
| `retrieval.modes_degraded` | event `RetrievalInfo` | `registry.go:633` |
| `publish.resource_count`, `publish.provider_ids` | event `RequestInfo` | `registry.go:743`, `:717` |

An event-level fact is **available to Task 24 but not free**. A query over
stored spans can join events, so nothing here is blocked — but
`opentelemetry.md:1000-1005` records that event attributes are harder to
aggregate in HyperDX than span attributes, which are a queryable map, and names
`result.empty` as the one candidate worth promoting to a span attribute for
exactly that reason. That note is about *this* path, not only the collector's.

The collector's `spanmetrics` connector is a second and stricter case: it reads
span attributes **only** and cannot reach an event at all. That is what settled
`result.empty` — it is now carried on both the event and the span
(`Definition.PromoteToSpan`), so it is cheap to aggregate here *and* nameable
as a collector dimension, which it was not before. The promotion copies rather
than moves, so `response_info`'s shape is unchanged; `fact.Validate` and
`TestOnlyTheDeclaredRowsArePromoted` keep it individual rather than wholesale,
because the events are the interop contract.

**The third state is load-bearing.** A promoted attribute is *absent*, not
false, on a publish span and on a discover that errored before responding.
`result.empty != false` therefore counts every failure as demand that was met;
the query is `result.empty = true`.

---

## 4. Proposed codes

Twelve codes in three tiers. The tiers are about **what still has to be
answered**, not about importance.

Common to all: `sum` aggregation, non-monotonic, `Delta` temporality,
proposed granularity **1-minute window**, proposed frequency **every 60s**.
Deviations are called out per row.

### Tier A — ready. Bounded labels, span attributes only, no open questions

| Code | Unit | Measures | Computed from |
|---|---|---|---|
| `discover_api_total_count` | `1` | discover requests served in the window | span count where `http.route` = `/discover` |
| `publish_api_total_count` | `1` | publish requests served | span count where `http.route` = `/publish` |
| `discover_api_failure_percent` | `%` | share of discover requests refused or failed | spans with `error_type` present ÷ total; label `error_type` |
| `publish_api_failure_percent` | `%` | same, for publish | as above |
| `discover_api_empty_result_percent` | `%` | **unmet demand** — asked, and we had nothing | spans with `result.empty` = true ÷ total |

These five are the ones to ratify first if the registry wants to move
incrementally. `discover_api_total_count` is deliberately byte-identical in
shape to onix's computed `<action>_api_total_count`, so a facilitator already
consuming onix needs no new case.

**`discover_api_empty_result_percent` is the one worth arguing for hardest. It
is the only code on this list that no other participant can produce.** An
adapter sees that a request was answered; only the service that ran the query
knows the answer was empty. Unmet demand is the number that tells a network
operator which sectors have no supply, and it exists nowhere else in the stack.

It is in Tier A rather than Tier B because `result.empty` was promoted onto the
span — `Definition.PromoteToSpan`, the only row that sets it. It remains on
`response_info` as well, so the event shape a facilitator reads is unchanged.
Note the third state: the attribute is **absent** on publish and on a discover
that errored before responding, and absent is not false. A query must say
`result.empty = true`, never `!= false`, or it counts every failed request as
demand that was met.

**One caveat that a dashboard author must be told, because it is counter-intuitive
and it is measured, not assumed.** Do not compute failure from span status.
`setStatus` (`middlewares/trace.go:202-207`) moves the span status only at 5xx,
on the deliberate reasoning that a 400 is the caller's mistake and counting it as
a server error reports how often *this service* broke when it did not. So every
4xx refusal — the whole of C1's `CONTEXT`, `DOMAIN`, `POLICY` and most of `CORE`
— arrives `STATUS_CODE_UNSET`, indistinguishable from a success. Verified on a
live stack: the three `DOMAIN` refusals in `examples/verify.sh` land as
`error_type=DOMAIN, status_code=UNSET`. **`error_type` is the error signal here,
not status.** A percentage derived from status alone reports 0% on a service
refusing every request it receives.

### Tier B — computable today, but each has one thing to settle first

| Code | Unit | Measures | Computed from | To settle |
|---|---|---|---|---|
| `discover_api_degraded_mode_count` | `1` | retrieval running without a mode | `retrieval.modes_degraded` on `RetrievalInfo`; label `mode` | needs an event join |
| `discover_api_unattributed_percent` | `%` | requests with no identifiable sender | `sender.unidentified`, a span attribute, ÷ total | is the number worth publishing |
| `discover_api_response_time_avg` | `ms` | mean discover latency | span end − start | question 1 |

Only the first needs an event join, and it is the only fact left on this list
that does. `retrieval.modes_degraded` is `KindStrings` on `RetrievalInfo`, and
unlike `result.empty` it was not promoted onto the span: a string-slice
dimension is a series per distinct *combination* of degraded modes, so
promoting it needs a scalar form — a count, or a bool per mode — which is a
design question rather than a one-line change. Until then this code is a query
over stored spans and cannot be derived by a collector.

`discover_api_unattributed_percent` measures the size of a hole rather than a
service property: `sender.id` is optional, unverified and often absent because
Task 6 (signature verification) is **parked** by decision. This code is how you
find out whether that parking is costing anything. If it reads near 100%,
`sender.id`-labelled anything is worthless network-wide — which is a fact the
registry owner should want before designing labels around participant identity.

`discover_api_response_time_avg` is the entry flagged in §2. A mean is not a sum
and cannot be re-aggregated across data points; it is proposed only because the
spec's `avg_api_response_time` example establishes the precedent. **The real ask
is question 1.** If the answer is "percentiles are out of scope for this spec
version", this row should be dropped rather than shipped as a mean somebody will
average again downstream.

### Tier C — blocked on question 2 (per-participant labels)

| Code | Unit | Measures | Computed from |
|---|---|---|---|
| `discover_api_provider_served_count` | `1` | **provider serve share** — which sources actually answer, and which never do | distinct `result.provider_ids` per span; label `provider` |
| `publish_api_resource_count` | `1` | publish volume per provider | `publish.resource_count`; label `provider` |
| `publish_api_catalog_age_seconds` | `s` | **catalog freshness** — time since a provider last published | now − last `RequestInfo` per `publish.provider_ids`; label `provider` |
| `discover_api_provider_failure_percent` | `%` | failure concentration by provider | `error_type` grouped by provider |

All four carry a `provider` label, and **`result.provider_ids` and
`publish.provider_ids` are `Cardinality: Unbounded` in our registry**
(`registry.go:683`, `:724`). That classification is correct for us: this service
cannot know how many providers exist. The *registry* can — the participant
registry (`docs/registry.md`) enumerates them. So the ceiling is
knowable, just not here. Hence question 2.

`publish_api_catalog_age_seconds` additionally does not fit the windowed-count
model: it is a **level**, not a count of events in a window. Reporting it as a
non-monotonic sum is the closest the permitted aggregation gets, and it is the
second place the sum-only rule bites. Granularity is per-observation; frequency
every 60s.

---

## 5. Deliberately not proposed

| Not proposed | Why |
|---|---|
| Latency percentiles (p50/p95/p99) | Unexpressible under sum-only. Question 1 |
| Anything from Task 25 | `pgxpool.empty_acquire` and its wait time are `Scope: Node`, monotonic, and per-replica — outside the METRIC profile on aggregation type alone. They are the node operator's, and `filter/network_metrics` is what stops them leaking |
| Anything the collector derives | `discovery_calls_total` and `discovery_duration_milliseconds` are a monotonic counter and a histogram. They answer the same questions for a local operator and are **not** METRIC-signal candidates. Naming them as though they were is how an unregistered stream reaches a facilitator |
| `retrieval.embedding_ms` as its own code | `Cardinality: Unbounded` (`registry.go:652`) and semantic search is deferred (A5). It survives as a span fact, which is what an exporter aggregates over |
| A per-`sender.id` breakdown of anything | Unbounded, and `discover_api_unattributed_percent` suggests the values are mostly absent anyway. Revisit if Task 6 is ever unparked |

---

## 6. Labels and the cardinality budget

`metric.label` (**not** `label` — an earlier draft of `opentelemetry.md` dropped
the prefix) is the only place a dimension belongs.

| Label | Values | Source |
|---|---|---|
| `error_type` | 5 — `CONTEXT`, `CORE`, `DOMAIN`, `POLICY`, `SYSTEM` | `registry.go:102` |
| `mode` | 5 — `lexical`, `fuzzy`, `semantic`, `spatial`, `jsonpath` | `registry.go:104` |
| `provider` | **unknown to us** — question 2 | `result.provider_ids` / `publish.provider_ids` |

`fact.MaxLabelSeries` is **200** per instrument (`instruments.go:56`), checked per
instrument rather than per attribute because the accident is multiplicative:
four labels can each be honestly `Bounded`, no single row wrong, and still
multiply to 800 streams. Tier A and B stay far under it. Tier C's ceiling is
whatever the registry says the provider count is, which is the whole of question
2 — at 200 providers one label alone exhausts the budget.

---

## 7. Questions for the registry owner

Numbered so they can be answered individually. 1 and 2 block work; the rest
prevent divergence. Note that these numbers are local to this list — question 6
refers to the plan's "open question 7", which is a different sequence.

1. **Is the sum-only, non-monotonic constraint intended to exclude latency
   distributions?** If yes, percentiles are out of scope for this spec version
   and `discover_api_response_time_avg` should be dropped rather than shipped as
   a re-averagable mean. If no, the constraint needs widening — and the spec's
   own `avg_api_response_time` example needs it too, since a mean is not a sum
   either.

2. **Are per-participant labels admissible, and what is the ceiling?** Four
   proposed codes carry a `provider` label. Provider identity is `Unbounded`
   from inside this service and bounded from inside the participant registry. We
   need a number, or a rule that says aggregate-only.

3. **Is `<action>_api_<measurement>` normative or onix-local?** It is where the
   spec's `search_api_total_count` example comes from, but the spec's third
   example — `avg_api_response_time` — follows a *different* shape:
   `<statistic>_api_<measurement>`, with no action at all. Two of three examples
   agree and the third does not. Until this is settled, two participants can
   name the same measurement differently and both pass. Already raised at
   `telemetry-examples/inventory.md:321-324`; repeated here because it is now
   load-bearing on twelve names.

4. **What are granularity and frequency, per code?** Task 24 must run one query
   per code *on the registry's stated* granularity and frequency
   (`implementation-plan.md` Task 23f). We propose a 1-minute window shipped
   every 60s uniformly, which is a guess and is stated as one. Levels such as
   `publish_api_catalog_age_seconds` do not have a window at all.

5. **How is `metric.category` assigned?** onix computes it as `Discovery` when
   the action ends `/search` or `/discovery`, else `NetworkHealth`
   (`inventory.md:221-222`). **Our route is `/discover`**
   (`router.go:95`, and `http.route` is pinned to `{/discover, /publish}` at
   `registry.go:291`), which is a suffix of neither. If that reading of onix is
   right, every OAN discover metric passing through onix is categorised
   `NetworkHealth` — the discovery network's discovery traffic filed under
   health. This needs confirming against onix's actual action string, which
   cannot be done from this repo; it is flagged, not asserted.

6. **Who runs the exporter?** Not a registry question, but the one that decides
   whether any of this ships. Nothing today queries the store, shapes
   `resourceMetrics` and POSTs on a schedule. "ClickStack does it" is false —
   ClickHouse stores, HyperDX charts, neither exports a METRIC signal. This is
   the plan's open question 7 — a different sequence from this list — and it has
   no owner.

7. **Does the registry care which *producer* computes a code, or only that the
   code and its value are right?** Every code above is derived from spans rather
   than instrumented, and three different things can do that derivation: the
   collector's `spanmetrics` connector (YAML, node-local, running today), a query
   over stored spans (what §3's tiers assume), or a real `fact.Instrument` in this
   process. We would rather the answer be "we do not care", and for the *value*
   it genuinely does not — export here is unsampled by construction, so a
   span-derived count equals an instrumented one.

   It is asked because four things are not value-equivalent, and a registry that
   is silent on producer will get all four wrong somewhere in the network:

   - **Wire type.** The METRIC signal permits a non-monotonic sum only.
     `spanmetrics` emits a monotonic counter plus a histogram, so a connector
     stream can never *be* the submitted signal — only a source something else
     reshapes. That interacts directly with question 1.
   - **Sampling is one environment variable away.** Unsampled export is a
     property of this deployment, not of the approach: we do not pass
     `WithSampler`, precisely so `OTEL_TRACES_SAMPLER` keeps working. A
     participant who sets it ships span-derived counts that undercount silently,
     with nothing on the wire saying so. An in-process instrument would not.
   - **Event-level facts are unreachable from the connector.** `spanmetrics` can
     name a span attribute as a dimension and cannot see a span *event* at all.
     That is why `result.empty` had to be promoted onto the span before
     `discover_api_empty_result_percent` was computable — a registry that assumes
     any span field is available will propose codes some producers cannot answer.
   - **Cardinality governance moves out of the code.** A connector `dimensions`
     entry multiplies series from a YAML file that `fact.MaxLabelSeries` cannot
     see, so §6's budget stops being enforceable by the guards §8 describes.

   If the answer is "any producer, we validate only the payload", say so
   explicitly and we will treat the four above as our own problem. If the
   registry intends to *require* a producer — or to require that submitted
   metrics be sampling-independent — that is a constraint we need before Task 24
   picks an implementation, not after.

---

## 8. How this gets encoded, once answered

Each ratified code becomes one `fact.Instrument` row with `Scope: Network` and
`Code` set — the same table Task 25's two rows already live in
(`instruments.go:95-115`). The guards then do the work: `Code` non-empty is
enforced for `Scope: Network`, the label product is checked against
`MaxLabelSeries`, `Temporality` may not be left unspecified, and the golden file
makes each addition a reviewable diff rather than a claim.

That is the reason to answer the questions rather than route around them. The
table refuses to hold an invented code today, and that refusal is the only thing
currently preventing this service from emitting a stream no facilitator can
interpret.
