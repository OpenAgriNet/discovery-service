# Telemetry: build vs reuse — audit

**Status: audit only. Binding on nothing. No file outside this one changed.**

Written 2026-09-09, after the observation that units-api produces OpenTelemetry
in a fraction of the code this service does and that the difference did not look
earned. This document answers whether it is earned, piece by piece, with the
measurements rather than the argument.

It deliberately does not recommend a single verdict for "the telemetry design".
The design is not one decision; it is about eight, and they do not all come out
the same way.

## Method, and what it could not check

Line counts are `wc -l` over `src/platform/telemetry/`, `middlewares/trace.go`
and the two logger files that carry the projection. Column-consumption counts
are `grep` for `.Field` across `src/` excluding `telemetry/fact/` itself, so a
column used only by its own package reads as zero.

Library versions were resolved live against `proxy.golang.org`. **Their APIs were
not compiled against.** Nothing here has been built; the effort estimates are
readings of published interfaces, not of a working branch. Treat every "would
replace" as a hypothesis with a known version number attached, not a result.

## Where the code actually is

| | Lines |
|---|---|
| `fact/registry.go` | 910 |
| `fact/fact.go` | 452 |
| `fact/record.go` | 267 |
| **`fact/` subtotal** | **1,629** |
| `telemetry.go` (init, exporter, scope) | 240 |
| `project_span.go` / `project_event.go` / `project_resource.go` | 367 |
| `build.go`, `propagation.go`, `spanuuid.go` | 194 |
| `testsupport.go` | 142 |
| **`telemetry/` non-test total** | **2,572** |
| `middlewares/trace.go` | 228 |
| `logger/project_fact.go` + `logger/logger.go` | 224 |
| **telemetry tests** | **2,022** |

`fact/` is 63% of the non-test code. Everything else — init, exporters, the
middleware, propagation, build info — is 940 lines put together.

This matters because the argument that has been used to justify the whole design
is an argument about `trace.go`, which is 228 lines, 9% of the total, and about
half comment. **The reuse question and the size question are not the same
question, and conflating them is how the design got here.**

## Finding 1 — the reason we reject `otelhttp` is misquoted in five places

`network-telemetry-spec/docs/otel-specification.md:140-145`:

```js
"scope": { // Optional
  "name": "String",    // Required. ... For ex: service name
  "version": "String", // Required. A version number of the network telemetry specification
```

and line 86: scope is *"intended only for transport & validity checks, without
any impact on the actual data and its usage."*

Five locations state instead that the spec **requires** `scope.name` and
`scope.version` **on every exported batch**:

| File | Line |
|---|---|
| `docs/adr/0011-telemetry-opentelemetry.md` | 65-66 |
| `docs/design/discover-and-publish.md` (A23) | 148 |
| `docs/design/discover-and-publish.md` | 5006-5007 |
| `docs/design/discover-and-publish.md` | 5049 |
| `src/platform/middlewares/trace.go` | 24-25 |
| `docs/design/implementation-prompts.md` | 186 |

The block is **Optional**, and `version` is *the spec's* version, not a library's.

**The code is correct.** `telemetry.go:32-34` says "Optional" and sets
`ScopeVersion = "1.0"`. Only the justification is wrong.

**The conclusion survives, on better ground.** `otelhttp` would report
`v0.69.0` in `scope.version`. The spec wants `"1.0"` there. No instrumentation
library will ever put a *specification* version in a field OTel defines as the
instrumentation library's version — this is a field the spec repurposed. So
hand-rolling `Trace` remains right, but because the spec collides with the OTel
data model, not because otelhttp omits something mandatory.

That is a smaller claim than the one on record, and it justifies exactly one
file.

## Finding 2 — `Visibility` is consumed by nothing

Every `Definition` column, counted by consumers outside `fact/`:

| Column | Consumers |
|---|---|
| `SpanKey` | 52 |
| `Kind` | 23 |
| `Values` | 15 |
| `TruncationFlag` | 14 |
| `AbsentFlag` | 13 |
| `LogKey` | 12 |
| `Signals` | 10 |
| `PresentFlag` | 6 |
| `Cardinality` | 1 |
| `ZeroIsAbsent` | 0 outside `fact/` — but read by `record.go:126,135` and `fact.go:327` |
| `Layer` | 0 outside `fact/` — but read by the cross-layer fixture, `registry_test.go:403-416` |
| **`Visibility`** | **0 runtime consumers, anywhere.** Two test consumers inside `fact/`: `registry_test.go:56` asserts no row is left `VisibilityUnspecified`, and `:89` sets it on a synthetic fixture. Neither checks that the value *does* anything — the first checks only that the table is fully populated. |

