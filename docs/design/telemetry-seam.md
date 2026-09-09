# The telemetry seam — one table, four projections

`opentelemetry.md` says **what** this service emits and why. This document says
**where the code that emits it lives**, and it exists to make one property true:

> Adding an attribute, renaming one, changing its cardinality, or deciding it may
> not leave the process is **one edit, in one file**, and it lands correctly on
> the span, the log line, the metric label and the Resource — or fails the build
> saying which one it could not reach.

That property does not survive good intentions. A rule that says "keep the span
and the log in step" is a rule someone breaks on a Tuesday, and the failure is
silent: the span has the attribute, the log line does not, both are valid, no
test fails, and the gap is discovered by whoever queries for the field that is
missing. So the property has to be structural — the projections must be unable
to name a key, and every call site must be unable to spell one.

Binding: `discover-and-publish.md` still wins on task shape and acceptance
criteria, and `opentelemetry.md` still wins on what each attribute means. This
document is subordinate to both and governs only the code layout.

---

## 1. What the spec makes us hold

`network-telemetry-spec/docs/otel-specification.md` defines three signals — API
(our spans), METRIC, LOG/AUDIT — that share one envelope:

```
resource<type>  ·  resource  ·  scope<type>  ·  scope  ·  <type>
```

Every signal carries the same Resource (`eid`, `producer`, `domain` are
Required), the same Scope (`scope.name`, `scope.version`), and its own body. Two
consequences drive the whole design:

**One Resource, constructed once.** The spec requires `producer` and `domain` to
agree across all three signals. Two construction sites is how they stop agreeing,
and the disagreement is invisible — each signal validates alone. So `Init` builds
the Resource and every provider that stamps it, and Task 24 and Task 25 receive
it rather than rebuilding it.

**`eid` differs per signal and nothing else does.** Three literal values, and
the spec is explicit about each: `API` for spans, `METRIC` for metrics and
**`AUDIT`** — not `LOG` — for log records (`otel-specification.md:271`, `:446`,
`:599`). The signal is *called* LOG/AUDIT and its `eid` is `AUDIT`; onix gets
this right (`otelsetup.go:122,143,159`) and it is the kind of detail a second
implementation guesses wrong. It is the one Resource attribute the projections
must vary, which is why it is a registry row with a projection rule and not a
literal in `Init`.

**Scope is fixed at span creation and immutable afterwards.** This is why A23
rejects `otelhttp` — a span that package starts carries *its* scope for ever, and
no later call corrects it. It is also why `Provider` hands out a pre-scoped
`Tracer()` rather than the provider itself: you cannot accidentally acquire an
unscoped tracer if the unscoped one is never reachable.

### Where we knowingly differ

`opentelemetry.md` carries the divergence list. Three of its entries are
**shape**, not naming, and this document draws the line they sit on because
getting it wrong puts protocol logic in the attribute table:

> **Same value under two keys → the registry** (an `Alias`).
> **Same key, different serialisation of an OTLP structural field → the exporter.**

- `status` is `{"code":"STATUS_CODE_OK"}` on the wire; the spec's examples show
  `"Ok"`. `status` is an OTLP `Span` **field**, not an attribute — no
  `attribute.KeyValue` can name it, so no `fact.Key` can either. Exporter.
- `kind` is `SPAN_KIND_SERVER`; the spec says `Server`. Same: an enum on the
  `Span` message. Exporter.
- `http.route` **is** an attribute, and both sides spell it identically. The
  divergence is in the *value* — we emit the route template, the spec's prose
  says URL. That is a value-derivation decision made at the observation site, and
  it is recorded in the registry as prose (`Definition.Note`) so a reader does not
  "fix" it toward the spec and start shipping query strings.

A test keeps the line drawn, because a rule stated only in prose lasts until the
first person who has not read it:

```
--- FAIL: TestNoDefinitionNamesAnOTLPStructuralField
    registry_test.go:96: Definition "SpanStatus" spells SpanKey "status".
        "status", "kind", "name", "traceId", "spanId", "parentSpanId",
        "startTimeUnixNano", "endTimeUnixNano" and "events" are OTLP Span FIELDS,
        not attributes: the SDK produces them and no attribute key can name one.
        A shape divergence in any of them is 23f's exporter, never a row here.
```

---

## 2. Two packages, and why the split is load-bearing

```
src/platform/telemetry/
  fact/                    imports context, iter, time. NOTHING ELSE.
    fact.go                Key, Signal, Kind, Cardinality, Visibility, Event, Alias, Definition
    registry.go            var registry [numKeys]Definition   ← THE ONE TABLE
    instrument.go          var instruments [numInstruments]Instrument   (Task 25)
    record.go              Record, New, From, Observe{String,Int64,Float64,Bool,Strings}
  telemetry.go             Init, Provider, Identity, ScopeName, ScopeVersion
  spanuuid.go              the span_uuid SpanProcessor      (23a)
  project_span.go          span attributes and events       (23c/23d)
  project_resource.go      the Resource                     (23a)
  project_label.go         metric label sets                (Task 25)
  redact.go                the facilitator deny-list        (23f)
src/platform/logger/
  project_log.go           logger.Fields(*fact.Record) []zap.Field
```

