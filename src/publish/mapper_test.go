package publish_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/publish"
)

const network = "agri.example.net"

func kolkata(t *testing.T) *time.Location {
	t.Helper()

	zone, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatalf("loading Asia/Kolkata: %v", err)
	}
	return zone
}

// mapOne runs the mapper over one wire catalog with an otherwise empty
// directive, so a test only states the field it is about.
func mapOne(t *testing.T, body string) (domain.CatalogPatch, []domain.Fault, []domain.Fault) {
	t.Helper()

	var catalog beckn.Catalog
	if err := json.Unmarshal([]byte(body), &catalog); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	return publish.MapCatalog(catalog, beckn.PublishDirective{CatalogID: catalog.ID}, network, kolkata(t))
}

// A9, the defaulted half. Both modes, because a default the mapper resolves is
// one the merge never has to have a branch for — and a branch that only runs
// under MERGE is a branch nothing tests under FULL.
func TestTheDeclaredDefaultsAreResolvedByTheMapper(t *testing.T) {
	patch, fatal, _ := mapOne(t, `{"id":"c1"}`)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none", fatal)
	}

	if !patch.Active {
		t.Error("Active = false for a payload that omitted isActive, want true")
	}
	if len(patch.VisibleTo) != 1 || patch.VisibleTo[0] != network {
		t.Errorf("VisibleTo = %v, want [%s]", patch.VisibleTo, network)
	}
}

// The default must not overwrite a deliberate false. This is exactly why
// beckn.Catalog.IsActive is a pointer.
func TestADeliberateFalseSurvivesTheDefault(t *testing.T) {
	patch, _, _ := mapOne(t, `{"id":"c1","isActive":false}`)
	if patch.Active {
		t.Error("Active = true for a payload that sent isActive:false")
	}
}

// A8, the NOT-defaulted half, and the one that makes MERGE implementable.
//
// Absence and explicit null are different instructions: keep what is stored,
// versus delete it. A mapper that flattens them to a zero value makes the
// second unexpressible, silently, and nothing downstream can recover it.
func TestAbsenceAndExplicitNullStayDistinct(t *testing.T) {
	absent, fatal, _ := mapOne(t, `{"id":"c1"}`)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none", fatal)
	}
	if absent.Provider != nil {
		t.Errorf("Provider = %s for an omitted key, want nil", absent.Provider)
	}
	if absent.Validity != nil {
		t.Errorf("Validity = %#v for an omitted key, want nil", absent.Validity)
	}

	cleared, fatal, _ := mapOne(t, `{"id":"c1","provider":null,"validity":null}`)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none", fatal)
	}
	if string(cleared.Provider) != "null" {
		t.Errorf("Provider = %s for an explicit null, want the delete", cleared.Provider)
	}
	if cleared.Validity == nil {
		t.Fatal("Validity = nil for an explicit null, want the four-column delete")
	}
	for name, got := range map[string]domain.Nullable[time.Time]{
		"StartDate": cleared.Validity.StartDate,
		"EndDate":   cleared.Validity.EndDate,
	} {
		if !got.Set || !got.Null {
			t.Errorf("Validity.%s = %#v, want Set and Null", name, got)
		}
	}
}

// The same distinction one level down, on a resource.
func TestAResourcesOptionalDocumentsKeepTheirThreeStates(t *testing.T) {
	patch, fatal, _ := mapOne(t, `{"id":"c1","resources":[
		{"id":"absent"},
		{"id":"cleared","descriptor":null,"resourceAttributes":null},
		{"id":"sent","descriptor":{"name":"Wheat"}}
	]}`)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none", fatal)
	}
	if len(patch.Resources) != 3 {
		t.Fatalf("Resources = %d, want 3", len(patch.Resources))
	}

	if patch.Resources[0].Descriptor != nil || patch.Resources[0].Attributes != nil {
		t.Errorf("an omitted document mapped to %s / %s, want nil",
			patch.Resources[0].Descriptor, patch.Resources[0].Attributes)
	}
	if string(patch.Resources[1].Descriptor) != "null" || string(patch.Resources[1].Attributes) != "null" {
		t.Errorf("an explicit null mapped to %s / %s, want the delete",
			patch.Resources[1].Descriptor, patch.Resources[1].Attributes)
	}
	if string(patch.Resources[2].Descriptor) != `{"name":"Wheat"}` {
		t.Errorf("Descriptor = %s, want the bytes as published", patch.Resources[2].Descriptor)
	}
}

// A resource with no id stores NOTHING.
//
// Resources merge by id, so an empty one is not a resource the merge can place:
// it would insert a row keyed on "" that the next publish silently patches
// instead of inserting beside.
func TestAResourceWithNoIDIsFatal(t *testing.T) {
	patch, fatal, _ := mapOne(t, `{"id":"c1","resources":[{"id":"wheat"},{"descriptor":{}}]}`)
	if len(fatal) != 1 {
		t.Fatalf("fatal = %v, want exactly one", fatal)
	}
	if !strings.Contains(fatal[0].Path, "resources") {
		t.Errorf("fault Path = %q, want it to name the resource", fatal[0].Path)
	}
	if len(patch.Resources) != 0 {
		t.Errorf("Resources = %v, want nothing stored — the whole catalog is refused", patch.Resources)
	}
}

