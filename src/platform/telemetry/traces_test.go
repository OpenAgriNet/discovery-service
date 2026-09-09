package telemetry_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// projected runs the span projection over a record built by the given writes and
// returns it as a map, which is what almost every assertion here wants. The
// ordered cases build their own.
func projected(t *testing.T, write func(*fact.Record)) map[string]attribute.Value {
	t.Helper()

	_, record := fact.New(context.Background())
	write(record)

	attributes := make(map[string]attribute.Value)
	for _, kv := range telemetry.SpanAttributes(record) {
		if _, already := attributes[string(kv.Key)]; already {
			t.Errorf("attribute %q emitted twice; the exporter keeps one and which one is undefined", kv.Key)
		}
		attributes[string(kv.Key)] = kv.Value
	}
	return attributes
}

// TestTheProjectionSpellsEveryKeyFromTheRegistry is the seam property stated for
// the span, and it is asserted by absence: no test below writes an attribute
// name that the registry does not also spell.
//
// The mechanism is the point rather than this one assertion. A projection with
// `attribute.String("beckn.action", ...)` in it would pass every test in this
// file and would silently stop following a rename in registry.go — which is the
// exact failure the seam (opentelemetry.md §The seam) exists to make impossible.
func TestTheProjectionSpellsEveryKeyFromTheRegistry(t *testing.T) {
	spelled := make(map[string]bool)
	for _, def := range fact.All() {
		if def.Signals&fact.Span == 0 {
			continue
		}
		spelled[def.SpanKey] = true
		for _, alias := range def.SpanAliases {
			spelled[alias.Key] = true
		}
		for _, derived := range []string{def.AbsentFlag, def.PresentFlag, def.TruncationFlag} {
			if derived != "" {
				spelled[derived] = true
			}
		}
	}

	attributes := projected(t, func(record *fact.Record) {
		record.ObserveString(fact.BecknAction, "discover")
		record.ObserveString(fact.SenderID, "farmer.oan.example.org")
		record.ObserveStrings(fact.BecknSchemaContext, []string{"$.message.catalogs[*]"})
		record.ObserveInt64(fact.HTTPStatusCode, 200)
	})

	for key := range attributes {
		if !spelled[key] {
			t.Errorf("the span carries %q, which no registry row spells; renaming it in "+
				"registry.go would not move it and nothing would say so", key)
		}
	}
}

// TestOneObservationBecomesBothStatusSpellings is divergence 3, and it is the
// reason Alias carries AsString at all.
//
// The spec declares http.status.code as Int in its structure and emits a STRING
// in all three of its examples; the semantic conventions say http.status_code as
// an int. A collector rule keying on either has to find it, so both go out —
// from one observation, so they cannot drift the way two separate observations
// of "the status" would. Neither spelling is onix's: it sends
// http.response.status_code, so this is a spec disagreement and not a
// cross-repo one.
func TestOneObservationBecomesBothStatusSpellings(t *testing.T) {
	attributes := projected(t, func(record *fact.Record) {
		record.ObserveInt64(fact.HTTPStatusCode, 404)
	})

	def := fact.Of(fact.HTTPStatusCode)
	if got := attributes[def.SpanKey]; got.AsInt64() != 404 {
		t.Errorf("%s = %v, want the int 404", def.SpanKey, got.AsInterface())
	}

	if len(def.SpanAliases) != 1 {
		t.Fatalf("HTTPStatusCode carries %d aliases, want the one spec spelling", len(def.SpanAliases))
	}
	alias := def.SpanAliases[0]
	got, present := attributes[alias.Key]
	if !present {
		t.Fatalf("no %s on the span; the facilitator reads this spelling and finds nothing", alias.Key)
	}
	if got.Type() != attribute.STRING || got.AsString() != "404" {
		t.Errorf("%s = %v (%s), want the string \"404\" — AsString is what divergence 3 asks for",
			alias.Key, got.AsInterface(), got.Type())
	}
}

