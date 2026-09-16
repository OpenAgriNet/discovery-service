package publish_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// The publish path's half of 23d: one request_info event, fired at intake.
//
// Against the record rather than an exported span, for the reason
// src/discover/telemetry_test.go gives at length: A23 keeps the OTel SDK out of
// this package, and the record-to-span projection is pinned once in
// telemetry/traces_test.go.

// recorded serves one publish request and gives back the facts it produced. The
// wrapper stands in for Trace, which cannot be mounted here because it needs a
// tracer and a tracer needs the SDK; fact.New is the whole of what Trace does
// that a controller depends on.
func recorded(t *testing.T, body string) *fact.Record {
	t.Helper()

	var record *fact.Record
	mounted := mount(t)

	post(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, allocated := fact.New(r.Context())
		record = allocated
		mounted.ServeHTTP(w, r.WithContext(ctx))
	}), "/publish", body)

	return record
}

func look(t *testing.T, record *fact.Record, key fact.Key) fact.Observation {
	t.Helper()

	observation, found := record.Lookup(key)
	if !found {
		t.Fatalf("%s was never observed", fact.Of(key).Name)
	}
	return observation
}

// TestAMasterPublishReportsMasterEvenThoughItIsRefused — the acceptance
// criterion in opentelemetry.md §How the derivation happens, and the entire
// reason request_info fires
// at intake rather than after the A1 gate.
//
// After the refusal, publish.catalog_types would read REGULAR on every span
// that exists, which is a table saying nobody ever tried. At intake it answers
// who is trying to publish master data to a network that refuses it, and the
// error event on the same span carries the refusal — so the two together say
// what happened and to whom.
func TestAMasterPublishReportsMasterEvenThoughItIsRefused(t *testing.T) {
	record := recorded(t, `{"context":{"action":"publish","networkId":"oan"},
		"message":{
			"catalogs":[{"id":"c1","bppId":"imd.gov.in"}],
			"publishDirectives":[{"catalogId":"c1","catalogType":"MASTER"}]}}`)

	if got := look(t, record, fact.PublishCatalogTypes).List; !slices.Equal(got, []string{"MASTER"}) {
		t.Errorf("publish.catalog_types = %v, want [MASTER] — as SENT, refused or not", got)
	}
}

// TestTheShapeOfAPublishIsObserved: the volume, who sent it, and the two
// directives that change what the write does.
func TestTheShapeOfAPublishIsObserved(t *testing.T) {
	record := recorded(t, `{"context":{"action":"publish","networkId":"oan"},
		"message":{
			"catalogs":[
				{"id":"c1","bppId":"imd.gov.in",
				 "validity":{"start":"2026-01-01T00:00:00Z","end":"2026-12-31T00:00:00Z"},
				 "resources":[{"id":"r1"},{"id":"r2"}],
				 "offers":[{"id":"o1"}]},
				{"id":"c2","bppId":"imd.gov.in","resources":[{"id":"r3"}]}],
			"publishDirectives":[
				{"catalogId":"c1","updateMode":"FULL","visibleTo":["oan","kisan"]}]}}`)

	for _, want := range []struct {
		key   fact.Key
		count int64
	}{
		{fact.PublishCatalogCount, 2},
		{fact.PublishResourceCount, 3},
		{fact.PublishOfferCount, 1},
	} {
		if got := look(t, record, want.key).Int; got != want.count {
			t.Errorf("%s = %d, want %d", fact.Of(want.key).SpanKey, got, want.count)
		}
	}

	// Distinct, the same way result.provider_ids is: two catalogs from one
	// publisher is one publisher, or the attribute counts payload size under a
	// name that says identity.
	if got := look(t, record, fact.PublishProviderIDs).List; !slices.Equal(got, []string{"imd.gov.in"}) {
		t.Errorf("publish.provider_ids = %v, want [imd.gov.in]", got)
	}

	// c1 said FULL; c2 has no directive at all and resolves to MERGE, which is
	// worth seeing — a FULL republish deletes what it does not mention, so a
	// request carrying both is a request that does two different things.
	modes := look(t, record, fact.PublishUpdateModes).List
	slices.Sort(modes)
	if want := []string{"FULL", "MERGE"}; !slices.Equal(modes, want) {
		t.Errorf("publish.update_modes = %v, want %v", modes, want)
	}

	if !look(t, record, fact.PublishValidityPresent).Bool {
		t.Errorf("publish.validity_present = false; c1 carries a window")
	}
}

// TestAnOmittedVisibleToResolvesToTheRequestsOwnNetwork (C8).
//
// The resolved set, not the sent one, and that is a deliberate difference from
// publish.catalog_types beside it. The question this attribute answers is which
// networks the data reached, and for an omitted visibleTo the honest answer is
// the request's own — reporting an empty list would say it reached none, which
// is the one thing that did not happen.
func TestAnOmittedVisibleToResolvesToTheRequestsOwnNetwork(t *testing.T) {
	record := recorded(t, `{"context":{"action":"publish","networkId":"oan"},
		"message":{"catalogs":[{"id":"c1"}]}}`)

	if got := look(t, record, fact.PublishVisibleTo).List; !slices.Equal(got, []string{"oan"}) {
		t.Errorf("publish.visible_to = %v, want [oan]", got)
	}
}

// TestAnAbsentDirectiveIsRegularAndMerge, never inferred from content (C9).
//
// The spec would have a catalog carrying no offers read as MASTER, which would
// make the A1 refusal reject every ordinary resource-only catalog — the common
// case. This pins that the attribute does not acquire that inference either:
// what goes out is what the defaults resolve to, not what the payload looks
// like.
func TestAnAbsentDirectiveIsRegularAndMerge(t *testing.T) {
	record := recorded(t, `{"context":{"action":"publish","networkId":"oan"},
		"message":{"catalogs":[{"id":"c1","resources":[{"id":"r1"}]}]}}`)

	if got := look(t, record, fact.PublishCatalogTypes).List; !slices.Equal(got, []string{"REGULAR"}) {
		t.Errorf("publish.catalog_types = %v, want [REGULAR]", got)
	}
	if got := look(t, record, fact.PublishUpdateModes).List; !slices.Equal(got, []string{"MERGE"}) {
		t.Errorf("publish.update_modes = %v, want [MERGE]", got)
	}
	if look(t, record, fact.PublishValidityPresent).Bool {
		t.Errorf("publish.validity_present = true; no catalog carries a window")
	}
}

// TestNothingIsObservedWhenTheMessageIsNotAPublishAction.
//
// There is no publish to describe, so there is no request_info event. An empty
// one stamped at the span's end would read as a publish that arrived and
// carried nothing — a different claim from a body that was never a publish at
// all, and the error event already says which.
func TestNothingIsObservedWhenTheMessageIsNotAPublishAction(t *testing.T) {
	record := recorded(t, `{"context":{"action":"publish"},"message":{"catalogs":"not an array"}}`)

	for _, key := range []fact.Key{
		fact.PublishCatalogCount,
		fact.PublishResourceCount,
		fact.PublishOfferCount,
		fact.PublishProviderIDs,
		fact.PublishCatalogTypes,
		fact.PublishUpdateModes,
		fact.PublishVisibleTo,
		fact.PublishValidityPresent,
	} {
		if observation, found := record.Lookup(key); found {
			t.Errorf("%s was observed as %+v for a body that is not a publish action",
				fact.Of(key).SpanKey, observation)
		}
	}
}
