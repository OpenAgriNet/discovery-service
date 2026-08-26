// Package discover holds the read path: a Beckn intent turned into a
// domain.SearchQuery, and the service that answers it.
package discover

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/jsonpath"
)

// Page is the request's pagination, which arrives as HTTP query parameters
// rather than inside the intent.
//
// A struct rather than two adjacent ints: `limit, offset` and `offset, limit`
// compile identically and mean different pages, and nothing downstream would
// notice the swap.
type Page struct {
	Limit  int
	Offset int
}

// answeredOps are the seven this service evaluates as cell-set algebra (A10).
var answeredOps = map[string]domain.SpatialOp{
	beckn.OpSIntersects: domain.OpIntersects,
	beckn.OpSDisjoint:   domain.OpDisjoint,
	beckn.OpSWithin:     domain.OpWithin,
	beckn.OpSContains:   domain.OpContains,
	beckn.OpSOverlaps:   domain.OpOverlaps,
	beckn.OpSEquals:     domain.OpEquals,
	beckn.OpSDWithin:    domain.OpDWithin,
}

// quantifiers, with the empty string reading as ANY. An unrecognised one is
// refused rather than downgraded: NONE and ANY ask opposite questions, so a
// typo would invert the caller's intent and answer it confidently.
var quantifiers = map[string]domain.Quantifier{
	"":                   domain.QuantifierAny,
	beckn.QuantifierAny:  domain.QuantifierAny,
	beckn.QuantifierAll:  domain.QuantifierAll,
	beckn.QuantifierNone: domain.QuantifierNone,
}

// The SRID spellings that all mean WGS 84. Anything else is refused, never
// ignored: EPSG:3857 coordinates are metres, and reading them as degrees puts
// the query in the Atlantic and returns an honest-looking empty page.
var wgs84 = map[string]bool{
	"":                           true,
	"EPSG:4326":                  true,
	"urn:ogc:def:crs:OGC::CRS84": true,
	"CRS84":                      true,
}

// geometryTypes is the RFC 7946 set a query geometry must be one of.
var geometryTypes = map[string]bool{
	beckn.GeometryPoint:              true,
	beckn.GeometryMultiPoint:         true,
	beckn.GeometryLineString:         true,
	beckn.GeometryMultiLineString:    true,
	beckn.GeometryPolygon:            true,
	beckn.GeometryMultiPolygon:       true,
	beckn.GeometryGeometryCollection: true,
}

// MapIntent reduces a discover request to the query a backend answers,
// separating the faults that must refuse the request from those that only
// qualify it.
//
// It rejects rather than skips. Every refusal here is a case where continuing
// would WIDEN the query — an unreadable targets pointer read as "every
// geometry", an unknown quantifier read as ANY, an ignored SRID read as
// degrees — and a widened answer is indistinguishable at the caller from a
// correct one.
//
// NetworkID is deliberately not set: the service reads it from the envelope,
// and empty means EVERY network. Defaulting it here to config.App.Network would
// borrow publish's visibleTo default (C8) — a different field answering a
// different question — and quietly return discover to single-network scoping.
func MapIntent(
	intent beckn.Intent, envelope beckn.Context, page Page, cfg config.Config,
) (domain.SearchQuery, []domain.Fault, []domain.Fault) {
	schemas, schemaFaults := mapSchemaContext(envelope)
	spatial, targets, spatialFatal, partial := mapSpatial(intent.Spatial, cfg)
	limit, offset, pageFaults := mapPage(page, cfg.Search)

	fatal := append(append(schemaFaults, spatialFatal...), pageFaults...)

	return domain.SearchQuery{
		Text:        intent.TextSearch,
		Schemas:     schemas,
		Spatial:     spatial,
		TargetPaths: targets,
		Limit:       limit,
		Offset:      offset,
	}, fatal, partial
}

// mapSchemaContext reads the schema predicate off the ENVELOPE, not the intent.
//
// Absent or empty returns nil, and the repository then emits no schema clause.
// Returning an empty non-nil slice would be the bug that empties every
// response.
func mapSchemaContext(envelope beckn.Context) ([]domain.SchemaFilter, []domain.Fault) {
	if len(envelope.SchemaContext) == 0 {
		return nil, nil
	}

	var out []domain.SchemaFilter
	var faults []domain.Fault

	for i, uri := range envelope.SchemaContext {
		base, fragment, _ := strings.Cut(uri, "#")
		if base == "" {
			faults = append(faults, domain.Fault{
				Path:    fmt.Sprintf("$['context']['schemaContext'][%d]", i),
				Code:    string(beckn.CodeContextInvalidField),
				Message: "schemaContext entry has no context URI",
			})
			// continue, not fall-through. Emitting SchemaFilter{Context: ""}
			// after faulting appends a predicate that matches nothing —
			// harmless only for as long as this fault stays fatal, and
			// silently emptying every response the day someone softens it.
			continue
		}
		// Cut splits on the FIRST '#', so a second one stays in the fragment.
		// An empty fragment means "any type under this context".
		out = append(out, domain.SchemaFilter{Context: base, Type: fragment})
	}

	return out, faults
}

