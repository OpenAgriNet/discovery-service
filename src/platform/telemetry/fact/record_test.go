package fact_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// TestObserveOnAContextWithNoRecordIsSilent.
//
// The probes chain (router.go:158) is RequestID + Recover: no allocator. A
// panicking probe must answer 500, not panic a second time inside the recovery
// that was answering the first. The acceptance and dbtest suites call
// controllers with no middleware at all and are covered by the same property.
func TestObserveOnAContextWithNoRecordIsSilent(t *testing.T) {
	ctx := context.Background()

	if got := fact.From(ctx); got != nil {
		t.Fatalf("From on a bare context = %v, want nil — allocating here would "+
			"make the record's lifetime depend on who looked", got)
	}

	fact.ObserveString(ctx, fact.BecknAction, "discover")
	fact.ObserveInt64(ctx, fact.ResultCatalogCount, 3)
	fact.ObserveFloat64(ctx, fact.RetrievalEmbeddingMs, 1.5)
	fact.ObserveBool(ctx, fact.ResultEmpty, true)
	fact.ObserveStrings(ctx, fact.RetrievalModesRun, []string{"lexical"})
}

func TestObserveOnANilRecordIsSilent(t *testing.T) {
	var record *fact.Record

	record.ObserveString(fact.BecknAction, "discover")
	record.ObserveInt64(fact.ResultCatalogCount, 3)
	record.ObserveBool(fact.ResultEmpty, true)

	if _, found := record.Lookup(fact.BecknAction); found {
		t.Error("Lookup on a nil Record found something")
	}
	if got := slices.Collect(record.All()); len(got) != 0 {
		t.Errorf("All on a nil Record yielded %v", got)
	}
}

func TestNewPutsARecordInTheContextAndHandsItBack(t *testing.T) {
	ctx, record := fact.New(context.Background())
	if record == nil {
		t.Fatal("New returned a nil Record")
	}
	if fact.From(ctx) != record {
		t.Error("From returned a different Record than New handed back; the " +
			"middleware reads it back after the handler returns and must get its own")
	}

	fact.ObserveString(ctx, fact.BecknAction, "discover")

	observed, found := record.Lookup(fact.BecknAction)
	if !found {
		t.Fatal("the context-form Observe did not reach the Record New allocated")
	}
	if observed.Text != "discover" {
		t.Errorf("Text = %q, want %q", observed.Text, "discover")
	}
	if observed.Kind != fact.KindString {
		t.Errorf("Kind = %v, want KindString", observed.Kind)
	}
}

// TestObservingTwiceKeepsTheLastValue. WriteHeader can fire more than once on a
// response the handler started writing and then faulted on, and the status the
// span reports has to be the one that went out.
func TestObservingTwiceKeepsTheLastValue(t *testing.T) {
	_, record := fact.New(context.Background())

	record.ObserveInt64(fact.HTTPStatusCode, 200)
	record.ObserveInt64(fact.HTTPStatusCode, 500)

	observed, _ := record.Lookup(fact.HTTPStatusCode)
	if observed.Int != 500 {
		t.Errorf("Int = %d, want 500", observed.Int)
	}
	if got := slices.Collect(record.All()); len(got) != 1 {
		t.Errorf("All yielded %d observations, want 1 — a second write must "+
			"replace the first rather than append beside it", len(got))
	}
}

