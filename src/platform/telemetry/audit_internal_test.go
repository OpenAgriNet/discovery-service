package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"

	"github.com/OpenAgriNet/discovery-service/src/platform/buildinfo"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// recording is a log.Processor that keeps what it is given.
//
// Written here rather than taken from a logtest package because it is four
// methods and the alternative is a dependency whose whole job is these four
// methods. OnEmit takes the record by pointer and the SDK reuses the backing
// array, so it is cloned before it is kept.
type recording struct{ records []sdklog.Record }

func (r *recording) OnEmit(_ context.Context, rec *sdklog.Record) error {
	r.records = append(r.records, rec.Clone())
	return nil
}
func (r *recording) Enabled(context.Context, sdklog.EnabledParameters) bool { return true }
func (r *recording) Shutdown(context.Context) error                         { return nil }
func (r *recording) ForceFlush(context.Context) error                       { return nil }

func attributesOfRecord(rec sdklog.Record) map[string]string {
	out := map[string]string{}
	rec.WalkAttributes(func(kv log.KeyValue) bool {
		out[kv.Key] = kv.Value.String()
		return true
	})
	return out
}

// auditProviderForTest wires a Provider whose audit logger records in memory.
func auditProviderForTest(t *testing.T) (*Provider, *recording) {
	t.Helper()

	res, err := projectResource(context.Background(), testIdentity(), buildinfo.Read())
	if err != nil {
		t.Fatalf("projectResource: %v", err)
	}
	auditRes, err := withEID(res, eidAudit)
	if err != nil {
		t.Fatalf("withEID: %v", err)
	}

	sink := &recording{}
	provider := sdklog.NewLoggerProvider(
		sdklog.WithResource(auditRes),
		sdklog.WithProcessor(sink),
	)

	return &Provider{logs: provider, auditLogger: provider.Logger(ScopeName)}, sink
}

// TestTheAuditRecordCarriesWhatTheSpecRequires pins the five attributes
// otel-specification.md:589 marks Required on a LOG record.
//
// They are Required rather than conventional: a facilitator answering a
// grievance reads item.prevstate and item.state to establish what changed, and
// a record missing either is not an audit event, it is a log line with an eid.
func TestTheAuditRecordCarriesWhatTheSpecRequires(t *testing.T) {
	provider, sink := auditProviderForTest(t)

	provider.EmitAudit(context.Background(), fact.AuditEvent{
		ItemType:  fact.ItemTypeCatalog,
		ItemID:    "catalog-7",
		PrevState: fact.CatalogState(false, false),
		State:     fact.CatalogState(true, true),
	})

	if len(sink.records) != 1 {
		t.Fatalf("emitted %d records, want exactly 1", len(sink.records))
	}
	record := sink.records[0]
	attributes := attributesOfRecord(record)

	for key, want := range map[string]string{
		"item.id":        "catalog-7",
		"item.type":      "Catalog",
		"item.prevstate": "absent",
		"item.state":     "active",
	} {
		if got := attributes[key]; got != want {
			t.Errorf("the record carries %s = %q, want %q", key, got, want)
		}
	}

	if attributes["log_uuid"] == "" {
		t.Error("the record carries no log_uuid; the spec makes it Required so a " +
			"consumer can discard a redelivered record rather than double-count it")
	}

	// The spec says "Required. Default to 12", and 12 is INFO4 rather than the
	// SDK's SeverityInfo, which is 9.
	if got := record.Severity(); got != log.SeverityInfo4 {
		t.Errorf("the record carries severity %d, want %d (otel-specification.md:616)",
			got, log.SeverityInfo4)
	}

	if record.Timestamp().IsZero() {
		t.Error("the record carries no timestamp; the spec makes timeUnixNano Required")
	}
}
