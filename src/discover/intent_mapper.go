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
// rather than inside the intent. A struct rather than two adjacent ints, because
// `limit, offset` and `offset, limit` compile identically.
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
// refused rather than downgraded: NONE and ANY ask opposite questions.
var quantifiers = map[string]domain.Quantifier{
	"":                   domain.QuantifierAny,
	beckn.QuantifierAny:  domain.QuantifierAny,
	beckn.QuantifierAll:  domain.QuantifierAll,
	beckn.QuantifierNone: domain.QuantifierNone,
}

// wgs84 is the SRID spellings that all mean WGS 84. Anything else is refused,
// never ignored: EPSG:3857 coordinates are metres, and reading them as degrees
// puts the query in the Atlantic and answers an honest-looking empty page.
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
// geometry", an unknown quantifier read as ANY, an ignored SRID read as degrees
// — and a widened answer is indistinguishable at the caller from a correct one.
//
// NetworkID is deliberately not set here; Service.Discover reads it off the
// envelope, and says why.
func MapIntent(
	intent beckn.Intent, envelope beckn.Context, page Page, cfg config.Config,
) (domain.SearchQuery, []domain.Fault, []domain.Fault) {
	schemas, schemaFaults := mapSchemaContext(envelope)
	spatial, targets, spatialFatal, partial := mapSpatial(intent.Spatial, cfg)
	limit, offset, pageFaults := mapPage(page, cfg.Search)

	// Trimmed ONCE, here, and every reader below takes it from this variable.
	// Whitespace is not a term: `"   "` is non-empty and narrows nothing, which
	// is exactly the input that walks past a guard spelled
	// `intent.TextSearch != ""`.
	text := strings.TrimSpace(intent.TextSearch)

	// Whether anything else has already cut the corpus down. Read from the
	// MAPPED values rather than from the intent: a spatial constraint that
	// faulted is not a constraint, and treating it as one would let mapFilters'
	// guard be defeated by sending a broken one.
	narrowed := text != "" || spatial != nil || len(schemas) > 0
	filters, filterFaults := mapFilters(intent.Filters, narrowed)

	fatal := append(append(append(schemaFaults, spatialFatal...), pageFaults...), filterFaults...)
	fatal = append(fatal, textSearchFaults(text, cfg.Search.EnableTextSearch)...)
	fatal = append(fatal, criterionFaults(intent, text, cfg.Search.EnableTextSearch)...)

	return domain.SearchQuery{
		Text:        text,
		Schemas:     schemas,
		Filters:     filters,
		Spatial:     spatial,
		TargetPaths: targets,
		Limit:       limit,
		Offset:      offset,
	}, fatal, partial
}

// textSearchFaults refuses a term on a deployment that switched free-text
// retrieval off (A27).
//
// Refused rather than degraded, which is the opposite of what negotiate does one
// layer down. There the mode is missing from the BACKEND and dropping it still
// leaves the query the caller wrote; here the term IS the query, and running the
// remaining modes without it answers the whole corpus under a 200 — the widening
// this mapper's doc comment says it exists to refuse. On an intent that also
// carried spatial or filters the answer would merely be too wide, which is the
// same failure with a smaller blast radius and no reason to treat differently.
//
// The TRIMMED term, like every other reader of it: `"   "` asks for no retrieval
// mode at all, so criterionFaults below is the honest complaint about it and
// this would be a false one.
//
// The message names the two criteria that still work and not the environment
// variable that turned this one off. A caller cannot act on the variable, and
// the operator has docs/publish-and-discover.md; the response says what to send
// instead, which is the part the caller can use.
func textSearchFaults(text string, enabled bool) []domain.Fault {
	if enabled || text == "" {
		return nil
	}
	return []domain.Fault{{
		Path: "$['message']['intent']['textSearch']",
		Code: string(beckn.CodeSchemaTypeNotSupported),
		Message: "textSearch is not supported here; please discover through " +
			"spatial or filters",
	}}
}

