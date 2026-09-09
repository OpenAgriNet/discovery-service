package telemetry

import (
	"context"
	"fmt"
	"time"

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
// The attribute sets come from the table via labelSet, which returns nil today
// because neither row declares a label. Passing nil through the projection
// rather than omitting the option is what makes "no labels" a decision the
// table records rather than a call site that forgot.
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