**`fact` imports only the standard library, and that is the property everything
else hangs from.** A23 requires that controllers record facts without linking the
telemetry stack. If the registry lived beside the SDK, `src/discover` naming a key
would pull `go.opentelemetry.io/otel` into a controller's dependency graph, and a
controller build would break on an SDK release. Splitting the table from the
exporter is what lets `fact` be the one package a controller imports.

### This narrows A23's wording, deliberately

A23 says "controllers never import the telemetry package". Read literally, a
registry under `src/platform/telemetry/` that controllers must name a key from
violates it. The rule's *purpose* is that a controller does not link an exporter
or an SDK, and that purpose is preserved exactly. So the rule is restated:

> **Controllers never import the OpenTelemetry SDK, and never import
> `src/platform/telemetry` itself. They import `src/platform/telemetry/fact`,
> whose entire dependency set is `context`, `iter` and `time`.**

Pinned in §5, not remembered.

### The projections do not all live together

Each projection lives in the package that already owns that signal's output. The
zap projection is in `src/platform/logger`, which already spells the log field
names — so `logger` stays OTel-free, `request_logger.go` gains no SDK dependency
in 23b, and the logger↔registry agreement check becomes a same-package test.

"One place to change an attribute" is the **table**, not the projection file.
Those are different things and conflating them buys nothing while forcing
`logger` to import telemetry.

---

## 3. The attribute table

```go
// Package fact is the attribute registry: one table naming every fact this
// service observes, how it is spelled on each signal, and what may be done with
// it. It imports context, iter and time and nothing else — that property is what
// lets src/discover and src/publish observe facts without linking the OTel SDK.
package fact

type Key uint8

const (
    RequestID Key = iota
    BecknTransactionID
    BecknMessageID
    BecknAction
    // … one per observable fact …
    ResourceEID
    ResourceProducer
    ResourceDomain
    ResourceServiceName
    ResourceNetworkID
    numKeys
)

// Signal is where a fact may appear. Four bits, and note which four: there is
// no Measure bit. A measurement is not a key — see §4.
type Signal uint8

const (
    Span     Signal = 1 << iota
    Log
    Label            // a metric DIMENSION. Requires Cardinality: Bounded.
    Resource         // set once at boot; Required members refuse the boot when empty.
)

// Kind is the Go type of the value, so a projection switches on the type and
// never on the key, and ObserveString against an Int64 key fails loudly rather
// than dropping the fact.
type Kind uint8
const (KindUnspecified Kind = iota; KindString; KindInt64; KindFloat64; KindBool; KindStrings)

type Cardinality uint8
const (CardinalityUnspecified Cardinality = iota; Bounded; Unbounded)

// Visibility governs 23f's facilitator deny-list. The zero value is
// Unspecified, and the projection treats Unspecified as LocalOnly at runtime —
// see §5. A forgotten field must not mean "exported".
type Visibility uint8
const (VisibilityUnspecified Visibility = iota; Public; LocalOnly)

// Event places a fact on a span event rather than on the span. The rule from
// opentelemetry.md: true for the whole request → attribute; produced at a point
// during processing → event.
type Event uint8
const (NoEvent Event = iota; RequestInfo; RetrievalInfo; ResponseInfo; ErrorEvent)

// Layer says whether this attribute's spelling is ours to choose.
type Layer uint8
const (
    LayerUnspecified Layer = iota
    Local                  // retrieval.embedding_ms — nothing outside this service reads it
    CrossLayer             // transaction_id, sender.id — onix's collectors are keyed on it
)

// Alias is a second key carrying the same value. It exists for exactly two
// cases and must not be stretched: the cross-layer join spellings onix's
// collectors key on (I1), and http.status.code beside http.status_code. Both are
// one value written once under two keys, which is what makes them unable to
// drift. AsString renders an Int64 as a string, which divergence 3 requires and
// nothing else does.
type Alias struct {
    Key      string
    AsString bool
}

type Definition struct {
    Name string // the Go constant's name; appears only in failure messages

    SpanKey     string
    SpanAliases []Alias
    LogKey      string
    MetricKey   string // may differ from SpanKey: span beckn.action, label action

    Signals Signal
    Kind    Kind
    Event   Event
    Layer   Layer

    Cardinality Cardinality
    Visibility  Visibility

    // Values is the closed value set for a Bounded key, nil when the set is
    // open. This is what turns Bounded from a claim into a pin, and what the
    // label projection enforces — see §5.
    Values []string

    // Bounds on a caller-supplied value. beckn.schemaContext is a URI the caller
    // wrote, and export is always-on and unsampled.
    MaxEntries, MaxRunes int
    TruncationFlag       string

    // Derived attributes. AbsentFlag is emitted true when the fact was never
    // observed (sender.unidentified); PresentFlag true when it was
    // (sender.unverified). Absent and empty stay distinguishable.
    AbsentFlag, PresentFlag string

    // A zero observation means "not observed". retrieval.embedding_ms must be
    // absent rather than 0 under EMBEDDING_PROVIDER=noop: a zero reads as a fast
    // embedding rather than as no embedding.
    ZeroIsAbsent bool

    // Required on the Resource. Signals must include Resource. Empty at boot
    // with the exporter on is a config error, not a span rejected later.
    Required bool

    // Note records a deliberate divergence in how this attribute's VALUE is
    // derived — not how it is encoded, which is the exporter's. See §1.
    Note string
}

func Of(k Key) Definition
func All() iter.Seq2[Key, Definition]
```