// TestObservingTheWrongKindPanics.
//
// telemetry-seam.md: ObserveString against an Int64 key fails loudly rather
// than dropping the fact. Loudly means panic, and the reason it is safe to
// panic on a request path is that the mistake is not data-dependent:
// ObserveString(ResultCatalogCount, …) is wrong for every request, so it fails
// on the first test that walks the path and never first in production. A
// dropped fact would instead be found by whoever queries for the attribute that
// is missing, which is the failure mode this whole table exists to end.
func TestObservingTheWrongKindPanics(t *testing.T) {
	cases := []struct {
		name    string
		observe func(*fact.Record)
	}{
		{"string into an int64 key", func(r *fact.Record) {
			r.ObserveString(fact.ResultCatalogCount, "3")
		}},
		{"int64 into a string key", func(r *fact.Record) {
			r.ObserveInt64(fact.BecknAction, 3)
		}},
		{"strings into a scalar key", func(r *fact.Record) {
			r.ObserveStrings(fact.BecknAction, []string{"discover"})
		}},
		{"bool into a strings key", func(r *fact.Record) {
			r.ObserveBool(fact.RetrievalModesRun, true)
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, record := fact.New(context.Background())
			assertPanics(t, func() { testCase.observe(record) })
		})
	}
}

// TestTheKindCheckRunsBeforeTheNilCheck. The probes chain has no record, so a
// mismatch written there would be swallowed by nil-tolerance and survive to
// production — nil-tolerance is for a missing record, not for a wrong call.
func TestTheKindCheckRunsBeforeTheNilCheck(t *testing.T) {
	var record *fact.Record
	assertPanics(t, func() { record.ObserveString(fact.ResultCatalogCount, "3") })
	assertPanics(t, func() {
		fact.ObserveString(context.Background(), fact.ResultCatalogCount, "3")
	})
}

// TestAZeroIsAbsentKeyDropsItsZero. retrieval.embedding_ms must be absent
// rather than 0 under EMBEDDING_PROVIDER=noop, which is every Phase 1
// deployment: a zero reads as a fast embedding rather than as no embedding.
//
// The drop is in Observe rather than in the projection because there are four
// projections and only one Observe.
func TestAZeroIsAbsentKeyDropsItsZero(t *testing.T) {
	_, record := fact.New(context.Background())

	record.ObserveFloat64(fact.RetrievalEmbeddingMs, 0)
	if _, found := record.Lookup(fact.RetrievalEmbeddingMs); found {
		t.Error("a zero reached the record for a ZeroIsAbsent key; absent and " +
			"zero are different answers and a dashboard cannot tell them apart")
	}

	record.ObserveFloat64(fact.RetrievalEmbeddingMs, 0.4)
	if _, found := record.Lookup(fact.RetrievalEmbeddingMs); !found {
		t.Error("a non-zero was dropped too")
	}
}

// TestAZeroOnAnOrdinaryKeyIsKept. result.catalog_count = 0 is the most valuable
// signal this service gives the network — somebody asked and nobody serves it —
// so the rule above must not generalise.
func TestAZeroOnAnOrdinaryKeyIsKept(t *testing.T) {
	_, record := fact.New(context.Background())

	record.ObserveInt64(fact.ResultCatalogCount, 0)

	if _, found := record.Lookup(fact.ResultCatalogCount); !found {
		t.Error("result.catalog_count = 0 was dropped; that is the unmet-demand signal")
	}
}

// TestACallerSuppliedListIsBoundedAtTheRecord. beckn.schemaContext is a URI the
// caller wrote and export is always-on and unsampled, so the bound is applied
// where the value enters rather than where it leaves.
func TestACallerSuppliedListIsBoundedAtTheRecord(t *testing.T) {
	_, record := fact.New(context.Background())

	entries := make([]string, 0, 20)
	for i := range 20 {
		entries = append(entries, strings.Repeat("x", 300)+string(rune('a'+i)))
	}
	record.ObserveStrings(fact.BecknSchemaContext, entries)

	observed, found := record.Lookup(fact.BecknSchemaContext)
	if !found {
		t.Fatal("the observation was dropped rather than bounded")
	}

	def := fact.Of(fact.BecknSchemaContext)
	if len(observed.List) != def.MaxEntries {
		t.Errorf("kept %d entries, want MaxEntries = %d", len(observed.List), def.MaxEntries)
	}
	for i, entry := range observed.List {
		if len([]rune(entry)) > def.MaxRunes {
			t.Errorf("entry %d is %d runes, want at most MaxRunes = %d",
				i, len([]rune(entry)), def.MaxRunes)
		}
	}
	if !observed.Truncated {
		t.Error("Truncated is false; beckn.schemaTruncated has nothing to be set from")
	}
}

