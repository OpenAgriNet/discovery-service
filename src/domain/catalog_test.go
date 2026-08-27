package domain

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"
)

func january(day int) time.Time {
	return time.Date(2026, time.January, day, 0, 0, 0, 0, time.UTC)
}

// storedCatalog is what the publisher already has in the store. Every case here
// patches it, so the fixture carries one of everything a patch can reach: two
// resources (so "the other one is untouched" is assertable), one offer covering
// one of them, both validity forms, and both defaulted fields set to the
// non-default value.
func storedCatalog() Catalog {
	return Catalog{
		ID:            "c1",
		Document:      json.RawMessage(`{"id":"c1","provider":{"name":"Anand Seeds","phone":"111"}}`),
		Active:        true,
		VisibleTo:     []string{"mahavistar", "oan"},
		ValidFrom:     january(1),
		ValidTo:       january(31),
		ValidTimeFrom: &TimeOfDay{Hour: 9},
		ValidTimeTo:   &TimeOfDay{Hour: 17},
		Resources: []Resource{
			{ID: "r1", Document: json.RawMessage(
				`{"id":"r1","descriptor":{"name":"Wheat"},"resourceAttributes":{"grade":"A","kg":50}}`)},
			{ID: "r2", Document: json.RawMessage(
				`{"id":"r2","descriptor":{"name":"Rice"},"resourceAttributes":{"grade":"B"}}`)},
		},
		Offers: []Offer{
			{ID: "o1", ResourceIDs: []string{"r1"}, Document: json.RawMessage(`{"price":10,"unit":"kg"}`)},
		},
	}
}

// defaultedPatch carries only the two fields A9 resolves before the merge runs,
// so a case that is not about them does not have to restate them — and cannot
// accidentally assert the zero value means "absent".
func defaultedPatch() CatalogPatch {
	return CatalogPatch{ID: "c1", Active: true, VisibleTo: []string{"mahavistar", "oan"}}
}

// attributePatch is a resource patch that touches `resourceAttributes` and
// nothing else — the shape most of these cases send.
//
// It carries the id inside the document as well as beside it, because that is
// what the publish mapper produces: the wire Resource has `id` as a required
// member, and a fixture that omitted it would be merging a document no
// publisher could send.
func attributePatch(id, attributes string) json.RawMessage {
	return json.RawMessage(`{"id":"` + id + `","resourceAttributes":` + attributes + `}`)
}

func resourceByID(t *testing.T, catalog Catalog, id string) Resource {
	t.Helper()

	for _, resource := range catalog.Resources {
		if resource.ID == id {
			return resource
		}
	}
	t.Fatalf("no resource %q in the merged catalog", id)
	return Resource{}
}

// The pin that stops a one-attribute patch from re-embedding a forty-resource
// catalog: the resource nobody named comes out identical, and `touched` names
// exactly one id.
func TestAPatchNamingOneResourceLeavesTheOtherIdentical(t *testing.T) {
	patch := defaultedPatch()
	patch.Resources = []ResourcePatch{{ID: "r1", Document: attributePatch("r1", `{"kg":100}`)}}

	merged, touched := MergeCatalog(storedCatalog(), patch)

	if got := string(resourceByID(t, merged, "r2").ResourceAttributes()); got != `{"grade":"B"}` {
		t.Errorf("r2 attributes = %s, want them untouched", got)
	}
	if !slices.Equal(touched, []string{"r1"}) {
		t.Errorf("touched = %v, want exactly [r1]", touched)
	}
}

// MERGE is per document, not per resource: the patched key changes and the
// sibling the patch did not mention survives.
func TestAResourcePatchMergesItsAttributesRatherThanReplacingThem(t *testing.T) {
	patch := defaultedPatch()
	patch.Resources = []ResourcePatch{{ID: "r1", Document: attributePatch("r1", `{"kg":100}`)}}

	merged, _ := MergeCatalog(storedCatalog(), patch)

	if !sameJSON(t, resourceByID(t, merged, "r1").ResourceAttributes(), json.RawMessage(`{"grade":"A","kg":100}`)) {
		t.Errorf("r1 attributes = %s, want grade kept and kg replaced",
			resourceByID(t, merged, "r1").ResourceAttributes())
	}
}

// Resources merge by id, not by array position (A8). A patch naming an id
// nothing stores is an insert — the only way a publisher adds a resource under
// MERGE.
func TestAResourceIDNothingStoresIsAnInsert(t *testing.T) {
	patch := defaultedPatch()
	patch.Resources = []ResourcePatch{{ID: "r3", Document: json.RawMessage(
		`{"id":"r3","descriptor":{"name":"Millet"}}`)}}

	merged, touched := MergeCatalog(storedCatalog(), patch)

	if len(merged.Resources) != 3 {
		t.Fatalf("the merged catalog holds %d resources, want 3", len(merged.Resources))
	}
	if !sameJSON(t, resourceByID(t, merged, "r3").Descriptor(), json.RawMessage(`{"name":"Millet"}`)) {
		t.Error("the inserted resource did not carry the patch's descriptor")
	}
	if !slices.Contains(touched, "r3") {
		t.Errorf("touched = %v, want it to name the inserted r3", touched)
	}
}