Three fields deserve their reason stated, because each was argued against.

**`Signals` has no `Measure` bit.** An earlier draft separated "this key is a
metric label" from "this key is a measured value". A measured value is not a key
at all — it is the number the instrument reports — so the fourth bit is
`Resource` instead, and that is what makes "the same Resource across all three
signals" true by construction rather than by Task 24 remembering.

**`Values` exists so `Cardinality` is checkable.** `Bounded` on its own is a
claim written by the same person, in the same commit, as the code that uses the
key — at the moment they are already convinced it is fine. `Values` gives the
label projection something to enforce and the instrument test something to
multiply (§5).

**Every enum's zero is `Unspecified` and the completeness test is fatal on it.**
`Cardinality: 0` or `Visibility: 0` is an author who did not decide, and a
default is a decision made by whoever wrote the type rather than by whoever added
the row.

---

## 4. The instrument table — and why it is a *second* table

The spec's METRIC signal requires `name`, `unit`, and per-datapoint `asDouble`,
`startTimeUnixNano` and `endTimeUnixNano`. None is on `Definition`, and putting
them there is the single most likely way this design collapses.

An attribute is a **dimension**; an instrument is a **thing measured**, and the
relation is many-to-many: `beckn.action` labels both a request count (unit `1`)
and a duration (unit `ms`). Put `Unit` on the attribute and it is needed per
*(instrument, attribute)* pair — which is a second table wearing the first one's
name. `name` is worse: an attribute belongs to several instruments and so has no
instrument name. And the two have different lifetimes — an instrument is
registered once per process, an attribute is observed thousands of times a
second.

So: a sibling table in the same package, added by Task 25 and read by Task 24.

```go
type Temporality uint8
const (TemporalityUnspecified Temporality = iota; Delta; Cumulative)

type Scope uint8
const (ScopeUnspecified Scope = iota; Node; Network)

type Instrument struct {
    Name        string // spec Required — metrics[].name
    Unit        string // spec Required — "1", "ms", "s", "%", "B"
    Description string
    Category    string // spec Optional — metric.category

    // Code is spec Required (metric.code) and comes from the network metrics
    // registry OAN does not have (open question 7). Empty on every Scope: Node
    // entry, and a completeness failure on any Scope: Network one — so Task 24
    // fails loudly rather than inventing a code no facilitator will know.
    Code string

    Temporality Temporality
    Monotonic   bool
    Kind        Kind

    // The ONLY link between the two tables, and it points one way.
    Labels []Key

    Scope Scope
}
```

Where the five spec-Required METRIC fields actually live:

| Spec field | Home |
|---|---|
| `name`, `unit` | `Instrument.Name`, `Instrument.Unit` |
| `asDouble` | Nowhere — it is the measurement, implied by `Instrument.Kind` |
| `startTimeUnixNano` / `endTimeUnixNano` | Nowhere — the reader's collection window, written by the periodic reader. Declaring them in a table would be declaring the clock |

**`opentelemetry.md`'s METRIC section needs two corrections found while checking
this**: it says "the same Resource attributes as the spans", which is wrong —
`eid` is `METRIC` there, not `API`; and the optional attribute is spelled
`metric.label`, not `label`.

---

## 5. Enforcement

Five mechanisms. Each one exists because the invariant above it is otherwise
enforced only by whoever is reviewing that day.

### 5a. Completeness, over the array

```
--- FAIL: TestEveryDefinitionIsComplete
    registry_test.go:31: registry[12] is the zero Definition. Key 12 (HTTPRoute)
        is declared in the const block and has no row: a fact observable by name
        and described nowhere is a fact four projections silently drop.
    registry_test.go:44: registry[7] (BecknSchemaContext): Cardinality is
        CardinalityUnspecified. Every Definition states Bounded or Unbounded
        before it can reach a metric label — the instrument check in 5d has
        nothing to check against otherwise.
    registry_test.go:52: registry[9] (SenderID): Visibility is
        VisibilityUnspecified. 23f's deny-list is a filter over this field.
    registry_test.go:60: registry[30] (RetrievalModesDegraded): Signals includes
        Label but Cardinality is Unbounded — a metric dimension with an open
        value set is a cardinality incident with a table row authorising it.
    registry_test.go:64: registry[30]: Signals includes Label and Values is
        empty. Bounded without the bound is a claim; the label projection has
        nothing to enforce and 5d has nothing to multiply.
    registry_test.go:68: registry[41] (ResourceProducer): Required is set but
        Signals omits Resource. Required means "the boot refuses when empty",
        which only a Resource attribute can be.
```

