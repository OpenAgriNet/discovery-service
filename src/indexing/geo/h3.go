// Package geo turns geometry into H3 cells, bounding boxes and metres.
//
// Every geometry gets two covers at one resolution: cells_full, the cells lying
// ENTIRELY inside it, and cells_cover, the cells it touches at all. The
// invariant the whole design rests on is
//
//	cells_full ⊆ the true geometry ⊆ cells_cover
//
// from which the single rule follows: PROVE with full, REFUTE with cover. A
// cover is a superset, so what it rules out is really ruled out; full is a
// subset, so what it asserts is really true. Swapping the two in either
// direction produces a predicate that is wrong rather than merely imprecise.
//
// This package knows about cells, boxes and metres. It knows nothing about
// JSONPath — the mapper builds document locations and never passes one here —
// and nothing about SQL: MatchesOp is the memory backend's twin of the
// `CASE spatial_op` block, and the two are written from the same table in the
// plan's Geospatial Design rather than from each other.
package geo

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"

	h3 "github.com/uber/h3-go/v4"

	"github.com/OpenAgriNet/discovery-service/src/domain"
)

const (
	// MaxIndexCoverCells is the ceiling on one PUBLISH cover — about 6,000 km²
	// at resolution 8. Past it BOTH cell columns are nil and the bounding box
	// decides alone; a cover truncated to fit would make a shape discoverable
	// only in whichever corner the fill happened to reach, which is a wrong
	// answer wearing the shape of a right one.
	MaxIndexCoverCells = 8192

	// MaxQueryCoverCells is the ceiling on one DISCOVER cover, and on a dilated
	// cover after dilation. Lower than the index budget because a query cover
	// is built per request while an index cover is built once.
	MaxQueryCoverCells = 4096

	// MaxCatalogWalkDepth bounds the publish walker. Publisher-shaped documents
	// are not trusted to be shallow, and pathological nesting must cost a
	// bounded walk rather than the stack.
	MaxCatalogWalkDepth = 32

	// MaxGeometriesPerCatalog is the publish budget for the general walk. Over
	// it the extra finds are PARTIAL faults naming their paths — never a silent
	// drop, because a geometry that vanished without a fault is a resource that
	// is simply undiscoverable with nothing to explain why.
	MaxGeometriesPerCatalog = 256

	// queryCircleVertices is the vertex count of the polygon approximating an
	// S_DWITHIN radius. Circumscribed rather than inscribed, at scale 1.0012 —
	// 0.12% too wide, which over-includes; inscribed would under-include, and
	// under-inclusion is the direction that loses real results.
	queryCircleVertices = 64
)

// MatchesOp reports whether a stored geometry could satisfy op against a query
// geometry, judging on cells alone.
//
// The memory backend's twin of the SQL `CASE spatial_op` block. All four slices
// must be sorted ascending and deduplicated — a precondition, not a
// convenience, because S_EQUALS compares element-wise exactly as PostgreSQL's
// array `=` does, and an unsorted pair of identical sets compares unequal
// there. CoverGeometry and CoverQuery are the two places that guarantee it.
//
// It takes NO bounding box. The backend applies the box itself and must skip it
// for S_DISJOINT, which inverts: two shapes whose boxes miss entirely ARE
// disjoint, so ANDing the box in would answer that operator with the complement
// of the truth and no error. Passing bounds in here would have buried that
// asymmetry inside a function whose name promises only the operator.
//
// MAYBE resolves as a match. Under ANY that makes the result set a superset of
// the exact answer — never a subset — and under NONE it inverts to a subset,
// which is the safe direction on both sides.
func MatchesOp(op domain.SpatialOp, aFull, aCover, qFull, qCover []uint64) bool {
	// Refused, not approximated: a cell decomposition cannot express a
	// measure-zero boundary relation at any resolution. The mapper rejects both
	// with a 400, so this is unreachable — false rather than true so that a
	// leak surfaces as an empty result rather than as the entire corpus
	// silently matching a predicate nobody wrote.
	if op == domain.OpTouches || op == domain.OpCrosses {
		return false
	}
	return !refutes(op, aFull, aCover, qFull, qCover)
}

