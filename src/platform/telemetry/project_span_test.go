package telemetry_test

import (
	"context"
	"slices"
	"strings"
	"testing"

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
// exact failure telemetry-seam.md exists to make impossible.
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
// onix writes http.status.code as a STRING; the semantic conventions say
// http.status_code as an int. A collector rule keying on either has to find it,
// so both go out — from one observation, so they cannot drift the way two
// separate observations of "the status" would.
func TestOneObservationBecomesBothStatusSpellings(t *testing.T) {
	attributes := projected(t, func(record *fact.Record) {
		record.ObserveInt64(fact.HTTPStatusCode, 404)
	})

	def := fact.Of(fact.HTTPStatusCode)
	if got := attributes[def.SpanKey]; got.AsInt64() != 404 {
		t.Errorf("%s = %v, want the int 404", def.SpanKey, got.AsInterface())
	}

	if len(def.SpanAliases) != 1 {
		t.Fatalf("HTTPStatusCode carries %d aliases, want the one onix spelling", len(def.SpanAliases))
	}
	alias := def.SpanAliases[0]
	got, present := attributes[alias.Key]
	if !present {
		t.Fatalf("no %s on the span; a collector rule keyed on onix's spelling finds nothing", alias.Key)
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
// /healthz runs with no record at all (router.go:158-163) and 23e's Trace still
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
