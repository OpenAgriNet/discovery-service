package postgres

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolStats reports the two acquire-wait totals (Task 25, OP3), and it is the
// seam between pgx and the OpenTelemetry SDK: the import guard bans pgx outside
// this package and the SDK outside src/platform/telemetry, so the number crosses
// on a signature built out of neither. telemetry declares the two methods as an
// interface, this satisfies them, and only the composition root knows both.
//
// Deliberately NOT the whole *pgxpool.Stat: returning it would put nine numbers
// the layers below already report one dot away from an instrument nobody
// reviewed.
type PoolStats struct {
	pool *pgxpool.Pool
}

// NewPoolStats wraps a pool.
func NewPoolStats(pool *pgxpool.Pool) PoolStats {
	return PoolStats{pool: pool}
}

// EmptyAcquireCount is acquires that had to wait because the pool was empty —
// the saturation number no layer outside this process can produce.
//
// Nil-tolerant: the composition root can register metrics with no pool open, and
// a metrics callback must not panic a healthy process.
func (s PoolStats) EmptyAcquireCount() int64 {
	if s.pool == nil {
		return 0
	}
	return s.pool.Stat().EmptyAcquireCount()
}

// EmptyAcquireWaitTime is the cumulative time spent in those waits. Cumulative
// and not a distribution because pgxpool exposes only totals; the consumer
// divides this rate by EmptyAcquireCount's for mean wait per acquire.
func (s PoolStats) EmptyAcquireWaitTime() time.Duration {
	if s.pool == nil {
		return 0
	}
	return s.pool.Stat().EmptyAcquireWaitTime()
}