// refutes is the FALSE column of the operator table, and the whole of it.
//
// The TRUE column has no code here on purpose: provably TRUE and MAYBE both
// resolve as a match, so only a refutation can change the answer. Writing the
// TRUE column out would add branches that cannot alter what is returned, and a
// branch that cannot matter is one a later reader will try to make matter.
//
// S_DWITHIN needs no arm of its own because CoverQuery has already dilated the
// query's covers by k; by the time they arrive here the operator IS
// S_INTERSECTS, which is what the table's `dilate(Q.full, k)` says.
func refutes(op domain.SpatialOp, aFull, aCover, qFull, qCover []uint64) bool {
	// An empty cover is a DECLINED cover — antimeridian, or over budget — never
	// a legitimately empty one, because CoverGeometry guarantees a non-empty
	// cover for every geometry it accepts and Postgres carries the same rule as
	// a CHECK. Nothing is refutable from a cover that was never built, and the
	// bounding box is left to decide.
	if len(aCover) == 0 || len(qCover) == 0 {
		return false
	}

	meet := overlaps(aCover, qCover)

	switch op {
	case domain.OpIntersects, domain.OpDWithin:
		return !meet
	case domain.OpDisjoint:
		return overlaps(aFull, qFull) || containedBy(aCover, qFull) || containedBy(qCover, aFull)
	case domain.OpWithin:
		return !containedBy(aCover, qCover)
	case domain.OpContains:
		return !containedBy(qCover, aCover)
	case domain.OpOverlaps:
		return !meet || containedBy(aCover, qFull) || containedBy(qCover, aFull)
	case domain.OpEquals:
		return !slices.Equal(aCover, qCover) || !slices.Equal(aFull, qFull)
	default:
		return false
	}
}

// overlaps reports whether two sorted cell sets share a cell — PostgreSQL's
// `&&`. A merge scan rather than a set built from one side: both inputs are
// already sorted, so there is nothing to gain by allocating.
func overlaps(left, right []uint64) bool {
	atLeft, atRight := 0, 0
	for atLeft < len(left) && atRight < len(right) {
		switch {
		case left[atLeft] < right[atRight]:
			atLeft++
		case left[atLeft] > right[atRight]:
			atRight++
		default:
			return true
		}
	}
	return false
}

// containedBy reports whether every cell of subset appears in superset —
// PostgreSQL's `<@`, including its answer for an empty left side, which is
// true. That is exactly why three refutations in the table are phrased over
// `cover` and not over `full`: a Point's full is permanently empty, and `'{}'
// <@ anything` would make those arms pass vacuously.
func containedBy(subset, superset []uint64) bool {
	at := 0
	for _, cell := range subset {
		for at < len(superset) && superset[at] < cell {
			at++
		}
		if at == len(superset) || superset[at] != cell {
			return false
		}
	}
	return true
}

// ErrGeometry is returned for a shape this package cannot represent: not
// readable JSON, not one of RFC 7946's seven types, a coordinate outside WGS
// 84, or an EMPTY coordinates array.
//
// The last is not pedantry. A structural GeoJSON check accepts
// `{"type":"Point","coordinates":[]}` — the keys are right and the value is an
// array — so nothing upstream refuses it, and a cover built from it would be
// empty. An empty cover does not lose precision: three of the operator table's
// refutations run through `A.cover <@ Q.cover`, the empty set is a subset of
// everything, and the row would silently stop refuting anything at all.
// Postgres carries the same rule as
// `CHECK (cells_cover IS NULL OR cardinality(cells_cover) > 0)`; this is the
// half that runs before a row exists.
var ErrGeometry = errors.New("geometry cannot be indexed")

// Cover is everything the index learns from one geometry.
//
// A nil Bounds or a nil cell slice means DECLINED, never empty: the geometry
// was over budget or crossed the antimeridian, and the predicates that would
// have used them are skipped rather than answered from nothing.
type Cover struct {
	Bounds     *domain.BBox
	CellsFull  []uint64
	CellsCover []uint64
}

// maxCollectionDepth bounds GeometryCollection recursion. A collection may hold
// collections, and a publisher's document is not trusted to terminate.
const maxCollectionDepth = 8

