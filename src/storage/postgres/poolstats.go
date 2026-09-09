package postgres

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolStats reports the two acquire-wait totals, and it exists to be the seam
// between pgx and the OpenTelemetry SDK.
//
// The import guard in tests/architecture bans pgx outside this package and bans
// the SDK outside src/platform/telemetry, so neither side can read the other's
// types and the number has to cross on a signature built out of neither. This
// type is that signature: telemetry declares the two methods as an interface it
// needs, this satisfies them, and the composition root is the only place that
// knows both names.
//
// Deliberately not the whole *pgxpool.Stat. Nine of its numbers are things the
// layers below already report — total, idle, max and acquired connections are
// pg_stat_activity's once the pool names itself — and returning the struct would
// put every one of them one dot away from an instrument nobody reviewed.
type PoolStats struct {
	pool *pgxpool.Pool
}

// NewPoolStats wraps a pool. A value rather than a pointer: it holds one
// pointer and is read from a metrics callback on a timer, so copying it is
// cheaper than dereferencing it and there is nothing to mutate.
func NewPoolStats(pool *pgxpool.Pool) PoolStats {
	return PoolStats{pool: pool}
}

// EmptyAcquireCount is acquires that had to wait because the pool was empty.
//
// The saturation number, and the only one in this service no other layer can
// produce: the wait happens inside this process, before any syscall, so
// cAdvisor sees only the container's resource envelope and Postgres never sees
// a statement that was not sent. It rises before anything fails.
//
// Nil-tolerant, because the composition root registers metrics on a path that
// can be reached with no pool open, and a metrics callback must not be the
// thing that panics a healthy process.
func (s PoolStats) EmptyAcquireCount() int64 {
	if s.pool == nil {
		return 0
	}
	return s.pool.Stat().EmptyAcquireCount()
}

// EmptyAcquireWaitTime is the cumulative time spent in those waits.
//
// Cumulative rather than a distribution because pgxpool exposes only totals; a
// real histogram would mean wrapping every Acquire on the request path. The
// consumer divides this rate by EmptyAcquireCount's for mean wait per acquire.
func (s PoolStats) EmptyAcquireWaitTime() time.Duration {
	if s.pool == nil {
		return 0
	}
	return s.pool.Stat().EmptyAcquireWaitTime()
}