`Signals&Label != 0 ⇒ Cardinality == Bounded ∧ len(Values) > 0` is the one
assertion here that prevents rather than records. Without it `Signals` is a
comment with a type.

### 5b. The import boundary — in `boundary_test.go`, not `depguard`

**Do not add `depguard`, and do not extend `forbidigo`.** Both were the obvious
answer and both are wrong for this repo:

- `depguard` is **not currently enabled** in `.golangci.yml`, so A23's "controllers
  never import the telemetry package" is today enforced by nothing. Adding it
  would mean a flat deny-list that cannot carry a per-path reason.
- `forbidigo` in this repo is deliberately **text**-matched — `.golangci.yml`
  records why `analyze-types` was rejected — so a `zap.String` ban would also fire
  inside `logger.go`'s eight legitimate constructors and inside `fact` itself,
  needing path exclusions under `issues.exclusions.rules`. That is a second
  allow-list, in a second file, in a different syntax, for one invariant. Two
  allow-lists for one rule drift apart.

`tests/architecture/boundary_test.go` is already this repo's mechanism for
exactly this shape: a `go/parser` import-graph walk with named allow-list
*functions*, each carrying a comment saying why that path is exempt. It already
holds two bans for the same class of reason. The telemetry ban reads like them:

```go
// otelOnly is the OpenTelemetry SDK and everything that links it. A23: a
// controller records facts and never links the telemetry stack, so that a
// controller's build does not break on an SDK release.
var otelOnly = []string{
    "go.opentelemetry.io",
    modulePath + "/src/platform/telemetry",   // the SDK half, NOT .../telemetry/fact
}

// The telemetry package itself, the composition root that constructs the
// provider, and the logger — which projects the record into zap fields and is
// the one place outside telemetry/ that reads a Definition.
func mayImportOtel(path string) bool { … }
```

Note the exclusion of `.../telemetry/fact` from the ban and its inclusion in
nothing: `fact` is importable everywhere, because it links nothing.

### 5c. The AST walk — structural, so it needs no allow-list

The precedent is `src/platform/errors/minted_codes_test.go`, and reading it
carefully changes the design. That test has **no allow-list of values**. It has
one categorical exemption (test files) with a stated reason, and its rule is
*structural*: the first argument to a family constructor must be a `beckn.CodeXxx`
selector resolving to a declared constant. A legitimate new code earns admission
by being declared — which is the thing you wanted anyway. There is no list to
edit and no way to be legitimately non-conforming.

An earlier draft of this seam proposed the inverse: "no string literal here",
exempted by a list of ~6 blessed values. That fails for a reason worth recording,
because it is the difference between a pin and a ritual. **A legitimate entry and
a smuggled telemetry key produce identical diffs** — one line added to a slice of
strings — so review cannot be the backstop, and review is the only backstop a
value allow-list has. Six becomes twenty becomes `t.Skip`.

Worse, the list would be wrong on arrival. There are **7 distinct non-registry
string log keys across 8 lines in 4 files** today — `path`, `code`, `reason`
(`src/discover/service.go:319-321`), `address` (`src/app/server.go:46`),
`spec_url` and `cache_path` (`src/platform/validation/spec_index.go:185,224`),
`catalog_id` (`src/publish/service.go:127,150`) — and three are telemetry in
disguise. `code` and `reason` duplicate `error_code` and the fault message that
`logNack` is meant to be the single recording site for; `catalog_id` is a
per-request business identifier logged on two paths. The allow-list's first act
would be to bless the exact drift the registry exists to end.

So the walk is structural and narrow:

- **Walk `attribute.String` / `attribute.Int64` / `attribute.Key` /
  `span.SetAttributes` only.** These are telemetry-only APIs with a closed world;
  over them, "the key must be a `fact.Key` reference" is enforceable with **zero**
  exemptions.
- **Do not walk `zap.String`.** It is general-purpose and used for things that
  are not telemetry keys — which is precisely why the six exemptions existed. The
  log side is governed differently, by 5e.
- **Forbid `span.RecordError` and free-text `span.SetStatus` messages** in the
  same walk. `RecordError` is the idiomatic call, is not `SetAttributes`, and
  writes `exception.message` and `exception.stacktrace` — a Go error string that
  in this codebase can carry a JSONPath expression or wrapped driver text. The
  registry never sees it. We have `logNack` and the `error` event; nothing needs
  it.