// TestTheCrossLayerAliasesCarryTheSameValueAsTheirKey is I1: the transaction and
// message ids go out under both the beckn.* spelling and the bare one onix's
// collectors join on. Both are projected from ONE observation, which is what
// makes "they cannot disagree" a fact about the code rather than a convention.
func TestTheCrossLayerAliasesCarryTheSameValueAsTheirKey(t *testing.T) {
	attributes := projected(t, func(record *fact.Record) {
		record.ObserveString(fact.BecknTransactionID, "a3f0")
		record.ObserveString(fact.BecknMessageID, "2f6b")
	})

	for _, key := range []fact.Key{fact.BecknTransactionID, fact.BecknMessageID} {
		def := fact.Of(key)
		for _, alias := range def.SpanAliases {
			if attributes[alias.Key].AsString() != attributes[def.SpanKey].AsString() {
				t.Errorf("%s = %q but %s = %q; the join spelling and ours disagree",
					def.SpanKey, attributes[def.SpanKey].AsString(),
					alias.Key, attributes[alias.Key].AsString())
			}
		}
	}
}

// TestAnUnobservedFactBecomesItsAbsentFlag is what keeps "we do not know who
// this was" distinguishable from "this was nobody".
//
// An empty sender.id says the second. sender.unidentified says the first, which
// is the one an operator can act on — and it has to be emitted by the projection
// rather than by the caller, because the caller's whole situation is that it had
// nothing to observe.
func TestAnUnobservedFactBecomesItsAbsentFlag(t *testing.T) {
	attributes := projected(t, func(*fact.Record) {})

	for key, def := range fact.All() {
		if def.Signals&fact.Span == 0 || def.Event != fact.NoEvent || def.AbsentFlag == "" {
			continue
		}
		got, present := attributes[def.AbsentFlag]
		if !present {
			t.Errorf("nothing observed %s and no %s on the span; an operator cannot tell "+
				"the unidentified case from the empty one", def.Name, def.AbsentFlag)
			continue
		}
		if !got.AsBool() {
			t.Errorf("%s = false with %s never observed", def.AbsentFlag, def.Name)
		}
		if _, leaked := attributes[def.SpanKey]; leaked {
			t.Errorf("%s is on the span despite never being observed", def.SpanKey)
		}
		_ = key
	}
}

// TestAnObservedFactBecomesItsPresentFlagAndNotItsAbsentOne is the other half,
// and the two flags are not opposites: sender.unidentified means we were told
// nothing, sender.unverified means we were told something and did not check it.
// Task 6 is parked, so today every identified sender is an unverified one — and
// this is what notices if the flag stops being emitted when signature
// verification lands and someone deletes the wrong branch.
func TestAnObservedFactBecomesItsPresentFlagAndNotItsAbsentOne(t *testing.T) {
	attributes := projected(t, func(record *fact.Record) {
		record.ObserveString(fact.SenderID, "farmer.oan.example.org")
	})

	def := fact.Of(fact.SenderID)
	if got := attributes[def.SpanKey]; got.AsString() != "farmer.oan.example.org" {
		t.Errorf("%s = %q, want the observed value", def.SpanKey, got.AsString())
	}
	if got, present := attributes[def.PresentFlag]; !present || !got.AsBool() {
		t.Errorf("%s missing on an identified sender; nothing on the span says the identity "+
			"was taken on trust", def.PresentFlag)
	}
	if _, present := attributes[def.AbsentFlag]; present {
		t.Errorf("%s is on the span alongside a sender.id; the two flags mean different "+
			"things and cannot both be true", def.AbsentFlag)
	}
}