// criterionFaults refuses an intent that gives the search nothing to run: one of
// textSearch, spatial or filters must be present, because modesFor reads those
// three and nothing else. examples/README.md, case 17, is the reasoning.
//
// Read off the RAW intent, unlike `narrowed` above: a faulted spatial must not
// count there and must count here, or "you sent no criteria" is a false sentence
// stacked on the fault that already names the real mistake. `text` is the
// exception and is taken as a parameter because it must be the TRIMMED value —
// modesFor reads that one, so a raw guard would admit `"   "`.
//
// The LIST it offers shrinks with the deployment (A27). Naming textSearch to a
// caller this service would then refuse walks them into a second 400: told to
// send one of three, they send the first and are told it is not answered. The
// GUARD does not shrink with it — a term still satisfies "you sent a criterion",
// and textSearchFaults above is the fault that names the real mistake. Both
// firing on one request would report a missing criterion that was sent.
func criterionFaults(intent beckn.Intent, text string, textSearchEnabled bool) []domain.Fault {
	if text != "" || len(intent.Spatial) > 0 || intent.Filters != nil {
		return nil
	}

	criteria := "textSearch, spatial or filters"
	if !textSearchEnabled {
		criteria = "spatial or filters (textSearch is not supported here)"
	}
	return []domain.Fault{{
		Path: "$['message']['intent']",
		Code: string(beckn.CodeSchemaInvalidFormat),
		Message: "an intent needs at least one of " + criteria + "; " +
			"schemaContext narrows a search but cannot drive one, so an intent " +
			"carrying only it would answer an empty page rather than a refusal",
	}}
}

// mapSchemaContext reads the schema predicate off the ENVELOPE, not the intent.
//
// Absent or empty returns nil, and the repository then emits no schema clause.
// An empty non-nil slice would be the bug that empties every response.
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
			// continue, not fall-through: SchemaFilter{Context: ""} is a
			// predicate matching nothing, harmless only for as long as this
			// fault stays fatal.
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
// A limit over MaxPageSize is clamped, because the caller still gets the results
// they asked about. A page past MaxCandidatesPerMode is refused, because it would
// come back empty and an empty page 26 is indistinguishable from the end.
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

// mapSpatial reduces the single supported spatial constraint to cells. It
// validates everything before covering anything, so that a fault names the input
// rather than the symptom.
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
// rather than the first, so a caller does not round-trip once per mistake.
func validateConstraint(
	c beckn.SpatialConstraint, cfg config.Config,
) (op domain.SpatialOp, targets []string, fatal, partial []domain.Fault) {
	const at = "$['message']['intent']['spatial'][0]"

	op, ok := answeredOps[c.Op]
	switch {
	case ok:
	case c.Op == beckn.OpSTouches, c.Op == beckn.OpSCrosses:
		// Refused, not deferred (A10) — so the message names the operator, for
		// a caller deciding whether to wait for a later release.
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
// is "Ignored for other ops", and a caller who sent one believes it filters.
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
// A pointer that does not canonicalise is a fault and is DROPPED — "" would match
// no stored path, and dropping it silently would leave an empty TargetPaths,
// which every backend reads as "every geometry".
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
// TOGETHER and lets Bounds decide. That is a widening, and the one this service
// accepts: the cells are an optimisation over the box, not the predicate.
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
	// haversine refinement applies to: a non-nil Center on any other operator
	// silently narrows that operator's answer.
	if op == domain.OpDWithin && c.Geometry.Type == beckn.GeometryPoint {
		var position []float64
		if err := json.Unmarshal(c.Geometry.Coordinates, &position); err == nil && len(position) >= 2 {
			filter.Center = &domain.GeoPoint{Lon: position[0], Lat: position[1]}
		}
	}

	return &filter
}

// queryGeoJSON re-renders a decoded query geometry. Unlike a published one it is
// never stored, so re-marshalling loses nothing a caller can ask back —
// Coordinates is already a json.RawMessage, so the numbers survive verbatim.
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