// minSpacingFactor converts a resolution's AVERAGE edge length into its minimum
// centre-to-centre spacing. Centres sit `√3 × edge` apart and H3 cell areas
// vary by up to ~1.99× within a resolution, so the minimum edge is ≥ 0.71× the
// average: √3 × 0.71 ≈ 1.23.
//
// Deliberately not the average. Sizing a dilation from the average
// under-dilates wherever cells run small, and under-inclusion is the one error
// direction this design does not permit.
const minSpacingFactor = 1.23

// densifyEdgeFraction is the share of a resolution's average edge one line
// sample advances — 133 m at r8. The minimum inradius is ≥ 0.61× the average
// edge, so a quarter-edge step cannot step over a cell.
const densifyEdgeFraction = 0.25

// CoverGeometry covers a STORED geometry at the given resolution.
//
// The resolution is a parameter rather than a package constant because
// GEO_RESOLUTION_CELLS is config: the accuracy/storage trade is a property of a
// deployment's data, and a constant here would put the decision in the wrong
// repository.
func CoverGeometry(geometry domain.Geometry, resolution int) (Cover, error) {
	parts, err := decodeShape(geometry.GeoJSON)
	if err != nil {
		return Cover{}, err
	}

	full, cover, err := fillBoth(parts, resolution, MaxIndexCoverCells)
	if err != nil {
		return Cover{}, err
	}
	return Cover{Bounds: boundsOf(parts), CellsFull: full, CellsCover: cover}, nil
}

// CoverQuery covers a CONSTRAINT geometry — the same two covers as a stored
// one, plus the distance handling only S_DWITHIN carries.
//
// Returns nil covers when the shape declines (it wraps the antimeridian) or
// exceeds the query budget. Nil disables the cell predicate only: the bounding
// box still runs, so the answer stays a superset and the query degrades to a
// scan of the scope-gated set rather than to a wrong answer.
func CoverQuery(
	geometry domain.Geometry, op domain.SpatialOp, distanceMeters float64, resolution int,
) (full, cover []uint64, err error) {
	parts, err := decodeShape(geometry.GeoJSON)
	if err != nil {
		return nil, nil, err
	}

	// The circle case, and worth its own branch because it is most of the
	// traffic: "suppliers within 5 km of me".
	if op == domain.OpDWithin && parts.isLonePoint() {
		return circleCells(parts.points[0], distanceMeters, resolution)
	}

	full, cover, err = fillBoth(parts, resolution, MaxQueryCoverCells)
	if err != nil || cover == nil || op != domain.OpDWithin {
		return full, cover, err
	}

	// "within 500 m of this canal" — a buffered LineString or Polygon. There is
	// no geometry engine here to buffer with, so dilate on the grid instead.
	rings, sized := ringsFor(distanceMeters, resolution)
	if !sized {
		return nil, nil, nil
	}
	full, cover = dilate(full, rings), dilate(cover, rings)
	if cover == nil || full == nil {
		return nil, nil, nil
	}
	return full, cover, nil
}

// BoundsFor is the bounding box alone, for callers that need the box without
// paying for a fill.
//
// Declines — nil, nil — on a shape spanning the antimeridian or reaching a
// pole, for the same reason CoverQuery does: the box of such a shape is the
// whole world the wrong way round, and a box that wide is worse than none.
func BoundsFor(geometry domain.Geometry, op domain.SpatialOp, distanceMeters float64) (*domain.BBox, error) {
	parts, err := decodeShape(geometry.GeoJSON)
	if err != nil {
		return nil, err
	}

	bounds := boundsOf(parts)
	if op != domain.OpDWithin || distanceMeters <= 0 {
		return bounds, nil
	}
	return expandedBy(bounds, distanceMeters), nil
}

// boxRounding is the outward nudge on an expanded box: one part in a billion, a
// millimetre on a 1000 km search.
//
// It is not slop. The caller derives the true edge with spherical trigonometry
// and this derives it by division; the two are different roundings of the same
// number and land an ULP apart. An edge exactly ON the boundary therefore falls
// outside it half the time, and the box stage drops a row the cells admitted.
// Rounding outward keeps every disagreement in the over-inclusive direction.
const boxRounding = 1 + 1e-9

