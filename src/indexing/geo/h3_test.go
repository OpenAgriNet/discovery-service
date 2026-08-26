package geo_test

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	h3 "github.com/uber/h3-go/v4"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
)

// res is the resolution every test here covers at. It matches the shipped
// default of GEO_RESOLUTION_CELLS rather than reading config, because these
// tests pin geometry and a deployment that retunes the resolution must not
// silently change what they assert.
const res = 8

// shaped builds a stored geometry from a GeoJSON body, the way the publish
// walker hands them over.
func shaped(kind, body string) domain.Geometry {
	return domain.Geometry{Type: kind, GeoJSON: json.RawMessage(body)}
}

// ring writes a closed square ring of the given half-width, in GeoJSON's
// [lon, lat] order.
func ring(at domain.GeoPoint, half float64) string {
	corners := [][2]float64{
		{at.Lon - half, at.Lat - half}, {at.Lon + half, at.Lat - half},
		{at.Lon + half, at.Lat + half}, {at.Lon - half, at.Lat + half},
		{at.Lon - half, at.Lat - half},
	}
	parts := make([]string, 0, len(corners))
	for _, corner := range corners {
		parts = append(parts, fmt.Sprintf("[%g,%g]", corner[0], corner[1]))
	}
	return "[" + parts[0] + "," + parts[1] + "," + parts[2] + "," + parts[3] + "," + parts[4] + "]"
}

func squarePolygon(at domain.GeoPoint, half float64) domain.Geometry {
	return shaped("Polygon", `{"type":"Polygon","coordinates":[`+ring(at, half)+`]}`)
}

// cellAt is the cell a coordinate falls in, computed through H3 directly so the
// expectation does not come from the code under test.
func cellAt(t *testing.T, at domain.GeoPoint) uint64 {
	t.Helper()
	cell, err := h3.LatLngToCell(h3.LatLng{Lat: at.Lat, Lng: at.Lon}, res)
	if err != nil {
		t.Fatalf("LatLngToCell(%v): %v", at, err)
	}
	return uint64(cell)
}

// subset is the test's own containment check, written independently of the one
// in the package so the sandwich property is not asserted with the same code it
// is asserting about.
func subset(inner, outer []uint64) bool {
	for _, cell := range inner {
		if !slices.Contains(outer, cell) {
			return false
		}
	}
	return true
}

func sortedAndUnique(cells []uint64) bool {
	for index := 1; index < len(cells); index++ {
		if cells[index] <= cells[index-1] {
			return false
		}
	}
	return true
}

// The single fact a lon/lat swap violates, and the only one that is silent when
// broken: both values stay in range, so nothing rejects a Bengaluru shopfront
// indexed off the coast of Somalia.
func TestAPointsCoverContainsItsOwnCellReadingLongitudeFirst(t *testing.T) {
	cover, err := geo.CoverGeometry(point(bengaluru), res)
	if err != nil {
		t.Fatalf("CoverGeometry: %v", err)
	}

	if !slices.Contains(cover.CellsCover, cellAt(t, bengaluru)) {
		t.Error("a Point's cover does not contain its own cell")
	}
	swapped := domain.GeoPoint{Lat: bengaluru.Lon, Lon: bengaluru.Lat}
	if slices.Contains(cover.CellsCover, cellAt(t, swapped)) {
		t.Error("the cover holds the lat/lon-swapped cell; GeoJSON is [lon, lat]")
	}
}

// The invariant everything else rests on, asserted directly rather than
// inferred from the operators that use it: cells_full ⊆ the true geometry ⊆
// cells_cover. A property over generated polygons, because a single fixture
// would pin one shape and this is a claim about all of them.
func TestFullIsAlwaysASubsetOfCover(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // fixture generation, not cryptography

	for attempt := range 40 {
		at := domain.GeoPoint{Lat: random.Float64()*60 - 30, Lon: random.Float64() * 120}
		half := 0.001 + random.Float64()*0.2

		cover, err := geo.CoverGeometry(squarePolygon(at, half), res)
		if err != nil {
			t.Fatalf("attempt %d at %v half %g: %v", attempt, at, half, err)
		}
		if !subset(cover.CellsFull, cover.CellsCover) {
			t.Fatalf("attempt %d at %v half %g: full is not a subset of cover", attempt, at, half)
		}
		if !sortedAndUnique(cover.CellsFull) || !sortedAndUnique(cover.CellsCover) {
			t.Fatalf("attempt %d: covers are not sorted and deduplicated", attempt)
		}
	}
}

