package conformance

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// network is the one network id every publish fixture below publishes into.
//
// Spelled once because two of these cases turn on the empty-VisibleTo fail-safe
// resolving to exactly this value, and a fixture carrying its own copy would
// pass against a backend that defaulted to something else entirely.
const network = "bap.example.com"

// PublishCases is the write-path suite: everything UpsertCatalog must do that
// can be seen through the ports.
//
// Everything it must do that CANNOT be seen through the ports — that a touched
// resource is written once rather than twice, that the statement count does not
// grow with the catalog — is a Postgres test in tests/dbtest, because xmin and
// a round-trip counter are not things a port has.
func PublishCases() []Case {
	cases := []Case{
		deriveWritesReachTheStore(),
		mergeKeepsAnOmittedResource(),
		fullRemovesAnOmittedResource(),
		anEmptyVisibleToBecomesTheNetwork(),
		theUpsertReturnsTheStoredRowOnConflict(),
		fieldLevelMergeSurvivesTheRoundTrip(),
		derivationRunsAfterTheMerge(),
		fullResetsTheCatalogRowItself(),
		theGateReachesEveryResource(),
		republishingReplacesAGeometry(),
		providerLocationsAreStoredOnceForTheCatalog(),
		aDanglingOfferReferenceIsANamedPartial(),
		anOfferPrunedToEmptyIsNotWritten(),
		deletingACatalogRemovesEverythingUnderIt(),
	}
	return cases
}

// ---------------------------------------------------------------------------
// fixture builders
// ---------------------------------------------------------------------------

// catalogPatch is the minimum a publish needs: an id, a network and the two
// A9-resolved fields the mapper has already defaulted by the time a patch
// reaches the repository.
func catalogPatch(id string, resources ...domain.ResourcePatch) domain.CatalogPatch {
	return domain.CatalogPatch{
		ID:        id,
		NetworkID: network,
		Active:    true,
		VisibleTo: []string{network},
		Resources: resources,
	}
}

// resourcePatch is one resource carrying an attributes document and nothing
// else, which is the shape most of these cases care about.
//
// The attributes are nested under `resourceAttributes` inside the resource
// document rather than standing alone, because since A17 that is where they
// live — and RFC 7396 merges recursively, so a case patching one leaf two
// levels down still means what it meant when the column was flat.
func resourcePatch(id, attributes string) domain.ResourcePatch {
	return domain.ResourcePatch{
		ID:       id,
		Document: json.RawMessage(`{"id":"` + id + `","resourceAttributes":` + attributes + `}`),
	}
}

// noDerive is the derive a case supplies when it is not testing derivation.
//
// Explicitly nil-returning rather than a nil DeriveFunc, so that every case
// exercises the same call path: a backend that skipped a nil derive and a
// backend that ran one would both pass a suite where half the fixtures passed
// nil, and they would disagree the moment a real one arrived.
func noDerive(*domain.Catalog, []string) []domain.Fault { return nil }

// deriveGeometries is a stand-in for Task 17's walker: it puts geometry on the
// merged catalog the way ExtractGeometries will, without depending on a walker
// that does not exist yet.
//
// It writes through the pointer, which is the entire point of the seam — see
// A15. Owners empty means catalog-level, so these land once with a NULL
// resource id however many resources the catalog holds.
func deriveGeometries(geometries ...domain.Geometry) domain.DeriveFunc {
	return func(merged *domain.Catalog, _ []string) []domain.Fault {
		merged.Geometries = geometries
		return nil
	}
}

// mustTime parses an RFC 3339 instant a fixture spelled as a literal.
//
// Panics rather than returning an error: these are constants in the source of
// this file, so a failure here is a typo in the fixture, not a condition a case
// could be written to handle.
func mustTime(literal string) time.Time {
	instant, err := time.Parse(time.RFC3339, literal)
	if err != nil {
		panic("fixture carries an unparseable time " + literal + ": " + err.Error())
	}
	return instant
}