- **Carry the vacuity guard.** `minted_codes_test.go` records having had its walk
  silently match nothing, when a dotfile check against `..` skipped the whole
  repository. `if checked == 0 { t.Fatal(…) }`.

```
--- FAIL: TestNoProductionSourceSpellsATelemetryKey
    keys_test.go:71: src/discover/controller.go:88:
        attribute.String("intent.filter_type", …) spells a telemetry key at the
        call site. Add it to src/platform/telemetry/fact/registry.go and observe
        it with fact.ObserveString — one table is what makes a key renameable for
        the span, the log and the metric label at once. (src/discover may not
        import the OTel SDK at all; see boundary_test.go.)
    keys_test.go:104: no call sites found; the walk is checking nothing.
```

Registering the three disguised keys and renaming their call sites leaves
`address` (`server.go:46`) and `grace` (`:76`, a `zap.Duration`) — startup, one
file, neither a request fact. The residual
exclusion is therefore **a path**, not a list of values, and a path exclusion does
not grow monotonically.

### 5d. The two tables agree, and the labels are bounded

```
--- FAIL: TestEveryInstrumentLabelIsALabel
    instrument_test.go:33: instrument "oan_discover_requests" names
        fact.BecknMessageID as a label, but that Definition is Cardinality:
        Unbounded — one message id per request, so this instrument mints a time
        series per request.
    instrument_test.go:41: instrument "oan_publish_resources" names
        fact.PublishBppIDs, whose Signals omit Label: it is declared span-and-log
        only. A key reaching a metric label without the Label bit has bypassed
        the cardinality review that bit exists for.
    instrument_test.go:52: instrument "oan_discover_duration" has 4 labels whose
        Values sets multiply to 800 series. Ceiling is 200. Every label is
        honestly Bounded and no single Definition is wrong — the accident is
        multiplicative, which is why it is checked here and not there.
    instrument_test.go:60: fact.ResultEmpty carries the Label bit and no
        instrument consumes it. Dead declaration.
```

That first check is *unwritable* if the two tables are merged, because there is
then nothing for `Cardinality` to be checked against. Keeping them separate is
what buys it.

### 5e. Log fields, pinned from both directions

`src/platform/logger/logger.go` stays the only place `zap.String` literals live —
and it is not merely exempted, it is cross-checked, which is strictly stronger
than the allow-list it replaces:

```
--- FAIL: TestLoggerFieldsAndTheRegistryAgree
    project_log_test.go:29: logger.go spells zap field "duration_ms" which no
        Definition claims as its LogKey. Either add the Definition or delete the
        constructor: a log field outside the registry is one the span and the
        metric label cannot be kept in step with.
    project_log_test.go:37: fact.ErrorCode declares LogKey "error_code" and
        logger.go has no constructor for it. The projection would emit a field
        with no typed constructor, which forbidigo's zap.Any ban exists to
        prevent.
```

There are **8 exported constructors and 4 production call sites across 3 files**
(`request_logger.go:136`, `response_writer.go:133-134`, `request_id.go:47`), so
folding them onto the registry is cheap. And three of the eight —
`TransactionID`, `MessageID`, `Action` — have **no production caller at all**
today; they are called only from tests. The projection would be their first real
caller, which is the cleanest possible moment to make them registry-derived.
Leave them independent and "one place" is false on day one.

### 5f. The deny-list runs over bytes, not keys

`Visibility` is closed-world over **keys**, and the privacy risk is in **values**.
`opentelemetry.md` already says this about `beckn.schemaContext` — a caller-supplied
URI whose query and fragment can carry text — and the same limitation applies to
`status.message`, to span and event names, and to `zap.Error(err)`, of which there
are 9 sites carrying no string literal at all.

So Task 26's conformance test asserts over the **serialised bytes of the
facilitator projection**, regexing the deny-list across string *values*, not key
names. It needs an in-memory exporter and nothing from 23f, which means it can
move into **23d** and stop being parked behind a blocked task. It runs against the
facilitator projection only — asserting it on the ClickStack one would forbid the
local analysis the split exists to permit. And it carries its own vacuity guard:
a conformance test that passes over zero inputs is a failure mode this repo has
already met once.

Belt and braces on the enum: the facilitator projection treats
`VisibilityUnspecified` as `LocalOnly` **at runtime**, not only in the test. A
skipped or deleted test must not become an export.

### 5g. Two fixtures, two jobs

**`registry.golden.txt`** — generated by `go:generate`, one line per row, diffed
by a test. Local. Its job is to make a registry edit show up as a reviewable diff
in the PR that makes it.

**`tests/testdata/cross-layer-attributes.json`** — hand-authored, ~15 rows,
vendored, byte-identical in both repos. The interop contract. Contains only
attributes that a component *other than the emitter* reads: `eid`, `producer`,
`domain`, `transaction_id`, `message_id`, `sender.id`, `recipient.id`,
`span_uuid`, `observedTimeUnixNano`, `http.method`, `http.host`, `http.route`,
`network.id`, `metric_uuid`, `metric.code`. Explicitly **not** onix's
`onix_step_*` / `onix_plugin_*` internals, nor our `sender.unverified` /
`sender.unidentified`, which are a policy the facilitator has not responded to.

