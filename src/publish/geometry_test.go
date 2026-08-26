package publish_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/platform/jsonpath"
	"github.com/OpenAgriNet/discovery-service/src/publish"
)

const point = `{"type":"Point","coordinates":[77.5946,12.9716]}`

// catalogWith builds a merged catalog carrying one provider document, so a test
// only has to say what shape it put where.
func catalogWith(provider string) domain.Catalog {
	return domain.Catalog{ID: "c1", Provider: json.RawMessage(provider)}
}

func typesOf(found []domain.Geometry) []string {
	out := make([]string, 0, len(found))
	for _, g := range found {
		out = append(out, g.Type)
	}
	return out
}

// The assertion the plan calls "the one that catches the empty-200".
//
// A caller asks for geometries at $.catalogs[*].provider.availableAt[*].geo.
// The walker never sees that string — it builds its own from where it landed.
// The two meet only in SQL, as `target_path = ANY($1)`, so if they disagree by
// a single byte the query answers 200 with an empty list and nothing anywhere
// reports a problem.
func TestStoredTargetPathEqualsACallersCanonicalisedTarget(t *testing.T) {
	catalog := catalogWith(`{"availableAt":[{"geo":` + point + `}]}`)

	found, faults := publish.ExtractGeometries(0, catalog)
	if len(faults) != 0 {
		t.Fatalf("faults = %v, want none", faults)
	}
	if len(found) != 1 {
		t.Fatalf("found %d geometries, want 1", len(found))
	}

	want := jsonpath.Canonicalise(`$.catalogs[*].provider.availableAt[*].geo`)
	if found[0].TargetPath != want {
		t.Errorf("TargetPath = %q, want %q", found[0].TargetPath, want)
	}
}

// SourcePath keeps the index that says WHICH availableAt entry this is, and
// wildcards the catalog's own index because that one belongs to the request
// rather than to the catalog.
func TestSourcePathKeepsItsIndexAndWildcardsTheCatalogs(t *testing.T) {
	catalog := catalogWith(`{"availableAt":[{"geo":` + point + `},{"geo":` + point + `}]}`)

	found, _ := publish.ExtractGeometries(7, catalog)
	if len(found) != 2 {
		t.Fatalf("found %d geometries, want 2", len(found))
	}

	want := []string{
		`$['catalogs'][*]['provider']['availableAt'][0]['geo']`,
		`$['catalogs'][*]['provider']['availableAt'][1]['geo']`,
	}
	for i, g := range found {
		if g.SourcePath != want[i] {
			t.Errorf("SourcePath[%d] = %q, want %q", i, g.SourcePath, want[i])
		}
		if g.TargetPath == g.SourcePath {
			t.Errorf("TargetPath and SourcePath agree at %q; the index should have been wildcarded in one of them", g.TargetPath)
		}
	}
}

// Ownership is decided by WHERE the shape sits, not by what the field is
// called. All three cases in one catalog, because the bug this pins is a walker
// that carries one owner set into a sibling subtree.
func TestOwnershipFollowsThePathNotTheFieldName(t *testing.T) {
	catalog := domain.Catalog{
		ID:       "c1",
		Provider: json.RawMessage(`{"availableAt":[{"geo":` + point + `}]}`),
		Resources: []domain.Resource{{
			ID:         "wheat",
			Attributes: json.RawMessage(`{"serviceArea":{"geo":` + point + `}}`),
		}},
		Offers: []domain.Offer{
			{
				ID:          "bulk",
				ResourceIDs: []string{"wheat", "rice"},
				Document:    json.RawMessage(`{"provider":{"availableAt":[{"geo":` + point + `}]}}`),
			},
			{
				ID:       "everything",
				Document: json.RawMessage(`{"provider":{"availableAt":[{"geo":` + point + `}]}}`),
			},
		},
	}

	found, faults := publish.ExtractGeometries(0, catalog)
	if len(faults) != 0 {
		t.Fatalf("faults = %v, want none", faults)
	}
	if len(found) != 4 {
		t.Fatalf("found %d geometries, want 4", len(found))
	}

	owners := map[string][]string{}
	for _, g := range found {
		owners[g.SourcePath] = g.Owners
	}

	cases := []struct {
		what string
		path string
		want []string
	}{
		{"the catalog's own provider", `$['catalogs'][*]['provider']['availableAt'][0]['geo']`, nil},
		{"a resource's attributes", `$['catalogs'][*]['resources'][0]['resourceAttributes']['serviceArea']['geo']`, []string{"wheat"}},
		{"an offer naming resources", `$['catalogs'][*]['offers'][0]['provider']['availableAt'][0]['geo']`, []string{"wheat", "rice"}},
		{"an offer naming none", `$['catalogs'][*]['offers'][1]['provider']['availableAt'][0]['geo']`, nil},
	}
	for _, c := range cases {
		got, ok := owners[c.path]
		if !ok {
			t.Errorf("%s: no geometry at %s; found %v", c.what, c.path, owners)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: Owners = %v, want %v", c.what, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: Owners = %v, want %v", c.what, got, c.want)
				break
			}
		}
	}
}