func TestAnAbsentProviderKeepsTheStoredOne(t *testing.T) {
	merged, _ := MergeCatalog(storedCatalog(), defaultedPatch())

	if !sameJSON(t, merged.Provider(), json.RawMessage(`{"name":"Anand Seeds","phone":"111"}`)) {
		t.Errorf("provider = %s, want the stored one", merged.Provider())
	}
}

func TestAProviderPatchMergesIntoTheStoredDocument(t *testing.T) {
	patch := defaultedPatch()
	patch.Document = json.RawMessage(`{"provider":{"phone":"222"}}`)

	merged, _ := MergeCatalog(storedCatalog(), patch)

	if !sameJSON(t, merged.Provider(), json.RawMessage(`{"name":"Anand Seeds","phone":"222"}`)) {
		t.Errorf("provider = %s, want the name kept and the phone replaced", merged.Provider())
	}
}

// A9's surprising half, and the one scenario 26 exists to make deliberate: the
// defaulted fields are resolved before the merge and applied unconditionally,
// so silence means "sent with its default" rather than "keep what is stored".
func TestTheDefaultedFieldsApplyEvenWhenTheyResetStoredValues(t *testing.T) {
	patch := CatalogPatch{ID: "c1", Active: false, VisibleTo: []string{"mahavistar"}}

	merged, _ := MergeCatalog(storedCatalog(), patch)

	if merged.Active {
		t.Error("isActive stayed true; a resolved default must overwrite the stored value")
	}
	if !slices.Equal(merged.VisibleTo, []string{"mahavistar"}) {
		t.Errorf("visibleTo = %v, want the resolved default to have replaced it", merged.VisibleTo)
	}
}

// The protocol version follows the same unconditional rule, and it is the
// reason the column has no merge semantics of its own: a catalog reports the
// version of the request that LAST wrote it, not of the one that created it.
// Keeping the stored value would make a republish claim a version its own
// envelope never declared, and the mapper has already resolved an absent
// context.version by the time a patch gets here — so there is no "absent" left
// for this function to honour.
func TestARepublishMovesTheProtocolVersion(t *testing.T) {
	stored := storedCatalog()
	stored.ProtocolVersion = "2.0.0"

	patch := defaultedPatch()
	patch.ProtocolVersion = "2.1.0"

	merged, _ := MergeCatalog(stored, patch)

	if merged.ProtocolVersion != "2.1.0" {
		t.Errorf("protocolVersion = %q, want the republishing envelope's 2.1.0", merged.ProtocolVersion)
	}
}

func TestAnAbsentValidityKeepsAllFourColumns(t *testing.T) {
	stored := storedCatalog()

	merged, _ := MergeCatalog(stored, defaultedPatch())

	if !merged.ValidFrom.Equal(stored.ValidFrom) || !merged.ValidTo.Equal(stored.ValidTo) {
		t.Error("an absent validity moved the calendar bounds")
	}
	if merged.ValidTimeFrom == nil || merged.ValidTimeTo == nil {
		t.Error("an absent validity cleared the daily window")
	}
}

// The whole reason TimePeriodPatch holds four independent tri-states rather
// than a *TimePeriod: RFC 7396 permits clearing one bound and keeping another,
// and two independent column pairs make that meaningful.
func TestValidityClearsOneBoundAndKeepsTheOthers(t *testing.T) {
	patch := defaultedPatch()
	patch.Validity = &TimePeriodPatch{
		EndDate: Nullable[time.Time]{Set: true, Null: true},
		EndTime: Nullable[TimeOfDay]{Set: true, Value: TimeOfDay{Hour: 20}},
	}

	merged, _ := MergeCatalog(storedCatalog(), patch)

	if !merged.ValidTo.IsZero() {
		t.Errorf("validTo = %v, want an explicit null to have cleared it", merged.ValidTo)
	}
	if !merged.ValidFrom.Equal(january(1)) {
		t.Error("clearing the end date moved the start date")
	}
	if merged.ValidTimeTo == nil || merged.ValidTimeTo.Hour != 20 {
		t.Errorf("validTimeTo = %v, want 20:00", merged.ValidTimeTo)
	}
	if merged.ValidTimeFrom == nil || merged.ValidTimeFrom.Hour != 9 {
		t.Errorf("validTimeFrom = %v, want the stored 09:00 kept", merged.ValidTimeFrom)
	}
}

// THE pin of this function. An offer moving from r1 to r2 touches BOTH: the
// geometry is stored on the resource, so r1's row can only be deleted by
// visiting r1, and the stored ids are the only record of where that geometry
// currently is. Under the merged reading alone r1 is never visited, its
// resource_geometries row never deleted, and a spatial search keeps returning a
// resource the offer no longer covers — for ever, because no later publish has
// any reason to name r1 either.
func TestAnOfferRelocationTouchesBothItsOldAndNewResources(t *testing.T) {
	patch := defaultedPatch()
	patch.Offers = []OfferPatch{{ID: "o1", ResourceIDs: []string{"r2"}}}

	_, touched := MergeCatalog(storedCatalog(), patch)

	for _, want := range []string{"r1", "r2"} {
		if !slices.Contains(touched, want) {
			t.Errorf("touched = %v, want it to name %s — the union of the offer's ids before and after", touched, want)
		}
	}
}

