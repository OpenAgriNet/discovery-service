// Package fact is the attribute registry: one table naming every fact this
// service observes, how it is spelled on each signal, and what may be done with
// it. Its entire dependency set is a closed list of standard-library packages,
// pinned by tests/architecture/boundary_test.go — that property is what lets
// src/discover and src/publish observe facts without linking the OpenTelemetry
// SDK, so a controller's build cannot break on an SDK release.
//
// The property the table exists to make true, from docs/design/telemetry-seam.md:
//
//	Adding an attribute, renaming one, changing its cardinality, or deciding it
//	may not leave the process is one edit, in one file, and it lands correctly on
//	the span, the log line, the metric label and the Resource — or fails the
//	build saying which one it could not reach.
//
// This package emits nothing. It is a table and its guards; the projections that
// read it live beside the signal each one writes — traces.go in
// src/platform/telemetry, fields.go in src/platform/logger — so that logger
// stays OpenTelemetry-free and a controller naming a key links no exporter.
//
// The files: definition.go is what a fact is and the rules a row must satisfy,
// registry.go the table of them, record.go what one request observed, and
// instruments.go the metric instruments with the rules an instrument must
// satisfy.
package fact

import (
	"fmt"
	"iter"
	"strings"
)

// Key names one observable fact. It indexes the registry array directly, so the
// const block below and the rows in registry.go are the same list read two ways
// and a key with no row is a test failure rather than a silent zero.
type Key uint8

// Signal is where a fact may appear.
//
// Four bits, and note which four: there is no Measure bit. A measured value is
// not a key at all — it is the number an instrument reports — so the fourth bit
// is Resource instead, which is what makes "the same Resource across all three
// signals" true by construction rather than by Task 24 remembering.
type Signal uint8

const (
	// Span puts the fact on the span, or on one of its events when Event is set.
	Span Signal = 1 << iota
	// Log puts it on the request's completion line.
	Log
	// Label makes it a metric DIMENSION. Requires Bounded and a non-empty Values.
	Label
	// Resource is set once at boot; Required members refuse the boot when empty.
	Resource
)

// Kind is the Go type of the value, so a projection switches on the type and
// never on the key, and ObserveString against an Int64 key fails loudly rather
// than dropping the fact.
type Kind uint8

const (
	// KindUnspecified is the zero, and the completeness test refuses it: a row
	// with no Kind is a row every Observe call panics on.
	KindUnspecified Kind = iota
	// KindString is a string value.
	KindString
	// KindInt64 is a whole number — a count or a millisecond duration.
	KindInt64
	// KindFloat64 is a fractional number.
	KindFloat64
	// KindBool is a flag.
	KindBool
	// KindStrings is a list of strings, and the only Kind MaxEntries applies to.
	KindStrings
)

// Cardinality is how many distinct values this fact can take.
type Cardinality uint8

const (
	// CardinalityUnspecified is the zero. Refused, because defaulting it either
	// way is wrong: Bounded would authorise a label the value set cannot back,
	// Unbounded would silently forbid one somebody meant to declare.
	CardinalityUnspecified Cardinality = iota
	// Bounded means Values lists every value this key can take. A metric label
	// requires it.
	Bounded
	// Unbounded means the value comes from the caller or the clock.
	Unbounded
)

// Event places a fact on a span event rather than on the span. The rule from
// opentelemetry.md: true for the whole request → attribute; produced at a point
// during processing → event.
type Event uint8

const (
	// NoEvent puts the fact on the span itself. Unlike the other enums this zero
	// is a real answer, and most rows carry it.
	NoEvent Event = iota
	// RequestInfo is what the caller asked for, known once the body parses.
	RequestInfo
	// RetrievalInfo is how the search went, known once the store answers.
	RetrievalInfo
	// ResponseInfo is what went back, known at WriteHeader.
	ResponseInfo
	// ErrorEvent is the failure, and only exists on requests that had one.
	ErrorEvent
)