Header carries the source repo, the pinned ref and the date. Pin to a **tag**
(`v1.8.2`, the newest valid semver on onix — its `v2.0` tags are not valid semver
and are unresolvable) rather than a bare commit.

**It is hand-authored, not generated, and that is deliberate.** A generator over
onix would have to AST-walk literal call sites in `stdHandler.go` *and* var
declarations in `pluginMetrics.go`, and a generator whose input is string literals
ratifies whatever was last typed, including a typo, the moment it is regenerated.
Fifteen reviewed rows is the right size for a file whose whole value is that a
human agreed to it.

**And here is the failure mode, stated rather than papered over.** If onix renames
a key and regenerates their copy while ours goes un-revendored, our test passes
green against a stale file and spans silently stop stitching. The fixture catches
drift *within* a repo; it never catches two repos holding stale copies. Any claim
otherwise is the same category of error as the ones §7 corrects.

Three mitigations, ranked honestly:

1. **Additive-only, enforced.** A new spelling is added to `aliases` beside the
   old key; the old key retires only after the collector reports zero spans
   carrying it. This is the only mitigation that *removes* the failure mode rather
   than detecting it, because it converts the dangerous operation (rename) into
   the safe one (alias). Under it a stale copy means "late adopting an alias", not
   "invisible spans".
2. **Put the contract version in `scope.version`.** Required inside the `scope`
   block — which is itself Optional, so this is a field we chose to send and are
   otherwise wasting on a constant. One panel grouping spans by `scope.version`
   then shows a participant on a stale contract, in production, with neither repo
   doing anything.
3. A **scheduled, non-gating** CI job that fetches onix's copy and opens an issue
   on a diff. Advisory on purpose — a PR gate here reintroduces the build coupling
   §6 refuses.

---

## 6. What we do **not** do: import onix's package

onix owns the *spelling* of the cross-layer join keys — three deployed adapters
and eight collector configs are keyed on them, and a fourth component does not get
to rename them. That is settled and uncontested.

It does **not** follow that both repos compile against onix's Go type, and they
must not. Five findings, each fatal alone:

1. **The file does not exist.** `pkg/telemetry/attributes.go` is not in onix. The
   keys are `AttrSenderID`/`AttrRecipientID` in `pluginMetrics.go`, and — decisively
   — `transaction_id` and `message_id` are set as **bare string literals** in
   `core/module/handler/stdHandler.go`, so the package nominated as normative does
   not govern the two keys we would import it for.
2. **It inverts the dependency A23 exists to prevent.** The type embeds
   `attribute.Key`, so importing it pulls the OTel SDK into `src/discover` and
   `src/publish`.
3. **It will not compile.** onix declares `go 1.26.8`; we declare `go 1.25.0`.
   The import forces a toolchain bump driven by another repo's schedule.
4. **One module, 105 requirements.** onix has a single root `go.mod`, so importing
   one package pulls the whole graph into our `go.sum` under MVS — and this repo's
   release pipeline gates on Trivy.
5. **It is not consumable at its own advertised version.** Module path has no
   `/v2` while the newest tags are `v2.0` and `v2.0.1-rc1`; `v2.0` is not valid
   semver. Only the v1.x line resolves, so the import would pin a telemetry package
   two majors stale.

The good half of that proposal is the fixture, and it is adopted in 5g. What is
also adopted, because they are better than what this design started with: the
`Resource` signal bit, and `Required` driving the boot refusal as a loop over the
registry rather than two hand-written `if`s that a third Resource attribute would
never be added to.

---

## 7. The record, and the paths that have none

Chain order (`src/app/router.go:134-141`), index: `0 RequestID · 1 Trace ·
2 RequestLogger · 3 Recover · 4 Envelope · 5 RateLimit · 6 SchemaValidator`.
`apply` nests so index 0 is outermost.

**`Trace` allocates, `RequestLogger` adopts** — adopt-or-allocate in both, so
`RequestLogger` mounted alone still allocates and `request_logger_test.go` passes
unedited, which is 23b's acceptance criterion.

Every rejection path below index 1 therefore has a live record. Verified path by
path: `Envelope`'s unreadable-body and 413, `RateLimit`'s 429,
`SchemaValidator`'s faults and its `notMounted` panic, `Recover`'s ordinary panic
and its committed-response abort, controller faults, and `WriteJSON`'s
broken-pipe warning. All reach `httpx.WriteNack` → `logNack`, the single fault
site 23d projects the `error` event from.

**One path has no record, and it is correct that it does not.** `probes()`
(`router.go:158-163`) mounts `RequestID` + `Recover` only. A panic in `/healthz`
reaches `logNack` with no record in context. Do not add an allocator: probes
deliberately emit no span, because a probe every few seconds carrying `eid: API`
would be most of the stream and none of the signal.

