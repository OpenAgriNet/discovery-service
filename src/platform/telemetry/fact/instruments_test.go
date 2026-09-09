package fact_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// TestEveryInstrumentIsComplete walks the instrument array the same way
// TestEveryDefinitionIsComplete walks the attribute one, and for the same
// reason: an InstrumentKey declared in the const block and given no row is a
// registration that reports under an empty name, which every backend accepts
// and no query finds.
func TestEveryInstrumentIsComplete(t *testing.T) {
	seen := 0
	for key, in := range fact.AllInstruments() {
		seen++

		if in.Name == "" && in.Scope == fact.ScopeUnspecified {
			t.Errorf("instruments[%d] is the zero Instrument", key)
			continue
		}
		if in.Name == "" {
			t.Errorf("instruments[%d]: Name is empty, and it is the metric's own "+
				"identity — spec Required, metrics[].name", key)
		}
		if in.Unit == "" {
			t.Errorf("instruments[%d] (%s): Unit is empty. Spec Required, and a "+
				"nanosecond total read as a count is the whole failure mode",
				key, in.Name)
		}
		if in.Description == "" {
			t.Errorf("instruments[%d] (%s): Description is empty. It reaches the "+
				"operator's dashboard, which is the only audience this table has",
				key, in.Name)
		}
		if in.Kind == fact.KindUnspecified {
			t.Errorf("instruments[%d] (%s): Kind is KindUnspecified. It is what "+
				"implies asDouble, which the spec Requires and no table declares",
				key, in.Name)
		}
		if in.Temporality == fact.TemporalityUnspecified {
			t.Errorf("instruments[%d] (%s): Temporality is TemporalityUnspecified. "+
				"Delta and Cumulative are the two answers and defaulting either "+
				"way silently rescales the operator's graph", key, in.Name)
		}
		if in.Scope == fact.ScopeUnspecified {
			t.Errorf("instruments[%d] (%s): Scope is ScopeUnspecified. Node keeps "+
				"the stream node-local, Network offers it to the facilitator and "+
				"therefore needs a Code", key, in.Name)
		}

		for _, problem := range fact.ValidateInstrument(in) {
			t.Errorf("instruments[%d] (%s): %s", key, in.Name, problem)
		}
	}

	if seen == 0 {
		t.Fatal("AllInstruments() yielded nothing; this test is checking an empty table")
	}
}

// TestEveryInstrumentLabelIsALabel is telemetry-seam.md 5d: the check that the
// two tables agree.
//
// It passes vacuously today, and deliberately so — the acquire-wait pair names
// no labels, because pgxpool.Stat() is per-pool and there is one pool, so every
// candidate dimension would be a constant. The check is here rather than with
// Task 24 because the moment a label IS named this is what refuses a bad one,
// and a guard introduced alongside the first thing it must reject has never
// been observed to reject anything.
func TestEveryInstrumentLabelIsALabel(t *testing.T) {
	consumed := map[fact.Key]bool{}

	for key, in := range fact.AllInstruments() {
		for _, label := range in.Labels {
			consumed[label] = true

			def := fact.Of(label)
			if def.Signals&fact.Label == 0 {
				t.Errorf("instruments[%d] (%s) names fact.%s as a label, whose "+
					"Signals omit Label: a key reaching a metric label without the "+
					"Label bit has bypassed the cardinality review that bit exists for",
					key, in.Name, def.Name)
			}
			if def.Cardinality != fact.Bounded {
				t.Errorf("instruments[%d] (%s) names fact.%s as a label, but that "+
					"Definition is Cardinality: %v — the instrument mints a time "+
					"series per distinct value", key, in.Name, def.Name, def.Cardinality)
			}
		}

		if series := fact.LabelSeries(in); series > fact.MaxLabelSeries {
			t.Errorf("instruments[%d] (%s) has %d labels whose Values sets multiply "+
				"to %d series. Ceiling is %d. Every label may be honestly Bounded "+
				"and no single Definition wrong — the accident is multiplicative, "+
				"which is why it is checked here and not there",
				key, in.Name, len(in.Labels), series, fact.MaxLabelSeries)
		}
	}

	// The other direction. A Label bit nothing consumes is a dead declaration,
	// and it is the shape a deleted instrument leaves behind.
	for key, def := range fact.All() {
		if def.Signals&fact.Label != 0 && !consumed[key] {
			t.Errorf("fact.%s carries the Label bit and no instrument consumes it. "+
				"Dead declaration", def.Name)
		}
	}
}