// Layer says whether this attribute's spelling is ours to choose.
type Layer uint8

const (
	// LayerUnspecified is the zero. Refused, because Local is the answer that
	// lets a key be renamed freely, and defaulting to it is how a cross-layer
	// rename ships without anyone noticing it was one.
	LayerUnspecified Layer = iota
	// Local means nothing outside this service reads it, so the spelling is
	// ours — retrieval.embedding_ms.
	Local
	// CrossLayer means something outside does, so a rename is a cross-repo
	// change — transaction_id and sender.id, which onix's collectors key on.
	CrossLayer
)

// Alias is a second key carrying the same value.
//
// It exists for exactly two cases and must not be stretched: the cross-layer
// join spellings onix's collectors key on (I1), and http.status.code beside
// http.status_code. Both are one value written once under two keys, which is
// what makes them unable to drift — unlike a duplicated duration, which is free
// to disagree with the span it duplicates. AsString renders an Int64 as a
// string, which divergence 3 requires and nothing else does.
type Alias struct {
	Key      string
	AsString bool
}

// Definition is one row. Every field an author can leave at its zero value and
// mean something by it is checked in Validate rather than defaulted here.
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

	// Values is the closed value set for a Bounded key. It is what turns Bounded
	// from a claim written by the same person, in the same commit, as the code
	// that uses the key into something the label projection can enforce and
	// Task 25's instrument test can multiply.
	Values []string

	// Bounds on a caller-supplied value. beckn.schemaContext is a URI the caller
	// wrote, and export is always-on and unsampled. Applied where the value
	// enters the record, not where it leaves, because there are four exits.
	MaxEntries, MaxRunes int
	TruncationFlag       string

	// Derived attributes. AbsentFlag is emitted true when the fact was never
	// observed (sender.unidentified); PresentFlag true when it was
	// (sender.unverified). Absent and empty stay distinguishable.
	AbsentFlag, PresentFlag string

	// ZeroIsAbsent drops a zero observation rather than recording it.
	// retrieval.embedding_ms must be absent rather than 0 under
	// EMBEDDING_PROVIDER=noop: a zero reads as a fast embedding rather than as
	// no embedding.
	ZeroIsAbsent bool

	// PromoteToSpan carries an event fact onto the span as well, and only ever
	// as well: the events are the interop contract and promotion copies rather
	// than moves. Requires both an Event and the Span bit, because on a row
	// with neither it is a bool that reads as meaningful and does nothing.
	//
	// The cost is one attribute shipped twice per span, so it is a row-by-row
	// opt-in rather than a rule. Two things buy it: span attributes are a
	// queryable map where event attributes are not, and the collector's
	// spanmetrics connector can name a span attribute as a metric dimension and
	// cannot reach an event at all. Which means every promoted row is also a
	// candidate dimension whose Values multiply that connector's series count —
	// promote a row and check fact.MaxLabelSeries against otel/collector.yaml.
	PromoteToSpan bool

	// Required on the Resource. Signals must include Resource. Empty at boot
	// with the exporter on is a config error, not a span rejected later.
	Required bool

	// Note records a deliberate divergence in how this attribute's VALUE is
	// derived — not how it is encoded, which is the exporter's.
	Note string
}

// Of returns the row for a key.
//
// It panics on a key with no row rather than returning the zero Definition,
// because a projection handed an empty SpanKey ships an attribute with no name:
// every backend accepts it and no query finds it. The completeness test makes
// this unreachable, and the panic is what keeps it unreachable if that test is
// ever skipped.
func Of(k Key) Definition {
	if int(k) >= len(registry) {
		panic(fmt.Sprintf("fact: key %d has no row; the const block and registry.go are one list", k))
	}
	return registry[k]
}

// All iterates the table in key order. Projections and guards both walk it, so
// a new row reaches every one of them without being added to a second list.
func All() iter.Seq2[Key, Definition] {
	return func(yield func(Key, Definition) bool) {
		for index := range registry {
			if !yield(Key(index), registry[index]) {
				return
			}
		}
	}
}

