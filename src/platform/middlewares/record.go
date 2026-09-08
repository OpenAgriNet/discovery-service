package middlewares

import (
	"context"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// recordFor adopts the request's fact record, or allocates one when this is the
// first link in the chain that needs it.
//
// This replaces middlewares.correlation, which held []zap.Field and was
// allocated by RequestLogger alone. The generalisation is not gratuitous: the
// status is knowable only from inside RequestLogger's responseRecorder, the
// trace and span ids only from Trace above it, and both the span and the
// completion line need both. So the record carries traffic in both directions,
// and there has to be exactly one of it per request.
//
// Adopt-or-allocate rather than allocate-in-Trace, and it matters from both
// sides:
//
//   - Trace runs first in the assembled chain (router.go:134-141), so it is
//     normally the one that allocates. Its adopt branch is what stops it
//     SHADOWING a record something above it allocated — a second record would
//     hold the same facts, because Envelope writes whichever is nearer, so every
//     assertion about the log line would still pass while the span quietly read
//     the other one.
//   - RequestLogger is mounted with no Trace above it by most of its own tests
//     (request_logger_test.go:167, 204) and by anything wanting a completion line
//     without a chain. Adopt-only would leave those with a nil record and a
//     completion line carrying neither status nor duration — and it would read as
//     the refactor having worked.
//
// Neither link asks about configuration. A record whose lifetime depended on
// OTEL_EXPORTER would make this invariant untestable in the configuration
// `make test` runs in, and would fail only where nobody is looking.
func recordFor(ctx context.Context) (context.Context, *fact.Record) {
	if record := fact.From(ctx); record != nil {
		return ctx, record
	}
	return fact.New(ctx)
}
