package telemetry

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/log"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// Audit events: the spec's LOG signal, whose eid is AUDIT.
//
// The signal carries "updates and state changes of entities within the network"
// (otel-specification.md:589). The entity this service transitions is the
// catalog: publish creates one, and the publisher's `isActive` takes it in and
// out of the discoverable set.

// EmitAudit records one entity state change as the spec's LOG signal.
//
// It takes a fact.AuditEvent — the same value src/publish produced and the
// Record carried — rather than four strings, so there is no seam here at which
// the fields could be reordered. The states are already resolved names, not
// booleans, because the record carries them verbatim: a consumer reading
// item.prevstate=absent, item.state=active learns the catalog was created, and
// no other record in this service says so.
//
// It emits through the audit logger's own provider, whose Resource carries
// eid=AUDIT — the one Resource attribute that differs from the API and METRIC
// projections (otel-specification.md:599).
//
// Nil-tolerant like every other Provider method: a service booted without
// telemetry drops the record rather than refusing the publish that produced it.
// An audit trail is worth less than the write it audits.
func (p *Provider) EmitAudit(ctx context.Context, event fact.AuditEvent) {
	if p == nil || p.auditLogger == nil {
		return
	}

	var record log.Record

	// Stamped here rather than left to the SDK so the timestamp is the moment
	// the state changed, not the moment a batch happened to be assembled.
	record.SetTimestamp(time.Now())

	// The spec says severityNumber is Required and defaults to 12, which is
	// INFO4 rather than the SDK's SeverityInfo (9). Spelled as the constant so
	// the mismatch is visible instead of arriving as a magic number.
	record.SetSeverity(log.SeverityInfo4)
	record.SetSeverityText("INFO")

	record.SetBody(log.StringValue("catalog state changed"))

	// The five spec-Required names are LITERALS here, which is the one place in
	// this service that an attribute key is not read from fact.
	//
	// Deliberate, and the alternative was tried: fact already has a Signal
	// called Log, and it means the request's zap completion line, not the OTLP
	// LOG signal. Filing these there would give one word two meanings in the
	// table whose whole job is that a name means one thing. They are also
	// written in exactly one place — here — so the drift the registry exists to
	// prevent has nowhere to happen. Add a second writer and that stops being
	// true; file them then.
	record.AddAttributes(
		// Required by otel-specification.md §LOG. log_uuid is per-RECORD, not
		// per-catalog: a consumer uses it to discard a redelivery rather than
		// count the same transition twice.
		log.String("log_uuid", uuid.NewString()),
		log.String("item.id", event.ItemID),
		log.String("item.type", event.ItemType),
		log.String("item.prevstate", event.PrevState),
		log.String("item.state", event.State),
	)

	// Emit reads the span context out of ctx itself, which is how the record
	// gets its traceId and spanId and how a facilitator lands on the request
	// that caused the change.
	p.auditLogger.Emit(ctx, record)
}