`fact.go:89` says it plainly: *"Visibility governs 23f's facilitator deny-list."*

`fact.go:90-91` goes further and describes behaviour that does not exist: *"the
projection treats Unspecified as LocalOnly at runtime as well as in the test."*
There is no facilitator projection at runtime. Whatever is decided about the
column, that sentence is currently untrue on main.

**23f does not exist.** `src/platform/telemetry/redact.go` is not a file. 23f is
blocked on O1/O2/O4 — three questions the network has not answered — and may
never land.

So 54 rows each carry a `Visibility` value, the golden file pins all 54, and
every future row will have to choose one, for a consumer that does not exist and
is not scheduled. This is the clearest instance of the general pattern: **we
built the governance layer before the thing it governs.**

It is also the smallest thing to act on. Deleting the column is mechanical: one
type, one struct field, 54 declarations, one golden regeneration. Re-adding it
when 23f unblocks is the same work in reverse, and 23f would be the commit that
knows what the deny-list actually needs — which today nobody does.

## Finding 3 — the library-by-library verdict

| Candidate | Latest | Would it replace ours? | Verdict |
|---|---|---|---|
| `otelhttp` v0.69.0 (already in `go.mod`, unused) | — | `trace.go`, 228 lines | **No.** Finding 1's `scope.version` collision. But delete the unused dependency or use it. |
| `contrib/bridges/otelzap` | v0.20.1 | Not 23e. | **No, and my earlier framing was wrong.** The bridge routes zap records into an OTel **LoggerProvider** — the logs signal over OTLP. `telemetry.go:102-123` builds a `TracerProvider` only; logs leave this process as zap JSON on stdout for the collector to scrape. Adopting otelzap is a *transport change* for logs, not a way to delete 23e's ~35 lines. Worth considering on its own merits, separately, and it is a bigger change than the one it was proposed to avoid. |
| `github.com/exaring/otelpgx` | v0.11.1 | Task 25's one instrument, plus DB query spans we do not have | **Genuinely promising, and the single best candidate here.** It is a `pgx` tracer, which is exactly our seam (`storage/postgres/pool.go`). Needs checking against pgx v5 and against whether its spans carry our scope — if it obtains its own tracer, Finding 1's collision applies to it too, and that is the thing to test first. |
| `contrib/exporters/autoexport` | — | `telemetry.go:151-226`, ~75 lines | **Marginal.** Replaces a small, working, well-understood function with a dependency, and gives up the explicit `OTEL_EXPORTER=none` path the tests rely on. Low value. |
| `contrib/instrumentation/runtime` | — | Nothing we have | Would *add* Go runtime metrics. Fails the layer test the same way Task 25's struck instruments did — cAdvisor already reports the container envelope. **No.** |
| `fact/` (1,629 lines) | — | — | **Nothing off the shelf does this.** Per-attribute governance across span/log/metric for a Beckn facilitator has no equivalent. The question is not whether to reuse it; it is whether we need all of it *now* — see Finding 2. |

## What this adds up to

The instinct that the implementation is too large is right, but the cause is not
a failure to reuse. Of six candidate libraries, one (`otelpgx`) is worth real
investigation and the rest are either blocked by the spec's own quirk or would
add code rather than remove it.

The cause is **sequencing**. `fact/` is a governance layer built to the
requirements of 23f, and 23f is blocked indefinitely. The 2,022 test lines
largely pin that layer's invariants. If 23f never lands, the `Visibility`
column is dead outright, and a good deal of the registry's ceremony is insurance
against a risk that was never underwritten.

## What I would do, in order

1. **Correct the five misquotes** (Finding 1). Cheap, clearly right, and it
   replaces a wrong reason with a sharper one. Amends ADR-0011's reasoning, not
   its decision.
2. **Delete the `Visibility` column** (Finding 2) and record in the plan's
   Deferred section that 23f reintroduces it. Removes dead weight from all 54
   rows and from every future one.
3. **Spike `otelpgx`** against Task 25 before implementing Task 25 by hand. One
   question decides it: does it let us supply our own tracer, or does it obtain
   one with its own scope? If the former, it likely subsumes Task 25 and adds DB
   spans for free. If the latter, Finding 1 rules it out and Task 25 proceeds as
   planned — one instrument, two counters.
4. **Resolve the unused `otelhttp` dependency** — either drop it from `go.mod` or
   note why it is retained.

Steps 1, 2 and 4 are doc-and-mechanical. Step 3 is the only one that could
change an architectural decision, and it is a spike, not a commitment.

## What I did not do

Compile against any candidate library. Change any binding document. Touch any
code. Assess whether the 2,022 test lines are proportionate — that needs the
`Visibility` decision made first, since it determines how much of the registry
suite survives.