// expandedBy grows a box by a radius in metres, and is why BoundsFor needs the
// operator at all.
//
// Under S_DWITHIN the constraint is the DILATED region, not the shape sent: a
// Point's own box has zero area, so an unexpanded one meets only geometries
// whose box contains the exact centre. The box stage would then refute every
// row the cell stage admitted, turning a radius search into a point lookup —
// the same wrong answer as an under-sized cover, arriving from the other side.
//
// Longitude is scaled at the latitude FURTHEST from the equator, where a degree
// is shortest and the same metres therefore span the most degrees. Using the
// centre latitude would under-expand the box's far edge.
func expandedBy(bounds *domain.BBox, distanceMeters float64) *domain.BBox {
	if bounds == nil {
		return nil
	}

	degreesLat := distanceMeters * boxRounding / (earthRadiusM * math.Pi / 180)
	widest := math.Max(math.Abs(bounds.MinLat), math.Abs(bounds.MaxLat)) + degreesLat
	if widest >= 90 {
		return nil // touches a pole; the same decline boundsOf makes, for the same reason
	}

	grown := domain.BBox{
		MinLat: bounds.MinLat - degreesLat, MaxLat: bounds.MaxLat + degreesLat,
		MinLon: bounds.MinLon - degreesLat/math.Cos(radians(widest)),
		MaxLon: bounds.MaxLon + degreesLat/math.Cos(radians(widest)),
	}
	if grown.MaxLon-grown.MinLon > 180 || grown.MaxLon > 180 || grown.MinLon < -180 {
		return nil // wraps the antimeridian; CoverQuery declines the same input
	}
	return &grown
}

// circleCells approximates an S_DWITHIN radius around a Point.
//
// An INSCRIBED n-gon sags to R·cos(π/n) between its vertices and would miss a
// sliver just inside the boundary; scaling every vertex by 1/cos(π/n) makes the
// polygon CONTAIN the circle. At n=64 that is 1.0012 — 0.12% too wide, which
// over-includes, and over-inclusion is the direction that keeps the superset
// guarantee.
func circleCells(center domain.GeoPoint, radiusM float64, resolution int) (full, cover []uint64, err error) {
	scale := 1 / math.Cos(math.Pi/queryCircleVertices)
	loop := make([]domain.GeoPoint, 0, queryCircleVertices)
	for vertex := range queryCircleVertices {
		bearing := 360 * float64(vertex) / queryCircleVertices
		loop = append(loop, destinationPoint(center, bearing, radiusM*scale))
	}

	parts := shape{polygons: []polygon{{outer: loop}}}
	if boundsOf(parts) == nil {
		return nil, nil, nil
	}
	return fillBoth(parts, resolution, MaxQueryCoverCells)
}

// destinationPoint solves the spherical direct problem: the coordinate reached
// by travelling distanceM from `from` along a bearing.
func destinationPoint(from domain.GeoPoint, bearingDeg, distanceM float64) domain.GeoPoint {
	angular := distanceM / earthRadiusM
	bearing, lat, lon := radians(bearingDeg), radians(from.Lat), radians(from.Lon)

	toLat := math.Asin(math.Sin(lat)*math.Cos(angular) +
		math.Cos(lat)*math.Sin(angular)*math.Cos(bearing))
	toLon := lon + math.Atan2(
		math.Sin(bearing)*math.Sin(angular)*math.Cos(lat),
		math.Cos(angular)-math.Sin(lat)*math.Sin(toLat))

	return domain.GeoPoint{Lat: toLat * 180 / math.Pi, Lon: toLon * 180 / math.Pi}
}

// ringsFor sizes a dilation in gridDisk rings, reporting false when the
// resolution's geometry cannot be read — in which case the caller declines
// rather than dilating by zero, since dilating by zero is under-inclusion
// wearing the shape of an answer.
func ringsFor(distanceMeters float64, resolution int) (int, bool) {
	edge, err := h3.HexagonEdgeLengthAvgM(resolution)
	if err != nil || edge <= 0 {
		return 0, false
	}
	if distanceMeters <= 0 {
		return 0, true
	}
	return int(math.Ceil(distanceMeters / (minSpacingFactor * edge))), true
}