// mapPage resolves the page, clamping what can be clamped honestly and refusing
// what cannot.
//
// A limit over MaxPageSize is clamped because the caller still gets the results
// they asked about. A page past the retrieval depth is refused because `fused`
// holds at most MaxCandidatesPerMode ids: the slice would come back empty while
// Total correctly reports thousands, and an empty page 26 is indistinguishable
// from having reached the end.
func mapPage(page Page, search config.Search) (limit, offset int, faults []domain.Fault) {
	limit = page.Limit
	if limit <= 0 {
		limit = search.DefaultPageSize
	}
	if limit > search.MaxPageSize {
		limit = search.MaxPageSize
	}

	offset = page.Offset
	if offset < 0 {
		offset = 0
	}

	if offset+limit > search.MaxCandidatesPerMode {
		faults = append(faults, domain.Fault{
			Path: "$['offset']",
			Code: string(beckn.CodeSchemaInvalidFormat),
			Message: fmt.Sprintf(
				"offset %d plus limit %d passes the retrieval depth of %d",
				offset, limit, search.MaxCandidatesPerMode),
		})
	}
	return limit, offset, faults
}

// mapSpatial reduces the single supported spatial constraint to cells.
//
// It validates everything before covering anything: a fault list built from a
// half-covered constraint would name the symptom rather than the input, and the
// caller needs the input.
func mapSpatial(
	constraints []beckn.SpatialConstraint, cfg config.Config,
) (*domain.SpatialFilter, []string, []domain.Fault, []domain.Fault) {
	if len(constraints) == 0 {
		return nil, nil, nil, nil
	}
	if len(constraints) > 1 {
		return nil, nil, []domain.Fault{{
			Path:    "$['message']['intent']['spatial']",
			Code:    string(beckn.CodeSchemaTypeNotSupported),
			Message: "more than one spatial constraint is not supported",
		}}, nil
	}

	c := constraints[0]
	op, targets, fatal, partial := validateConstraint(c, cfg)
	if len(fatal) > 0 {
		return nil, nil, fatal, partial
	}

	return coverConstraint(c, op, cfg), targets, nil, partial
}

// validateConstraint checks every field of a constraint and reports all of them
// rather than the first, because a caller fixing one at a time round-trips once
// per mistake.
func validateConstraint(
	c beckn.SpatialConstraint, cfg config.Config,
) (op domain.SpatialOp, targets []string, fatal, partial []domain.Fault) {
	const at = "$['message']['intent']['spatial'][0]"

	op, ok := answeredOps[c.Op]
	switch {
	case ok:
	case c.Op == beckn.OpSTouches, c.Op == beckn.OpSCrosses:
		// Not "not yet": a cell decomposition has no measure-zero boundary, so
		// no resolution answers these. The message says which operator, because
		// a caller deciding whether to wait for a later release needs to know
		// it will never arrive.
		fatal = append(fatal, spatialFault(at+"['op']", beckn.CodeSchemaTypeNotSupported,
			c.Op+" cannot be approximated by a cell decomposition at any resolution"))
	default:
		fatal = append(fatal, spatialFault(at+"['op']", beckn.CodeSchemaInvalidFormat,
			"unknown spatial operator "+c.Op))
	}

	if !wgs84[c.SRID] {
		fatal = append(fatal, spatialFault(at+"['srid']", beckn.CodeSchemaInvalidFormat,
			"srid "+c.SRID+" is not WGS 84; coordinates are only read as EPSG:4326"))
	}
	if _, known := quantifiers[c.Quantifier]; !known {
		fatal = append(fatal, spatialFault(at+"['quantifier']", beckn.CodeSchemaInvalidFormat,
			"unknown quantifier "+c.Quantifier))
	}

	fatal = append(fatal, validateGeometry(c, at)...)
	distanceFatal, distancePartial := validateDistance(c, at, cfg)
	fatal = append(fatal, distanceFatal...)
	partial = append(partial, distancePartial...)

	targets, targetFaults := canonicalTargets(c.Targets, at)
	fatal = append(fatal, targetFaults...)

	return op, targets, fatal, partial
}

// validateGeometry refuses a query geometry that is not one of the seven
// RFC 7946 types, or is one but cannot be read.
func validateGeometry(c beckn.SpatialConstraint, at string) []domain.Fault {
	if c.Geometry == nil {
		return []domain.Fault{spatialFault(at+"['geometry']", beckn.CodeSchemaInvalidFormat,
			"a spatial constraint needs a geometry")}
	}
	if !geometryTypes[c.Geometry.Type] {
		return []domain.Fault{spatialFault(at+"['geometry']", beckn.CodeSchemaInvalidFormat,
			c.Geometry.Type+" is not one of the seven RFC 7946 geometry types")}
	}
	if err := geo.Validate(queryGeoJSON(c.Geometry)); err != nil {
		return []domain.Fault{spatialFault(at+"['geometry']", beckn.CodeSchemaInvalidFormat,
			fmt.Sprintf("query geometry cannot be read: %v", err))}
	}
	return nil
}