// TestAListInsideItsBoundsIsNotFlaggedTruncated. schemaTruncated is absent
// rather than false when nothing was cut, and a flag that is always true says
// nothing.
func TestAListInsideItsBoundsIsNotFlaggedTruncated(t *testing.T) {
	_, record := fact.New(context.Background())

	record.ObserveStrings(fact.BecknSchemaContext, []string{"https://example.org/x#MandiPrice"})

	observed, _ := record.Lookup(fact.BecknSchemaContext)
	if observed.Truncated {
		t.Error("Truncated is set on a list that fitted")
	}
}

// TestSchemaContextAndSchemaTypeTruncateToTheSameLength. Truncating one alone
// is what turns a correct pairing into silently wrong pairs — MandiPrice
// attributed to the wrong @context.
func TestSchemaContextAndSchemaTypeTruncateToTheSameLength(t *testing.T) {
	_, record := fact.New(context.Background())

	contexts := make([]string, 40)
	types := make([]string, 40)
	for i := range contexts {
		contexts[i] = "https://example.org/c"
		types[i] = "MandiPrice"
	}
	record.ObserveStrings(fact.BecknSchemaContext, contexts)
	record.ObserveStrings(fact.BecknSchemaType, types)

	gotContexts, _ := record.Lookup(fact.BecknSchemaContext)
	gotTypes, _ := record.Lookup(fact.BecknSchemaType)

	if len(gotContexts.List) != len(gotTypes.List) {
		t.Errorf("beckn.schemaContext kept %d and beckn.schemaType kept %d; the "+
			"two are parallel and same-length by contract",
			len(gotContexts.List), len(gotTypes.List))
	}
}

// TestAnEmptyListIsNotTheSameAsNoList. schemaContext absent means "no schema
// predicate at all" — every capability matches, the seeking-anything bucket. An
// empty list means a seeker who sent an empty array. Collapsing them loses the
// larger of the two.
func TestAnEmptyListIsNotTheSameAsNoList(t *testing.T) {
	_, record := fact.New(context.Background())

	record.ObserveStrings(fact.BecknSchemaContext, []string{})

	observed, found := record.Lookup(fact.BecknSchemaContext)
	if !found {
		t.Fatal("an explicitly empty list was dropped; it is a different answer " +
			"from the field being absent, which is what never calling Observe means")
	}
	if len(observed.List) != 0 {
		t.Errorf("List = %v, want empty", observed.List)
	}
}

// TestAllYieldsEveryObservationOnce is what the projections walk.
func TestAllYieldsEveryObservationOnce(t *testing.T) {
	_, record := fact.New(context.Background())

	record.ObserveString(fact.BecknAction, "discover")
	record.ObserveInt64(fact.ResultCatalogCount, 2)
	record.ObserveBool(fact.ResultEmpty, false)

	var keys []fact.Key
	for observed := range record.All() {
		keys = append(keys, observed.Key)
	}
	slices.Sort(keys)

	want := []fact.Key{fact.BecknAction, fact.ResultCatalogCount, fact.ResultEmpty}
	slices.Sort(want)

	if !slices.Equal(keys, want) {
		t.Errorf("All yielded %v, want %v", keys, want)
	}
}

// TestOfRefusesAKeyWithNoRow. Of returning a zero Definition would let a
// projection ship an attribute with an empty name, which every backend accepts
// and no query finds.
func TestOfRefusesAKeyWithNoRow(t *testing.T) {
	assertPanics(t, func() { fact.Of(fact.Key(200)) })
}

func assertPanics(t *testing.T, call func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Error("call did not panic")
		}
	}()
	call()
}