**So nil-tolerance is a load-bearing property of `*fact.Record`, not a
convenience.** Every `Observe*` is a no-op on nil and `From` returns nil rather
than allocating — exactly as `correlation.record` already tolerates a nil receiver
today. The same property covers the acceptance and dbtest suites, which call
controllers with no middleware at all. Pin it from the failing side:

```go
func TestObserveOnAContextWithNoRecordIsSilent(t *testing.T) {
    // The probes chain (router.go:158) is RequestID + Recover: no allocator.
    // A panicking probe must answer 500, not panic a second time inside the
    // recovery that was answering the first.
}
```

**`Trace` must allocate unconditionally, including under `OTEL_EXPORTER=none`.**
Making allocation conditional on a live tracer is the tempting optimisation and it
is the bug: `RequestLogger` would then allocate on some deployments and adopt on
others, so the record's lifetime varies by environment variable and 23b's
invariant becomes untestable. Pinned by driving a 429 with exporter `none` and
asserting the completion line still carries `transaction_id` and `status=429`.

**Two hazards for 23c, found in the same read.** `Recover.abort` re-panics with
`http.ErrAbortHandler`, which unwinds through `Trace`; if 23c sets the status and
calls `span.End()` inline after `next.ServeHTTP` returns, that span leaks on
exactly the path an operator most needs. `Trace` must do both in a **deferred**
function, matching what `RequestLogger` already does. And `fact.From` returns nil
above index 1, so a middleware inserted at index 0 that records a fact records
nothing, silently — a comment on `chain()` plus the order assertion, not runtime
machinery on the hot path.

**One mechanical consequence.** `responseRecorder.correlators` becomes
`record *fact.Record`, so `WriteHeader` records the status through the record it
holds rather than through a context it does not. The API therefore has two
receivers and one name, and the AST walk matches the selector regardless:

```go
func (r *Record) ObserveInt64(k Key, v int64)                    // nil-safe
func ObserveInt64(ctx context.Context, k Key, v int64) {         // the common form
    From(ctx).ObserveInt64(k, v)
}
```

---

## 8. `Init`, and what later tasks add without reopening it

```go
const (
    ScopeName    = "discovery_service"
    ScopeVersion = "1.0"   // example-derived; the spec repo carries no tags (decision 1)
)

// Identity is what the Resource says about this deployment. Separate from
// config.OTel because three fields arrive from -ldflags -X rather than from the
// environment, and config.Config must stay a pure function of the environment.
type Identity struct {
    // The registered subscriber id — an FQDN, e.g. discovery.oan.example.org.
    // Feeds Resource `producer` AND the span's `recipient.id`, which are one
    // value: who this participant is. NOT service.name, which names what
    // software this is (`discovery-service`) and is a struct-tag constant —
    // collapse them and either ClickStack cannot group the service or the
    // network cannot identify the participant.
    //
    // Optional, unlike APP_NETWORK_ID. That one is required because it fills
    // publishDirectives.visibleTo (C8) — a functional dependency. This one
    // feeds telemetry only until Task 6 resolves it to a public key, so
    // requiring it now would refuse the boot of every running deployment for
    // the sake of an attribute. Unset means recipient.id is omitted and
    // recipient.unidentified is true.
    SubscriberID string // APP_SUBSCRIBER_ID

    Producer  string // = the SubscriberID above. Required when Exporter != "none".
    Domain    string // the sector. Required likewise.
    NetworkID string // APP_NETWORK_ID (C8)
    Version   string // "dev" when unstamped — never "" (OP5)
    Commit, TreeState, BuildDate string
}

// Init builds the Resource once and every provider that shares it.
//
// It takes config.OTel rather than config.Config for the reason logger.New takes
// config.Log: Database.URL carries a password, and the component that ships data
// off the box is the last one to hand a secret to.
//
// One Provider, not one per signal: the Resource carries producer, domain and
// network.id, and a second construction site is a second place those can differ
// between a span and a metric. Task 25 fills in Meter(); it does not widen this
// signature and does not touch App.Close.
//
// It never returns nil on success. With Exporter == "none" it returns a Provider
// whose Tracer() is the no-op tracer, so no caller anywhere branches on nil.
//
// It does NOT call otel.SetTracerProvider. Those are package-level globals — the
// No globals constraint — and installing one is what would make "every
// instrument under our own scope" unenforceable.
func Init(ctx context.Context, cfg config.OTel, id Identity) (*Provider, error)

func (p *Provider) Tracer() trace.Tracer            // pre-scoped; an unscoped one is unreachable
func (p *Provider) Meter() metric.Meter             // no-op until Task 25
func (p *Provider) Resource() *resource.Resource    // what every signal stamps
func (p *Provider) Shutdown(ctx context.Context) error
```