// validateDistance refuses a radius the deployment will not serve, and reports
// one sent where it has no meaning.
//
// The second is a PARTIAL rather than silence: `beckn.yaml` says distanceMeters
// is "Ignored for other ops", ignoring it is what we do, and a caller who sent
// one believes it is filtering.
func validateDistance(
	c beckn.SpatialConstraint, at string, cfg config.Config,
) (fatal, partial []domain.Fault) {
	if c.Op != beckn.OpSDWithin {
		if c.DistanceMeters != nil {
			partial = append(partial, spatialFault(at+"['distanceMeters']",
				beckn.CodeSchemaInvalidFormat, "distanceMeters is ignored for "+c.Op))
		}
		return nil, partial
	}

	maximum := float64(cfg.Search.MaxRadiusMeters)
	switch {
	case c.DistanceMeters == nil, *c.DistanceMeters <= 0:
		fatal = append(fatal, spatialFault(at+"['distanceMeters']",
			beckn.CodeSchemaInvalidFormat, "S_DWITHIN needs a distanceMeters above zero"))
	case *c.DistanceMeters > maximum:
		// Refused, not clamped: a clamped radius answers a different question
		// from the one asked, and nothing tells the caller it happened.
		fatal = append(fatal, spatialFault(at+"['distanceMeters']", beckn.CodeSchemaInvalidFormat,
			fmt.Sprintf("distanceMeters %g is above the configured maximum of %d",
				*c.DistanceMeters, cfg.Search.MaxRadiusMeters)))
	}
	return fatal, nil
}

// canonicalTargets puts the caller's pointers through the same function the
// publish walker used, which is what makes `target_path = ANY($1)` plain
// equality.
//
// A pointer that does not canonicalise is a fault and is DROPPED. Letting it
// through as "" would match no stored path; dropping it silently would leave an
// empty TargetPaths, which every backend reads as "every geometry" — the
// widened answer this mapper exists to refuse.
func canonicalTargets(targets beckn.Targets, at string) ([]string, []domain.Fault) {
	if len(targets) == 0 {
		return nil, nil
	}

	out := make([]string, 0, len(targets))
	var faults []domain.Fault

	for i, target := range targets {
		canonical := jsonpath.Canonicalise(target)
		if canonical == "" {
			faults = append(faults, spatialFault(
				fmt.Sprintf("%s['targets'][%d]", at, i),
				beckn.CodeSchemaInvalidJSONPath,
				"unrecognised targets expression "+target))
			continue
		}
		out = append(out, canonical)
	}

	if len(out) == 0 {
		return nil, faults
	}
	return out, faults
}

// coverConstraint reduces a validated constraint to the cell sets and box the
// repository compares against.
//
// A cover that declines — antimeridian, over budget — leaves the cell sets nil
// TOGETHER and lets Bounds decide. That is a widening, and it is the one this
// service accepts: the cells are an optimisation over the box, not the
// predicate itself.
func coverConstraint(c beckn.SpatialConstraint, op domain.SpatialOp, cfg config.Config) *domain.SpatialFilter {
	raw := queryGeoJSON(c.Geometry)
	query := domain.Geometry{Type: c.Geometry.Type, GeoJSON: raw}

	var radius float64
	if op == domain.OpDWithin && c.DistanceMeters != nil {
		radius = *c.DistanceMeters
	}

	filter := domain.SpatialFilter{
		Op:         op,
		RadiusM:    radius,
		Quantifier: quantifiers[c.Quantifier],
	}

	if full, cover, err := geo.CoverQuery(query, op, radius, cfg.Geo.ResolutionCells); err == nil {
		filter.CellsFull, filter.CellsCover = full, cover
	}
	if bounds, err := geo.BoundsFor(query, op, radius); err == nil {
		filter.Bounds = bounds
	}

	// Populated ONLY for Point-to-Point S_DWITHIN, the single case the exact
	// haversine refinement applies to. A non-nil Center on any other operator
	// would silently narrow that operator's answer.
	if op == domain.OpDWithin && c.Geometry.Type == beckn.GeometryPoint {
		var position []float64
		if err := json.Unmarshal(c.Geometry.Coordinates, &position); err == nil && len(position) >= 2 {
			filter.Center = &domain.GeoPoint{Lon: position[0], Lat: position[1]}
		}
	}

	return &filter
}

// queryGeoJSON re-renders a decoded query geometry.
//
// Unlike a published one, a query geometry is never stored, so re-marshalling
// loses nothing a caller can ask for back. Coordinates is already a
// json.RawMessage, so the numbers themselves survive verbatim.
func queryGeoJSON(geometry *beckn.GeoJSONGeometry) json.RawMessage {
	raw, err := json.Marshal(geometry)
	if err != nil {
		return nil
	}
	return raw
}

// spatialFault names a constraint fault at a path into the request.
func spatialFault(path string, code beckn.ErrorCode, message string) domain.Fault {
	return domain.Fault{Path: path, Code: string(code), Message: message}
}
