package logger

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// The ten field constructors that spell a key the registry also spells, each
// beside the fact.Key whose Definition claims to describe it.
//
// Three of them — request_id and 23e's trace_id / span_id — name a row nothing
// observes onto a record: they reach the line through the request-scoped
// logger, which is what makes them appear on EVERY line rather than only on the
// completion one. The rows exist so those three spellings are checked here like
// the rest, since a Log key with no constructor is a key nothing proves the
// registry agrees with.
//
// This table is the whole point of the projection living in this package
// (telemetry-seam.md:130-136): the log field names are spelled here and the
// registry's LogKey column claims to be the same spelling, so the check that
// they agree is a same-package test rather than a cross-package convention
// nobody runs.
var agreements = []struct {
	key   fact.Key
	field zapcore.Field
}{
	{fact.RequestID, RequestID("r")},
	{fact.TraceID, TraceID("e4062ac74ed7b560fdc9315dfbedb82e")},
	{fact.SpanID, SpanID("9263e6b8529a63ba")},
	{fact.BecknTransactionID, TransactionID("t")},
	{fact.BecknMessageID, MessageID("m")},
	{fact.BecknAction, Action("discover")},
	{fact.ErrorType, ErrorType("SYSTEM")},
	{fact.ErrorCode, ErrorCode("NET_CATALOG_SOURCE_UNAVAILABLE")},
	{fact.HTTPStatusCode, Status(200)},
	{fact.DurationMS, DurationMS(time.Millisecond)},
}

// TestTheLogKeysAgreeWithTheRegistry is the agreement check.
//
// A constructor and a Definition disagreeing about the *name* means one query
// finds nothing and nobody notices which — the failure the comment above
// logger.go's field constructors says the single spelling exists to prevent,
// reintroduced the moment a second table claims the same spellings.
func TestTheLogKeysAgreeWithTheRegistry(t *testing.T) {
	for _, agreement := range agreements {
		def := fact.Of(agreement.key)

		if def.LogKey != agreement.field.Key {
			t.Errorf("%s: registry LogKey is %q and the constructor writes %q; "+
				"one of the two spellings reaches no query",
				def.Name, def.LogKey, agreement.field.Key)
		}
	}
}

// TestTheLogKindsAgreeWithTheRegistry is the other half, and it is the half
// that actually bites.
//
// A name mismatch is found by the first person who greps for the field. A KIND
// mismatch is not found at all: the projection reads Definition.Kind to decide
// which zap constructor to call, so a Definition claiming Int64 for a field
// written as Float64 silently rounds every value. duration_ms carries
// microsecond precision on purpose — DurationMS's own comment says integer
// milliseconds would report every request inside the 20 ms budget as one of
// twenty indistinguishable values — and rounding it is a change no test of the
// name would see.
func TestTheLogKindsAgreeWithTheRegistry(t *testing.T) {
	want := map[zapcore.FieldType]fact.Kind{
		zapcore.StringType:  fact.KindString,
		zapcore.Int64Type:   fact.KindInt64,
		zapcore.Float64Type: fact.KindFloat64,
		zapcore.BoolType:    fact.KindBool,
	}

	for _, agreement := range agreements {
		def := fact.Of(agreement.key)

		kind, known := want[agreement.field.Type]
		if !known {
			t.Errorf("%s: the constructor writes zap type %v, which no fact.Kind covers",
				def.Name, agreement.field.Type)
			continue
		}
		if def.Kind != kind {
			t.Errorf("%s: registry Kind is %v and the constructor writes %v (zap %v); "+
				"the projection reads Kind to pick a constructor, so the narrower of "+
				"the two silently wins",
				def.Name, def.Kind, kind, agreement.field.Type)
		}
	}
}