// An offer's resourceIds has a declared default of [] — CATALOG-WIDE, not
// "none" — so the mapper resolves it and it cannot be absent by merge time.
func TestAnOffersResourceIDsAreResolvedToTheCatalogWideDefault(t *testing.T) {
	patch, fatal, _ := mapOne(t, `{"id":"c1","offers":[{"id":"o1","price":{"value":"10"}}]}`)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none", fatal)
	}
	if len(patch.Offers) != 1 {
		t.Fatalf("Offers = %d, want 1", len(patch.Offers))
	}
	if patch.Offers[0].ResourceIDs == nil {
		t.Error("ResourceIDs = nil, want the resolved empty slice")
	}
	if len(patch.Offers[0].ResourceIDs) != 0 {
		t.Errorf("ResourceIDs = %v, want empty", patch.Offers[0].ResourceIDs)
	}
}

// The offer document reaches the column VERBATIM, extras included.
//
// The spec leaves Offer.additionalProperties unset, so a publisher may send
// members no struct here names. Re-marshalling a parsed shape drops exactly
// those, and the `offer` JSONB column's claim to hold what was published would
// be false for precisely the publishers who relied on it.
func TestAnOffersUnknownMembersSurviveIntoTheDocument(t *testing.T) {
	patch, _, _ := mapOne(t, `{"id":"c1","offers":[{"id":"o1","loyaltyTier":"gold"}]}`)
	if len(patch.Offers) != 1 {
		t.Fatalf("Offers = %d, want 1", len(patch.Offers))
	}
	if !strings.Contains(string(patch.Offers[0].Document), "loyaltyTier") {
		t.Errorf("Document = %s, want the publisher's own member kept", patch.Offers[0].Document)
	}
}

// The two halves of `validity` are independent, and the clock half is
// normalised to UTC before anything downstream compares it.
//
// Both spellings land on the same instant, which is the point: an offset form
// and a bare clock resolved in APP_DEFAULT_TIMEZONE describe the same moment in
// Bengaluru, and a store that saw two different numbers would answer the same
// question two ways.
func TestBothSpellingsOfADailyWindowNormaliseToUTC(t *testing.T) {
	for _, sent := range []string{`"09:00:00+05:30"`, `"09:00:00"`} {
		body := `{"id":"c1","validity":{"startTime":` + sent + `,"endTime":"18:00:00+05:30"}}`

		patch, fatal, _ := mapOne(t, body)
		if len(fatal) != 0 {
			t.Fatalf("%s: fatal = %v, want none", sent, fatal)
		}
		if patch.Validity == nil || !patch.Validity.StartTime.Set {
			t.Fatalf("%s: StartTime was not set", sent)
		}

		want := domain.TimeOfDay{Hour: 3, Minute: 30}
		if got := patch.Validity.StartTime.Value; got != want {
			t.Errorf("%s normalised to %+v, want %+v", sent, got, want)
		}
	}
}

// The calendar half is an RFC 3339 instant and stays one.
func TestTheCalendarHalfIsParsedAsAnInstant(t *testing.T) {
	patch, fatal, _ := mapOne(t, `{"id":"c1","validity":{"startDate":"2026-01-01T00:00:00Z","endDate":null}}`)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none", fatal)
	}

	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !patch.Validity.StartDate.Set || !patch.Validity.StartDate.Value.Equal(want) {
		t.Errorf("StartDate = %#v, want %v", patch.Validity.StartDate, want)
	}
	if !patch.Validity.EndDate.Set || !patch.Validity.EndDate.Null {
		t.Errorf("EndDate = %#v, want the explicit delete", patch.Validity.EndDate)
	}
	if patch.Validity.StartTime.Set || patch.Validity.EndTime.Set {
		t.Error("the clock half was touched by a payload that named only the calendar half")
	}
}

// Half a daily window is a FATAL fault.
//
// The spec's anyOf requires both, and guessing the missing bound invents a
// window the publisher never stated — one that then silently decides whether
// every resource in the catalog is findable at 23:00.
func TestHalfADailyWindowIsFatal(t *testing.T) {
	for _, body := range []string{
		`{"id":"c1","validity":{"startTime":"09:00:00"}}`,
		`{"id":"c1","validity":{"endTime":"18:00:00"}}`,
	} {
		_, fatal, _ := mapOne(t, body)
		if len(fatal) != 1 {
			t.Fatalf("%s: fatal = %v, want exactly one", body, fatal)
		}
		if !strings.Contains(fatal[0].Message, "Time") {
			t.Errorf("%s: Message = %q, want it to name the missing bound", body, fatal[0].Message)
		}
	}
}

// An unreadable time is a fault rather than a zero value, on both halves.
func TestAnUnreadableValidityIsFatal(t *testing.T) {
	for _, body := range []string{
		`{"id":"c1","validity":{"startDate":"not-a-date"}}`,
		`{"id":"c1","validity":{"startTime":"25:00:00","endTime":"26:00:00"}}`,
	} {
		_, fatal, _ := mapOne(t, body)
		if len(fatal) == 0 {
			t.Errorf("%s: no fault for an unreadable validity", body)
		}
	}
}

// A directive's visibleTo wins over the default, and the catalog id comes from
// the directive rather than the body — the directive is what publishOne keyed
// the catalog on.
func TestTheDirectiveSuppliesTheIdentityAndTheVisibility(t *testing.T) {
	patch, fatal, _ := publish.MapCatalog(
		beckn.Catalog{ID: "c1"},
		beckn.PublishDirective{CatalogID: "c1", VisibleTo: []string{"a.net", "b.net"}},
		network, time.UTC,
	)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none", fatal)
	}
	if patch.ID != "c1" {
		t.Errorf("ID = %q, want c1", patch.ID)
	}
	if len(patch.VisibleTo) != 2 {
		t.Errorf("VisibleTo = %v, want the directive's own list", patch.VisibleTo)
	}
	if patch.NetworkID != network {
		t.Errorf("NetworkID = %q, want %q", patch.NetworkID, network)
	}
}