// resourceByID finds a stored resource, failing the case rather than returning
// a zero value: every assertion below reads a field off the result, and a zero
// Resource answers every one of them with something plausible.
func resourceByID(t *testing.T, resources []domain.Resource, id string) domain.Resource {
	t.Helper()

	for _, resource := range resources {
		if resource.ID == id {
			return resource
		}
	}

	stored := make([]string, 0, len(resources))
	for _, resource := range resources {
		stored = append(stored, resource.ID)
	}
	t.Fatalf("no resource %q in the catalog; it holds %v", id, stored)
	return domain.Resource{}
}

// resourceIDs is the stored ids in order, for the cases asserting membership
// rather than content.
func resourceIDs(resources []domain.Resource) []string {
	ids := make([]string, 0, len(resources))
	for _, resource := range resources {
		ids = append(ids, resource.ID)
	}
	slices.Sort(ids)
	return ids
}

// mustGet reads a catalog back, failing the case if it is not there.
func mustGet(t *testing.T, backends Backends, id string) domain.Catalog {
	t.Helper()

	stored, err := backends.Catalogs.GetCatalog(t.Context(), id)
	if err != nil {
		t.Fatalf("reading catalog %q back: %v", id, err)
	}
	return stored
}

// ---------------------------------------------------------------------------
// the cases
// ---------------------------------------------------------------------------

// The derive seam itself, asserted before anything that depends on it.
//
// derive is where search text, embeddings and geometry are computed (A8), and
// every one of those is a WRITE onto the merged catalog rather than a value
// returned — DeriveFunc returns only faults. So a backend whose derive cannot
// write is a backend that stores an underived catalog while reporting success:
// no tsvector, no hash, no geometry rows, and no error anywhere.
//
// This is first in the list because six cases below assert something derive
// produced, and all six would fail with the same confusing symptom.
func deriveWritesReachTheStore() Case {
	geometry := PointGeometryAt(0, domain.GeoPoint{Lat: 12.97, Lon: 77.64})

	return Case{
		Name: "what derive writes onto the merged catalog is what gets stored",
		Given: []Publish{{
			Patch: catalogPatch("c1", resourcePatch("r1", `{"grade":"A"}`)),
			Mode:  domain.UpdateModeMerge,
			Derive: func(merged *domain.Catalog, touched []string) []domain.Fault {
				// Catalog-level: reachable only through the pointer, because a
				// slice field assignment on a value copy dies with the copy.
				merged.Geometries = []domain.Geometry{geometry}

				// Per-resource: reachable either way, since a slice element
				// write goes through the shared backing array. Both are
				// asserted, so a backend that made only the second work is
				// still caught.
				for index := range merged.Resources {
					if !slices.Contains(touched, merged.Resources[index].ID) {
						continue
					}
					merged.Resources[index].SearchText = "grade A"
					merged.Resources[index].EmbeddingSourceHash = []byte{0xde, 0xad}
				}
				return nil
			},
		}},
		Then: func(t *testing.T, backends Backends) {
			stored := mustGet(t, backends, "c1")

			if len(stored.Geometries) != 1 {
				t.Fatalf("the catalog stored %d geometries, want 1 — derive's write to "+
					"merged.Geometries did not reach the store", len(stored.Geometries))
			}
			if stored.Geometries[0].SourcePath != geometry.SourcePath {
				t.Errorf("stored geometry is %q, want %q",
					stored.Geometries[0].SourcePath, geometry.SourcePath)
			}

			resource := resourceByID(t, stored.Resources, "r1")
			if !slices.Equal(resource.EmbeddingSourceHash, []byte{0xde, 0xad}) {
				t.Errorf("r1 stored the hash %x, want dead — a NULL hash makes the Phase 2 "+
					"backfill unable to tell a stale embedding from a missing one",
					resource.EmbeddingSourceHash)
			}
		},
	}
}

// Scenario 2: MERGE is an update, not a replacement.
func mergeKeepsAnOmittedResource() Case {
	return Case{
		Name: "a MERGE republish keeps a resource the payload does not name",
		Given: []Publish{
			{
				Patch: catalogPatch("c1",
					resourcePatch("r1", `{"grade":"A"}`),
					resourcePatch("r2", `{"grade":"B"}`)),
				Mode:   domain.UpdateModeMerge,
				Derive: noDerive,
			},
			{
				Patch:  catalogPatch("c1", resourcePatch("r1", `{"grade":"A+"}`)),
				Mode:   domain.UpdateModeMerge,
				Derive: noDerive,
			},
		},
		Then: func(t *testing.T, backends Backends) {
			stored := mustGet(t, backends, "c1")

			if got := resourceIDs(stored.Resources); !slices.Equal(got, []string{"r1", "r2"}) {
				t.Fatalf("after a MERGE republish naming only r1 the catalog holds %v, "+
					"want [r1 r2] — MERGE deletes nothing", got)
			}
		},
	}
}