| | Adds | Reopens `Init`'s signature? |
|---|---|---|
| **23a** | The `span_uuid` `SpanProcessor`, as a `WithSpanProcessor` option inside `Init`. Registering it starts no spans, so 23a's "boots only" acceptance is untouched | no |
| **23c** | **Nothing** — and that is the sampler decision. `Init` passes **no `WithSampler`**: the option overrides `OTEL_TRACES_SAMPLER`, and the default with no option is already `ParentBased(AlwaysSample)`. Writing it explicitly would look like compliance and silently kill the knob | no |
| **23e** | Nothing. `Trace` observes `TraceID`/`SpanID` into the record; `logger.Fields` projects them. `logger` never imports OTel | no |
| **23f** | A second exporter and its shutdown leg. `Resource()` is already shared | no |
| **Task 25** | A real `MeterProvider` over the same Resource, replacing the no-op, plus its shutdown leg | no |

`Shutdown` runs a slice of named legs and joins their errors, and one test earns
its place: `TestShutdownRunsEveryLegAndJoinsTheirErrors`, with a failing leg
first. Otherwise Task 25's meter leg is appended after a trace leg that returns
early, the meter is never flushed, and it reads as "no metrics at shutdown" and
gets diagnosed as a metrics bug.

`App.Close()` stays `func()` and bounds the flush with its own timeout, per
decision 3.

---

## 9. Cost, and where it lands in the build order

**A sub-task is missing from the plan and must be added.** 23a's Produces is
`telemetry.go`; `src/platform/telemetry/fact/` sits in no task. Add **23a0** — the
registry, the completeness test, the AST pin, the boundary rule and the two
fixtures — and make 23b depend on it. Otherwise the first implementer discovers
that the plan does not describe the work.

| New | ~Lines |
|---|---|
| `fact/fact.go` · `fact/registry.go` · `fact/record.go` | 130 · 260 · 130 |
| `fact/registry_test.go` · `fact/record_test.go` · `registry.golden.txt` | 120 · 90 · 47 |
| `telemetry.go` · `spanuuid.go` · `project_span.go` · `project_resource.go` | 190 · 45 · 120 · 45 |
| `logger/project_log.go` · `telemetry/keys_test.go` · `crosslayer_test.go` | 55 · 140 · 75 |
| `tests/testdata/cross-layer-attributes.json` | 70 |
| `fact/instrument.go` + `project_label.go` + `instrument_test.go` | Task 25 |

Modified: `tests/architecture/boundary_test.go` (the otel ban), `go.mod`/`go.sum`
(otel promoted from indirect; `+sdk`, `+otlptracegrpc`), `Makefile` (`-ldflags -X`
for OP5), `config/config.go` (`OTel` gains `Producer`/`Domain`; `validateOTel`),
`app/container.go`, `app/router.go` (`Trace` takes a tracer, so it stops being a
bare `func(http.Handler) http.Handler`), `middlewares/{correlation,trace,request_logger,envelope}.go`,
`httpx/response_writer.go`, both controllers, `logger/logger.go`.

**Two honesty notes on that accounting**, because "9 of 11 were already planned"
is true by filename and misleading by kind:

- Those nine edits were **additive** under 23a–23d — add an attribute at a call
  site, one sub-task, one gate. Under the registry each becomes **dependent**: add
  a key to `fact/`, then reference it. `fact/` becomes a node 23b, 23c and 23d all
  edit, so the six sub-tasks stop being independently reviewable. A23 split Task 23
  into six *for the gates*; this partly re-couples them, and 23a0 is what keeps the
  coupling in one place.
- `correlation.go` **changes kind**: 23b's planned edit widens the correlation
  record; this deletes the type and substitutes `fact.Record`. Different task,
  different test surface.
- The AST walk is a **policy**, not a file: every future PR touching a span pays
  it, forever. That, not the line count, is what decides whether it survives — and
  it is why §5c is structural.

---

## 10. Open, and owned

| | What | Owner |
|---|---|---|
| 1 | The export topology — direct to ClickStack + facilitator is *decided* in `opentelemetry.md`; it must be **recorded in the chart** that deploys this service | whoever owns `OpenAgriNet/helmcharts` |
| 2 | `metric.code` registry for OAN. `Instrument.Code` is empty on every `Scope: Node` row and a completeness failure on any `Scope: Network` one, so Task 24 fails loudly rather than inventing codes | network |
| 3 | Whether the cross-layer fixture's home should be `network-telemetry-spec/schemas/`, whose `schemas/` and `examples/` are placeholder READMEs today. Both repos already treat that repo as normative; vendoring from one upstream turns "two copies that silently diverge" into "two copies with a visible version skew". Propose it; do not block 23a0 on it | spec repo |
| 4 | onix migrating its audit-log `receiver.id` to `recipient.id` with the old key retained as an alias. Nothing shipped breaks — four references repo-wide, no dashboard and no collector config reads it — but it should land with onix's own attribute seam, not as a two-line edit that leaves the cause in place | onix |
