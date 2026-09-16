package publish_test

import (
	"encoding/json"
	"slices"
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
	return publish.MapCatalog(catalog, beckn.PublishDirective{CatalogID: catalog.ID}, network, kolkata(t), beckn.Version)
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
	if got := documentMember(t, absent.Document, "provider"); got != nil {
		t.Errorf("provider = %s for an omitted key, want it absent from the document", got)
	}
	if absent.Validity != nil {
		t.Errorf("Validity = %#v for an omitted key, want nil", absent.Validity)
	}

	cleared, fatal, _ := mapOne(t, `{"id":"c1","provider":null,"validity":null}`)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none", fatal)
	}
	if got := documentMember(t, cleared.Document, "provider"); string(got) != "null" {
		t.Errorf("provider = %s for an explicit null, want the delete carried verbatim", got)
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

	omittedDescriptor := documentMember(t, patch.Resources[0].Document, "descriptor")
	omittedAttributes := documentMember(t, patch.Resources[0].Document, "resourceAttributes")
	if omittedDescriptor != nil || omittedAttributes != nil {
		t.Errorf("an omitted document mapped to %s / %s, want both absent",
			omittedDescriptor, omittedAttributes)
	}

	clearedDescriptor := documentMember(t, patch.Resources[1].Document, "descriptor")
	clearedAttributes := documentMember(t, patch.Resources[1].Document, "resourceAttributes")
	if string(clearedDescriptor) != "null" || string(clearedAttributes) != "null" {
		t.Errorf("an explicit null mapped to %s / %s, want the delete",
			clearedDescriptor, clearedAttributes)
	}

	if got := documentMember(t, patch.Resources[2].Document, "descriptor"); string(got) != `{"name":"Wheat"}` {
		t.Errorf("descriptor = %s, want the bytes as published", got)
	}
}

// A9 lives INSIDE the document since A17, and the mapper is what settles it.
//
// RFC 7396 keeps a member a patch does not mention, and A9 says an omitted
// `isActive` RESETS the catalog to live — the two rules point opposite ways at
// the same bytes. Resolving it here means the merge only ever sees a patch that
// mentions it, so scenario 26 keeps its answer and MergeCatalog keeps having no
// branch for a default.
//
// It is also what stops the document and the `active` column from disagreeing:
// both are this one resolved bool.
func TestTheResolvedIsActiveIsWrittenIntoTheStoredDocument(t *testing.T) {
	for _, unit := range []struct {
		name string
		body string
		want string
	}{
		{"omitted, so the default", `{"id":"c1"}`, "true"},
		{"a deliberate false", `{"id":"c1","isActive":false}`, "false"},
		{"an explicit true", `{"id":"c1","isActive":true}`, "true"},
	} {
		t.Run(unit.name, func(t *testing.T) {
			patch, fatal, _ := mapOne(t, unit.body)
			if len(fatal) != 0 {
				t.Fatalf("fatal = %v, want none", fatal)
			}

			if got := documentMember(t, patch.Document, "isActive"); string(got) != unit.want {
				t.Errorf("the stored document holds isActive %s, want %s", got, unit.want)
			}
			if patch.Active != (unit.want == "true") {
				t.Errorf("the Active column holds %v while the document holds %s; "+
					"they are one resolved value and must not disagree",
					patch.Active, unit.want)
			}
		})
	}
}

// The two child arrays do NOT reach the catalog document: they own their own
// rows (A17), and a document keeping a copy would give one MERGE two places to
// apply and no rule for which wins.
func TestTheCatalogDocumentDoesNotCarryItsChildren(t *testing.T) {
	patch, fatal, _ := mapOne(t, `{"id":"c1","bppId":"b1","descriptor":{"name":"Depot"},
		"resources":[{"id":"r1"}],"offers":[{"id":"o1"}]}`)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none", fatal)
	}

	for _, child := range []string{"resources", "offers"} {
		if got := documentMember(t, patch.Document, child); got != nil {
			t.Errorf("the catalog document still carries %s = %s", child, got)
		}
	}

	// And everything else is still there, verbatim — the whole point of
	// keeping the document rather than a projection of it.
	if got := documentMember(t, patch.Document, "bppId"); string(got) != `"b1"` {
		t.Errorf("bppId = %s, want it kept", got)
	}
	if got := documentMember(t, patch.Document, "descriptor"); string(got) != `{"name":"Depot"}` {
		t.Errorf("descriptor = %s, want it kept", got)
	}
}