// Looks like a bug on first reading and is not: neither shape has interior
// area, so no cell lies entirely inside one. It is why S_INTERSECTS against a
// Point can never be PROVEN and is answered in the MAYBE band.
func TestAPointAndALineStringHaveAnEmptyFull(t *testing.T) {
	line := shaped("LineString", `{"type":"LineString","coordinates":[[77.5946,12.9716],[77.6946,13.0716]]}`)

	for name, geometry := range map[string]domain.Geometry{"Point": point(bengaluru), "LineString": line} {
		t.Run(name, func(t *testing.T) {
			cover, err := geo.CoverGeometry(geometry, res)
			if err != nil {
				t.Fatalf("CoverGeometry: %v", err)
			}
			if len(cover.CellsFull) != 0 {
				t.Errorf("a %s produced %d full cells, want 0", name, len(cover.CellsFull))
			}
			if len(cover.CellsCover) == 0 {
				t.Errorf("a %s produced an empty cover", name)
			}
		})
	}
}

// everyType is one geometry of each of RFC 7946's seven, used both as stored
// and as query geometry.
func everyType() map[string]domain.Geometry {
	return map[string]domain.Geometry{
		"Point":      point(bengaluru),
		"MultiPoint": shaped("MultiPoint", `{"type":"MultiPoint","coordinates":[[77.5946,12.9716],[80.2707,13.0827]]}`),
		"LineString": shaped("LineString", `{"type":"LineString","coordinates":[[77.5946,12.9716],[77.6946,13.0716]]}`),
		"MultiLineString": shaped("MultiLineString",
			`{"type":"MultiLineString","coordinates":[[[77.59,12.97],[77.69,13.07]],[[80.2,13.0],[80.3,13.1]]]}`),
		"Polygon": squarePolygon(bengaluru, 0.05),
		"MultiPolygon": shaped("MultiPolygon",
			`{"type":"MultiPolygon","coordinates":[[`+ring(bengaluru, 0.05)+`],[`+ring(chennai, 0.05)+`]]}`),
		"GeometryCollection": shaped("GeometryCollection",
			`{"type":"GeometryCollection","geometries":[`+
				`{"type":"Point","coordinates":[77.5946,12.9716]},`+
				`{"type":"Polygon","coordinates":[`+ring(chennai, 0.05)+`]}]}`),
	}
}

// A cover is never empty, and this is a property rather than a spot check
// because three of MatchesOp's branches refute through `aCover ⊆ qCover` and
// the empty set is a subset of everything: an empty cover does not lose
// precision, it silently stops refuting.
func TestEveryTypeIsAcceptedAsStoredAndAsQueryAndCoversSomething(t *testing.T) {
	for name, geometry := range everyType() {
		t.Run(name, func(t *testing.T) {
			stored, err := geo.CoverGeometry(geometry, res)
			if err != nil {
				t.Fatalf("CoverGeometry: %v", err)
			}
			if len(stored.CellsCover) == 0 {
				t.Error("stored cover is empty")
			}
			if stored.Bounds == nil {
				t.Error("stored geometry has no bounding box")
			}

			_, queryCover, err := geo.CoverQuery(geometry, domain.OpIntersects, 0, res)
			if err != nil {
				t.Fatalf("CoverQuery: %v", err)
			}
			if len(queryCover) == 0 {
				t.Error("query cover is empty")
			}
		})
	}
}

// The companion to the property above, and the half of Postgres's
// `CHECK (cells_cover IS NULL OR cardinality(cells_cover) > 0)` that runs
// before a row exists. The shape passes a structural GeoJSON check, so only
// CoverGeometry can catch it.
func TestAWellFormedButEmptyCoordinatesArrayIsRefused(t *testing.T) {
	empty := map[string]string{
		"Point":      `{"type":"Point","coordinates":[]}`,
		"LineString": `{"type":"LineString","coordinates":[]}`,
		"Polygon":    `{"type":"Polygon","coordinates":[]}`,
		"MultiPoint": `{"type":"MultiPoint","coordinates":[]}`,
	}
	for name, body := range empty {
		t.Run(name, func(t *testing.T) {
			cover, err := geo.CoverGeometry(shaped(name, body), res)
			if err == nil {
				t.Fatalf("an empty %s was accepted with %d cover cells", name, len(cover.CellsCover))
			}
		})
	}
}