// An object that merely carries `"type": "Point"` is not a geometry.
//
// This is the reason looksLikeGeoJSON tests two things. Recognising on the type
// name alone would turn any document node with a `type` member into a geometry,
// and then its missing `coordinates` would be reported as MALFORMED — a publish
// partial raised against a node that was never geographic.
func TestATypeNameWithoutItsMemberIsNotAGeometryAndNotAFault(t *testing.T) {
	catalog := catalogWith(`{"rating":{"type":"Point","value":4.5},"geo":` + point + `}`)

	found, faults := publish.ExtractGeometries(0, catalog)
	if len(faults) != 0 {
		t.Fatalf("faults = %v, want none — the rating is not a geometry", faults)
	}
	if len(found) != 1 {
		t.Fatalf("found %d geometries, want 1", len(found))
	}
}

// One bad geometry costs one geometry. It is named, it is a partial, and the
// catalog still publishes everything else.
func TestOneMalformedGeometryCostsOneGeometry(t *testing.T) {
	broken := `{"type":"Polygon","coordinates":[[[0,0],[1]]]}`
	catalog := catalogWith(`{"availableAt":[{"geo":` + broken + `},{"geo":` + point + `}]}`)

	found, faults := publish.ExtractGeometries(0, catalog)
	if len(found) != 1 {
		t.Fatalf("found %d geometries, want 1 — the good one survives", len(found))
	}
	if len(faults) != 1 {
		t.Fatalf("faults = %v, want exactly 1", faults)
	}

	const want = `$['catalogs'][0]['provider']['availableAt'][0]['geo']`
	if faults[0].Path != want {
		t.Errorf("fault Path = %q, want %q", faults[0].Path, want)
	}
	if !strings.Contains(faults[0].Message, "Polygon") {
		t.Errorf("fault Message = %q, want it to name the offending value", faults[0].Message)
	}
}

// A GeometryCollection is ONE find.
//
// Its members are part of it, not siblings of it, so the walk stops at the
// collection. Descending would store the same shapes twice and let a caller
// targeting the collection's path and a caller targeting a member's path get
// different counts for the same ground.
func TestAGeometryCollectionIsOneFind(t *testing.T) {
	collection := `{"type":"GeometryCollection","geometries":[` + point + `,` + point + `]}`
	catalog := catalogWith(`{"geo":` + collection + `}`)

	found, faults := publish.ExtractGeometries(0, catalog)
	if len(faults) != 0 {
		t.Fatalf("faults = %v, want none", faults)
	}
	if len(found) != 1 {
		t.Fatalf("found %v, want exactly one GeometryCollection", typesOf(found))
	}
	if found[0].Type != "GeometryCollection" {
		t.Errorf("Type = %q, want GeometryCollection", found[0].Type)
	}
}

// Every one of the seven types is indexed, not just Point.
//
// The reference implementation keeps only points, which is how a publisher's
// service polygon becomes invisible to the geo search that was the reason they
// drew it.
func TestANonPointGeometryIsIndexed(t *testing.T) {
	polygon := `{"type":"Polygon","coordinates":[[[77.5,12.9],[77.7,12.9],[77.7,13.1],[77.5,13.1],[77.5,12.9]]]}`
	catalog := catalogWith(`{"serviceArea":{"geo":` + polygon + `}}`)

	found, faults := publish.ExtractGeometries(0, catalog)
	if len(faults) != 0 {
		t.Fatalf("faults = %v, want none", faults)
	}
	if len(found) != 1 || found[0].Type != "Polygon" {
		t.Fatalf("found %v, want one Polygon", typesOf(found))
	}
	if string(found[0].GeoJSON) != polygon {
		t.Errorf("GeoJSON = %s, want the bytes as published", found[0].GeoJSON)
	}
}

// The depth bound is a bound, not a hope: a document nested past it terminates
// rather than recursing until the stack does.
func TestAWalkPastTheDepthBoundTerminates(t *testing.T) {
	var deep strings.Builder
	const levels = publish.MaxCatalogWalkDepth + 10
	for i := 0; i < levels; i++ {
		deep.WriteString(`{"down":`)
	}
	deep.WriteString(point)
	deep.WriteString(strings.Repeat("}", levels))

	found, _ := publish.ExtractGeometries(0, catalogWith(deep.String()))
	if len(found) != 0 {
		t.Errorf("found %d geometries past the bound, want none", len(found))
	}
}

// The geometry over the ceiling is NAMED. A silent drop would publish a catalog
// reporting success while some of its shapes match nothing, which is
// indistinguishable from a publisher's own mistake.
func TestTheGeometryOverTheBudgetIsANamedPartial(t *testing.T) {
	entries := make([]string, 0, publish.MaxGeometriesPerCatalog+1)
	for i := 0; i <= publish.MaxGeometriesPerCatalog; i++ {
		entries = append(entries, `{"geo":`+point+`}`)
	}
	catalog := catalogWith(`{"availableAt":[` + strings.Join(entries, ",") + `]}`)

	found, faults := publish.ExtractGeometries(0, catalog)
	if len(found) != publish.MaxGeometriesPerCatalog {
		t.Fatalf("found %d geometries, want %d", len(found), publish.MaxGeometriesPerCatalog)
	}
	if len(faults) != 1 {
		t.Fatalf("faults = %d, want exactly 1 for the one geometry over", len(faults))
	}

	want := fmt.Sprintf(`$['catalogs'][0]['provider']['availableAt'][%d]['geo']`, publish.MaxGeometriesPerCatalog)
	if faults[0].Path != want {
		t.Errorf("fault Path = %q, want %q", faults[0].Path, want)
	}
	if faults[0].Code != "POL_GENERIC_ERROR" {
		t.Errorf("fault Code = %q, want POL_GENERIC_ERROR", faults[0].Code)
	}
}
