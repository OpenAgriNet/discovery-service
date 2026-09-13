package fact

import "context"

// Audit events: the spec's LOG signal, whose eid is AUDIT
// (otel-specification.md:589 and :599).
//
// They live here, beside the span facts, for the reason the span facts do —
// tests/architecture/boundary_test.go refuses the OTel SDK to src/publish, so
// the write path can NAME a state change but cannot emit one. This package
// links nothing but the standard library, which is what makes it reachable from
// both sides.
//
// They are NOT Observations. An Observation is one value about the request,
// last-write-wins per Key, projected onto a span attribute; a state change is a
// separate record of its own, and one request can carry many. Filing them
// through put would collapse twelve catalogs into one.

// The three states a catalog can be in, as the spec's item.prevstate and
// item.state spell them. Free text in the spec, so these are a decision: a
// grievance answered from this history reads these strings.
const (
	stateAbsent   = "absent"
	stateActive   = "active"
	stateInactive = "inactive"
)

// CatalogState names the state of one side of a catalog write.
//
// It takes the two facts rather than a domain.Catalog because this package
// links nothing but the standard library — that is what lets src/publish call
// it — and importing the domain model would end that.
func CatalogState(exists, active bool) string {
	if !exists {
		return stateAbsent
	}
	if active {
		return stateActive
	}
	return stateInactive
}

// ItemTypeCatalog is the spec's item.type — WHAT kind of entity changed. One
// value today because the catalog is the only entity this service transitions.
const ItemTypeCatalog = "Catalog"

// AuditEvent is one entity transition, in the spec's own vocabulary.
//
// ItemType and the two states are strings rather than enums because the spec
// leaves them free text; the values this service uses are pinned by
// telemetry.CatalogState and telemetry's itemTypeCatalog, which is the layer
// that knows the domain.
type AuditEvent struct {
	ItemType  string
	ItemID    string
	PrevState string
	State     string
}

// AuditStateChange records that an entity moved between two states.
//
// prev and next are both required, and both are already resolved to a state
// name: the spec makes item.prevstate Required, and a record that cannot say
// what the entity was before answers no question a grievance asks.
func AuditStateChange(ctx context.Context, itemType, itemID, prevState, state string) {
	record := From(ctx)
	if record == nil {
		return
	}

	record.mutex.Lock()
	defer record.mutex.Unlock()
	record.audits = append(record.audits, AuditEvent{
		ItemType:  itemType,
		ItemID:    itemID,
		PrevState: prevState,
		State:     state,
	})
}

// Audits hands back the state changes this request made, in the order they
// happened.
//
// A copy, because the caller is the Trace middleware's deferred completion and
// the record outlives it by however long the response writer takes; handing out
// the live slice would let an emit race a late write.
func (r *Record) Audits() []AuditEvent {
	if r == nil {
		return nil
	}

	r.mutex.Lock()
	defer r.mutex.Unlock()
	if len(r.audits) == 0 {
		return nil
	}
	return append([]AuditEvent(nil), r.audits...)
}