// dilate grows a cell set by `rings` gridDisk rings, or returns nil when the
// result would exceed the query budget.
//
// gridDisk(c, k) is 3k² + 3k + 1 cells per seed, so a 500-cell river cover at
// k=5 is up to 45,500 before deduplication. Past the budget the constraint
// falls back to its bounding box — wider, never narrower.
func dilate(cells []uint64, rings int) []uint64 {
	if rings <= 0 {
		return cells
	}

	grown := make([]uint64, 0, len(cells)*(3*rings*rings+3*rings+1))
	for _, cell := range cells {
		disk, err := h3.GridDisk(h3.Cell(cell), rings)
		if err != nil {
			return nil
		}
		grown = appendNonZero(grown, disk)

		// A pre-deduplication bail, so a pathological seed set costs a bounded
		// allocation. It can decline a set that would have deduplicated under
		// budget; that answer is the bounding box, which is still a superset.
		if len(grown) > MaxQueryCoverCells*8 {
			return nil
		}
	}

	grown = compact(grown)
	if len(grown) > MaxQueryCoverCells {
		return nil
	}
	return grown
}

// appendNonZero drops the zero entries gridDisk leaves where a disk is
// truncated by a pentagon. A zero is not a cell, and one reaching a cover would
// be a cell id no geometry can ever match.
func appendNonZero(into []uint64, cells []h3.Cell) []uint64 {
	for _, cell := range cells {
		if cell != 0 {
			into = append(into, uint64(cell))
		}
	}
	return into
}

// fillBoth builds the two covers under one budget.
//
// Both are dropped together when either is over: keeping `full` beside a nil
// `cover` would leave the operators that prove through `full` answering from a
// set whose superset was never built.
func fillBoth(parts shape, resolution, budget int) (full, cover []uint64, err error) {
	cover, err = fill(parts, resolution, h3.ContainmentOverlapping, budget)
	if err != nil {
		return nil, nil, err
	}
	if cover == nil {
		return nil, nil, nil
	}
	if len(cover) == 0 {
		return nil, nil, fmt.Errorf("%w: covers no cell", ErrGeometry)
	}

	full, err = fill(parts, resolution, h3.ContainmentFull, budget)
	if err != nil {
		return nil, nil, err
	}
	if full == nil {
		return nil, nil, nil
	}
	return full, cover, nil
}

// fill produces one cover in one containment mode, or nil when over budget.
//
// Points and lines contribute to the OVERLAPPING cover only — neither has
// interior area, so no cell lies entirely inside one and their `full` is
// permanently empty. Polygon boundaries are walked into the overlapping cover
// too: a boundary lies on the shape, so its cells genuinely touch it, and
// including them is what guarantees a non-empty cover for a polygon smaller
// than the fill can see.
func fill(parts shape, resolution int, mode h3.ContainmentMode, budget int) ([]uint64, error) {
	cells := make([]uint64, 0, 64)

	if mode == h3.ContainmentOverlapping {
		walked, within := walkCells(parts, resolution, budget)
		if !within {
			return nil, nil
		}
		cells = append(cells, walked...)
	}

	for _, surface := range parts.polygons {
		filled, err := h3.PolygonToCellsExperimental(surface.h3(), resolution, mode, int64(budget)+1)
		if errors.Is(err, h3.ErrMemoryBounds) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrGeometry, err)
		}
		cells = appendNonZero(cells, filled)
	}

	cells = compact(cells)
	if len(cells) > budget {
		return nil, nil
	}
	return cells, nil
}

// walkCells covers every point, line and polygon boundary in the shape,
// reporting false when the walk itself exceeds the budget.
//
// Lines are DENSIFIED rather than sampled at their vertices: RFC 7946 §3.1.1
// makes a segment a straight line in the CRS, and a straight line crosses cells
// its endpoints never touch. A query aimed at the middle of a canal has to find
// the canal.
func walkCells(parts shape, resolution, budget int) ([]uint64, bool) {
	step, sized := densifyStepM(resolution)
	if !sized {
		return nil, false
	}

	cells := make([]uint64, 0, 64)
	for _, at := range parts.points {
		cells = appendCell(cells, at, resolution)
	}
	for _, path := range parts.paths() {
		cells = appendPath(cells, path, step, resolution)
		// Samples sit a quarter-edge apart, so more than four per budgeted cell
		// means the cover is over budget for any line that does not double back
		// — and for one that does, declining costs a bounding-box answer, which
		// is still a superset.
		if len(cells) > 4*budget {
			return nil, false
		}
	}
	return cells, true
}

