// Package fact_test holds the guards that make the attribute registry a table
// rather than a suggestion. Task 23a0 emits nothing; these tests are the whole
// deliverable beside the table itself.
package fact_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// TestEveryDefinitionIsComplete walks the array rather than a hand-written list
// of keys, so a Key declared in the const block and given no row fails here
// instead of being silently dropped by four projections.
//
// Every check below is fatal on an enum's Unspecified zero. A zero Cardinality
// is not "the author chose Unbounded", it is "the author did not choose", and a
// default here is a decision made by whoever wrote the type rather than by
// whoever added the row.
func TestEveryDefinitionIsComplete(t *testing.T) {
	seen := 0
	for key, def := range fact.All() {
		seen++

		// The zero Definition, detected through Name and Signals rather than by
		// comparing the struct: Definition holds slices and so is not comparable,
		// and adding a comparable-only shadow type to make one == work is more
		// machinery than the two fields it would replace.
		if def.Name == "" && def.Signals == 0 {
			t.Errorf("registry[%d] is the zero Definition: a fact observable by "+
				"name and described nowhere is a fact four projections silently drop", key)
			continue
		}
		if def.Name == "" {
			t.Errorf("registry[%d]: Name is empty. It is the only thing a failure "+
				"message here can call the row", key)
		}
		if def.Signals == 0 {
			t.Errorf("registry[%d] (%s): Signals is empty. A fact that reaches no "+
				"signal is observed into nothing", key, def.Name)
		}
		if def.Kind == fact.KindUnspecified {
			t.Errorf("registry[%d] (%s): Kind is KindUnspecified. The projections "+
				"switch on Kind and never on the key", key, def.Name)
		}
		if def.Cardinality == fact.CardinalityUnspecified {
			t.Errorf("registry[%d] (%s): Cardinality is CardinalityUnspecified. Every "+
				"Definition states Bounded or Unbounded before it can reach a metric "+
				"label — the instrument check in Task 25 has nothing to check against "+
				"otherwise", key, def.Name)
		}
		if def.Layer == fact.LayerUnspecified {
			t.Errorf("registry[%d] (%s): Layer is LayerUnspecified. CrossLayer says "+
				"the spelling is not ours to change; Local says it is", key, def.Name)
		}

		for _, problem := range fact.Validate(def) {
			t.Errorf("registry[%d] (%s): %s", key, def.Name, problem)
		}
	}

	if seen == 0 {
		t.Fatal("All() yielded nothing; this test is checking an empty table")
	}
}