// TestATruncatedValueSaysSo is why MaxRunes has a flag beside it.
//
// beckn.schemaContext is a JSONPath the caller wrote and export is unsampled, so
// it is clamped. A clamped value that does not say it was clamped is worse than
// a dropped one: it reads as a complete predicate that happens to be short, and
// somebody debugging a filter compares it against theirs and concludes the
// service received something different from what they sent.
func TestATruncatedValueSaysSo(t *testing.T) {
	def := fact.Of(fact.BecknSchemaContext)
	if def.MaxRunes == 0 {
		t.Fatal("BecknSchemaContext carries no MaxRunes; this test clamps nothing")
	}

	attributes := projected(t, func(record *fact.Record) {
		record.ObserveStrings(fact.BecknSchemaContext,
			[]string{strings.Repeat("$", def.MaxRunes+50)})
	})

	if got, present := attributes[def.TruncationFlag]; !present || !got.AsBool() {
		t.Errorf("%s missing on a clamped value; the span shows a short predicate and "+
			"nothing says it is not the whole one", def.TruncationFlag)
	}
	for _, entry := range attributes[def.SpanKey].AsStringSlice() {
		if len([]rune(entry)) > def.MaxRunes {
			t.Errorf("%s carries a %d-rune entry, want at most %d",
				def.SpanKey, len([]rune(entry)), def.MaxRunes)
		}
	}
}

// TestTheSharedTruncationFlagIsEmittedOnce is the consequence of
// beckn.schemaContext and beckn.schemaType naming the same flag — one cut, one
// flag. Emitting it per key would write the attribute twice on the span, and an
// exporter keeps one of the two with nothing saying which.
func TestTheSharedTruncationFlagIsEmittedOnce(t *testing.T) {
	def := fact.Of(fact.BecknSchemaContext)
	long := []string{strings.Repeat("$", def.MaxRunes+50)}

	// projected fails the test on any duplicate key, which is the assertion; the
	// count below is what makes the failure readable when it happens.
	attributes := projected(t, func(record *fact.Record) {
		record.ObserveStrings(fact.BecknSchemaContext, long)
		record.ObserveStrings(fact.BecknSchemaType, long)
	})

	if _, present := attributes[def.TruncationFlag]; !present {
		t.Errorf("%s missing though both halves of the pair were cut", def.TruncationFlag)
	}
}

// TestAnUnclampedValueCarriesNoTruncationFlag is the pair, and it matters
// because a flag that is always present is a flag nothing can be filtered by.
func TestAnUnclampedValueCarriesNoTruncationFlag(t *testing.T) {
	attributes := projected(t, func(record *fact.Record) {
		record.ObserveStrings(fact.BecknSchemaContext, []string{"$.message.catalogs[*]"})
	})

	if _, present := attributes[fact.Of(fact.BecknSchemaContext).TruncationFlag]; present {
		t.Error("a short predicate is flagged as truncated")
	}
}

// TestLogOnlyFactsStayOffTheSpan is the Signals bits doing their job in the
// direction that is easy to get wrong.
//
// duration_ms is Log-only on purpose: the span already carries its own start and
// end, and a duration attribute beside them is a second answer to the same
// question that is free to disagree with the first. The projection reads the
// bits rather than emitting everything it finds, and this is what says so.
func TestLogOnlyFactsStayOffTheSpan(t *testing.T) {
	attributes := projected(t, func(record *fact.Record) {
		record.ObserveFloat64(fact.DurationMS, 12.5)
		record.ObserveInt64(fact.HTTPStatusCode, 200)
	})

	if _, present := attributes[fact.Of(fact.DurationMS).LogKey]; present {
		t.Errorf("%s is on the span; the span's own start and end already answer that, "+
			"and two answers drift", fact.Of(fact.DurationMS).LogKey)
	}
	if len(attributes) == 0 {
		t.Fatal("the projection emitted nothing at all, so the assertion above is vacuous")
	}
}

// TestEventFactsStayOffTheSpanItself is the placement rule from
// opentelemetry.md: true for the whole request → attribute, produced at a point
// during processing → event. 23d puts these on events; a projection that also
// put them on the span would duplicate every one of them.
func TestEventFactsStayOffTheSpanItself(t *testing.T) {
	attributes := projected(t, func(record *fact.Record) {
		record.ObserveString(fact.ErrorCode, "40000")
		record.ObserveString(fact.BecknAction, "discover")
	})

	if _, present := attributes[fact.Of(fact.ErrorCode).SpanKey]; present {
		t.Errorf("%s is on the span; it belongs on the error event 23d writes",
			fact.Of(fact.ErrorCode).SpanKey)
	}
	if _, present := attributes[fact.Of(fact.BecknAction).SpanKey]; !present {
		t.Error("beckn.action is not on the span, so the assertion above proves nothing")
	}
}

