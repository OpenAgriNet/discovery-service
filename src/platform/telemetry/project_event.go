package telemetry

import (
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// SpanEvent is one projected event: what happened, when, and the shape of it.
//
// It is data rather than a call against a span, for the same reason
// SpanAttributes returns a slice — the caller decides when, and the answer is
// once, from Trace's deferred block. It is also what lets this be tested without
// an exporter.
type SpanEvent struct {
	Name       string
	Time       time.Time
	Attributes []attribute.KeyValue
}

// SpanEvents projects the record's point-in-time facts onto span events.
//
// The rule the registry encodes (opentelemetry.md:641): true for the whole
// request → span attribute; produced at a point during processing → event. This
// is the second half of that partition, and the two functions never emit the
// same key — SpanAttributes skips every row with an Event set and this one skips
// every row without.
//
// An event whose facts were never observed is not emitted. A discover that never
// reached the store has no retrieval_info, and an empty event stamped at the
// span's end would read as a phase that ran and produced nothing — a different
// and much more alarming claim than a phase that did not run.
//
// A nil record projects nothing, and unlike the span half there is no
// absent-flag pass to run over it: an event that did not happen is reported by
// its absence, which is what a reader of a trace already expects.
func SpanEvents(record *fact.Record) []SpanEvent {
	building := make(map[fact.Event]*SpanEvent)

	for observation := range record.All() {
		def := fact.Of(observation.Key)
		if def.Signals&fact.Span == 0 || def.Event == fact.NoEvent {
			continue
		}

		event := building[def.Event]
		if event == nil {
			event = &SpanEvent{Name: def.Event.EventName(), Time: observation.Time}
			building[def.Event] = event
		}

		// The earliest of its facts, not the latest. A phase happens over an
		// interval however instantaneous it looks — response_info is three
		// separate writes — and anchoring at the last of them slides every event
		// toward the span's end by however long its own work took, which is
		// exactly the interval the deltas between events are meant to measure.
		//
		// Earliest of what it CURRENTLY holds, which for a corrected fact is the
		// correction: the event reports the values that went out, so the moment
		// those became true is the moment it happened.
		if observation.Time.Before(event.Time) {
			event.Time = observation.Time
		}

		event.Attributes = append(event.Attributes, attribute.KeyValue{
			Key:   attribute.Key(def.SpanKey),
			Value: spanValue(def.Kind, observation),
		})

		// Clamped at the record, reported here, exactly as on the span. A
		// result.provider_ids cut to its bound and not saying so reads as a query
		// answered by precisely sixteen providers.
		if observation.Truncated && def.TruncationFlag != "" {
			event.Attributes = append(event.Attributes,
				attribute.Bool(def.TruncationFlag, true))
		}
	}

	return ordered(building)
}

// ordered flattens the events into the order they happened in.
//
// Sorted rather than emitted in record order, and the case that needs it is not
// hypothetical: the error event is written from logNack, which on a
// partially-written response runs after the result facts were observed. Record
// order there would put the failure before the success it interrupted.
//
// Map iteration order is randomised in Go, so this sort is also what makes the
// output deterministic at all.
func ordered(building map[fact.Event]*SpanEvent) []SpanEvent {
	events := make([]SpanEvent, 0, len(building))
	for _, event := range building {
		events = append(events, *event)
	}
	slices.SortFunc(events, func(a, b SpanEvent) int { return a.Time.Compare(b.Time) })
	return events
}
