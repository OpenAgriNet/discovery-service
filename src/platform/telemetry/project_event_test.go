package telemetry_test

import (
	"context"
	"testing"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// events runs the event projection over a record built by the given writes.
func events(t *testing.T, write func(*fact.Record)) []telemetry.SpanEvent {
	t.Helper()

	_, record := fact.New(context.Background())
	write(record)
	return telemetry.SpanEvents(record)
}

// named returns the one event with this name, failing rather than indexing.
func named(t *testing.T, projected []telemetry.SpanEvent, name string) telemetry.SpanEvent {
	t.Helper()

	var found []telemetry.SpanEvent
	for _, event := range projected {
		if event.Name == name {
			found = append(found, event)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d events named %q, want exactly 1; the projection has %d in all: %v",
			len(found), name, len(projected), names(projected))
	}
	return found[0]
}

func names(projected []telemetry.SpanEvent) []string {
	spelled := make([]string, 0, len(projected))
	for _, event := range projected {
		spelled = append(spelled, event.Name)
	}
	return spelled
}

// value returns one attribute off an event as its string form, for the
// assertions that only care whether it is there and roughly what it says.
func value(event telemetry.SpanEvent, key string) (string, bool) {
	for _, kv := range event.Attributes {
		if string(kv.Key) == key {
			return kv.Value.String(), true
		}
	}
	return "", false
}

// TestTheFourEventsAreProjectedUnderTheSpecsOwnNames.
//
// The names are the wire contract — a facilitator keys on them — so they are
// asserted as literals here and nowhere else. Every other test in this file
// reaches them through fact.Event.EventName(), which is what stops a rename from
// having to be made in two places.
func TestTheFourEventsAreProjectedUnderTheSpecsOwnNames(t *testing.T) {
	for _, want := range []struct {
		event fact.Event
		name  string
	}{
		{fact.RequestInfo, "request_info"},
		{fact.RetrievalInfo, "retrieval_info"},
		{fact.ResponseInfo, "response_info"},
		{fact.ErrorEvent, "error"},
	} {
		if got := want.event.EventName(); got != want.name {
			t.Errorf("%v.EventName() = %q, want %q", want.event, got, want.name)
		}
	}

	// NoEvent is not an event and must not name one. A projection that grouped
	// on it would emit a fifth event carrying every span attribute — the whole
	// span, twice.
	if got := fact.NoEvent.EventName(); got != "" {
		t.Errorf("NoEvent.EventName() = %q, want empty", got)
	}
}

// TestAnEventIsOmittedWhenNothingBelongingToItWasObserved.
//
// A discover that never reached the store emits no retrieval_info, and an
// error-free request emits no error event. The alternative — an empty event at
// the span's end — reads as a phase that ran and produced nothing, which is a
// different and much more alarming thing than a phase that did not run.
func TestAnEventIsOmittedWhenNothingBelongingToItWasObserved(t *testing.T) {
	projected := events(t, func(record *fact.Record) {
		record.ObserveStrings(fact.IntentKinds, []string{"textSearch"})
	})

	if len(projected) != 1 {
		t.Fatalf("events = %v, want only request_info", names(projected))
	}
	if projected[0].Name != fact.RequestInfo.EventName() {
		t.Errorf("the one event is %q", projected[0].Name)
	}
}

// TestSpanAttributesAreNotRepeatedOnEvents and the converse. The two projections
// partition the registry between them; a key on both sides is a value free to
// disagree with itself.
func TestSpanAttributesAreNotRepeatedOnEvents(t *testing.T) {
	projected := events(t, func(record *fact.Record) {
		record.ObserveString(fact.BecknAction, "discover")
		record.ObserveInt64(fact.HTTPStatusCode, 200)
		record.ObserveInt64(fact.ResultCatalogCount, 4)
	})

	event := named(t, projected, fact.ResponseInfo.EventName())
	for _, key := range []string{
		fact.Of(fact.BecknAction).SpanKey,
		fact.Of(fact.HTTPStatusCode).SpanKey,
	} {
		if _, present := value(event, key); present {
			t.Errorf("%s is on response_info as well as on the span", key)
		}
	}
}

// TestEveryEventKeyComesFromTheRegistry is the seam property for the event half.
//
// Asserted the same way as its span counterpart: the projection may emit no key
// the table does not spell. A literal "result.empty" in project_event.go would
// pass every other test here and stop following a rename in registry.go.
func TestEveryEventKeyComesFromTheRegistry(t *testing.T) {
	spelled := make(map[string]bool)
	for _, def := range fact.All() {
		if def.Signals&fact.Span == 0 || def.Event == fact.NoEvent {
			continue
		}
		spelled[def.SpanKey] = true
		if def.TruncationFlag != "" {
			spelled[def.TruncationFlag] = true
		}
	}

	projected := events(t, func(record *fact.Record) {
		record.ObserveStrings(fact.IntentKinds, []string{"textSearch"})
		record.ObserveString(fact.IntentFilterType, "jsonpath")
		record.ObserveStrings(fact.IntentSpatialOps, []string{"S_DWITHIN"})
		record.ObserveBool(fact.IntentScoped, true)
		record.ObserveStrings(fact.RetrievalModesRun, []string{"lexical"})
		record.ObserveStrings(fact.RetrievalModesDegraded, []string{"semantic"})
		record.ObserveFloat64(fact.RetrievalEmbeddingMs, 12.5)
		record.ObserveInt64(fact.ResultCatalogCount, 4)
		record.ObserveStrings(fact.ResultProviderIDs, []string{"imd.gov.in"})
		record.ObserveBool(fact.ResultEmpty, false)
		record.ObserveString(fact.ErrorEventType, "SCHEMA_ERROR")
		record.ObserveString(fact.ErrorCode, "NET_SCHEMA_VALIDATION_FAILED")
	})

	for _, event := range projected {
		for _, kv := range event.Attributes {
			if !spelled[string(kv.Key)] {
				t.Errorf("event %s carries %q, which no Definition spells", event.Name, kv.Key)
			}
		}
	}
}

// TestAPromotedFactStaysOnItsEvent is the other half of PromoteToSpan, and it
// is the half that is easy to lose.
//
// Promotion copies result.empty onto the span; it must not MOVE it. The events
// are the interop contract — response_info's shape is what a facilitator reads
// — so a promotion that emptied the event to avoid duplicating the attribute
// would optimise away the thing the events exist for. The duplication is the
// deliberate cost, paid once, on one row.
func TestAPromotedFactStaysOnItsEvent(t *testing.T) {
	projected := events(t, func(record *fact.Record) {
		record.ObserveBool(fact.ResultEmpty, true)
	})

	event := named(t, projected, fact.ResponseInfo.EventName())
	for _, kv := range event.Attributes {
		if string(kv.Key) == fact.Of(fact.ResultEmpty).SpanKey {
			return
		}
	}
	t.Errorf("%s is not on response_info; promotion copies a fact onto the span, "+
		"it does not move it off the event a facilitator reads",
		fact.Of(fact.ResultEmpty).SpanKey)
}

// TestAnEventIsAnchoredAtTheEarliestOfItsFacts, not the latest.
//
// The event marks when the phase HAPPENED, and a phase happens over an interval
// however instantaneous it looks — the response facts are three separate writes.
// Anchoring at the last of them would slide every event toward the span's end by
// however long its own work took, which is precisely the interval the deltas
// between events are supposed to measure.
func TestAnEventIsAnchoredAtTheEarliestOfItsFacts(t *testing.T) {
	_, record := fact.New(context.Background())

	record.ObserveInt64(fact.ResultCatalogCount, 4)
	first, _ := record.Lookup(fact.ResultCatalogCount)

	time.Sleep(2 * time.Millisecond)
	record.ObserveBool(fact.ResultEmpty, false)

	event := named(t, telemetry.SpanEvents(record), fact.ResponseInfo.EventName())
	if !event.Time.Equal(first.Time) {
		t.Errorf("response_info is at %v, want the first fact's %v", event.Time, first.Time)
	}
}

// TestCorrectingAFactMovesTheEventItBelongsTo, which is the other half of the
// rule above and reads as its opposite until you ask what the event is claiming.
//
// A re-observation replaces the value, so the record replaces the stamp with it
// (TestReObservingAKeyMovesItsStampForward). The event therefore anchors at the
// earliest fact it CURRENTLY holds, not at the earliest one ever written — and
// that is the honest answer, because the event reports the values that went out,
// and the moment those values became true is the moment of the correction. An
// event dated to a superseded value's write claims a shape that was not yet the
// shape.
//
// The case is narrow by construction: nothing on the request path re-observes an
// event fact today, and this pins what happens if something starts to.
func TestCorrectingAFactMovesTheEventItBelongsTo(t *testing.T) {
	_, record := fact.New(context.Background())

	record.ObserveInt64(fact.ResultCatalogCount, 4)
	superseded, _ := record.Lookup(fact.ResultCatalogCount)

	time.Sleep(2 * time.Millisecond)
	record.ObserveInt64(fact.ResultCatalogCount, 0)

	event := named(t, telemetry.SpanEvents(record), fact.ResponseInfo.EventName())
	if !event.Time.After(superseded.Time) {
		t.Errorf("response_info is still at the superseded write's %v", superseded.Time)
	}
}

// TestEventsComeBackInTimeOrder, whatever order the facts were observed in.
//
// The record preserves first-observed order and the phases normally arrive that
// way, so this looks free — and it is not: the error event is written from
// logNack, which on a partially-written response fires AFTER the result facts.
// Emitting the events in record order there would put the failure before the
// success it interrupted.
func TestEventsComeBackInTimeOrder(t *testing.T) {
	_, record := fact.New(context.Background())

	record.ObserveInt64(fact.ResultCatalogCount, 4)
	time.Sleep(time.Millisecond)
	record.ObserveString(fact.ErrorEventType, "INTERNAL_ERROR")
	time.Sleep(time.Millisecond)
	record.ObserveStrings(fact.IntentKinds, []string{"textSearch"})

	projected := telemetry.SpanEvents(record)
	for index := 1; index < len(projected); index++ {
		if !projected[index].Time.After(projected[index-1].Time) {
			t.Errorf("event %d (%s) at %v is not after event %d (%s) at %v",
				index, projected[index].Name, projected[index].Time,
				index-1, projected[index-1].Name, projected[index-1].Time)
		}
	}
}

// TestATruncatedEventValueSaysSo. result.provider_ids is bounded at 16, and a
// list silently cut to 16 reads as a query that was answered by exactly sixteen
// providers.
func TestATruncatedEventValueSaysSo(t *testing.T) {
	many := make([]string, 0, 40)
	for index := range 40 {
		many = append(many, string(rune('a'+index%26))+".example.org")
	}

	event := named(t, events(t, func(record *fact.Record) {
		record.ObserveStrings(fact.ResultProviderIDs, many)
	}), fact.ResponseInfo.EventName())

	flag := fact.Of(fact.ResultProviderIDs).TruncationFlag
	if got, present := value(event, flag); !present || got != "true" {
		t.Errorf("%s = %q (present=%v), want true", flag, got, present)
	}
}

// TestANilRecordProjectsNoEvents. The probes chain allocates none, and unlike
// the span attributes there is no absent-flag pass to run: an event that did not
// happen is reported by not being there.
func TestANilRecordProjectsNoEvents(t *testing.T) {
	if projected := telemetry.SpanEvents(nil); len(projected) != 0 {
		t.Errorf("SpanEvents(nil) = %v, want none", names(projected))
	}
}
