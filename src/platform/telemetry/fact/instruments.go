package fact

import (
	"fmt"
	"iter"
)

// Temporality is whether a reported value covers the collection interval or the
// process lifetime. The spec's aggregationTemporality: 1 delta, 2 cumulative.
type Temporality uint8

const (
	// TemporalityUnspecified is the zero, and the completeness test refuses it:
	// defaulting either way silently rescales the operator's graph by the scrape
	// interval.
	TemporalityUnspecified Temporality = iota
	// Delta reports what happened since the last collection.
	Delta
	// Cumulative reports a running total since process start.
	Cumulative
)

// Scope is who a stream is for, and it decides whether Code is required.
type Scope uint8

const (
	// ScopeUnspecified is the zero, and is refused: the two answers have
	// opposite Code requirements, so neither is a safe default.
	ScopeUnspecified Scope = iota
	// Node is a node-operator stream. It stays local — beckn-onix's
	// filter/network_metrics drops every metric it does not name — and needs no
	// metric.code.
	Node
	// Network is offered to the facilitator and therefore needs a Code from the
	// network's metrics registry.
	Network
)

// InstrumentKey names one instrument and indexes the array below, exactly as
// Key does for Definition.
type InstrumentKey uint8

const (
	// PoolAcquireWaits counts acquires that had to wait on an empty pool.
	PoolAcquireWaits InstrumentKey = iota
	// PoolAcquireWaitNanos is the cumulative time spent in those waits.
	PoolAcquireWaitNanos

	numInstruments
)

// MaxLabelSeries is the per-instrument ceiling on the product of its labels'
// value sets (opentelemetry.md §Two tables, not one). Per instrument rather
// than per Definition
// because the accident is multiplicative: four labels can each be honestly
// Bounded and still multiply to 800 streams.
const MaxLabelSeries = 200

// Instrument is one metric stream. A sibling table to registry rather than a
// column on it, because an attribute belongs to several instruments and the two
// have different lifetimes (opentelemetry.md §Two tables, not one).
type Instrument struct {
	Name        string // spec Required — metrics[].name
	Unit        string // spec Required — "1", "ns", "ms", "s", "%", "B"
	Description string
	Category    string // spec Optional — metric.category

	// Code is spec Required (metric.code) and comes from the network metrics
	// registry OAN does not have. Empty on every Scope: Node row, and a
	// completeness failure on any Scope: Network one, so Task 24 fails loudly
	// rather than inventing a code no facilitator will know.
	Code string

	Temporality Temporality
	Monotonic   bool
	Kind        Kind

	// Labels are the metric's dimensions. The ONLY link between the two tables,
	// and it points one way: an instrument names keys, a key never names an
	// instrument.
	Labels []Key

	Scope Scope

	// Note records why this instrument exists at all — the entry bar is "a
	// number the layer below is blind to", and this is where a row answers it.
	Note string
}

// instruments is the table. Two rows, and the count is a test.
//
// Both halves come off one pgxpool.Stat() call and neither is on the request
// path: this is a sampling callback over a struct the pool maintains anyway.
// container.go already reads EmptyAcquireCount for /readyz, so what is new is
// only that the level becomes visible on a clock.
var instruments = [numInstruments]Instrument{
	PoolAcquireWaits: {
		Name:        "pgxpool.empty_acquire",
		Unit:        "1",
		Description: "Cumulative count of successful acquires from the pool that waited for a connection to be released or constructed because the pool was empty.",
		Kind:        KindInt64,
		Temporality: Cumulative,
		Monotonic:   true,
		Scope:       Node,
		Note: "The one operator number no other layer can produce. Queueing " +
			"happens inside this process, before any syscall: cAdvisor sees the " +
			"container's resource envelope and Postgres never sees a statement " +
			"that was not sent. It rises before anything fails, which is what " +
			"separates it from the pool utilisation gauge it replaced — pgxpool " +
			"holds connections open when idle, so 32 established with 2 acquired " +
			"is indistinguishable from 32 acquired in pg_stat_activity. " +
			"Name borrowed from github.com/exaring/otelpgx rather than invented: " +
			"that library reports this exact number under this exact name, so a " +
			"dashboard written against it works here, and the plan pins no name " +
			"of its own. We do not import it — see PoolAcquireWaitNanos.",
	},
	PoolAcquireWaitNanos: {
		Name:        "pgxpool.empty_acquire_wait_time",
		Unit:        "ns",
		Description: "Cumulative time spent waiting for a connection to be released or constructed because the pool was empty.",
		Kind:        KindInt64,
		Temporality: Cumulative,
		Monotonic:   true,
		Scope:       Node,
		Note: "The second half of the pair, and a pair rather than a histogram " +
			"as a constraint rather than a preference: pgxpool exposes only " +
			"cumulative totals, so a real distribution would mean wrapping every " +
			"Acquire on the hot path. The consumer divides this rate by " +
			"pgxpool.empty_acquire's for mean wait per acquire, which answers the " +
			"saturation question without touching the request path. " +
			"otelpgx supplies both names and both semantics and is still not " +
			"imported, for three reasons its RecordStats cannot be configured " +
			"out of: it registers thirteen instruments all-or-nothing, including " +
			"the pgxpool.acquired_connections gauge Task 25 struck; it obtains " +
			"its own instrumentation scope, so scope.version would report " +
			"otelpgx's version where the spec wants the specification's; and its " +
			"labels are semconv, one of which embeds the database host in a " +
			"metric dimension.",
	},
}