// TestValidateRefusesTheRowsTheRegistryHappensNotToHaveToday exercises the
// invariants from the failing side.
//
// No row carries the Label bit yet — Task 25 declares the instruments that
// would consume one — so over the live registry the label rules pass
// vacuously. A rule that has never rejected anything is a rule nobody knows
// works, and this is the sub-task that can still find out cheaply.
func TestValidateRefusesTheRowsTheRegistryHappensNotToHaveToday(t *testing.T) {
	base := fact.Definition{
		Name:        "Synthetic",
		SpanKey:     "synthetic",
		Signals:     fact.Span,
		Kind:        fact.KindString,
		Cardinality: fact.Bounded,
		Layer:       fact.Local,
		Values:      []string{"a"},
	}
	with := func(mutate func(*fact.Definition)) fact.Definition {
		def := base
		def.Values = slices.Clone(base.Values)
		mutate(&def)
		return def
	}

	cases := []struct {
		name string
		def  fact.Definition
		want string
	}{
		{
			name: "a metric dimension with an open value set",
			def: with(func(d *fact.Definition) {
				d.Signals |= fact.Label
				d.MetricKey = "synthetic"
				d.Cardinality = fact.Unbounded
			}),
			want: "Cardinality is Unbounded",
		},
		{
			name: "Bounded without the bound",
			def: with(func(d *fact.Definition) {
				d.Signals |= fact.Label
				d.MetricKey = "synthetic"
				d.Values = nil
			}),
			want: "Values is empty",
		},
		{
			name: "Required off the Resource",
			def: with(func(d *fact.Definition) {
				d.Required = true
			}),
			want: "Signals omits Resource",
		},
		{
			name: "Values on an Unbounded key",
			def: with(func(d *fact.Definition) {
				d.Cardinality = fact.Unbounded
			}),
			want: "Values is set",
		},
		{
			name: "the Span bit with no span spelling",
			def: with(func(d *fact.Definition) {
				d.SpanKey = ""
			}),
			want: "SpanKey is empty",
		},
		{
			name: "a span spelling with no Span bit",
			def: with(func(d *fact.Definition) {
				d.Signals = fact.Log
				d.LogKey = "synthetic"
			}),
			want: "SpanKey is set",
		},
		{
			name: "the Log bit with no log spelling",
			def: with(func(d *fact.Definition) {
				d.Signals |= fact.Log
			}),
			want: "LogKey is empty",
		},
		{
			name: "a truncation bound with no flag to report it",
			def: with(func(d *fact.Definition) {
				d.Kind = fact.KindStrings
				d.MaxEntries = 16
			}),
			want: "TruncationFlag is empty",
		},
		{
			name: "an entry bound on a scalar",
			def: with(func(d *fact.Definition) {
				d.MaxEntries = 16
				d.TruncationFlag = "synthetic.truncated"
			}),
			want: "MaxEntries is set",
		},
		{
			name: "an absent-zero rule on a string",
			def: with(func(d *fact.Definition) {
				d.ZeroIsAbsent = true
			}),
			want: "ZeroIsAbsent",
		},
		{
			// PromoteToSpan means "carry this onto the span AS WELL AS its
			// event". A row with no event is already on the span, so the bool
			// says nothing there — and a bool that reads as meaningful and does
			// nothing is how someone concludes the mechanism is broken.
			name: "a promotion on a row that has no event",
			def: with(func(d *fact.Definition) {
				d.PromoteToSpan = true
			}),
			want: "PromoteToSpan",
		},
		{
			// The Log-only case. Promotion with no Span bit is a request to put
			// an attribute on a span this row never reaches.
			name: "a promotion on a row that is not on the span",
			def: with(func(d *fact.Definition) {
				d.Signals = fact.Log
				d.SpanKey = ""
				d.LogKey = "synthetic"
				d.Event = fact.ResponseInfo
				d.PromoteToSpan = true
			}),
			want: "PromoteToSpan",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			problems := fact.Validate(testCase.def)
			if !slices.ContainsFunc(problems, func(p string) bool {
				return strings.Contains(p, testCase.want)
			}) {
				t.Errorf("Validate did not reject %s\n  want a problem containing: %s\n  got: %v",
					testCase.name, testCase.want, problems)
			}
		})
	}

	if problems := fact.Validate(base); len(problems) != 0 {
		t.Errorf("Validate rejected the base Definition every case above mutates: %v", problems)
	}
}

// otlpStructuralFields are fields on the OTLP Span message. The SDK produces
// them and no attribute.KeyValue can name one, so no fact.Key may either.
//
// The line this draws is the one from telemetry-seam.md §1: same value under two
// keys is a registry Alias; same key, different serialisation of a structural
// field is the exporter's. `status` reaching the wire as
// {"code":"STATUS_CODE_OK"} where the spec's examples show "Ok" is a divergence
// 23f fixes in an exporter, and a row here claiming to fix it would emit a
// second, unrelated attribute that happens to share the name.
var otlpStructuralFields = []string{
	"status", "kind", "name", "traceId", "spanId", "parentSpanId",
	"startTimeUnixNano", "endTimeUnixNano", "events",
}

func TestNoDefinitionNamesAnOTLPStructuralField(t *testing.T) {
	for key, def := range fact.All() {
		spellings := []string{def.SpanKey, def.MetricKey}
		for _, alias := range def.SpanAliases {
			spellings = append(spellings, alias.Key)
		}
		spellings = append(spellings, def.AbsentFlag, def.PresentFlag, def.TruncationFlag)

		for _, spelling := range spellings {
			if spelling == "" {
				continue
			}
			if slices.Contains(otlpStructuralFields, spelling) {
				t.Errorf("registry[%d] Definition %q spells SpanKey %q.\n"+
					"        %s are OTLP Span FIELDS, not attributes: the SDK produces\n"+
					"        them and no attribute key can name one. A shape divergence in\n"+
					"        any of them is 23f's exporter, never a row here.",
					key, def.Name, spelling, strings.Join(otlpStructuralFields, ", "))
			}
		}
	}
}