// TestTheAcquireWaitPairIsTheWholeTable is the count gate from
// opentelemetry.md's Task 25 "Tests pin": a second instrument arrives with a
// reviewer attached, and the reviewer's question is the layer table — which
// layer below us is blind to this number?
//
// Two rows rather than the one the plan's prose says, because Instrument
// carries Name and Unit per row and the pair differ in both: a count is "1"
// and a wait total is nanoseconds. The plan's own METRIC section calls this
// "one instrument ... a pair of monotonic counters", so the concept is one and
// the rows are two. Pinning the names as well as the count is what makes this a
// scope gate rather than an arithmetic one — a third row fails it, and so does
// swapping the pair for something else while keeping the total at two.
func TestTheAcquireWaitPairIsTheWholeTable(t *testing.T) {
	var got []string
	for _, in := range fact.AllInstruments() {
		got = append(got, in.Name)
	}

	want := []string{"pgxpool.empty_acquire", "pgxpool.empty_acquire_wait_time"}
	if !slices.Equal(got, want) {
		t.Errorf("the instrument table is %v, want exactly %v.\n"+
			"Task 25 is acquire-wait and nothing else: liveness went to kubelet, "+
			"pool utilisation to pg_stat_activity, and rate/errors/duration to the "+
			"collector's spanmetrics connector. Adding a row here means answering "+
			"the layer table first", got, want)
	}

	for _, in := range fact.AllInstruments() {
		if !in.Monotonic {
			t.Errorf("%s is not Monotonic. Both halves are cumulative totals off "+
				"pgxpool.Stat(); a non-monotonic acquire-wait count would mean the "+
				"pool forgot it waited", in.Name)
		}
		if in.Scope != fact.Node {
			t.Errorf("%s is Scope %v, want Node. These are operator numbers and "+
				"are outside the spec's METRIC profile on aggregation type alone",
				in.Name, in.Scope)
		}
		if in.Code != "" {
			t.Errorf("%s carries Code %q. metric.code comes from a network registry "+
				"OAN does not have, and inventing one is worse than failing loudly",
				in.Name, in.Code)
		}
	}
}

// TestValidateInstrumentRefusesRowsTheTableHappensNotToHaveToday exercises the
// instrument invariants from the failing side, for the reason Validate's own
// doc comment gives: over the live table every label rule passes vacuously, and
// a rule that has never rejected anything is a rule nobody knows works.
func TestValidateInstrumentRefusesRowsTheTableHappensNotToHaveToday(t *testing.T) {
	base := fact.Instrument{
		Name:        "synthetic",
		Unit:        "1",
		Description: "a synthetic instrument",
		Kind:        fact.KindInt64,
		Temporality: fact.Cumulative,
		Monotonic:   true,
		Scope:       fact.Node,
	}
	with := func(mutate func(*fact.Instrument)) fact.Instrument {
		in := base
		in.Labels = slices.Clone(base.Labels)
		mutate(&in)
		return in
	}

	cases := []struct {
		name string
		in   fact.Instrument
		want string
	}{
		{
			name: "a network stream with no metric code",
			in:   with(func(i *fact.Instrument) { i.Scope = fact.Network }),
			want: "Scope is Network and Code is empty",
		},
		{
			name: "a node stream carrying a metric code anyway",
			in:   with(func(i *fact.Instrument) { i.Code = "OAN_INVENTED" }),
			want: "Scope is Node and Code is set",
		},
		{
			name: "a label the attribute table declares span-and-log only",
			in:   with(func(i *fact.Instrument) { i.Labels = []fact.Key{fact.BecknAction} }),
			want: "Signals omit Label",
		},
		{
			name: "a label over an open value set",
			in:   with(func(i *fact.Instrument) { i.Labels = []fact.Key{fact.BecknTransactionID} }),
			want: "Cardinality is Unbounded",
		},
		{
			name: "a string-valued counter",
			in:   with(func(i *fact.Instrument) { i.Kind = fact.KindString }),
			want: "Kind is KindString",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			problems := strings.Join(fact.ValidateInstrument(testCase.in), "\n")
			if !strings.Contains(problems, testCase.want) {
				t.Errorf("ValidateInstrument did not report %q.\ngot:\n%s",
					testCase.want, problems)
			}
		})
	}

	if problems := fact.ValidateInstrument(base); len(problems) != 0 {
		t.Errorf("the base Instrument is meant to be valid, got %v", problems)
	}
}