// densifyStepM is a quarter of the resolution's average edge.
func densifyStepM(resolution int) (float64, bool) {
	edge, err := h3.HexagonEdgeLengthAvgM(resolution)
	if err != nil || edge <= 0 {
		return 0, false
	}
	return densifyEdgeFraction * edge, true
}

// appendPath walks one polyline, emitting a cell per sample.
func appendPath(into []uint64, path []domain.GeoPoint, stepM float64, resolution int) []uint64 {
	if len(path) == 1 {
		return appendCell(into, path[0], resolution)
	}
	for index := 1; index < len(path); index++ {
		from, to := path[index-1], path[index]
		samples := int(math.Ceil(manhattanLengthM(from, to)/stepM)) + 1
		// A segment of zero length — a publisher's duplicated vertex, which RFC
		// 7946 permits — needs one sample, and the ratio below would be 0/0.
		// NaN reaches H3, every cell of the path is dropped, and a line that
		// covers one cell perfectly well is faulted as covering none.
		if samples < 2 {
			into = appendCell(into, from, resolution)
			continue
		}
		for sample := range samples {
			ratio := float64(sample) / float64(samples-1)
			into = appendCell(into, domain.GeoPoint{
				Lat: from.Lat + (to.Lat-from.Lat)*ratio,
				Lon: from.Lon + (to.Lon-from.Lon)*ratio,
			}, resolution)
		}
	}
	return into
}

// appendCell adds the cell a coordinate falls in, dropping it if H3 refuses.
// A refusal here cannot make the cover unsound: it is caught by fillBoth's
// non-empty check, or by the bounding box.
func appendCell(into []uint64, at domain.GeoPoint, resolution int) []uint64 {
	cell, err := h3.LatLngToCell(h3.LatLng{Lat: at.Lat, Lng: at.Lon}, resolution)
	if err != nil || cell == 0 {
		return into
	}
	return append(into, uint64(cell))
}

// manhattanLengthM bounds a segment's length from its lat and lon spans, taken
// at the WIDEST parallel it touches — deliberately not haversine, which is the
// shorter great-circle distance and would under-count the samples a segment
// straight in the CRS actually needs.
func manhattanLengthM(from, to domain.GeoPoint) float64 {
	const metresPerDegree = earthRadiusM * math.Pi / 180

	widest := math.Min(math.Abs(from.Lat), math.Abs(to.Lat))
	return math.Abs(to.Lat-from.Lat)*metresPerDegree +
		math.Abs(to.Lon-from.Lon)*metresPerDegree*math.Cos(radians(widest))
}

// compact sorts and deduplicates in place. Sorted is MatchesOp's precondition
// and PostgreSQL's for array `=`; deduplicated because a repeated cell inflates
// every budget check against it.
func compact(cells []uint64) []uint64 {
	slices.Sort(cells)
	return slices.Compact(cells)
}

// boundsOf is the axis-aligned box over every vertex, or nil for a shape that
// spans the antimeridian or reaches a pole.
//
// The span test is how a wrap is detected at all: coordinates arrive normalised
// into [-180, 180], so a shape straddling ±180° appears as one spanning almost
// the whole globe. Declining is honest; a box that wide is the world.
func boundsOf(parts shape) *domain.BBox {
	vertices := parts.vertices()
	if len(vertices) == 0 {
		return nil
	}

	bounds := domain.BBox{
		MinLat: vertices[0].Lat, MaxLat: vertices[0].Lat,
		MinLon: vertices[0].Lon, MaxLon: vertices[0].Lon,
	}
	for _, at := range vertices[1:] {
		bounds.MinLat, bounds.MaxLat = math.Min(bounds.MinLat, at.Lat), math.Max(bounds.MaxLat, at.Lat)
		bounds.MinLon, bounds.MaxLon = math.Min(bounds.MinLon, at.Lon), math.Max(bounds.MaxLon, at.Lon)
	}

	if bounds.MaxLon-bounds.MinLon > 180 || bounds.MaxLat >= 90 || bounds.MinLat <= -90 {
		return nil
	}
	return &bounds
}

