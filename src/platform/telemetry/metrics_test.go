package telemetry_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry"
)

// stubPoolStats is the acquire-wait source, held at values the assertions can
// name. The real one is postgres.PoolStats; this test is about the registration,
// and src/storage/postgres has its own test for the numbers being real.
type stubPoolStats struct {
	waits int64
	spent time.Duration
}

func (s stubPoolStats) EmptyAcquireCount() int64            { return s.waits }
func (s stubPoolStats) EmptyAcquireWaitTime() time.Duration { return s.spent }

// TestRegisterPoolStatsEmitsTheAcquireWaitPair is the shape of Task 25's whole
// deliverable: two observable counters, read off the source on collection, with
// the names and units the fact table declares.
//
// Read through a ManualReader rather than a real exporter so the assertion is
// on what was registered, not on whether a collector happened to be up.
func TestRegisterPoolStatsEmitsTheAcquireWaitPair(t *testing.T) {
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shut down the meter provider: %v", err)
		}
	})

	stats := stubPoolStats{waits: 7, spent: 1500 * time.Millisecond}
	if err := telemetry.RegisterPoolStats(provider, stats); err != nil {
		t.Fatalf("register the pool stats instruments: %v", err)
	}

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("collect: %v", err)
	}

	if len(collected.ScopeMetrics) != 1 {
		t.Fatalf("collected %d scopes, want 1 — everything this service emits "+
			"shares one instrumentation scope", len(collected.ScopeMetrics))
	}
	scope := collected.ScopeMetrics[0]

	if scope.Scope.Name != telemetry.ScopeName {
		t.Errorf("scope.name is %q, want %q", scope.Scope.Name, telemetry.ScopeName)
	}
	if scope.Scope.Version != telemetry.ScopeVersion {
		t.Errorf("scope.version is %q, want %q. It is the network telemetry "+
			"specification's version, not a library's — which is the reason a "+
			"vendored instrumentation library cannot supply this scope",
			scope.Scope.Version, telemetry.ScopeVersion)
	}

	var names []string
	for _, m := range scope.Metrics {
		names = append(names, m.Name)
	}
	slices.Sort(names)
	want := []string{"pgxpool.empty_acquire", "pgxpool.empty_acquire_wait_time"}
	if !slices.Equal(names, want) {
		t.Fatalf("registered %v, want exactly %v", names, want)
	}

	for _, m := range scope.Metrics {
		sum, ok := m.Data.(metricdata.Sum[int64])
		if !ok {
			t.Errorf("%s is %T, want a Sum[int64]: pgxpool exposes only "+
				"cumulative totals, so a histogram here would mean wrapping "+
				"every Acquire on the request path", m.Name, m.Data)
			continue
		}
		if !sum.IsMonotonic {
			t.Errorf("%s is not monotonic; both halves are running totals", m.Name)
		}
		if sum.Temporality != metricdata.CumulativeTemporality {
			t.Errorf("%s has temporality %v, want cumulative", m.Name, sum.Temporality)
		}
		if len(sum.DataPoints) != 1 {
			t.Errorf("%s has %d data points, want 1. The pair names no labels: "+
				"pgxpool.Stat() is per-pool and there is one pool, so every "+
				"candidate dimension would be a constant", m.Name, len(sum.DataPoints))
			continue
		}

		point := sum.DataPoints[0]
		if point.Attributes.Len() != 0 {
			t.Errorf("%s carries %d attributes, want none", m.Name, point.Attributes.Len())
		}

		switch m.Name {
		case "pgxpool.empty_acquire":
			if m.Unit != "1" {
				t.Errorf("%s has unit %q, want %q", m.Name, m.Unit, "1")
			}
			if point.Value != 7 {
				t.Errorf("%s is %d, want 7 off the source", m.Name, point.Value)
			}
		case "pgxpool.empty_acquire_wait_time":
			if m.Unit != "ns" {
				t.Errorf("%s has unit %q, want %q — a nanosecond total read as "+
					"milliseconds is off by a million and still plausible",
					m.Name, m.Unit, "ns")
			}
			if point.Value != int64(1500*time.Millisecond) {
				t.Errorf("%s is %d, want %d nanoseconds off the source",
					m.Name, point.Value, int64(1500*time.Millisecond))
			}
		}
	}
}

// TestRegisterPoolStatsReReadsTheSourceOnEachCollection pins that these are
// observable counters rather than values captured once.
//
// A registration that snapshotted at boot would report a flat line forever,
// which is indistinguishable on a dashboard from a pool that never waits — the
// exact reading this instrument exists to contradict.
func TestRegisterPoolStatsReReadsTheSourceOnEachCollection(t *testing.T) {
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shut down the meter provider: %v", err)
		}
	})

	moving := &movingPoolStats{}
	if err := telemetry.RegisterPoolStats(provider, moving); err != nil {
		t.Fatalf("register: %v", err)
	}

	first := collectAcquireWaits(t, reader)
	moving.waits = 42
	second := collectAcquireWaits(t, reader)

	if first != 0 {
		t.Errorf("first collection is %d, want 0", first)
	}
	if second != 42 {
		t.Errorf("second collection is %d, want 42; the callback captured a "+
			"value instead of reading the source", second)
	}
}

type movingPoolStats struct{ waits int64 }

func (m *movingPoolStats) EmptyAcquireCount() int64            { return m.waits }
func (m *movingPoolStats) EmptyAcquireWaitTime() time.Duration { return 0 }

func collectAcquireWaits(t *testing.T, reader *metric.ManualReader) int64 {
	t.Helper()

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "pgxpool.empty_acquire" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("pgxpool.empty_acquire is %T, want a Sum[int64]", m.Data)
			}
			return sum.DataPoints[0].Value
		}
	}
	t.Fatal("pgxpool.empty_acquire was not collected")
	return 0
}

// TestInitRegistersNoGlobalMeterProvider is the metrics half of the property
// A23 and the import guard exist for.
//
// A global would let any package reach the SDK through otel.Meter() and declare
// an instrument nobody reviewed against the layer table — which is the one gate
// Task 25 has, since "which layer below us is blind to this?" is a question
// asked in review and by nothing at compile time.
func TestInitRegistersNoGlobalMeterProvider(t *testing.T) {
	before := otel.GetMeterProvider()

	provider, err := telemetry.Init(context.Background(), config.Config{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := provider.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("Shutdown: %v", shutdownErr)
		}
	})

	if otel.GetMeterProvider() != before {
		t.Error("Init replaced the global meter provider. The provider travels " +
			"as a value to the composition root, which is what keeps the SDK " +
			"behind a seam rather than merely behind an import")
	}
}

// TestMeterProviderIsNilTolerant matches Tracer's contract, and for the same
// population: router_test.go builds an App by hand and the acceptance suite
// calls controllers with no provider at all.
func TestMeterProviderIsNilTolerant(t *testing.T) {
	var absent *telemetry.Provider

	if got := absent.MeterProvider(); got == nil {
		t.Fatal("MeterProvider() on a nil Provider returned nil; a metrics " +
			"registration must not be the thing that panics a healthy boot")
	}

	// Registering against it must also be safe, and must emit nothing.
	if err := telemetry.RegisterPoolStats(absent.MeterProvider(), stubPoolStats{waits: 1}); err != nil {
		t.Errorf("registering against a nil Provider's meter: %v", err)
	}
}