// Scenario 3's other half: FULL is a replacement, and the deletion is the whole
// difference between the two modes.
func fullRemovesAnOmittedResource() Case {
	return Case{
		Name: "a FULL republish removes a resource the payload does not name",
		Given: []Publish{
			{
				Patch: catalogPatch("c1",
					resourcePatch("r1", `{"grade":"A"}`),
					resourcePatch("r2", `{"grade":"B"}`)),
				Mode:   domain.UpdateModeMerge,
				Derive: noDerive,
			},
			{
				Patch:  catalogPatch("c1", resourcePatch("r1", `{"grade":"A"}`)),
				Mode:   domain.UpdateModeFull,
				Derive: noDerive,
			},
		},
		Then: func(t *testing.T, backends Backends) {
			stored := mustGet(t, backends, "c1")

			if got := resourceIDs(stored.Resources); !slices.Equal(got, []string{"r1"}) {
				t.Fatalf("after a FULL republish naming only r1 the catalog holds %v, want [r1]", got)
			}
		},
	}
}

// The fail-safe: a catalog visible to nobody is findable by nobody while
// reporting success, so the writer fills an empty list with the request's own
// network. The mapper already did this (A9); this pins the belt-and-braces copy
// in the repository, which is the one that survives a mapper change.
func anEmptyVisibleToBecomesTheNetwork() Case {
	patch := catalogPatch("c1", resourcePatch("r1", `{"grade":"A"}`))
	patch.VisibleTo = nil

	return Case{
		Name:  "an empty visibleTo is filled with the publishing network",
		Given: []Publish{{Patch: patch, Mode: domain.UpdateModeMerge, Derive: noDerive}},
		Then: func(t *testing.T, backends Backends) {
			stored := mustGet(t, backends, "c1")

			if !slices.Equal(stored.VisibleTo, []string{network}) {
				t.Errorf("the catalog is visible to %v, want [%s] — a catalog visible to "+
					"nobody reports success and is findable by no one", stored.VisibleTo, network)
			}
			if resource := resourceByID(t, stored.Resources, "r1"); !slices.Equal(resource.VisibleTo, []string{network}) {
				t.Errorf("r1 is visible to %v, want [%s]; discover reads the copy on the "+
					"resource and never joins to the catalog", resource.VisibleTo, network)
			}
		},
	}
}

// The lock-and-load upsert has to RETURN the stored row on conflict.
//
// `ON CONFLICT DO NOTHING` returns zero rows, which would make every republish
// a merge against an empty document — and would pass every MERGE case that
// publishes only once.
func theUpsertReturnsTheStoredRowOnConflict() Case {
	first := catalogPatch("c1")
	first.Document = json.RawMessage(
		`{"id":"c1","provider":{"name":"Anitha Farms","gstin":"29ABCDE"}}`)

	// A republish that says nothing about the provider at all.
	second := catalogPatch("c1", resourcePatch("r1", `{"grade":"A"}`))

	return Case{
		Name: "a republish merges against the stored catalog, not against an empty one",
		Given: []Publish{
			{Patch: first, Mode: domain.UpdateModeMerge, Derive: noDerive},
			{Patch: second, Mode: domain.UpdateModeMerge, Derive: noDerive},
		},
		Then: func(t *testing.T, backends Backends) {
			stored := mustGet(t, backends, "c1")

			var provider struct {
				Name  string `json:"name"`
				GSTIN string `json:"gstin"`
			}
			if err := json.Unmarshal(stored.Provider(), &provider); err != nil {
				t.Fatalf("the stored provider is not an object: %v (%s)", err, stored.Provider())
			}
			if provider.Name != "Anitha Farms" || provider.GSTIN != "29ABCDE" {
				t.Errorf("after a republish carrying no provider the catalog holds %s, want the "+
					"first publish's provider — the upsert returned no row to merge against",
					stored.Provider())
			}
		},
	}
}