// A patch that names an offer and no resource at all still has to re-derive the
// resources that offer's geometry is written on. Without this, moving a
// shopfront re-derives the geometry and writes it nowhere.
func TestAnOfferPatchTouchesItsResourcesWithNoResourceNamed(t *testing.T) {
	patch := defaultedPatch()
	patch.Offers = []OfferPatch{{ID: "o1", ResourceIDs: []string{"r1"}, Document: json.RawMessage(`{"price":12}`)}}

	merged, touched := MergeCatalog(storedCatalog(), patch)

	if !slices.Contains(touched, "r1") {
		t.Errorf("touched = %v, want it to name r1", touched)
	}
	if !sameJSON(t, merged.Offers[0].Document, json.RawMessage(`{"price":12,"unit":"kg"}`)) {
		t.Errorf("offer document = %s, want the unit kept and the price replaced", merged.Offers[0].Document)
	}
}

// `touched` is iterated by the repository to decide what to re-derive, so a
// duplicate is a resource embedded twice.
func TestTouchedNamesEachResourceOnce(t *testing.T) {
	patch := defaultedPatch()
	patch.Resources = []ResourcePatch{{ID: "r1", Document: attributePatch("r1", `{"kg":1}`)}}
	patch.Offers = []OfferPatch{{ID: "o1", ResourceIDs: []string{"r1"}}}

	_, touched := MergeCatalog(storedCatalog(), patch)

	if !slices.Equal(touched, []string{"r1"}) {
		t.Errorf("touched = %v, want [r1] exactly once", touched)
	}
}

// A patch touching nothing re-derives nothing. The empty case is what a
// republish of an unchanged catalog looks like.
func TestAPatchNamingNoCollectionTouchesNoResource(t *testing.T) {
	_, touched := MergeCatalog(storedCatalog(), defaultedPatch())

	if len(touched) != 0 {
		t.Errorf("touched = %v, want nothing", touched)
	}
}

// MergeCatalog is a pure function on values, and the stored catalog is what a
// concurrent reader still holds.
func TestMergeCatalogLeavesTheStoredCatalogAlone(t *testing.T) {
	stored := storedCatalog()
	patch := defaultedPatch()
	patch.Resources = []ResourcePatch{{ID: "r1", Document: attributePatch("r1", `{"kg":100}`)}}

	MergeCatalog(stored, patch)

	if got := string(stored.Resources[0].ResourceAttributes()); got != `{"grade":"A","kg":50}` {
		t.Errorf("the stored resource was modified in place: %s", got)
	}
}

// A publisher can send the same id twice in one `resources` array — nothing in
// the schema forbids it, since JSON Schema cannot express uniqueness on a key.
// The second entry must merge into the first, because A8 says resources match
// by id, and "by id" cannot mean two rows under one id.
func TestTwoPatchesForOneNewResourceAreOneResource(t *testing.T) {
	patch := defaultedPatch()
	patch.Resources = []ResourcePatch{
		{ID: "r9", Document: json.RawMessage(`{"id":"r9","descriptor":{"name":"first"}}`)},
		{ID: "r9", Document: attributePatch("r9", `{"colour":"red"}`)},
	}

	merged, _ := MergeCatalog(Catalog{ID: "c1"}, patch)

	if got := len(merged.Resources); got != 1 {
		t.Fatalf("stored %d resources under one id, want 1", got)
	}
	resource := merged.Resources[0]
	if !sameJSON(t, resource.Descriptor(), json.RawMessage(`{"name":"first"}`)) {
		t.Errorf("descriptor = %s, want the first patch's", resource.Descriptor())
	}
	if !sameJSON(t, resource.ResourceAttributes(), json.RawMessage(`{"colour":"red"}`)) {
		t.Errorf("resourceAttributes = %s, want the second patch's", resource.ResourceAttributes())
	}
}

// The same hole on the offers side, and it is worse there: two offers under one
// id means the LATER one's resourceIds never reach `touched` as a before-state,
// so a relocation across the pair loses a geometry row.
func TestTwoPatchesForOneNewOfferAreOneOffer(t *testing.T) {
	patch := defaultedPatch()
	patch.Offers = []OfferPatch{
		{ID: "o9", ResourceIDs: []string{"r1"}},
		{ID: "o9", ResourceIDs: []string{"r2"}},
	}

	merged, touched := MergeCatalog(Catalog{ID: "c1"}, patch)

	if got := len(merged.Offers); got != 1 {
		t.Fatalf("stored %d offers under one id, want 1", got)
	}
	if got := merged.Offers[0].ResourceIDs; !reflect.DeepEqual(got, []string{"r2"}) {
		t.Errorf("ResourceIDs = %v, want the second patch's [r2]", got)
	}
	if !slices.Contains(touched, "r1") || !slices.Contains(touched, "r2") {
		t.Errorf("touched = %v, want both r1 and r2 — the pair is a relocation", touched)
	}
}
