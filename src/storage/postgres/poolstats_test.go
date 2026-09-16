package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres"
	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// TestAcquireWaitIsVisibleUnderAFullPool is the acceptance criterion
// opentelemetry.md's Task 25 names: a pool deliberately sized to 1, a second
// concurrent caller that must therefore wait, and both counters moving.
//
// Sized to 1 rather than to the production 32 because the number under test is
// *saturation*, and a test that had to generate 32 concurrent callers to
// observe it would be timing-dependent on a loaded CI box. One connection makes
// the contention deterministic.
//
// The point of the assertion is not that pgxpool counts correctly — it does —
// but that these two numbers are reachable through a seam that keeps pgx on this
// side of it and the OpenTelemetry SDK on the other.
func TestAcquireWaitIsVisibleUnderAFullPool(t *testing.T) {
	dsn := dbtest.DSN(t)
	ctx := context.Background()

	pool, err := postgres.NewPool(ctx, config.Database{URL: dsn, MaxConns: 1, MinConns: 1})
	if err != nil {
		t.Fatalf("open a pool sized to 1: %v", err)
	}
	defer pool.Close()

	stats := postgres.NewPoolStats(pool)

	if got := stats.EmptyAcquireCount(); got != 0 {
		t.Fatalf("EmptyAcquireCount is %d on a fresh pool, want 0", got)
	}

	// Hold the only connection, then have a second caller ask for one. The
	// second acquire cannot be served until the first is released, which is
	// exactly what EmptyAcquireCount counts.
	held, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire the only connection: %v", err)
	}

	var waiter sync.WaitGroup
	waiter.Add(1)
	waited := make(chan error, 1)
	go func() {
		defer waiter.Done()
		conn, acquireErr := pool.Acquire(ctx)
		if acquireErr != nil {
			waited <- acquireErr
			return
		}
		conn.Release()
		waited <- nil
	}()

	// Give the goroutine time to block on an empty pool before releasing. A
	// release that happened first would be served immediately and counted as an
	// ordinary acquire, which is the way this test goes green while measuring
	// nothing.
	time.Sleep(50 * time.Millisecond)
	held.Release()

	waiter.Wait()
	if waitErr := <-waited; waitErr != nil {
		t.Fatalf("the second caller never got a connection: %v", waitErr)
	}

	if got := stats.EmptyAcquireCount(); got < 1 {
		t.Errorf("EmptyAcquireCount is %d after a contended acquire, want at "+
			"least 1. This is the number no layer below this process can see: "+
			"cAdvisor sees the container's resource envelope and Postgres never "+
			"sees a statement that was not sent", got)
	}
	if got := stats.EmptyAcquireWaitTime(); got <= 0 {
		t.Errorf("EmptyAcquireWaitTime is %v after a contended acquire, want "+
			"more than zero. The pair is only useful divided — this is the "+
			"numerator of mean wait per acquire", got)
	}
}

// TestThePoolNamesItselfToPostgres pins the one line that hands pool
// utilisation to pg_stat_activity instead of to an instrument here.
//
// opentelemetry.md struck the utilisation gauge on the grounds that
// `pg_stat_activity` grouped by application_name already answers it — which was
// true of Postgres and false of this service, because nothing set the name. An
// unset application_name shows as the empty string, and every pod's connections
// pool into one indistinguishable group.
func TestThePoolNamesItselfToPostgres(t *testing.T) {
	dsn := dbtest.DSN(t)
	ctx := context.Background()

	pool, err := postgres.NewPool(ctx, config.Database{URL: dsn, MaxConns: 2, MinConns: 1})
	if err != nil {
		t.Fatalf("open a pool: %v", err)
	}
	defer pool.Close()

	var name string
	if queryErr := pool.QueryRow(ctx, "SELECT current_setting('application_name')").Scan(&name); queryErr != nil {
		t.Fatalf("read application_name: %v", queryErr)
	}

	if name != "discovery-service" {
		t.Errorf("application_name is %q, want %q. It is what makes "+
			"pg_stat_activity group by service, and it is the whole reason "+
			"Task 25 emits no utilisation gauge", name, "discovery-service")
	}
}