// A8 at the column, not at the function. The domain test proves MergePatch;
// this proves the merged document is what the column ends up holding.
func fieldLevelMergeSurvivesTheRoundTrip() Case {
	return Case{
		Name: "a field-level MERGE keeps, replaces and deletes the right attributes",
		Given: []Publish{
			{
				Patch: catalogPatch("c1", resourcePatch("r1",
					`{"grade":"A","moisture":"12%","origin":"Kolar"}`)),
				Mode:   domain.UpdateModeMerge,
				Derive: noDerive,
			},
			{
				Patch: catalogPatch("c1", resourcePatch("r1",
					`{"moisture":"9%","origin":null}`)),
				Mode:   domain.UpdateModeMerge,
				Derive: noDerive,
			},
		},
		Then: func(t *testing.T, backends Backends) {
			stored := mustGet(t, backends, "c1")
			resource := resourceByID(t, stored.Resources, "r1")

			var attributes map[string]any
			if err := json.Unmarshal(resource.ResourceAttributes(), &attributes); err != nil {
				t.Fatalf("the stored attributes are not an object: %v (%s)",
					err, resource.ResourceAttributes())
			}

			if attributes["grade"] != "A" {
				t.Errorf("grade is %v, want A — a key the patch never named must stand", attributes["grade"])
			}
			if attributes["moisture"] != "9%" {
				t.Errorf("moisture is %v, want 9%%", attributes["moisture"])
			}
			if _, present := attributes["origin"]; present {
				t.Errorf("origin is still present as %v; an explicit null deletes the key",
					attributes["origin"])
			}
		},
	}
}

// derive runs POST-merge, and the proof is that it sees a field the patch never
// carried. A derive running on the patch would see an empty descriptor here.
func derivationRunsAfterTheMerge() Case {
	first := catalogPatch("c1")
	first.Resources = []domain.ResourcePatch{{
		ID: "r1",
		Document: json.RawMessage(
			`{"id":"r1","descriptor":{"name":"Alphonso mangoes"},"resourceAttributes":{"grade":"A"}}`),
	}}

	// Touches attributes only. The descriptor is absent, so it survives the
	// merge — and derive must see the SURVIVOR.
	second := catalogPatch("c1", resourcePatch("r1", `{"moisture":"9%"}`))

	return Case{
		Name: "derive sees the merged document, not the patch",
		Given: []Publish{
			{Patch: first, Mode: domain.UpdateModeMerge, Derive: recordDescriptorName()},
			{Patch: second, Mode: domain.UpdateModeMerge, Derive: recordDescriptorName()},
		},
		Then: func(t *testing.T, backends Backends) {
			stored := mustGet(t, backends, "c1")
			resource := resourceByID(t, stored.Resources, "r1")

			if resource.Name != "Alphonso mangoes" {
				t.Errorf("derive read the name %q on a patch that carried no descriptor, want "+
					"%q — it ran against the patch rather than the merge result",
					resource.Name, "Alphonso mangoes")
			}
		},
	}
}

// recordDescriptorName is the smallest derivation that can tell a patch from a
// merge result: it reads a field only the STORED document has and writes it to
// a column, so the assertion is on what the column holds.
func recordDescriptorName() domain.DeriveFunc {
	return func(merged *domain.Catalog, touched []string) []domain.Fault {
		for index := range merged.Resources {
			if !slices.Contains(touched, merged.Resources[index].ID) {
				continue
			}
			var descriptor struct {
				Name string `json:"name"`
			}
			// A resource with no descriptor at all decodes as "unexpected end
			// of JSON input", which is not a fixture problem — it is the
			// ordinary shape of a patch that carried only attributes. It
			// contributes no name.
			if err := json.Unmarshal(merged.Resources[index].Descriptor(), &descriptor); err != nil {
				continue
			}
			merged.Resources[index].Name = descriptor.Name
		}
		return nil
	}
}

