package middlewares

import (
	"context"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// recordFor adopts the request's fact record, or allocates one when this is the
// first link in the chain that needs it.
//
// Both branches are load-bearing, from both sides (telemetry-seam.md:663):
// adopting is what stops Trace shadowing a record allocated above it, and
// allocating is what gives RequestLogger a record when it is mounted with no
// Trace above it. Neither link asks about configuration.
func recordFor(ctx context.Context) (context.Context, *fact.Record) {
	if record := fact.From(ctx); record != nil {
		return ctx, record
	}
	return fact.New(ctx)
}