func TestAnUnparseableGeometryIsRefusedRatherThanCoveredEmpty(t *testing.T) {
	for name, body := range map[string]string{
		"not json":     `{"type":"Point",`,
		"unknown type": `{"type":"Sphere","coordinates":[77.5,12.9]}`,
		"lat past 90":  `{"type":"Point","coordinates":[77.5,120]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := geo.CoverGeometry(shaped("Point", body), res); err == nil {
				t.Error("accepted a geometry it cannot represent")
			}
		})
	}
}

func TestAPolygonCoversItsInteriorPoints(t *testing.T) {
	cover, err := geo.CoverGeometry(squarePolygon(bengaluru, 0.05), res)
	if err != nil {
		t.Fatalf("CoverGeometry: %v", err)
	}

	for _, offset := range []float64{0, 0.01, -0.02, 0.03} {
		inside := domain.GeoPoint{Lat: bengaluru.Lat + offset, Lon: bengaluru.Lon + offset}
		if !slices.Contains(cover.CellsCover, cellAt(t, inside)) {
			t.Errorf("cover misses interior point %v", inside)
		}
	}
}

// A hole is a hole in both covers. If it were only honoured by `full`, a query
// aimed at the hole would still be answered MAYBE and the exclusion would be
// invisible from outside.
func TestAHoleIsNotCovered(t *testing.T) {
	holed := shaped("Polygon", `{"type":"Polygon","coordinates":[`+
		ring(bengaluru, 0.2)+`,`+ring(bengaluru, 0.05)+`]}`)

	cover, err := geo.CoverGeometry(holed, res)
	if err != nil {
		t.Fatalf("CoverGeometry: %v", err)
	}
	if slices.Contains(cover.CellsCover, cellAt(t, bengaluru)) {
		t.Error("the cover includes the centre of the hole")
	}
	if slices.Contains(cover.CellsFull, cellAt(t, bengaluru)) {
		t.Error("cells_full includes the centre of the hole")
	}
	edge := domain.GeoPoint{Lat: bengaluru.Lat, Lon: bengaluru.Lon + 0.12}
	if !slices.Contains(cover.CellsCover, cellAt(t, edge)) {
		t.Error("the cover misses the ring between the hole and the outer boundary")
	}
}

// It behaves like a Point, correctly: no cell lies entirely inside it, and one
// cell touches it.
func TestAPolygonSmallerThanOneCellHasNoFullAndSomeCover(t *testing.T) {
	cover, err := geo.CoverGeometry(squarePolygon(bengaluru, 0.0002), res)
	if err != nil {
		t.Fatalf("CoverGeometry: %v", err)
	}
	if len(cover.CellsFull) != 0 {
		t.Errorf("a sub-cell polygon produced %d full cells, want 0", len(cover.CellsFull))
	}
	if len(cover.CellsCover) == 0 {
		t.Error("a sub-cell polygon produced an empty cover")
	}
}

// Densification, and the reason a segment is not sampled at its vertices: a
// straight line crosses cells its endpoints never touch, and a query aimed at
// the middle of a canal must still find it.
func TestALineStringCoversCellsBetweenItsVertices(t *testing.T) {
	from := bengaluru
	to := domain.GeoPoint{Lat: bengaluru.Lat, Lon: bengaluru.Lon + 0.2}
	line := shaped("LineString", fmt.Sprintf(
		`{"type":"LineString","coordinates":[[%g,%g],[%g,%g]]}`, from.Lon, from.Lat, to.Lon, to.Lat))

	cover, err := geo.CoverGeometry(line, res)
	if err != nil {
		t.Fatalf("CoverGeometry: %v", err)
	}

	for step := 1; step < 20; step++ {
		between := domain.GeoPoint{Lat: from.Lat, Lon: from.Lon + 0.2*float64(step)/20}
		if !slices.Contains(cover.CellsCover, cellAt(t, between)) {
			t.Fatalf("cover misses %v, which lies on the segment", between)
		}
	}
}

// Nil covers, never a truncated one. A cover truncated to fit would make the
// shape discoverable only in whichever corner the fill reached — and the box
// has to survive, because for an oversize row it is the entire predicate.
func TestAnOversizeGeometryHasNilCoversAndKeepsItsBox(t *testing.T) {
	cover, err := geo.CoverGeometry(squarePolygon(domain.GeoPoint{Lat: 11, Lon: 77}, 1.0), res)
	if err != nil {
		t.Fatalf("CoverGeometry: %v", err)
	}
	if cover.CellsFull != nil || cover.CellsCover != nil {
		t.Errorf("an oversize geometry kept %d full and %d cover cells; both must be nil",
			len(cover.CellsFull), len(cover.CellsCover))
	}
	if cover.Bounds == nil {
		t.Fatal("an oversize geometry lost its bounding box, which is its only predicate")
	}
	if cover.Bounds.MinLat > 10.0001 || cover.Bounds.MaxLat < 11.9999 {
		t.Errorf("bounds %+v do not span the geometry", *cover.Bounds)
	}
}

func TestBoundsForSpansEveryVertex(t *testing.T) {
	bounds, err := geo.BoundsFor(squarePolygon(bengaluru, 0.05), domain.OpIntersects, 0)
	if err != nil {
		t.Fatalf("BoundsFor: %v", err)
	}
	if bounds == nil {
		t.Fatal("BoundsFor declined an ordinary polygon")
	}
	want := domain.BBox{
		MinLat: bengaluru.Lat - 0.05, MaxLat: bengaluru.Lat + 0.05,
		MinLon: bengaluru.Lon - 0.05, MaxLon: bengaluru.Lon + 0.05,
	}
	if math.Abs(bounds.MinLat-want.MinLat) > 1e-9 || math.Abs(bounds.MaxLat-want.MaxLat) > 1e-9 ||
		math.Abs(bounds.MinLon-want.MinLon) > 1e-9 || math.Abs(bounds.MaxLon-want.MaxLon) > 1e-9 {
		t.Errorf("BoundsFor = %+v, want %+v", *bounds, want)
	}
}

// Declining is not failing. A nil cover disables the cell predicate only; the
// box still runs, the answer stays a superset, and the query degrades to a scan
// of the scope-gated set. Unreachable for an India deployment — written down
// because "it worked in testing" and "the cover silently stopped narrowing"
// look identical from outside.
func TestAnAntimeridianCircleDeclinesRatherThanWrapping(t *testing.T) {
	fiji := domain.GeoPoint{Lat: -17.7, Lon: 179.95}

	full, cover, err := geo.CoverQuery(point(fiji), domain.OpDWithin, 50000, res)
	if err != nil {
		t.Fatalf("CoverQuery: %v", err)
	}
	if full != nil || cover != nil {
		t.Errorf("an antimeridian circle produced %d full and %d cover cells; it must decline",
			len(full), len(cover))
	}

	spanning := shaped("LineString", `{"type":"LineString","coordinates":[[179.5,-17.7],[-179.5,-17.7]]}`)
	bounds, err := geo.BoundsFor(spanning, domain.OpIntersects, 0)
	if err != nil {
		t.Fatalf("BoundsFor: %v", err)
	}
	if bounds != nil {
		t.Errorf("BoundsFor returned %+v for an antimeridian-spanning line; it must decline", *bounds)
	}
}

// The circle case, which is most of the traffic. An INSCRIBED n-gon sags to
// R·cos(π/n) between its vertices and would miss a sliver near the boundary, so
// the polygon is scaled to CONTAIN the circle: every bearing at the radius must
// be inside the cover.
func TestADWithinCircleContainsItsWholeRadius(t *testing.T) {
	const radiusM = 3000

	_, cover, err := geo.CoverQuery(point(bengaluru), domain.OpDWithin, radiusM, res)
	if err != nil {
		t.Fatalf("CoverQuery: %v", err)
	}
	if len(cover) == 0 {
		t.Fatal("an ordinary circle produced no cover")
	}

	for bearing := 0; bearing < 360; bearing += 5 {
		edge := destination(bengaluru, float64(bearing), radiusM)
		if !slices.Contains(cover, cellAt(t, edge)) {
			t.Errorf("the cover misses the circle's edge at bearing %d°", bearing)
		}
	}
}

// Sized from the MINIMUM centre spacing, not the average, so it must hold where
// cells run small — the seed sits on a pentagon, which is where they do.
func TestDilationIsASupersetEvenWhereCellsRunSmall(t *testing.T) {
	const radiusM = 4000

	seed := nearAPentagon(t)
	line := shaped("LineString", fmt.Sprintf(
		`{"type":"LineString","coordinates":[[%.9f,%.9f],[%.9f,%.9f]]}`,
		seed.Lon, seed.Lat, seed.Lon+0.001, seed.Lat))

	_, cover, err := geo.CoverQuery(line, domain.OpDWithin, radiusM, res)
	if err != nil {
		t.Fatalf("CoverQuery: %v", err)
	}
	if len(cover) == 0 {
		t.Fatal("dilation produced no cover")
	}

	for bearing := 0; bearing < 360; bearing += 15 {
		reached := destination(seed, float64(bearing), radiusM)
		if !slices.Contains(cover, cellAt(t, reached)) {
			t.Errorf("dilated cover misses %v, %d m from the seed at bearing %d°",
				reached, radiusM, bearing)
		}
	}
}

// nearAPentagon returns a coordinate at an icosahedron vertex, where H3 cells
// are at their smallest and an average-sized dilation would fall short.
func nearAPentagon(t *testing.T) domain.GeoPoint {
	t.Helper()

	pentagons, err := h3.Pentagons(res)
	if err != nil || len(pentagons) == 0 {
		t.Fatalf("Pentagons(%d): %v", res, err)
	}
	at, err := h3.CellToLatLng(pentagons[0])
	if err != nil {
		t.Fatalf("CellToLatLng: %v", err)
	}
	return domain.GeoPoint{Lat: at.Lat, Lon: at.Lng}
}

// destination is the spherical direct problem: where you arrive travelling
// distanceM from a point along a bearing. Written here rather than taken from
// the package under test, because a dilation test built on the same arithmetic
// as the dilation would agree with itself.
func destination(from domain.GeoPoint, bearingDeg, distanceM float64) domain.GeoPoint {
	const earthRadiusM = 6371008.8
	angular := distanceM / earthRadiusM
	bearing := bearingDeg * math.Pi / 180
	lat := from.Lat * math.Pi / 180
	lon := from.Lon * math.Pi / 180

	toLat := math.Asin(math.Sin(lat)*math.Cos(angular) +
		math.Cos(lat)*math.Sin(angular)*math.Cos(bearing))
	toLon := lon + math.Atan2(math.Sin(bearing)*math.Sin(angular)*math.Cos(lat),
		math.Cos(angular)-math.Sin(lat)*math.Sin(toLat))

	return domain.GeoPoint{Lat: toLat * 180 / math.Pi, Lon: toLon * 180 / math.Pi}
}

// The box under S_DWITHIN must cover the RADIUS, not the query shape. A Point's
// own box has zero area, so an unexpanded one meets only geometries whose box
// contains the exact centre — and the box stage would then refute every row the
// cells admitted, inverting the two-stage design into a point lookup.
func TestTheDWithinBoxCoversTheWholeRadius(t *testing.T) {
	center := domain.GeoPoint{Lat: 12.9716, Lon: 77.5946}
	query := shaped("Point", `{"type":"Point","coordinates":[77.5946,12.9716]}`)

	bounds, err := geo.BoundsFor(query, domain.OpDWithin, 5000)
	if err != nil {
		t.Fatalf("BoundsFor: %v", err)
	}
	if bounds == nil {
		t.Fatal("BoundsFor declined a Point in Bengaluru")
	}

	// Every bearing at the radius must land inside the box, or a resource the
	// cells admitted is lost at the box.
	for bearing := 0; bearing < 360; bearing += 15 {
		edge := destination(center, float64(bearing), 5000)
		if edge.Lat < bounds.MinLat || edge.Lat > bounds.MaxLat ||
			edge.Lon < bounds.MinLon || edge.Lon > bounds.MaxLon {
			t.Errorf("the point 5 km at bearing %d° fell outside the S_DWITHIN box", bearing)
		}
	}
}

// And it must not expand on any other operator: distanceMeters is ignored there
// by the protocol, and a box widened by an ignored field returns rows the
// operator excludes.
func TestOnlySDWithinExpandsTheBox(t *testing.T) {
	query := shaped("Point", `{"type":"Point","coordinates":[77.5946,12.9716]}`)

	plain, err := geo.BoundsFor(query, domain.OpIntersects, 5000)
	if err != nil {
		t.Fatalf("BoundsFor: %v", err)
	}
	if plain == nil || plain.MinLat != plain.MaxLat || plain.MinLon != plain.MaxLon {
		t.Errorf("S_INTERSECTS widened a Point's box to %+v; distanceMeters is ignored there", plain)
	}
}
