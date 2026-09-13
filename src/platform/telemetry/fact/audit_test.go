package fact_test

import (
	"context"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// Audit events ride the Record for the reason every other fact does: the
// architecture guard refuses the OTel SDK to src/publish, so the write path can
// name a state change but cannot emit one. Trace drains what it finds.

func TestAStateChangeIsHeldOnTheRecordUntilSomeoneDrainsIt(t *testing.T) {
	ctx, record := fact.New(context.Background())

	fact.AuditStateChange(ctx, "Catalog", "catalog-7", "absent", "active")

	audits := record.Audits()
	if len(audits) != 1 {
		t.Fatalf("the record holds %d audit events, want 1", len(audits))
	}
	got := audits[0]
	if got.ItemType != "Catalog" || got.ItemID != "catalog-7" ||
		got.PrevState != "absent" || got.State != "active" {
		t.Errorf("the record holds %+v, want the four values as passed", got)
	}
}

// One publish action carries many catalogs, so the buffer is a list and not a
// slot. The previous shape of this bug is a request that changed twelve
// catalogs and audited the last one.
func TestEveryStateChangeInOneRequestIsKept(t *testing.T) {
	ctx, record := fact.New(context.Background())

	fact.AuditStateChange(ctx, "Catalog", "first", "absent", "active")
	fact.AuditStateChange(ctx, "Catalog", "second", "active", "inactive")

	if got := len(record.Audits()); got != 2 {
		t.Fatalf("the record holds %d audit events, want 2", got)
	}
	if record.Audits()[1].ItemID != "second" {
		t.Error("the second state change did not survive the first; order is the order they happened")
	}
}

// Nil-tolerant for the reason every Record method is: the acceptance suite and
// dbtest call controllers with no middleware, so there is no record in context.
// A publish that panicked because nobody was collecting telemetry would make the
// audit trail more fragile than the write it audits.
func TestAStateChangeWithNobodyCollectingIsDropped(t *testing.T) {
	fact.AuditStateChange(context.Background(), "Catalog", "catalog-7", "absent", "active")

	var absent *fact.Record
	if got := absent.Audits(); got != nil {
		t.Errorf("a nil Record drained %v, want nil", got)
	}
}