// TestNoTwoDefinitionsShareASpelling. Two rows writing one key is the failure
// the whole table exists to prevent: whichever projection runs second wins, and
// which one that is depends on iteration order.
//
// Aliases are in scope. `transaction_id` is BecknTransactionID's alias, and a
// second row claiming it as a SpanKey would be two values under one name — the
// exact thing an alias is defined not to be.
func TestNoTwoDefinitionsShareASpelling(t *testing.T) {
	type origin struct {
		name  string
		where string
	}
	spanSpelling := map[string]origin{}
	logSpelling := map[string]origin{}
	metricSpelling := map[string]origin{}
	names := map[string]bool{}

	claim := func(t *testing.T, into map[string]origin, spelling, name, where, signal string) {
		t.Helper()
		if spelling == "" {
			return
		}
		if prior, taken := into[spelling]; taken {
			t.Errorf("%s and %s both spell the %s key %q (as %s and %s). "+
				"Whichever projection runs second wins",
				prior.name, name, signal, spelling, prior.where, where)
			return
		}
		into[spelling] = origin{name: name, where: where}
	}

	for _, def := range fact.All() {
		if names[def.Name] {
			t.Errorf("two Definitions are both named %q; Name is what a failure "+
				"message calls the row, so it has to be unique", def.Name)
		}
		names[def.Name] = true

		claim(t, spanSpelling, def.SpanKey, def.Name, "SpanKey", "span")
		for _, alias := range def.SpanAliases {
			claim(t, spanSpelling, alias.Key, def.Name, "an Alias", "span")
		}
		claim(t, spanSpelling, def.AbsentFlag, def.Name, "AbsentFlag", "span")
		claim(t, spanSpelling, def.PresentFlag, def.Name, "PresentFlag", "span")
		claim(t, logSpelling, def.LogKey, def.Name, "LogKey", "log")
		claim(t, metricSpelling, def.MetricKey, def.Name, "MetricKey", "metric")
	}

	// TruncationFlag is deliberately NOT claimed above: beckn.schemaContext and
	// beckn.schemaType share beckn.schemaTruncated by design, because they are
	// truncated in one step to keep their lengths parallel. Two flags for one
	// truncation would let a reader believe the two were bounded separately.
	if fact.Of(fact.BecknSchemaContext).TruncationFlag != fact.Of(fact.BecknSchemaType).TruncationFlag {
		t.Error("BecknSchemaContext and BecknSchemaType no longer share a TruncationFlag; " +
			"they are truncated in one step and a reader has to be able to tell")
	}
}

// TestRequiredIsExactlyTheSpecsThreeResourceFields.
//
// Required means "the boot refuses when this is empty". The spec marks eid,
// producer and domain Required on every signal; service.name and network.id are
// ours and a deployment that has not set one should still boot. Pinning the set
// rather than the count keeps a fourth row from quietly acquiring a boot
// failure, and a demotion of one of the three from quietly acquiring a span the
// facilitator rejects.
func TestRequiredIsExactlyTheSpecsThreeResourceFields(t *testing.T) {
	want := []string{"domain", "eid", "producer"}

	var got []string
	for _, def := range fact.All() {
		if def.Required {
			got = append(got, def.SpanKey)
		}
	}
	slices.Sort(got)

	if !slices.Equal(got, want) {
		t.Errorf("Required Resource attributes = %v, want %v", got, want)
	}
}

// crossLayerFixture mirrors tests/testdata/cross-layer-attributes.json.
type crossLayerFixture struct {
	Source struct {
		Repo string `json:"repo"`
		Ref  string `json:"ref"`
	} `json:"source"`
	Attributes []struct {
		Key   string `json:"key"`
		Where string `json:"where"`
		Fact  string `json:"fact"`
		Owner string `json:"owner"`
	} `json:"attributes"`
}

