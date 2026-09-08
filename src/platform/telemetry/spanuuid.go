package telemetry

import (
	"context"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// spanUUID stamps the spec's Required `span_uuid` on every span the service
// starts.
//
// A SpanProcessor rather than a line in the Trace middleware, for two reasons.
// It cannot be forgotten: any span from any tracer this provider hands out gets
// one, including whatever 23d adds and whatever a later task instruments. And
// it belongs to the provider, so the middleware stays a projection of a
// fact.Record and acquires no identity-minting of its own.
//
// It is not a fact.Key observation like everything else on the span, and that
// is deliberate: a Record is per request and this is per span, so recording it
// as a fact would put one value on a request that may hold more than one span.
// The key spelling still comes from the registry — the seam is about where the
// name lives, not about which mechanism writes it.
type spanUUID struct{}

// OnStart is where the attribute has to be set. OnEnd receives a
// ReadOnlySpan, which cannot take one — the same constraint that puts
// observedTimeUnixNano in the middleware just before End rather than here.
func (spanUUID) OnStart(_ context.Context, span sdktrace.ReadWriteSpan) {
	span.SetAttributes(attribute.String(keyOf(fact.SpanUUID), uuid.NewString()))
}

// OnEnd does nothing. This processor is not in the export path; the batcher is.
func (spanUUID) OnEnd(sdktrace.ReadOnlySpan) {}

// Shutdown has nothing to release. It holds no buffer, no connection and no
// goroutine — every span it touches is finished with by the time OnStart
// returns.
func (spanUUID) Shutdown(context.Context) error { return nil }

// ForceFlush has nothing to flush, for the same reason.
func (spanUUID) ForceFlush(context.Context) error { return nil }
