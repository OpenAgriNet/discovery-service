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

| File | Line | |
|---|---|---|
| `docs/adr/0011-telemetry-opentelemetry.md` | 65-66 | **corrected** |
| `docs/design/discover-and-publish.md` (A23) | 148 | **corrected** |
| `docs/design/discover-and-publish.md` | 5006-5007 | **corrected** |
| `docs/design/discover-and-publish.md` | 5049 | **not a misquote** — "ours and not a dependency's" is what the test pins and is true |
| `src/platform/middlewares/trace.go` | 24-25 | **corrected** |
| `docs/design/implementation-prompts.md` | 186 | **corrected** |
| `docs/design/opentelemetry.md` | 1282 | **missed by this audit**, found on 2026-09-09 while applying it — a verbatim quote of `trace.go`'s old comment, kept as a quote with the error marked, because a quote silently improved stops being evidence |

So the count was five and the locations were six, one of which was innocent and
one of which was elsewhere. `opentelemetry.md:442-443` had it right all along
and is the sentence the corrections were written against. `telemetry-seam.md:597`
called `scope.version` "a Required field" without saying required *inside an
optional block*; tightened rather than counted, since it is not the otelhttp
argument.

**All corrected on 2026-09-09.** The finding below is what they now say.

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
| `ZeroIsAbsent` | 0 outside `fact/` — but read by `record.go:112,121` and `definition.go:368` |
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

**Applied on 2026-09-09.** The column is gone from `fact.go`, from all 54 rows,
from the completeness test and from the golden file. `discover-and-publish.md`'s
Deferred table carries the entry that puts it back with 23f, and
`telemetry-seam.md` §3 keeps the type declaration and its rationale verbatim so
23f restores the design rather than re-deriving it.

One fact found while applying it, which the audit above had not measured and
which makes the case stronger than it argued: **all 54 rows carried `Public`.
Not one row anywhere in the registry ever carried `LocalOnly`.** So the column
was not merely unconsumed — it had never distinguished one row from another. A
deny-list filter over a field with one value in practice would have denied
nothing, and the test at `registry_test.go:56` that asserted no row was left
`Unspecified` was checking that 54 authors had each typed the same constant.

Two things did **not** change, and both are the point of the finding rather than
casualties of it. `fact.go`'s claim that "the projection treats Unspecified as
LocalOnly at runtime" described behaviour that never existed, so deleting the
column deleted a false sentence rather than a guarantee. And Task 26's
conformance test — which asserts over the serialised **bytes** of the
facilitator projection, regexing string *values* — was always the actual
enforcer, because the privacy risk lives in values and no per-row column over
keys can reach it. See Finding 1's neighbour in `telemetry-seam.md` §5f, which
was corrected in the same pass: it had been left claiming Task 26 could move
into 23d, contradicting `opentelemetry.md` OP11.

## Finding 3 — the library-by-library verdict

| Candidate | Latest | Would it replace ours? | Verdict |
|---|---|---|---|
| `otelhttp` v0.69.0 (in `go.mod` as `// indirect`) | — | `trace.go`, 228 lines | **No.** Finding 1's `scope.version` collision. ~~But delete the unused dependency or use it.~~ **That half was wrong and is resolved: it is not ours to delete.** `go mod why` traces it to `tests/dbtest` → `testcontainers-go/modules/postgres` → `moby/moby/client`, which imports it directly. `// indirect` is the correct marking, `go mod tidy` leaves `go.mod` byte-identical, and removing the line by hand would be undone by the next tidy. Verified 2026-09-09. |
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

1. ~~**Correct the five misquotes** (Finding 1).~~ **Done 2026-09-09.** Cheap,
   clearly right, and it replaced a wrong reason with a sharper one. Amended
   ADR-0011's reasoning, not its decision. The count was five and the locations
   were six — see Finding 1's table.
2. ~~**Delete the `Visibility` column** (Finding 2) and record in the plan's
   Deferred section that 23f reintroduces it.~~ **Done 2026-09-09.** Removed
   dead weight from all 54 rows and from every future one; the Deferred entry
   is in place and `telemetry-seam.md` §3 keeps the design for 23f.
3. ~~**Spike `otelpgx`** against Task 25 before implementing Task 25 by hand.~~
   **Overtaken by events — this step can no longer be taken as written.** Task 25
   shipped by hand in `4c52ce0`, before this audit was written, so the "before
   implementing" framing was already false on the day it was recommended. The
   underlying question survives and is now a different, smaller one: does
   `otelpgx` supply spans for DB queries we do not currently have, and can it
   accept our tracer rather than obtaining one with its own scope? If it obtains
   its own, Finding 1's `scope.version` collision applies to it too. Worth a
   spike on its own merits; **not** a gate on anything, and not a reason to
   revisit Task 25's two counters, which are a *levels* signal `otelpgx` does
   not produce.
4. ~~**Resolve the unused `otelhttp` dependency**.~~ **Done 2026-09-09 — by
   disproving the premise.** It is not unused and not ours to delete: `go mod why`
   traces it to `tests/dbtest` → `testcontainers-go/modules/postgres` →
   `moby/moby/client`, which imports it directly. `// indirect` is the correct
   marking and `go mod tidy` leaves `go.mod` byte-identical. See Finding 3.

Steps 1, 2 and 4 were doc-and-mechanical and are complete. Step 3 was the only
one that could have changed an architectural decision; it is now a standalone
spike rather than a commitment or a gate.

## What I did not do

Compile against any candidate library. Change any binding document. Touch any
code. Assess whether the 2,022 test lines are proportionate — that needs the
`Visibility` decision made first, since it determines how much of the registry
suite survives.