// TestCrossLayerKeysMatchTheVendoredFixture checks both directions, because
// each catches a different mistake.
//
// Fixture → registry catches a rename on our side: the contract still says
// transaction_id and we now emit something else, so onix's collector stops
// stitching. Registry → fixture catches a key promoted to CrossLayer in the
// table and never agreed with anyone, which is a claim that another repo reads
// it made unilaterally.
//
// What neither direction catches is both repos holding stale copies of the same
// file. telemetry-seam.md §5g says so plainly and ranks the three mitigations;
// this test is not one of them.
func TestCrossLayerKeysMatchTheVendoredFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../../tests/testdata/cross-layer-attributes.json")
	if err != nil {
		t.Fatalf("read the interop contract: %v", err)
	}
	var fixture crossLayerFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parse the interop contract: %v", err)
	}
	if fixture.Source.Ref == "" {
		t.Error("the fixture names no pinned ref; a vendored copy of somebody " +
			"else's contract has to say which version it is a copy of")
	}

	byName := map[string]fact.Definition{}
	for _, def := range fact.All() {
		byName[def.Name] = def
	}

	// spellingsOf returns every key this Definition puts on the wire, so a
	// fixture row matches whether the contract spelling is our primary key
	// (recipient.id) or an alias beside it (transaction_id).
	spellingsOf := func(def fact.Definition) []string {
		spellings := []string{def.SpanKey}
		for _, alias := range def.SpanAliases {
			spellings = append(spellings, alias.Key)
		}
		return spellings
	}

	claimed := map[string]bool{}
	checked := 0

	for _, row := range fixture.Attributes {
		if row.Fact == "" {
			// metric_uuid and metric.code are the METRIC signal's, and no
			// Definition owns them until Task 24 and Task 25. They stay in the
			// contract because the contract is the interop surface, not our
			// build order — but an owner has to be named.
			if row.Owner == "" {
				t.Errorf("fixture key %q names no fact and no owner: an interop "+
					"attribute nobody is building is a row that will still be here "+
					"in a year", row.Key)
			}
			continue
		}
		claimed[row.Fact] = true

		def, known := byName[row.Fact]
		if !known {
			t.Errorf("fixture key %q names fact.%s, which the registry does not "+
				"declare. Either the contract moved ahead of the table or a row "+
				"was renamed without re-vendoring", row.Key, row.Fact)
			continue
		}
		checked++

		if def.Layer != fact.CrossLayer {
			t.Errorf("fixture key %q maps to fact.%s, whose Layer is %v. Anything "+
				"in the contract is read by a component other than this one, so its "+
				"spelling is not ours to change", row.Key, row.Fact, def.Layer)
		}
		if !slices.Contains(spellingsOf(def), row.Key) {
			t.Errorf("the contract spells %q; fact.%s emits %v. A span spelled our "+
				"way is never stitched to anything",
				row.Key, row.Fact, spellingsOf(def))
		}
	}

	for _, def := range fact.All() {
		if def.Layer == fact.CrossLayer && !claimed[def.Name] {
			t.Errorf("fact.%s is declared CrossLayer and appears in no fixture row. "+
				"CrossLayer asserts that another repository reads this key; that is "+
				"an agreement, so it is written down", def.Name)
		}
	}

	if checked == 0 {
		t.Fatal("no fixture row resolved to a Definition; the contract is checking nothing")
	}
}

// --- The golden file: the reviewability guard -----------------------------

// update rewrites the golden file instead of comparing against it.
//
// telemetry-seam.md 5g says "generated by go:generate". This is that, with the
// generator living in the test rather than in a second main package. The
// directive below invokes it, so `go generate ./...` still works and there is
// no separate binary whose own correctness nobody checks. The difference the
// doc cares about — a registry edit shows up as a reviewable diff in the PR
// that makes it — is unaffected.
//
//go:generate go test . -run TestTheGoldenFileMatchesTheRegistry -update
var update = flag.Bool("update", false, "rewrite testdata/registry.golden.txt")

const goldenPath = "testdata/registry.golden.txt"

