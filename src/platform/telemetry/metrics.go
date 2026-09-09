package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// PoolStatsSource is the two acquire-wait totals, and it is declared here rather
// than imported because of the two import bans that meet at this number.
//
// The guard in tests/architecture keeps pgx inside src/storage/postgres and the
// OpenTelemetry SDK inside this package, so neither side may name the other's
// types. postgres.PoolStats satisfies this without knowing it exists, which is
// what makes the seam structural rather than a comment asking people to be
// careful.
type PoolStatsSource interface {
	// EmptyAcquireCount is acquires that had to wait on an empty pool.
	EmptyAcquireCount() int64
	// EmptyAcquireWaitTime is the cumulative time spent in those waits.
	EmptyAcquireWaitTime() time.Duration
}

// RegisterPoolStats registers Task 25's whole deliverable: the acquire-wait pair,
// as two observable counters read off the source on each collection.
//
// Observable rather than synchronous, which is the reason this costs the request
// path nothing: pgxpool maintains these totals whether or not anyone reads them,
// so the instrument is a sampling callback over an existing struct rather than
// new bookkeeping. A synchronous counter would mean incrementing inside Acquire.
//
// Two counters rather than one histogram, and that is a constraint rather than a
// preference — pgxpool exposes only cumulative totals, so a real distribution
// would mean wrapping every Acquire on the hot path. The consumer divides one
// rate by the other for mean wait per acquire, which answers the saturation
// question without touching the request path at all.
//
// Both instrument definitions come out of fact.instruments rather than being
// spelled here, so a rename is one edit in the table and this function has no
// string literals to disagree with it.
//
// One callback for both, so the two numbers are read from the same
// pgxpool.Stat() moment. Registered separately they could straddle a release,
// and a wait total that belongs to a different instant than its count divides
// into a mean wait that never happened.
//
// The attribute sets come from the table via labelSet below, which returns nil
// today. Passing nil through the projection rather than omitting the option is
// what makes "no labels" a decision the table records rather than a call site
// that forgot.
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
// function is here rather than deferred because the registration path needs an
// attribute set to pass, and deriving that set from the table is the difference
// between "no labels" being a decision and being an omission.
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
