// Package logger builds the service's JSON logger and carries a request-scoped
// one in the context, so a function far below the middleware chain logs the
// request's identifiers without every caller in between having to pass them.
package logger

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

// contextKey types the context value so nothing outside this package can name
// it — a string key would let any package replace the logger a middleware
// installed, or collide with it by accident.
type contextKey struct{}

// New builds the production JSON logger at the configured level.
//
// It takes config.Log rather than the whole Config for two reasons. The
// signature then says what the logger reads, which is the level and nothing
// else; and Config.Database.URL carries a password, so handing the logger the
// whole struct would put a secret inside the one component whose job is to
// write things down.
func New(cfg config.Log) (*zap.Logger, error) {
	built, err := zapConfig(cfg)
	if err != nil {
		return nil, err
	}

	log, err := built.Build()
	if err != nil {
		return nil, fmt.Errorf("build logger at level %q: %w", cfg.Level, err)
	}

	return log, nil
}

// zapConfig resolves the level and the encoder. Split out from New so a test
// can assert on the shape rather than on stderr.
func zapConfig(cfg config.Log) (zap.Config, error) {
	level, err := zap.ParseAtomicLevel(cfg.Level)
	if err != nil {
		return zap.Config{}, fmt.Errorf("parse log level %q: %w", cfg.Level, err)
	}

	built := zap.NewProductionConfig()
	built.Level = level

	// Production sampling keys on level and message only, and drops all but
	// the first hundred entries sharing a pair within a second. Every request
	// completion line shares one message and differs only in its fields, so
	// sampling would discard most of the request log at exactly the load worth
	// reading it at.
	built.Sampling = nil

	return built, nil
}

// NewContext returns a context carrying log. The middleware chain installs the
// service logger once per request; everything below reads it with FromContext.
func NewContext(ctx context.Context, log *zap.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, log)
}

// FromContext returns the request-scoped logger, or a no-op logger when the
// context carries none. Never nil: a call site that logs must not have to ask
// whether the middleware that installs one ran.
func FromContext(ctx context.Context) *zap.Logger {
	if log, ok := ctx.Value(contextKey{}).(*zap.Logger); ok && log != nil {
		return log
	}

	return zap.NewNop()
}

// With returns a context whose logger also carries fields. Fields accumulate
// down the chain rather than replacing what is already there, and the parent
// context is left alone, so one request cannot inherit a sibling's fields.
//
// On a context with no logger the fields land on the no-op logger, which is
// the same silence as logging without one.
func With(ctx context.Context, fields ...zap.Field) context.Context {
	if len(fields) == 0 {
		return ctx
	}

	return NewContext(ctx, FromContext(ctx).With(fields...))
}

// The fields a request-scoped logger is pre-populated with, and the two the
// response writer adds when it writes a fault. They are spelled here and
// nowhere else: one key spelled two ways is two fields to whatever queries the
// logs, and the mistake stays invisible until someone searches for the
// spelling that is missing.

// RequestID names this service's own per-request identifier.
func RequestID(id string) zap.Field { return zap.String("request_id", id) }

// TraceID names the trace this request's span belongs to, so an operator
// holding a span reaches the logs.
//
// It is the SPAN's id, not a second one minted here — Trace reads it off the
// span context it just created. The direction matters: request_id has no span
// attribute precisely because this pair exists, and shipping an internal handle
// to the facilitator to solve a problem the log line already solves would be
// the same join paid for twice (fact.RequestID's Note).
//
// Set on the request-scoped logger rather than appended to one line, so every
// line the request writes carries it — including httpx.WriteNack's, which is
// the one an operator following a failed span most wants.
func TraceID(id string) zap.Field { return zap.String("trace_id", id) }

// SpanID names this service's own span within that trace. trace_id narrows the
// logs to the exchange; this narrows them to our part of it.
func SpanID(id string) zap.Field { return zap.String("span_id", id) }

// TransactionID names the Beckn transaction the request belongs to, which spans
// every hop of the exchange.
func TransactionID(id string) zap.Field { return zap.String("transaction_id", id) }

// MessageID names the single Beckn message, which is one hop of that exchange.
func MessageID(id string) zap.Field { return zap.String("message_id", id) }

// Action names the Beckn action from the envelope's context.
func Action(action string) zap.Field { return zap.String("action", action) }

// ErrorType names the PRD error category (C1). The category was dropped from
// the v2.0.0 body, so it reaches the caller as the X-Beckn-Error-Type header
// and the operator as this field — both, because a category that exists only
// on the wire is one nothing can be aggregated over.
func ErrorType(category string) zap.Field { return zap.String("error_type", category) }

// ErrorCode names the Beckn code that went out with the fault, so a log line
// and the body the caller received can be reconciled without a timestamp
// search.
func ErrorCode(code string) zap.Field { return zap.String("error_code", code) }

// Status names the HTTP status the response went out with.
func Status(code int) zap.Field { return zap.Int("status", code) }

// DurationMS names how long the request took, in milliseconds. The unit is in
// the name because a bare `duration` is a number two dashboards will read as
// two different quantities, and zap's own duration encoder writes seconds.
func DurationMS(elapsed time.Duration) zap.Field {
	return zap.Float64("duration_ms", Millis(elapsed))
}

// Millis is the number DurationMS carries, exported because it is now written
// in three places that must agree: this field, the X-Response-Time header, and
// the fact record the projection reads. Milliseconds to microsecond precision —
// integer milliseconds would report every request this service is built to
// serve, the 20 ms budget, as one of twenty indistinguishable values, and a
// sub-millisecond one as zero.
func Millis(elapsed time.Duration) float64 {
	return float64(elapsed.Microseconds()) / 1000
}