// TestTheGoldenFileMatchesTheRegistry is the reviewability guard, and it is the
// only test here that asserts nothing about correctness.
//
// Every other test in this package checks that a row is self-consistent. None of
// them can check that a row is what somebody INTENDED — that sender.id is
// CrossLayer rather than Local, that result.catalog_count is Unbounded. Those
// are judgements, and the only mechanism that catches a wrong one is a human
// reading the change. This file is what puts the change in front of them: a
// one-character edit to a Layer deep inside a 54-row table is invisible in
// a diff of registry.go and unmissable in a diff of this.
func TestTheGoldenFileMatchesTheRegistry(t *testing.T) {
	rendered := render()

	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("create the testdata directory: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(rendered), 0o644); err != nil {
			t.Fatalf("write the golden file: %v", err)
		}
		t.Logf("wrote %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read the golden file: %v\n"+
			"        Regenerate it with: go test ./src/platform/telemetry/fact -update", err)
	}

	if string(want) != rendered {
		t.Errorf("the registry and %s disagree.\n"+
			"        If the registry change was intended, regenerate:\n"+
			"            go test ./src/platform/telemetry/fact -update\n"+
			"        and put the resulting diff in the same commit — that diff IS the\n"+
			"        review. If it was not intended, the registry is what to fix.\n\n%s",
			goldenPath, firstDifference(string(want), rendered))
	}
}

// render writes one line per row, and a header naming every field of Definition
// in declaration order.
//
// The header is not decoration. Rows print only their non-zero fields, which
// keeps a line readable, and that alone would make ADDING a field to Definition
// invisible in the golden whenever the new field is unset on all 54 rows — which
// is exactly the state a field is in on the commit that introduces it. The
// header changes then, so the diff exists.
func render() string {
	var out strings.Builder

	definitionType := reflect.TypeOf(fact.Definition{})
	fields := make([]string, 0, definitionType.NumField())
	for index := range definitionType.NumField() {
		fields = append(fields, definitionType.Field(index).Name)
	}

	out.WriteString("# Generated by go test ./src/platform/telemetry/fact -update. Do not hand-edit.\n")
	out.WriteString("# One line per fact.Key, in key order. Fields at their zero value are omitted.\n")
	out.WriteString("# Definition fields: " + strings.Join(fields, " ") + "\n")

	for key, def := range fact.All() {
		out.WriteString(fmt.Sprintf("%3d %s\n", int(key), renderRow(def, fields)))
	}
	return out.String()
}

func renderRow(def fact.Definition, fields []string) string {
	value := reflect.ValueOf(def)

	parts := make([]string, 0, len(fields))
	for index, name := range fields {
		field := value.Field(index)
		if field.IsZero() {
			continue
		}
		parts = append(parts, name+"="+renderValue(field))
	}
	return strings.Join(parts, " ")
}

// renderValue prefers a Stringer, so the golden reads Bounded rather than 1 and
// a reviewer can see what changed without holding the const blocks in their head.
func renderValue(field reflect.Value) string {
	if stringer, ok := field.Interface().(fmt.Stringer); ok {
		return stringer.String()
	}
	if field.Kind() == reflect.Slice {
		entries := make([]string, field.Len())
		for index := range field.Len() {
			entries[index] = renderValue(field.Index(index))
		}
		return "[" + strings.Join(entries, ",") + "]"
	}
	if field.Kind() == reflect.Struct {
		return fmt.Sprintf("%+v", field.Interface())
	}
	return fmt.Sprintf("%v", field.Interface())
}

// firstDifference reports the first line that differs, because a whole-file diff
// of 54 rows in a Go failure message is something nobody reads to the end of.
func firstDifference(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")

	for index := range max(len(wantLines), len(gotLines)) {
		wantLine, gotLine := at(wantLines, index), at(gotLines, index)
		if wantLine != gotLine {
			return fmt.Sprintf("        first difference, line %d:\n"+
				"        golden:   %s\n"+
				"        registry: %s", index+1, wantLine, gotLine)
		}
	}
	return "        the files differ but no line does; check the trailing newline"
}

func at(lines []string, index int) string {
	if index >= len(lines) {
		return "(end of file)"
	}
	return lines[index]
}