// String renders a Signal set for a failure message and for the golden file.
func (s Signal) String() string {
	if s == 0 {
		return "none"
	}
	var parts []string
	for _, named := range []struct {
		bit  Signal
		name string
	}{{Span, "Span"}, {Log, "Log"}, {Label, "Label"}, {Resource, "Resource"}} {
		if s&named.bit != 0 {
			parts = append(parts, named.name)
		}
	}
	return strings.Join(parts, "|")
}

func (k Kind) String() string {
	return name(int(k), []string{"KindUnspecified", "KindString", "KindInt64",
		"KindFloat64", "KindBool", "KindStrings"})
}

func (c Cardinality) String() string {
	return name(int(c), []string{"CardinalityUnspecified", "Bounded", "Unbounded"})
}

func (e Event) String() string {
	return name(int(e), []string{"NoEvent", "RequestInfo", "RetrievalInfo",
		"ResponseInfo", "ErrorEvent"})
}

// EventName is the name the event goes out under, and it is a second spelling
// rather than a lowercasing of String().
//
// String() names the Go constant and appears only in failure messages; this one
// is on the wire, where a facilitator keys on it. Deriving one from the other
// would tie a debugging string to a contract, so that renaming ErrorEvent to
// something clearer in a panic message would rename the event a collector
// filters on.
//
// NoEvent answers empty, and the projection reads that as "not an event". It is
// the zero value, so a Definition that simply forgot to set Event would
// otherwise land its fact on a fifth event carrying the whole span again.
func (e Event) EventName() string {
	switch e {
	case RequestInfo:
		return "request_info"
	case RetrievalInfo:
		return "retrieval_info"
	case ResponseInfo:
		return "response_info"
	case ErrorEvent:
		return "error"
	case NoEvent:
		return ""
	default:
		return ""
	}
}

func (l Layer) String() string {
	return name(int(l), []string{"LayerUnspecified", "Local", "CrossLayer"})
}

// name is the shared tail of the String methods above. An out-of-range value
// prints its number rather than a blank, because a blank in a failure message
// is how an unhandled enum member gets read as the zero one.
func name(value int, names []string) string {
	if value < 0 || value >= len(names) {
		return fmt.Sprintf("%%!(unknown:%d)", value)
	}
	return names[value]
}

// --- The rules a row must satisfy -----------------------------------------

// Validate returns every way a Definition contradicts itself, as prose a failure
// message can print directly.
//
// Exported because the guard that runs it over the live table must also run it
// over rows the table does not have today: no row carries the Label bit, and a
// rule that has never rejected anything is a rule nobody knows works.
//
// It does not check the enums for their Unspecified zeros — those are the
// completeness test's, which reports them per row with the reason each matters.
// Folding them in here would give one failure two voices.
func Validate(def Definition) []string {
	report, collect := newProblems()

	checkKeysMatchSignals(def, report)
	checkLabelIsBounded(def, report)
	checkBoundsAreFlagged(def, report)
	checkPlacement(def, report)

	return collect()
}

// reporter is the shared signature. Named rather than repeated at every checker,
// because a func(string, ...any) in a parameter list reads as plumbing and this
// one is the whole output.
type reporter func(format string, args ...any)

// newProblems returns a reporter and the accumulated list, so the caller keeps
// one list in one order. ValidateInstrument in instruments.go uses it too.
func newProblems() (reporter, func() []string) {
	var problems []string
	return func(format string, args ...any) {
			problems = append(problems, fmt.Sprintf(format, args...))
		}, func() []string {
			return problems
		}
}