// Under FULL an omission is a RESET, and that has to reach the catalog's own
// columns and not just its resources — a carried-forward validity would keep a
// withdrawn seasonal catalog live for another year.
func fullResetsTheCatalogRowItself() Case {
	from := mustTime("2026-01-01T00:00:00Z")
	to := mustTime("2026-03-31T00:00:00Z")

	dated := catalogPatch("c1", resourcePatch("r1", `{"grade":"A"}`))
	dated.Validity = &domain.TimePeriodPatch{
		StartDate: domain.Nullable[time.Time]{Value: from, Set: true},
		EndDate:   domain.Nullable[time.Time]{Value: to, Set: true},
		StartTime: domain.Nullable[domain.TimeOfDay]{Value: domain.TimeOfDay{Hour: 9}, Set: true},
		EndTime:   domain.Nullable[domain.TimeOfDay]{Value: domain.TimeOfDay{Hour: 17}, Set: true},
	}

	// No validity at all, under FULL.
	reset := catalogPatch("c1", resourcePatch("r1", `{"grade":"A"}`))

	return Case{
		Name: "a FULL republish without a validity clears all four validity columns",
		Given: []Publish{
			{Patch: dated, Mode: domain.UpdateModeMerge, Derive: noDerive},
			{Patch: reset, Mode: domain.UpdateModeFull, Derive: noDerive},
		},
		Then: func(t *testing.T, backends Backends) {
			stored := mustGet(t, backends, "c1")

			if !stored.ValidFrom.IsZero() || !stored.ValidTo.IsZero() {
				t.Errorf("the calendar window is %v–%v, want both cleared; under FULL an "+
					"omission is a reset, not a carry-forward", stored.ValidFrom, stored.ValidTo)
			}
			if stored.ValidTimeFrom != nil || stored.ValidTimeTo != nil {
				t.Errorf("the daily window is %v–%v, want both cleared",
					stored.ValidTimeFrom, stored.ValidTimeTo)
			}
		},
	}
}

// The statement that makes the denormalised gate safe, tested on the publish
// that makes it necessary: one carrying NO resources at all.
//
// All six columns, because Resource has no validity of its own — a column this
// UPDATE forgets keeps yesterday's value forever, and no later publish can
// correct it.
func theGateReachesEveryResource() Case {
	seeded := catalogPatch("c1",
		resourcePatch("r1", `{"grade":"A"}`),
		resourcePatch("r2", `{"grade":"B"}`))

	// A catalog-only republish: no resources, a narrowed audience, and a
	// validity where there was none.
	narrowed := catalogPatch("c1")
	narrowed.VisibleTo = []string{"other.example.com"}
	narrowed.Active = false
	narrowed.Validity = &domain.TimePeriodPatch{
		StartDate: domain.Nullable[time.Time]{Value: mustTime("2026-06-01T00:00:00Z"), Set: true},
		StartTime: domain.Nullable[domain.TimeOfDay]{Value: domain.TimeOfDay{Hour: 6}, Set: true},
		EndTime:   domain.Nullable[domain.TimeOfDay]{Value: domain.TimeOfDay{Hour: 18}, Set: true},
	}

	return Case{
		Name: "a republish carrying no resources still moves the gate on every resource",
		Given: []Publish{
			{Patch: seeded, Mode: domain.UpdateModeMerge, Derive: noDerive},
			{Patch: narrowed, Mode: domain.UpdateModeMerge, Derive: noDerive},
		},
		Then: func(t *testing.T, backends Backends) {
			stored := mustGet(t, backends, "c1")

			if len(stored.Resources) != 2 {
				t.Fatalf("the catalog holds %d resources, want 2", len(stored.Resources))
			}
			for _, resource := range stored.Resources {
				if !slices.Equal(resource.VisibleTo, []string{"other.example.com"}) {
					t.Errorf("%s is visible to %v, want [other.example.com] — the payload named "+
						"no resources, which is the case this propagate exists for",
						resource.ID, resource.VisibleTo)
				}
				if resource.Active {
					t.Errorf("%s is still active", resource.ID)
				}
				if resource.ValidFrom.IsZero() {
					t.Errorf("%s has no valid_from; the calendar window did not propagate", resource.ID)
				}
				if resource.ValidTimeFrom == nil || resource.ValidTimeTo == nil {
					t.Errorf("%s has the daily window %v–%v, want 06:00–18:00 — these are the two "+
						"columns a propagate is most likely to forget, and Resource carries no "+
						"validity of its own for a later publish to correct them with",
						resource.ID, resource.ValidTimeFrom, resource.ValidTimeTo)
				}
			}
		},
	}
}

