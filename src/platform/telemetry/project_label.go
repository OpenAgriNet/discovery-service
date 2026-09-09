package telemetry

import (
	"go.opentelemetry.io/otel/attribute"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// labelSet projects an instrument's declared dimensions into the attribute set
// its data points carry.
//
// It exists so that a metric label is never spelled at a call site. The
// dimension's name comes from the Definition's MetricKey and from nowhere else,
// which is what makes a rename one edit — and MetricKey is a separate field from
// SpanKey precisely because the two legitimately differ: the span says
// beckn.action, the label says action.
//
// Today every instrument in the table names zero labels, so this returns nil on
// the only path that calls it, and that is the honest state rather than an
// oversight: pgxpool.Stat() is per-pool and there is one pool, so every
// candidate dimension for the acquire-wait pair would be a constant, and a label
// with one value is a series multiplier of one and a column of noise. The
// function is here rather than deferred to Task 24 because the registration path
// needs an attribute set to pass, and deriving that set from the table is the
// difference between "no labels" being a decision and being an omission.
//
// A key that reaches here without the Label bit is skipped rather than spelled.
// ValidateInstrument already rejects it at test time; this is what stops the
// same mistake shipping an attribute whose name is the empty string, which every
// backend accepts and no query finds.
func labelSet(instrument fact.Instrument, record *fact.Record) []attribute.KeyValue {
	if len(instrument.Labels) == 0 {
		return nil
	}

	labels := make([]attribute.KeyValue, 0, len(instrument.Labels))
	for _, key := range instrument.Labels {
		def := fact.Of(key)
		if def.Signals&fact.Label == 0 || def.MetricKey == "" {
			continue
		}

		observation, ok := record.Lookup(key)
		if !ok {
			// An unobserved dimension is left off rather than defaulted. A label
			// silently set to "" merges two different states into one series,
			// and absent is a state a dashboard can see.
			continue
		}

		labels = append(labels, attribute.KeyValue{
			Key:   attribute.Key(def.MetricKey),
			Value: spanValue(def.Kind, observation),
		})
	}
	return labels
}