// shape is a decoded geometry reduced to the three primitives a fill can use.
// Every RFC 7946 type collapses into it, which is why there is one fill rather
// than one per type.
type shape struct {
	points   []domain.GeoPoint
	lines    [][]domain.GeoPoint
	polygons []polygon
}

// polygon is an outer ring and its holes, in GeoJSON's sense: a coordinate
// inside a hole is outside the shape.
type polygon struct {
	outer []domain.GeoPoint
	holes [][]domain.GeoPoint
}

// isLonePoint reports whether the shape is exactly one Point — the only case
// CoverQuery answers with a circle rather than a dilation.
func (s shape) isLonePoint() bool {
	return len(s.points) == 1 && len(s.lines) == 0 && len(s.polygons) == 0
}

// paths is every polyline in the shape, including polygon boundaries, since a
// boundary is walked into the overlapping cover exactly as a line is.
func (s shape) paths() [][]domain.GeoPoint {
	paths := slices.Clone(s.lines)
	for _, surface := range s.polygons {
		paths = append(paths, surface.outer)
		paths = append(paths, surface.holes...)
	}
	return paths
}

// vertices is every coordinate the shape names, undensified — a segment is
// straight in the CRS, so its box is the box of its endpoints.
func (s shape) vertices() []domain.GeoPoint {
	vertices := slices.Clone(s.points)
	for _, path := range s.paths() {
		vertices = append(vertices, path...)
	}
	return vertices
}

// h3 converts a polygon to H3's own shape. The closing vertex GeoJSON requires
// is dropped: H3 loops are implicitly closed, and a repeated vertex is a
// zero-length edge for it to trace.
func (p polygon) h3() h3.GeoPolygon {
	converted := h3.GeoPolygon{GeoLoop: h3Loop(p.outer), Holes: make([]h3.GeoLoop, 0, len(p.holes))}
	for _, hole := range p.holes {
		converted.Holes = append(converted.Holes, h3Loop(hole))
	}
	return converted
}

func h3Loop(ring []domain.GeoPoint) h3.GeoLoop {
	if len(ring) > 1 && ring[0] == ring[len(ring)-1] {
		ring = ring[:len(ring)-1]
	}
	loop := make(h3.GeoLoop, 0, len(ring))
	for _, at := range ring {
		loop = append(loop, h3.LatLng{Lat: at.Lat, Lng: at.Lon})
	}
	return loop
}

// decodeShape reads a GeoJSON geometry into the primitives a fill can use.
func decodeShape(raw json.RawMessage) (shape, error) {
	return decodeAt(raw, maxCollectionDepth)
}