// TestNoLogKeyNeedsADerivedFlag is a guard on an absence, and it is here
// because the absence is the only reason Fields is as short as it is.
//
// Three Definition fields ask the projection to emit a SECOND attribute beside
// the value: TruncationFlag when the value was cut short, and AbsentFlag /
// PresentFlag to keep "never observed" distinguishable from "observed empty".
// Every row that declares one is Span-only today, so Fields ignores all three
// and loses nothing. The day a Log key declares one, this fails — rather than
// the log quietly reporting a truncated provider list as the whole list, which
// is the failure mode the flag was added to prevent.
func TestNoLogKeyNeedsADerivedFlag(t *testing.T) {
	for _, def := range logDefinitions() {
		if def.TruncationFlag != "" {
			t.Errorf("%s carries the Log signal and declares TruncationFlag %q, which "+
				"Fields does not emit: the log line would report a truncated value as "+
				"the whole one", def.Name, def.TruncationFlag)
		}
		if def.AbsentFlag != "" || def.PresentFlag != "" {
			t.Errorf("%s carries the Log signal and declares AbsentFlag %q / PresentFlag %q, "+
				"which Fields does not emit: absent and empty stop being distinguishable "+
				"on the log line", def.Name, def.AbsentFlag, def.PresentFlag)
		}
	}
}

// logDefinitions is every row the log projection is responsible for, read off
// the registry rather than listed here — a list here would be a second table to
// keep true, and it is the copy that rots.
func logDefinitions() []fact.Definition {
	var carried []fact.Definition
	for _, def := range fact.All() {
		if def.Signals&fact.Log != 0 {
			carried = append(carried, def)
		}
	}
	return carried
}

// recordOf builds a record carrying the given facts, in order.
func recordOf(t *testing.T, observe func(*fact.Record)) *fact.Record {
	t.Helper()

	_, record := fact.New(context.Background())
	observe(record)
	return record
}

// TestFieldsProjectsInObservationOrder is 23b's acceptance criterion in one
// assertion.
//
// The completion line must read identically before and after this sub-task, and
// its field order comes from the order Envelope recorded the correlators in
// (envelope.go's correlate loop). Record.All yields first-observed order, so the
// projection must preserve it rather than sorting by key or by registry
// position — either of which would reorder every log line in the service while
// changing nothing else, which is the kind of diff nobody reviews and every
// dashboard notices.
func TestFieldsProjectsInObservationOrder(t *testing.T) {
	record := recordOf(t, func(r *fact.Record) {
		r.ObserveString(fact.BecknTransactionID, "a3f0")
		r.ObserveString(fact.BecknMessageID, "2f6b")
		r.ObserveString(fact.BecknAction, "publish")
	})

	want := []string{"transaction_id", "message_id", "action"}
	got := Fields(record)

	if len(got) != len(want) {
		t.Fatalf("projected %d fields, want %d: %v", len(got), len(want), got)
	}
	for index, key := range want {
		if got[index].Key != key {
			t.Errorf("field %d is %q, want %q — the projection reordered the line",
				index, got[index].Key, key)
		}
	}
}

// TestFieldsSkipsAKeyWithNoLogSignal keeps the span's attributes off the log
// line.
//
// From 23c the record carries far more than the log needs — beckn.version and
// beckn.networkId are Span-only, and 23d adds a dozen more. A projection that
// wrote everything it found would turn one completion line into the whole span,
// and it would do it silently, growing the log volume of every deployment.
func TestFieldsSkipsAKeyWithNoLogSignal(t *testing.T) {
	if def := fact.Of(fact.BecknVersion); def.Signals&fact.Log != 0 {
		t.Fatalf("BecknVersion now carries the Log signal, so it is the wrong key "+
			"for this test: %v", def.Signals)
	}

	record := recordOf(t, func(r *fact.Record) {
		r.ObserveString(fact.BecknTransactionID, "a3f0")
		r.ObserveString(fact.BecknVersion, "2.0.0")
	})

	got := Fields(record)
	if len(got) != 1 || got[0].Key != "transaction_id" {
		t.Errorf("Fields = %v, want transaction_id alone — a Span-only key reached the log", got)
	}
}