// sameJSON compares two documents by value rather than by byte.
//
// A jsonb column stores a PARSED value, not the text it was sent: whitespace
// goes, keys are reordered and numbers are renormalised, so a byte comparison
// would fail against PostgreSQL for a document it stored perfectly. Comparing
// decoded values is what the suite actually means by "the same document", and
// it is a comparison both backends can pass honestly.
func sameJSON(t *testing.T, got, want json.RawMessage) bool {
	t.Helper()

	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("the stored document is not JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("the expected document is not JSON: %v (%s)", err, want)
	}
	return reflect.DeepEqual(gotValue, wantValue)
}

// Geometry rows are REPLACED, never merged: a geometry has no id, so there is
// nothing to key an identity merge on. A republish that moves a shopfront must
// leave one row, not two.
func republishingReplacesAGeometry() Case {
	first := PointGeometryAt(0, domain.GeoPoint{Lat: 12.97, Lon: 77.64})
	moved := PointGeometryAt(0, domain.GeoPoint{Lat: 13.20, Lon: 77.70})

	return Case{
		Name: "a republish replaces a geometry rather than accumulating it",
		Given: []Publish{
			{
				Patch:  catalogPatch("c1", resourcePatch("r1", `{"grade":"A"}`)),
				Mode:   domain.UpdateModeMerge,
				Derive: deriveGeometries(first),
			},
			{
				Patch:  catalogPatch("c1", resourcePatch("r1", `{"grade":"A"}`)),
				Mode:   domain.UpdateModeMerge,
				Derive: deriveGeometries(moved),
			},
		},
		Then: func(t *testing.T, backends Backends) {
			stored := mustGet(t, backends, "c1")

			if len(stored.Geometries) != 1 {
				t.Fatalf("the catalog holds %d geometries, want 1 — a republish replaces "+
					"geometry rows, it does not accumulate them", len(stored.Geometries))
			}
			if !sameJSON(t, stored.Geometries[0].GeoJSON, moved.GeoJSON) {
				t.Errorf("the stored geometry is %s, want %s",
					stored.Geometries[0].GeoJSON, moved.GeoJSON)
			}
		},
	}
}

// Three provider locations across forty resources are three rows, not 120.
//
// The locations belong to the CATALOG. Attaching them to each resource was the
// reference implementation's shape and it multiplies both the row count and the
// H3 fill count by the size of the catalog.
func providerLocationsAreStoredOnceForTheCatalog() Case {
	locations := PointGeometries(
		domain.GeoPoint{Lat: 12.97, Lon: 77.64},
		domain.GeoPoint{Lat: 13.08, Lon: 77.58},
		domain.GeoPoint{Lat: 12.90, Lon: 77.61},
	)

	resources := make([]domain.ResourcePatch, 0, 40)
	for index := range 40 {
		resources = append(resources, resourcePatch(resourceName(index), `{"grade":"A"}`))
	}

	return Case{
		Name: "three provider locations across forty resources are three catalog-level geometries",
		Given: []Publish{{
			Patch:  catalogPatch("c1", resources...),
			Mode:   domain.UpdateModeMerge,
			Derive: deriveGeometries(locations...),
		}},
		Then: func(t *testing.T, backends Backends) {
			stored := mustGet(t, backends, "c1")

			if len(stored.Geometries) != 3 {
				t.Fatalf("the catalog holds %d geometries, want 3 — provider locations are "+
					"covered once for the catalog, not once per resource (which would be 120)",
					len(stored.Geometries))
			}
			for _, resource := range stored.Resources {
				if len(resource.Geometries) != 0 {
					t.Errorf("%s carries %d geometries of its own, want 0; the provider's "+
						"locations belong to the catalog", resource.ID, len(resource.Geometries))
				}
			}
		},
	}
}

// resourceName numbers the forty resources above so their ids sort readably.
func resourceName(index int) string {
	return "r" + string(rune('a'+index/10)) + string(rune('0'+index%10))
}