// OfInstrument returns the row for a key, and panics on a key with no row for
// the reason Of does: a registration under an empty name is accepted by every
// backend and found by no query.
func OfInstrument(k InstrumentKey) Instrument {
	if int(k) >= len(instruments) {
		panic(fmt.Sprintf("fact: instrument key %d has no row; the const block and the table are one list", k))
	}
	return instruments[k]
}

// AllInstruments iterates the table in key order, so a new row reaches every
// guard without being added to a second list.
func AllInstruments() iter.Seq2[InstrumentKey, Instrument] {
	return func(yield func(InstrumentKey, Instrument) bool) {
		for index := range instruments {
			if !yield(InstrumentKey(index), instruments[index]) {
				return
			}
		}
	}
}

// LabelSeries is how many time series an instrument's labels can produce: the
// product of their value sets, or 1 for an instrument with no labels — an
// unlabelled instrument still produces a stream, and a zero would make the
// ceiling check pass for the wrong reason.
func LabelSeries(in Instrument) int {
	series := 1
	for _, label := range in.Labels {
		values := len(Of(label).Values)
		if values == 0 {
			// Skipped, not multiplied in: the label rules reject a Bounded row
			// with no Values separately, and a zero here would hide that behind
			// a ceiling check that passes.
			continue
		}
		series *= values
	}
	return series
}

// ValidateInstrument returns every way an Instrument contradicts itself or
// disagrees with the attribute table, as prose a failure message can print.
//
// Exported for the same reason Validate is: the acquire-wait pair names no
// labels, so over the live table the label rules pass vacuously, and the test
// runs it over rows the table happens not to have.
func ValidateInstrument(in Instrument) []string {
	report, collect := newProblems()

	checkCodeMatchesScope(in, report)
	checkInstrumentLabels(in, report)
	checkMeasurementIsNumeric(in, report)

	return collect()
}

// checkCodeMatchesScope: metric.code is the facilitator's routing key. Both
// directions are errors — a Network row without one cannot be routed, and a Node
// row with one is a code somebody invented.
func checkCodeMatchesScope(in Instrument, report reporter) {
	if in.Scope == Network && in.Code == "" {
		report("Scope is Network and Code is empty. metric.code is spec Required " +
			"and comes from the network metrics registry; a stream the facilitator " +
			"cannot route is worse than one that was never offered")
	}
	if in.Scope == Node && in.Code != "" {
		report("Scope is Node and Code is set to %q. A node-operator stream needs "+
			"no metric.code, and a code that is not in the network's registry is "+
			"one this service made up", in.Code)
	}
}

// checkInstrumentLabels is the two-tables-agree check (opentelemetry.md §Two
// tables, not one), the half that runs per row.
// The cross-table direction — a Label bit no instrument consumes — needs the
// whole table and lives in the test.
func checkInstrumentLabels(in Instrument, report reporter) {
	for _, label := range in.Labels {
		def := Of(label)

		if def.Signals&Label == 0 {
			report("names %s as a label, whose Signals omit Label: a key reaching "+
				"a metric label without the Label bit has bypassed the cardinality "+
				"review that bit exists for", def.Name)
		}
		if def.Cardinality != Bounded {
			report("names %s as a label, but that Definition's Cardinality is %v — "+
				"one value per request means one time series per request",
				def.Name, def.Cardinality)
		}
	}

	if series := LabelSeries(in); series > MaxLabelSeries {
		report("has %d labels whose Values sets multiply to %d series, over the "+
			"ceiling of %d. The accident is multiplicative: every label can be "+
			"honestly Bounded and no single Definition wrong",
			len(in.Labels), series, MaxLabelSeries)
	}
}

// checkMeasurementIsNumeric: Kind stands in for the measurement itself, and a
// string or a list cannot be summed.
func checkMeasurementIsNumeric(in Instrument, report reporter) {
	switch in.Kind {
	case KindInt64, KindFloat64:
	default:
		report("Kind is %v, and a measurement is a number: Kind is what implies "+
			"the spec's asDouble, which no table declares because it is the "+
			"measurement", in.Kind)
	}
}

// String renders a Temporality for a failure message and the golden file.
func (t Temporality) String() string {
	return name(int(t), []string{"TemporalityUnspecified", "Delta", "Cumulative"})
}

// String renders a Scope for a failure message and the golden file.
func (s Scope) String() string {
	return name(int(s), []string{"ScopeUnspecified", "Node", "Network"})
}