// TestAPromotedEventFactIsAlsoOnTheSpan is the exception to the test above, and
// it exists as a row-by-row opt-in rather than a relaxation of the rule.
//
// opentelemetry.md's ClickStack note: span attributes are a queryable map and
// event attributes are harder to aggregate, so a fact a dashboard reads hot is
// worth carrying twice. result.empty is the one — unmet demand is the single
// metric no other participant in the network can produce, so it is read by
// every consumer of this telemetry and by the spanmetrics connector, which sees
// span attributes ONLY and cannot reach an event at all.
//
// "Individually, not wholesale; the events are the interop contract" is the
// whole design of PromoteToSpan: a bool per row, not a change to onTheSpan's
// meaning. Promoting every event fact would duplicate 54 attributes to save
// one query.
func TestAPromotedEventFactIsAlsoOnTheSpan(t *testing.T) {
	attributes := projected(t, func(record *fact.Record) {
		record.ObserveBool(fact.ResultEmpty, true)
	})

	value, present := attributes[fact.Of(fact.ResultEmpty).SpanKey]
	if !present {
		t.Fatalf("%s is not on the span; the spanmetrics connector reads span "+
			"attributes only, so unmet demand cannot be counted without it",
			fact.Of(fact.ResultEmpty).SpanKey)
	}
	if value.AsBool() != true {
		t.Errorf("%s = %v on the span, want true", fact.Of(fact.ResultEmpty).SpanKey, value.AsBool())
	}
}

// TestOnlyTheDeclaredRowsArePromoted keeps the opt-in honest. The failure this
// catches is someone reaching for the bool to save a query and quietly turning
// the events into a second copy of the span.
func TestOnlyTheDeclaredRowsArePromoted(t *testing.T) {
	var promoted []string
	for _, def := range fact.All() {
		if def.PromoteToSpan {
			promoted = append(promoted, def.SpanKey)
		}
	}

	want := []string{fact.Of(fact.ResultEmpty).SpanKey}
	if !slices.Equal(promoted, want) {
		t.Errorf("promoted rows = %v, want %v.\n"+
			"Adding one is a deliberate cost: the attribute ships on both the span "+
			"and its event, and every promoted row is also a candidate dimension "+
			"whose value set multiplies the connector's series count. If the new row "+
			"is right, say why in its Note and update this list.", promoted, want)
	}
}

// TestTheSpanUUIDIsNotProjected is a boundary between two things that both write
// span attributes.
//
// span_uuid is stamped by the SpanProcessor at OnStart, because it must exist on
// spans this middleware never touches. Projecting it here as well would write it
// twice per span, and the second write silently replaces the first — so a
// facilitator correlating on span_uuid would follow whichever value the
// projection happened to have, which is none.
func TestTheSpanUUIDIsNotProjected(t *testing.T) {
	attributes := projected(t, func(record *fact.Record) {
		record.ObserveString(fact.BecknAction, "discover")
	})

	if _, present := attributes[fact.Of(fact.SpanUUID).SpanKey]; present {
		t.Errorf("%s came out of the record projection as well as the SpanProcessor; "+
			"one of the two writes wins and nothing says which",
			fact.Of(fact.SpanUUID).SpanKey)
	}
}