// resource_ids has no foreign key, so a typo stores an offer attached to
// nothing and reports success. Named as a PARTIAL rather than pruned in
// silence.
func aDanglingOfferReferenceIsANamedPartial() Case {
	patch := catalogPatch("c1", resourcePatch("r1", `{"grade":"A"}`))
	patch.Offers = []domain.OfferPatch{{
		ID:          "o1",
		Document:    json.RawMessage(`{"price":{"value":"100"}}`),
		ResourceIDs: []string{"r1", "typo"},
	}}

	return Case{
		Name: "an offer naming a missing resource is a named partial and keeps the ids that exist",
		Given: []Publish{{
			Patch:          patch,
			Mode:           domain.UpdateModeMerge,
			Derive:         noDerive,
			WantFaultCodes: []string{string(beckn.CodeBusinessItemNotFound)},
		}},
		Then: func(t *testing.T, backends Backends) {
			stored := mustGet(t, backends, "c1")

			if len(stored.Offers) != 1 {
				t.Fatalf("the catalog holds %d offers, want 1 — the catalog still lands, "+
					"this is a PARTIAL", len(stored.Offers))
			}
			if got := stored.Offers[0].ResourceIDs; !slices.Equal(got, []string{"r1"}) {
				t.Errorf("the offer is stored against %v, want [r1] — the dangling id is "+
					"pruned, but only after being named", got)
			}
		},
	}
}

// The degenerate case, and the more dangerous one: an offer pruned to EMPTY
// must not be written at all, because empty means CATALOG-WIDE. Writing it
// would promote a one-resource offer to the provider's entire inventory.
func anOfferPrunedToEmptyIsNotWritten() Case {
	patch := catalogPatch("c1", resourcePatch("r1", `{"grade":"A"}`))
	patch.Offers = []domain.OfferPatch{{
		ID:          "o1",
		Document:    json.RawMessage(`{"price":{"value":"100"}}`),
		ResourceIDs: []string{"typo"},
	}}

	return Case{
		Name: "an offer whose every resource id is a typo is not written at all",
		Given: []Publish{{
			Patch:          patch,
			Mode:           domain.UpdateModeMerge,
			Derive:         noDerive,
			WantFaultCodes: []string{string(beckn.CodeBusinessItemNotFound)},
		}},
		Then: func(t *testing.T, backends Backends) {
			stored := mustGet(t, backends, "c1")

			if len(stored.Offers) != 0 {
				t.Fatalf("the catalog holds %d offers, want 0 — an offer pruned to empty would "+
					"read as CATALOG-WIDE, attaching it to the provider's whole inventory",
					len(stored.Offers))
			}
		},
	}
}

// Deleting a catalog takes its resources, offers and geometries with it.
func deletingACatalogRemovesEverythingUnderIt() Case {
	patch := catalogPatch("c1", resourcePatch("r1", `{"grade":"A"}`))
	patch.Offers = []domain.OfferPatch{{
		ID:          "o1",
		Document:    json.RawMessage(`{"price":{"value":"100"}}`),
		ResourceIDs: []string{"r1"},
	}}

	return Case{
		Name: "deleting a catalog removes its resources, offers and geometries",
		Given: []Publish{{
			Patch:  patch,
			Mode:   domain.UpdateModeMerge,
			Derive: deriveGeometries(PointGeometryAt(0, domain.GeoPoint{Lat: 12.97, Lon: 77.64})),
		}},
		Then: func(t *testing.T, backends Backends) {
			if err := backends.Catalogs.DeleteCatalog(t.Context(), "c1"); err != nil {
				t.Fatalf("deleting c1: %v", err)
			}

			if _, err := backends.Catalogs.GetCatalog(t.Context(), "c1"); err == nil {
				t.Fatal("the catalog is still readable after a delete")
			}

			// Idempotent: a publisher retrying a delete it already completed is
			// ordinary, and a store that failed the second attempt would make
			// the retry the thing that reports a problem.
			if err := backends.Catalogs.DeleteCatalog(t.Context(), "c1"); err != nil {
				t.Errorf("deleting c1 a second time: %v, want nil — delete is idempotent", err)
			}
		},
	}
}