// documentMember reads one top-level member of a stored document, or nil when
// the document does not carry it.
//
// Absence and an explicit null are the distinction most of these cases are
// about, so they must not collapse: an absent member is nil here, and a null
// one is the four bytes `null`.
func documentMember(t *testing.T, document json.RawMessage, name string) json.RawMessage {
	t.Helper()

	if len(document) == 0 {
		return nil
	}

	var members map[string]json.RawMessage
	if err := json.Unmarshal(document, &members); err != nil {
		t.Fatalf("the stored document is not an object: %v (%s)", err, document)
	}
	return members[name]
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
		network, time.UTC, beckn.Version,
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

// The protocol version the publisher declared travels from `context.version`
// onto the catalog, so a stored catalog says which version of the protocol it
// was written under. Read from the envelope rather than from `beckn.Version`,
// which is what THIS BUILD serves: the two agree today and the whole point of
// storing it is the day they do not.
func TestTheProtocolVersionIsCarriedFromTheEnvelope(t *testing.T) {
	var catalog beckn.Catalog
	if err := json.Unmarshal([]byte(`{"id":"c1"}`), &catalog); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}

	patch, fatal, _ := publish.MapCatalog(
		catalog, beckn.PublishDirective{CatalogID: catalog.ID}, network, kolkata(t), "2.1.0")
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none", fatal)
	}
	if patch.ProtocolVersion != "2.1.0" {
		t.Errorf("ProtocolVersion = %q, want %q", patch.ProtocolVersion, "2.1.0")
	}
}

// An envelope with no `version` is not a catalog with no version. The column is
// NOT NULL and the mapper is the last place that can say what the default is,
// because by the time the row is written an empty string is indistinguishable
// from a publisher who sent one.
func TestAnAbsentProtocolVersionFallsBackToTheBuildsOwn(t *testing.T) {
	var catalog beckn.Catalog
	if err := json.Unmarshal([]byte(`{"id":"c1"}`), &catalog); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}

	patch, fatal, _ := publish.MapCatalog(
		catalog, beckn.PublishDirective{CatalogID: catalog.ID}, network, kolkata(t), "")
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none", fatal)
	}
	if patch.ProtocolVersion != beckn.Version {
		t.Errorf("ProtocolVersion = %q, want %q", patch.ProtocolVersion, beckn.Version)
	}
}

// catalogID prefers the directive's id, but every other test in this file
// builds a directive whose CatalogID already equals the catalog's own — this
// is the only one where the directive's is empty, so the fallback to the
// body's own id is the branch actually taken.
func TestAnEmptyDirectiveCatalogIDFallsBackToTheBodys(t *testing.T) {
	patch, fatal, _ := publish.MapCatalog(
		beckn.Catalog{ID: "c1"}, beckn.PublishDirective{}, network, time.UTC, beckn.Version)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none", fatal)
	}
	if patch.ID != "c1" {
		t.Errorf("ID = %q, want the catalog's own c1", patch.ID)
	}
}

// A catalog whose Raw does not decode as a JSON object — reachable only by a
// caller building beckn.Catalog directly rather than through json.Unmarshal,
// which is exactly the contract protocolVersion's own doc comment describes:
// MapCatalog has no one particular gate it works behind.
func TestACatalogRawThatIsNotAnObjectIsFatal(t *testing.T) {
	patch, fatal, _ := publish.MapCatalog(
		beckn.Catalog{ID: "c1", Raw: json.RawMessage(`[1,2,3]`)},
		beckn.PublishDirective{CatalogID: "c1"}, network, time.UTC, beckn.Version)
	if len(fatal) != 1 {
		t.Fatalf("fatal = %v, want exactly one", fatal)
	}
	if fatal[0].Code != string(beckn.CodeSchemaInvalidFormat) {
		t.Errorf("Code = %q, want SCH_INVALID_FORMAT", fatal[0].Code)
	}
	if patch.Document != nil {
		t.Errorf("Document = %s, want nothing stored", patch.Document)
	}
}

// resourceDocument's fallback — json.Marshal(resource) rather than Raw — is
// reached only by a resource built in Go rather than decoded off the wire,
// since UnmarshalJSON always sets Raw. Every resource fixture in this file
// until now went through mapOne's json.Unmarshal, so the fallback itself was
// never exercised: this constructs the catalog directly instead.
func TestAResourceBuiltInGoFallsBackToMarshallingItsFields(t *testing.T) {
	patch, fatal, _ := publish.MapCatalog(
		beckn.Catalog{ID: "c1", Resources: []beckn.Resource{{ID: "r1"}}},
		beckn.PublishDirective{CatalogID: "c1"}, network, time.UTC, beckn.Version)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none", fatal)
	}
	if len(patch.Resources) != 1 || string(patch.Resources[0].Document) != `{"id":"r1"}` {
		t.Errorf("Document = %s, want the fields marshalled since there is no Raw", patch.Resources[0].Document)
	}
}

// The other half of the fallback: a hand-built resource whose Descriptor is
// not valid JSON fails to marshal, and the resource is stored with no
// document rather than the mapper crashing or the catalog refusing whole —
// mapResources does not check resourceDocument's return for an error.
func TestAResourceBuiltInGoWithUnmarshallableFieldsStoresNoDocument(t *testing.T) {
	patch, fatal, _ := publish.MapCatalog(
		beckn.Catalog{ID: "c1", Resources: []beckn.Resource{
			{ID: "r1", Descriptor: json.RawMessage(`{not valid`)},
		}},
		beckn.PublishDirective{CatalogID: "c1"}, network, time.UTC, beckn.Version)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none — mapResources does not inspect resourceDocument's result", fatal)
	}
	if len(patch.Resources) != 1 || patch.Resources[0].Document != nil {
		t.Errorf("Document = %s, want nil", patch.Resources[0].Document)
	}
}

