package middlewares

import (
	"context"

	"go.uber.org/zap"
)

// correlation carries the envelope's correlators back *up* the chain.
//
// Everything else a middleware learns travels down, by deriving a context the
// next handler is called with, and that is the right direction for all of it
// but this. RequestLogger writes the one line per request that says how the
// request ended, and it sits above Envelope, which is what learns the
// transaction the request belongs to. A derived context cannot reach a
// middleware that has already run, so a request's correlators would reach every
// line about it except the one an operator starts from.
//
// So RequestLogger allocates one of these per request, puts a pointer to it in
// the context, and reads it back after the handler returns; Envelope fills it in
// passing. There is no synchronisation because there is no concurrency to
// synchronise: the write happens in Envelope, the read after everything below
// Envelope has returned, both on the goroutine net/http gave the request. A
// handler that spawns goroutines must not touch this, and none can — the type
// and its key are unexported.
//
// A middleware mounted without RequestLogger above it finds nothing in the
// context and records nothing, which is why record is written to tolerate a nil
// receiver rather than making every caller ask first.
type correlation struct{ fields []zap.Field }

// An unexported key type, so no other package's context value can collide.
type correlationKey struct{}

// newCorrelation returns a context carrying a fresh correlation, and the
// correlation itself so the caller can read it back without a second lookup.
func newCorrelation(ctx context.Context) (context.Context, *correlation) {
	recorded := &correlation{}
	return context.WithValue(ctx, correlationKey{}, recorded), recorded
}

// correlationFrom returns the correlation to fill, or nil when nothing above
// this middleware is collecting one.
func correlationFrom(ctx context.Context) *correlation {
	if recorded, ok := ctx.Value(correlationKey{}).(*correlation); ok {
		return recorded
	}
	return nil
}

// record keeps fields for whatever logs above. Safe on a nil receiver.
func (c *correlation) record(fields ...zap.Field) {
	if c == nil {
		return
	}
	c.fields = fields
}

// recorded returns what was filled in below, or nothing.
func (c *correlation) recorded() []zap.Field {
	if c == nil {
		return nil
	}
	return c.fields
}