// TestFieldsOnANilRecordIsEmpty is the probes chain.
//
// router.go:158-163 mounts RequestID + Recover only and allocates no record, so
// a panicking /healthz reaches this projection with nothing to project. It must
// answer an empty line, not panic a second time inside the recovery that was
// answering the first.
func TestFieldsOnANilRecordIsEmpty(t *testing.T) {
	if got := Fields(nil); len(got) != 0 {
		t.Errorf("Fields(nil) = %v, want no fields", got)
	}
}

// TestFieldsKeepsMicrosecondPrecisionOnTheDuration is the regression the kind
// agreement above prevents, asserted on the value rather than on the table.
//
// The registry declared duration_ms as Int64 until this sub-task read Kind to
// pick a constructor. Nothing consumed the column, so the mismatch cost
// nothing — right up to the commit that made the projection round every
// duration to a whole millisecond and reported the 20 ms budget as one of
// twenty values.
func TestFieldsKeepsMicrosecondPrecisionOnTheDuration(t *testing.T) {
	record := recordOf(t, func(r *fact.Record) {
		r.ObserveFloat64(fact.DurationMS, 1.234)
	})

	got := Fields(record)
	if len(got) != 1 {
		t.Fatalf("Fields = %v, want one field", got)
	}
	if got[0].Type != zapcore.Float64Type {
		t.Fatalf("duration_ms is zap type %v, want Float64 — an integer field rounds it",
			got[0].Type)
	}
	if got[0].Interface == nil && got[0].Integer == 0 {
		t.Fatal("duration_ms carries no value")
	}
	if rendered := DurationMS(1234 * time.Microsecond); rendered.Key != got[0].Key {
		t.Errorf("the projection writes %q and the constructor writes %q",
			got[0].Key, rendered.Key)
	}
}

// TestFieldsRendersEveryLogSignalKeyInTheRegistry is the completeness guard,
// driven off the table rather than off a list here.
//
// The projection switches on Kind, and a Kind it does not handle drops the
// field — silently, since a dropped log field looks exactly like a request that
// did not have one. Today the Log column uses three of the five kinds; the day
// someone adds a Bool one, this fails rather than shipping a field that never
// appears.
func TestFieldsRendersEveryLogSignalKeyInTheRegistry(t *testing.T) {
	for key, def := range fact.All() {
		if def.Signals&fact.Log == 0 {
			continue
		}

		record := recordOf(t, func(r *fact.Record) { observeSomething(t, r, key, def) })

		got := Fields(record)
		if len(got) != 1 {
			t.Errorf("%s (%v): projected %d fields, want 1 — the projection does not "+
				"handle this Kind", def.Name, def.Kind, len(got))
			continue
		}
		if got[0].Key != def.LogKey {
			t.Errorf("%s: projected under %q, want the registry's LogKey %q",
				def.Name, got[0].Key, def.LogKey)
		}
	}
}

// observeSomething records a non-zero value of whatever kind the key declares,
// so the caller can assert on the projection without knowing the kind. Non-zero
// matters: a ZeroIsAbsent key drops its zero, and a dropped fact would read as
// a projection that could not render it.
func observeSomething(t *testing.T, record *fact.Record, key fact.Key, def fact.Definition) {
	t.Helper()

	switch def.Kind {
	case fact.KindString:
		value := "x"
		if def.Cardinality == fact.Bounded && len(def.Values) > 0 {
			value = def.Values[0]
		}
		record.ObserveString(key, value)
	case fact.KindInt64:
		record.ObserveInt64(key, 1)
	case fact.KindFloat64:
		record.ObserveFloat64(key, 1.5)
	case fact.KindBool:
		record.ObserveBool(key, true)
	case fact.KindStrings:
		record.ObserveStrings(key, []string{"x"})
	default:
		t.Fatalf("%s declares Kind %v, which this helper cannot observe", def.Name, def.Kind)
	}
}