// checkKeysMatchSignals: every signal a row claims has a key to write under, and
// every key it carries has a signal that writes it. A key with no signal is dead
// and a signal with no key ships an attribute with no name.
func checkKeysMatchSignals(def Definition, report reporter) {
	// SpanKey carries the Resource spelling too — eid and producer are
	// attribute.KeyValues like any other, stamped once at boot rather than per
	// request. So only a row reaching neither may leave it empty.
	if def.Signals&(Span|Resource) != 0 && def.SpanKey == "" {
		report("Signals includes %v and SpanKey is empty", def.Signals&(Span|Resource))
	}
	if def.Signals&(Span|Resource) == 0 && def.SpanKey != "" {
		report("SpanKey is set to %q and Signals omits Span and Resource, so nothing writes it", def.SpanKey)
	}
	if def.Signals&Log != 0 && def.LogKey == "" {
		report("Signals includes Log and LogKey is empty")
	}
	if def.Signals&Log == 0 && def.LogKey != "" {
		report("LogKey is set to %q and Signals omits Log, so nothing writes it", def.LogKey)
	}
}

// checkLabelIsBounded is the cardinality gate, and the one rule here whose
// violation costs money rather than clarity: a metric dimension over an open
// value set is a time series per distinct value.
func checkLabelIsBounded(def Definition, report reporter) {
	if def.Signals&Label != 0 {
		if def.MetricKey == "" {
			report("Signals includes Label and MetricKey is empty")
		}
		if def.Cardinality != Bounded {
			report("Signals includes Label but Cardinality is Unbounded — a metric " +
				"dimension with an open value set is a cardinality incident with a " +
				"table row authorising it")
		}
		if len(def.Values) == 0 {
			report("Signals includes Label and Values is empty. Bounded without the " +
				"bound is a claim; the label projection has nothing to enforce")
		}
	}
	if def.Signals&Label == 0 && def.MetricKey != "" {
		report("MetricKey is set to %q and Signals omits Label", def.MetricKey)
	}

	if def.Cardinality == Unbounded && len(def.Values) != 0 {
		report("Values is set on an Unbounded key; one of the two is wrong")
	}
}

// checkBoundsAreFlagged: a bound with no flag truncates silently, and a value
// silently cut short reads as the value the caller sent. A flag with no bound is
// the same mistake facing the other way — an attribute nothing can ever set.
func checkBoundsAreFlagged(def Definition, report reporter) {
	if def.MaxEntries != 0 && def.Kind != KindStrings {
		report("MaxEntries is set on a %v key; an entry count bounds a list", def.Kind)
	}
	if (def.MaxEntries != 0 || def.MaxRunes != 0) && def.TruncationFlag == "" {
		report("a bound is set and TruncationFlag is empty; a value silently cut " +
			"short reads as the value the caller sent")
	}
	if def.TruncationFlag != "" && def.MaxEntries == 0 && def.MaxRunes == 0 {
		report("TruncationFlag is set with no bound to trip it")
	}

	if def.ZeroIsAbsent {
		switch def.Kind {
		case KindInt64, KindFloat64:
		default:
			report("ZeroIsAbsent is set on a %v key; it exists so a measured zero "+
				"is not read as a measurement", def.Kind)
		}
	}
}

// checkPlacement: where a fact sits, as opposed to what it says. The Resource is
// stamped once at boot, so a Resource attribute on a span event names a moment
// that has already passed by the time anything could record it.
func checkPlacement(def Definition, report reporter) {
	if def.Required && def.Signals&Resource == 0 {
		report("Required is set but Signals omits Resource. Required means \"the " +
			"boot refuses when empty\", which only a Resource attribute can be")
	}

	if def.Signals&Resource != 0 && def.Event != NoEvent {
		report("a Resource attribute is placed on the %v event; the Resource is "+
			"set once at boot", def.Event)
	}

	if def.PromoteToSpan {
		if def.Event == NoEvent {
			report("PromoteToSpan is set on a row with no Event. Promotion means " +
				"\"onto the span AS WELL AS its event\"; with no event the row is " +
				"already a span attribute and the bool changes nothing, which is " +
				"how someone concludes the mechanism is broken")
		}
		if def.Signals&Span == 0 {
			report("PromoteToSpan is set and Signals omits Span, so the promotion " +
				"targets a span this row never reaches")
		}
	}
}