func decodeAt(raw json.RawMessage, depth int) (shape, error) {
	if depth <= 0 {
		return shape{}, fmt.Errorf("%w: geometries nested deeper than %d", ErrGeometry, maxCollectionDepth)
	}

	var envelope struct {
		Type        string            `json:"type"`
		Coordinates json.RawMessage   `json:"coordinates"`
		Geometries  []json.RawMessage `json:"geometries"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return shape{}, fmt.Errorf("%w: %v", ErrGeometry, err)
	}

	if envelope.Type == "GeometryCollection" {
		return decodeCollection(envelope.Geometries, depth-1)
	}
	return decodePrimitive(envelope.Type, envelope.Coordinates)
}

// decodeCollection merges the members of a GeometryCollection into one shape.
// An empty collection is refused for the same reason an empty coordinates array
// is: it would cover nothing and silently stop refuting.
func decodeCollection(members []json.RawMessage, depth int) (shape, error) {
	if len(members) == 0 {
		return shape{}, fmt.Errorf("%w: GeometryCollection holds no geometry", ErrGeometry)
	}

	var merged shape
	for _, member := range members {
		part, err := decodeAt(member, depth)
		if err != nil {
			return shape{}, err
		}
		merged.points = append(merged.points, part.points...)
		merged.lines = append(merged.lines, part.lines...)
		merged.polygons = append(merged.polygons, part.polygons...)
	}
	return merged, nil
}

func decodePrimitive(kind string, raw json.RawMessage) (shape, error) {
	switch kind {
	case "Point":
		at, err := decodePosition(raw)
		return shape{points: []domain.GeoPoint{at}}, err
	case "MultiPoint":
		points, err := decodePath(raw, 1)
		return shape{points: points}, err
	case "LineString":
		line, err := decodePath(raw, 2)
		return shape{lines: [][]domain.GeoPoint{line}}, err
	case "MultiLineString":
		lines, err := decodePaths(raw, 2)
		return shape{lines: lines}, err
	case "Polygon":
		surface, err := decodeSurface(raw)
		return shape{polygons: []polygon{surface}}, err
	case "MultiPolygon":
		return decodeMultiPolygon(raw)
	default:
		return shape{}, fmt.Errorf("%w: unknown type %q", ErrGeometry, kind)
	}
}

func decodeMultiPolygon(raw json.RawMessage) (shape, error) {
	var surfaces []json.RawMessage
	if err := json.Unmarshal(raw, &surfaces); err != nil {
		return shape{}, fmt.Errorf("%w: %v", ErrGeometry, err)
	}
	if len(surfaces) == 0 {
		return shape{}, fmt.Errorf("%w: MultiPolygon holds no polygon", ErrGeometry)
	}

	decoded := make([]polygon, 0, len(surfaces))
	for _, surface := range surfaces {
		one, err := decodeSurface(surface)
		if err != nil {
			return shape{}, err
		}
		decoded = append(decoded, one)
	}
	return shape{polygons: decoded}, nil
}

// decodeSurface reads a polygon's rings: the first is the outer boundary and
// the rest are holes.
func decodeSurface(raw json.RawMessage) (polygon, error) {
	rings, err := decodePaths(raw, 3)
	if err != nil {
		return polygon{}, err
	}
	return polygon{outer: rings[0], holes: rings[1:]}, nil
}

func decodePaths(raw json.RawMessage, minPoints int) ([][]domain.GeoPoint, error) {
	var paths []json.RawMessage
	if err := json.Unmarshal(raw, &paths); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGeometry, err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("%w: coordinates is empty", ErrGeometry)
	}

	decoded := make([][]domain.GeoPoint, 0, len(paths))
	for _, path := range paths {
		points, err := decodePath(path, minPoints)
		if err != nil {
			return nil, err
		}
		decoded = append(decoded, points)
	}
	return decoded, nil
}

func decodePath(raw json.RawMessage, minPoints int) ([]domain.GeoPoint, error) {
	var positions []json.RawMessage
	if err := json.Unmarshal(raw, &positions); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGeometry, err)
	}
	if len(positions) < minPoints {
		return nil, fmt.Errorf("%w: %d coordinates, want at least %d", ErrGeometry, len(positions), minPoints)
	}

	points := make([]domain.GeoPoint, 0, len(positions))
	for _, position := range positions {
		at, err := decodePosition(position)
		if err != nil {
			return nil, err
		}
		points = append(points, at)
	}
	return points, nil
}

// decodePosition reads one GeoJSON position. Index 0 is LONGITUDE — the reverse
// of every argument list in this package, and the one mistake nothing
// downstream can catch, because a swap of Bengaluru's 77.59/12.97 leaves both
// values in range and puts the shopfront off the coast of Somalia.
func decodePosition(raw json.RawMessage) (domain.GeoPoint, error) {
	var position []float64
	if err := json.Unmarshal(raw, &position); err != nil {
		return domain.GeoPoint{}, fmt.Errorf("%w: %v", ErrGeometry, err)
	}
	if len(position) < 2 {
		return domain.GeoPoint{}, fmt.Errorf("%w: position has %d values, want 2", ErrGeometry, len(position))
	}

	at := domain.GeoPoint{Lat: position[1], Lon: position[0]}
	if at.Lat < -90 || at.Lat > 90 || at.Lon < -180 || at.Lon > 180 {
		return domain.GeoPoint{}, fmt.Errorf("%w: coordinate %v is outside WGS 84", ErrGeometry, at)
	}
	return at, nil
}
