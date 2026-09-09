package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// PoolStatsSource is the two acquire-wait totals.
//
// Declared here rather than imported because tests/architecture keeps pgx inside
// src/storage/postgres and the OTel SDK inside this package, so neither side may
// name the other's types. postgres.PoolStats satisfies this without knowing it
// exists.
type PoolStatsSource interface {
	// EmptyAcquireCount is acquires that had to wait on an empty pool.
	EmptyAcquireCount() int64
	// EmptyAcquireWaitTime is the cumulative time spent in those waits.
	EmptyAcquireWaitTime() time.Duration
}

// RegisterPoolStats registers Task 25's whole deliverable: the acquire-wait
// pair, as two observable counters read off the source on each collection.
//
// Observable rather than synchronous, and two counters rather than a histogram,
// are both decided at opentelemetry.md:1253-1267.
//
// ONE callback for both, so the two numbers come from the same pgxpool.Stat()
// moment. Registered separately they could straddle a release, and a wait total
// belonging to a different instant than its count divides into a mean wait that
// never happened.
func RegisterPoolStats(provider metric.MeterProvider, stats PoolStatsSource) error {
	if provider == nil || stats == nil {
		return nil
	}

	meter := provider.Meter(ScopeName, metric.WithInstrumentationVersion(ScopeVersion))

	waits := fact.OfInstrument(fact.PoolAcquireWaits)
	spent := fact.OfInstrument(fact.PoolAcquireWaitNanos)

	waitCounter, err := meter.Int64ObservableCounter(
		waits.Name,
		metric.WithUnit(waits.Unit),
		metric.WithDescription(waits.Description),
	)
	if err != nil {
		return fmt.Errorf("create the %s counter: %w", waits.Name, err)
	}

	spentCounter, err := meter.Int64ObservableCounter(
		spent.Name,
		metric.WithUnit(spent.Unit),
		metric.WithDescription(spent.Description),
	)
	if err != nil {
		return fmt.Errorf("create the %s counter: %w", spent.Name, err)
	}

	waitLabels := metric.WithAttributes(labelSet(waits, nil)...)
	spentLabels := metric.WithAttributes(labelSet(spent, nil)...)

	_, err = meter.RegisterCallback(
		func(_ context.Context, observer metric.Observer) error {
			observer.ObserveInt64(waitCounter, stats.EmptyAcquireCount(), waitLabels)
			observer.ObserveInt64(spentCounter, stats.EmptyAcquireWaitTime().Nanoseconds(), spentLabels)
			return nil
		},
		waitCounter,
		spentCounter,
	)
	if err != nil {
		return fmt.Errorf("register the acquire-wait callback: %w", err)
	}
	return nil
}

// labelSet projects an instrument's declared dimensions into the attribute set
// its data points carry.
//
// A label name comes from the Definition's MetricKey and nowhere else, so a
// rename is one edit in the table (telemetry-seam.md:237). Every instrument
// names zero labels today, so this returns nil on the only path that calls it —
// which is a decision the table records rather than a call site that forgot.
//
// A key without the Label bit is skipped rather than spelled: ValidateInstrument
// already rejects it at test time, and this stops the same mistake shipping an
// attribute named the empty string, which every backend accepts and no query
// finds.
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
			// Left off rather than defaulted: a label silently set to "" merges
			// two states into one series, and absent is one a dashboard sees.
			continue
		}

		labels = append(labels, attribute.KeyValue{
			Key:   attribute.Key(def.MetricKey),
			Value: spanValue(def.Kind, observation),
		})
	}
	return labels
}
