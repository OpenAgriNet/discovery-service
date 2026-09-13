package publish_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// The AUDIT signal, from the write path's side. What this file asserts is that
// publish NAMES the transition; that the name reaches a collector is
// middlewares' half, and the spec-shape of the record is telemetry's.
//
// The question the whole signal exists to answer is "why did this catalog stop
// appearing in discover results", and until now nothing in this service wrote
// that down: the row's `active` column held the answer and kept no history.

// publishAudits runs each message as its own REQUEST against one store, and
// hands back the state changes each recorded.
//
// Separate requests rather than one payload with two entries, because A1
// refuses the same catalog id twice inside a single action — so a transition
// can only ever be observed across requests, which is also how a publisher
// actually withdraws a catalog.
func publishAudits(t *testing.T, messages ...string) [][]fact.AuditEvent {
	t.Helper()

	service := newService(t, newRepo(), &recordingReplicator{})

	recorded := make([][]fact.AuditEvent, 0, len(messages))
	for _, message := range messages {
		var action beckn.CatalogPublishAction
		if err := json.Unmarshal([]byte(message), &action); err != nil {
			t.Fatalf("decoding the fixture: %v", err)
		}

		ctx, record := fact.New(context.Background())
		service.Publish(ctx, beckn.Context{Action: beckn.ActionPublish}, action)
		recorded = append(recorded, record.Audits())
	}
	return recorded
}

// A catalog nobody had stored comes from `absent`, not from `inactive`. The
// distinction is the point: inactive means a publisher withdrew it, absent
// means it never existed, and a grievance turns on which.
func TestPublishingANewCatalogAuditsItAsArrivingFromAbsent(t *testing.T) {
	audits := publishAudits(t, `{"catalogs":[{"id":"c1","provider":{"id":"p1"}}]}`)[0]

	if len(audits) != 1 {
		t.Fatalf("the request recorded %d state changes, want 1", len(audits))
	}
	got := audits[0]
	if got.ItemID != "c1" {
		t.Errorf("item.id = %q, want the catalog's own id", got.ItemID)
	}
	if got.ItemType != "Catalog" {
		t.Errorf("item.type = %q, want Catalog", got.ItemType)
	}
	if got.PrevState != "absent" {
		t.Errorf("item.prevstate = %q, want absent for a catalog that was not stored", got.PrevState)
	}
	if got.State != "active" {
		t.Errorf("item.state = %q, want active: isActive defaults true", got.State)
	}
}

// The transition the signal was built for. A publisher setting isActive=false
// takes the catalog out of the discoverable set, and this is the only record
// that it happened.
func TestWithdrawingACatalogAuditsTheActiveToInactiveTransition(t *testing.T) {
	requests := publishAudits(t,
		`{"catalogs":[{"id":"c1","provider":{"id":"p1"}}]}`,
		`{"catalogs":[{"id":"c1","provider":{"id":"p1"},"isActive":false}]}`)

	if len(requests[1]) != 1 {
		t.Fatalf("the withdrawal recorded %d state changes, want 1", len(requests[1]))
	}
	got := requests[1][0]
	if got.PrevState != "active" {
		t.Errorf("item.prevstate = %q, want active: prevstate is what the STORE held, "+
			"read before the write, not what this payload says", got.PrevState)
	}
	if got.State != "inactive" {
		t.Errorf("item.state = %q, want inactive; the directive said isActive=false", got.State)
	}
}

// A refused catalog changed nothing, so there is nothing to audit. An audit
// trail that records attempts as if they were transitions is worse than none:
// it says a catalog went active when the store still holds nothing.
func TestARejectedCatalogAuditsNoTransition(t *testing.T) {
	audits := publishAudits(t, `{
		"catalogs":[{"id":"c1","provider":{"id":"p1"}}],
		"publishDirectives":[{"catalogId":"c1","catalogType":"MASTER"}]
	}`)[0]

	if len(audits) != 0 {
		t.Errorf("a refused publish recorded %+v, want no state change at all", audits)
	}
}