// An offer with no id is fatal, the same as a resource with no id — offers
// merge by id too.
func TestAnOfferWithNoIDIsFatal(t *testing.T) {
	patch, fatal, _ := mapOne(t, `{"id":"c1","offers":[{"id":"o1"},{"price":{"value":"10"}}]}`)
	if len(fatal) != 1 {
		t.Fatalf("fatal = %v, want exactly one", fatal)
	}
	if !strings.Contains(fatal[0].Path, "offers") {
		t.Errorf("fault Path = %q, want it to name the offer", fatal[0].Path)
	}
	if len(patch.Offers) != 0 {
		t.Errorf("Offers = %v, want nothing stored — the whole catalog is refused", patch.Offers)
	}
}

// resolvedResourceIDs' other half: an offer that DID name resource ids passes
// them through unchanged. TestAnOffersResourceIDsAreResolvedToTheCatalogWideDefault
// covers only the absent case.
func TestAnOffersOwnResourceIDsPassThroughUnchanged(t *testing.T) {
	patch, fatal, _ := mapOne(t, `{"id":"c1","offers":[{"id":"o1","resourceIds":["wheat"]}]}`)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none", fatal)
	}
	if len(patch.Offers) != 1 || !slices.Equal(patch.Offers[0].ResourceIDs, []string{"wheat"}) {
		t.Errorf("ResourceIDs = %v, want [wheat]", patch.Offers[0].ResourceIDs)
	}
}

// validity ITSELF of the wrong JSON type is fatal — every other unreadable-
// validity case in this file names a bad SUB-member; this is the container.
func TestAValidityThatIsNotAnObjectIsFatal(t *testing.T) {
	_, fatal, _ := mapOne(t, `{"id":"c1","validity":"not-an-object"}`)
	if len(fatal) != 1 {
		t.Fatalf("fatal = %v, want exactly one", fatal)
	}
	if fatal[0].Code != string(beckn.CodeSchemaInvalidFormat) {
		t.Errorf("Code = %q, want SCH_INVALID_FORMAT", fatal[0].Code)
	}
}

// A validity that is nothing but whitespace is neither absent, an explicit
// null, nor a readable period — isJSONNull's own whitespace-skipping loop
// runs out without finding 'n' or anything else, and what follows is the same
// unreadable-shape fault a wrong JSON type gets.
//
// A whitespace-only Validity RawMessage also fails WithoutChildren's own
// re-marshal (it is not, on its own, valid JSON either), so this fatals
// twice over — asserted by finding the validity fault among them rather than
// by an exact count, since that second fault belongs to a different branch.
func TestAWhitespaceOnlyValidityIsFatal(t *testing.T) {
	_, fatal, _ := publish.MapCatalog(
		beckn.Catalog{ID: "c1", Validity: json.RawMessage("   ")},
		beckn.PublishDirective{CatalogID: "c1"}, network, time.UTC, beckn.Version)

	found := false
	for _, f := range fatal {
		if strings.Contains(f.Message, "time period") {
			found = true
		}
	}
	if !found {
		t.Fatalf("fatal = %v, want one fault naming validity as not a time period", fatal)
	}
}

// isJSONNull's whitespace-skipping loop, the other side: leading whitespace
// before the literal null is still the explicit delete, not an unreadable
// shape. Only reachable by building the RawMessage directly — a real decode
// never captures leading whitespace inside a token.
func TestLeadingWhitespaceBeforeNullIsStillTheExplicitClear(t *testing.T) {
	patch, fatal, _ := publish.MapCatalog(
		beckn.Catalog{ID: "c1", Validity: json.RawMessage(" null")},
		beckn.PublishDirective{CatalogID: "c1"}, network, time.UTC, beckn.Version)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %v, want none", fatal)
	}
	if patch.Validity == nil || !patch.Validity.StartDate.Null {
		t.Errorf("Validity = %#v, want the explicit clear on all four members", patch.Validity)
	}
}

// scalar's own two failure modes, distinct from instant/clock's parse
// failures TestAnUnreadableValidityIsFatal already covers: a member that is
// not a JSON string at all, and one that is an empty string.
func TestAValidityMemberOfTheWrongTypeOrEmptyIsFatal(t *testing.T) {
	for _, body := range []string{
		`{"id":"c1","validity":{"startDate":123}}`,
		`{"id":"c1","validity":{"startDate":""}}`,
	} {
		_, fatal, _ := mapOne(t, body)
		if len(fatal) != 1 {
			t.Fatalf("%s: fatal = %v, want exactly one", body, fatal)
		}
		if !strings.Contains(fatal[0].Message, "non-empty string") {
			t.Errorf("%s: Message = %q, want it to name the expected shape", body, fatal[0].Message)
		}
	}
}