// TestANilRecordStillCarriesTheAbsentFlags is the probes chain.
//
// /healthz runs with no record at all (router.go's probes) and 23e's Trace still
// starts a span for it. A projection that returned nil there would emit a span
// with no sender.unidentified on it, which reads as a request whose sender was
// checked — the exact confusion the flag exists to prevent. Nil is "we observed
// nothing", and "nothing" is precisely what the absent flags describe.
func TestANilRecordStillCarriesTheAbsentFlags(t *testing.T) {
	var absent *fact.Record

	var keys []string
	for _, kv := range telemetry.SpanAttributes(absent) {
		keys = append(keys, string(kv.Key))
	}

	want := fact.Of(fact.SenderID).AbsentFlag
	if !slices.Contains(keys, want) {
		t.Errorf("a nil record projects %v, with no %s; a span with no sender flag at all "+
			"reads as one whose sender was verified", keys, want)
	}
}

// TestTheProjectionEmitsNothingUnnamed is the failure mode fact.Of's panic
// exists to prevent, asserted from the other end: every backend accepts an
// attribute with an empty key and no query ever finds it again.
func TestTheProjectionEmitsNothingUnnamed(t *testing.T) {
	_, record := fact.New(context.Background())
	record.ObserveString(fact.BecknAction, "discover")
	record.ObserveInt64(fact.HTTPStatusCode, 200)
	record.ObserveString(fact.SenderID, "farmer.oan.example.org")

	for _, kv := range telemetry.SpanAttributes(record) {
		if string(kv.Key) == "" {
			t.Fatal("an attribute with an empty key; it exports and nothing can read it back")
		}
	}
}

// --- Span events ----------------------------------------------------------

// events runs the event projection over a record built by the given writes.
func events(t *testing.T, write func(*fact.Record)) []telemetry.SpanEvent {
	t.Helper()

	_, record := fact.New(context.Background())
	write(record)
	return telemetry.SpanEvents(record)
}

// named returns the one event with this name, failing rather than indexing.
func named(t *testing.T, emitted []telemetry.SpanEvent, name string) telemetry.SpanEvent {
	t.Helper()

	var found []telemetry.SpanEvent
	for _, event := range emitted {
		if event.Name == name {
			found = append(found, event)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d events named %q, want exactly 1; the projection has %d in all: %v",
			len(found), name, len(emitted), names(emitted))
	}
	return found[0]
}

func names(emitted []telemetry.SpanEvent) []string {
	spelled := make([]string, 0, len(emitted))
	for _, event := range emitted {
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
	emitted := events(t, func(record *fact.Record) {
		record.ObserveStrings(fact.IntentKinds, []string{"textSearch"})
	})

	if len(emitted) != 1 {
		t.Fatalf("events = %v, want only request_info", names(emitted))
	}
	if emitted[0].Name != fact.RequestInfo.EventName() {
		t.Errorf("the one event is %q", emitted[0].Name)
	}
}

// TestSpanAttributesAreNotRepeatedOnEvents and the converse. The two projections
// partition the registry between them; a key on both sides is a value free to
// disagree with itself.
func TestSpanAttributesAreNotRepeatedOnEvents(t *testing.T) {
	emitted := events(t, func(record *fact.Record) {
		record.ObserveString(fact.BecknAction, "discover")
		record.ObserveInt64(fact.HTTPStatusCode, 200)
		record.ObserveInt64(fact.ResultCatalogCount, 4)
	})

	event := named(t, emitted, fact.ResponseInfo.EventName())
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
// the table does not spell. A literal "result.empty" in traces.go would
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

	emitted := events(t, func(record *fact.Record) {
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

	for _, event := range emitted {
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
	emitted := events(t, func(record *fact.Record) {
		record.ObserveBool(fact.ResultEmpty, true)
	})

	event := named(t, emitted, fact.ResponseInfo.EventName())
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

	emitted := telemetry.SpanEvents(record)
	for index := 1; index < len(emitted); index++ {
		if !emitted[index].Time.After(emitted[index-1].Time) {
			t.Errorf("event %d (%s) at %v is not after event %d (%s) at %v",
				index, emitted[index].Name, emitted[index].Time,
				index-1, emitted[index-1].Name, emitted[index-1].Time)
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
	if emitted := telemetry.SpanEvents(nil); len(emitted) != 0 {
		t.Errorf("SpanEvents(nil) = %v, want none", names(emitted))
	}
}
