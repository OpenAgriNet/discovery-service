# ADR-0011 — OpenTelemetry for tracing

**Status:** Accepted
**Date:** 2026-08-25
**Amended:** 2026-09-07, 2026-09-08 — see Amendments

## Context

TRD §6 and §7 want a request followable across a network hop, and RED metrics
per route. This service sits in the middle of a Beckn chain, so the hop is the
normal case rather than the exception.

## Decision

OpenTelemetry tracing, W3C Trace Context propagated in and out, OTLP exporter
defaulting to `none`. zap carries structured logs alongside, correlated by trace
id.

The tracing middleware is **hand-rolled** against `go.opentelemetry.io/otel/trace`
rather than mounted from `otelhttp`.

**The network's METRIC signal is not emitted from this process.** Its RED
figures are computed downstream, from the spans, by a component outside this
binary. **Node-operator metrics are emitted from this process** — the two are
different obligations to different audiences, and Amendment 3 says why.

## Alternatives considered

- **zap alone** — structured logs correlate within one process. Only a
  propagation standard makes one request followable across the chain, and the
  chain is the point.
- **`otelhttp`** — the obvious way to instrument a `http.Handler`, and the
  original decision. Rejected under Amendment 1 below.
- **In-process metrics (Prometheus client, or an OTel meter)** — the original
  decision. Rejected for the *network* signal under Amendment 2, reinstated for
  the *operator* signal under Amendment 3.

## Consequences

The exporter defaults to `none` so a collector-less deploy still boots — a
telemetry dependency that prevents startup is a telemetry dependency that gets
removed. Dashboards and analytics over the exported data are out of scope for
this service (an add-on, e.g. Obsrv, owns them).

Hand-rolling the middleware means roughly thirty lines this repository owns and
tests, in exchange for control of the instrumentation scope. Emitting no network
metrics means the participant's mandatory METRIC obligation is discharged
elsewhere, and `docs/design/discover-and-publish.md` Task 24 is where that is
tracked so it is not mistaken for solved. Task 25 is the operator set that stays
here.

## Amendments

**2026-09-07, by A23.** This ADR read "OpenTelemetry traces **and metrics**,
`otelhttp` instrumentation". Both halves were wrong by the time Task 23 was
specified, and the record is corrected here rather than left to contradict the
plan — an accepted ADR disagreeing with a binding plan is a decision the next
reader has to arbitrate.

What changed is not the decision to adopt OpenTelemetry. It is two details
inside it, and both moved for the same reason: tracing stopped being local
debugging and became an interop contract with the Sunbird-Obsrv network
telemetry spec, which a facilitator consumes.

1. **`otelhttp` cannot be used.** The spec requires `scope.name` and
   `scope.version` on every exported batch. The instrumentation scope is fixed
   when the span is created and is immutable afterwards, so a span started by
   `otelhttp` carries *that package's* name and version permanently and no later
   call can correct it. This was not a consideration when the ADR was written,
   because nothing then read the scope.

2. **Metrics leave the process.** A stateless service behind N replicas
   computing a counter in memory emits N partial counts that no consumer can
   reassemble, because nothing on the wire says what N was.
   `ref-impl-design.md` §Micro Observability puts metric computation in the tier
   that has storage and aggregation for exactly this reason. The obligation is
   real and mandatory for the participant — it is discharged by Task 24, over
   the spans this ADR's tracing produces, not by a meter here.

**2026-09-08, by A25.** Amendment 2 was read as "this service emits no metrics",
and that is not what its argument supports. It is narrowed here rather than
reversed.

3. **Amendment 2 covers the network signal only.** The reasoning is that a
   stateless replica cannot compute a windowed *network* aggregate — true, and
   it is why Task 24 exists. It says nothing about the *levels* a node operator
   reads to keep this replica alive, which are per-replica by definition and so
   have nothing to reassemble. Read as a blanket ban it produced a service with
   three configured ceilings — the connection pool, the rate limiter, the body
   size — and no way to see any of them approached.

   The first version of this amendment justified that with "a 429 is written by
   middleware above the handler and therefore produces no span, no event and no
   counter". **That is false, and it is corrected here rather than quietly
   dropped, because it was the sentence the task was sized against.** `Trace` is
   index 1 in `src/app/router.go:134-141`; `Envelope` and `RateLimit` are inside
   it and refuse through the one `httpx.WriteNack` → `logNack` path 23d projects
   the `error` event from. A 429 is a span with a status, an `error_type` and an
   `error` event. Only "no counter" held, and a counter restating a span fact is
   the second copy `opentelemetry.md`'s *No duration attribute* already refuses.

   What survives is narrower and does not depend on that claim: a ceiling is a
   **level** and a span is an **event**, so the distance to a ceiling is
   observable only by sampling it on a clock, and no aggregation over spans
   recovers it. Task 25 emits that set, under our own scope, and is not blocked
   by what blocks Task 24. It is smaller than this amendment first described it:
   no rejection counters, and no per-provider freshness — that one is a `SELECT`,
   and as a metric it would be a windowed aggregate over N partial replicas,
   which is Task 24's definition rather than this one's.

4. **Propagation is not enough on its own.** "W3C Trace Context propagated in
   and out" is necessary and does not by itself make this service visible to the
   network. beckn-onix's collectors admit spans on one attribute spelling and
   join them on another, so a correctly propagated span can still be dropped
   after export or stitched to nothing — with no error reported anywhere. The
   alias attributes that fix it are named in `opentelemetry.md` *The stack — who
   answers what*; recorded here because "we propagate `traceparent`" is the
   sentence that makes someone think the interop question is closed.

The wire shape those spans must have is `docs/design/opentelemetry.md`, which is
binding on the shape of a span. This ADR remains the decision to use
OpenTelemetry at all.
