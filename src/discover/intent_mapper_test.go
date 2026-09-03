package discover_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/discover"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

// settings is the config the mapper reads, with the plan's declared defaults.
func settings() config.Config {
	return config.Config{
		Search: config.Search{
			DefaultPageSize:      20,
			MaxPageSize:          100,
			MaxCandidatesPerMode: 500,
			MaxRadiusMeters:      200000,
		},
		Geo: config.Geo{ResolutionCells: 8},
	}
}

func bengaluru() *beckn.GeoJSONGeometry {
	return &beckn.GeoJSONGeometry{
		Type:        beckn.GeometryPoint,
		Coordinates: json.RawMessage(`[77.5946,12.9716]`),
	}
}

// spatialIntent builds a one-constraint intent, so a test only states the part
// it is about.
func spatialIntent(c beckn.SpatialConstraint) beckn.Intent {
	return beckn.Intent{Spatial: []beckn.SpatialConstraint{c}}
}

func within(target string) beckn.SpatialConstraint {
	return beckn.SpatialConstraint{
		Op:       beckn.OpSIntersects,
		Targets:  beckn.Targets{target},
		Geometry: bengaluru(),
	}
}

func codesOf(faults []domain.Fault) string {
	out := make([]string, 0, len(faults))
	for _, f := range faults {
		out = append(out, f.Code+"@"+f.Path)
	}
	return strings.Join(out, ", ")
}

// The other half of the byte-identity pin. TestStoredTargetPathEqualsA...
// asserts it from the publish side; this asserts the mapper puts the same bytes
// in the ANY() array, so the two halves of the equality are checked against the
// same constant rather than against each other's implementation.
func TestTargetsAreCanonicalisedToWhatThePublishWalkerStores(t *testing.T) {
	intent := spatialIntent(within(`$.catalogs[*].provider.availableAt[*].geo`))

	query, fatal, _ := discover.MapIntent(intent, beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 0 {
		t.Fatalf("fatal = %s, want none", codesOf(fatal))
	}

	want := []string{`$['catalogs'][*]['provider']['availableAt'][*]['geo']`}
	if len(query.TargetPaths) != 1 || query.TargetPaths[0] != want[0] {
		t.Errorf("TargetPaths = %v, want %v", query.TargetPaths, want)
	}
}

// A targets expression this service cannot read is a 400, never an empty
// TargetPaths.
//
// Empty means "every geometry the resource can be found by", so reading a bad
// pointer as empty would answer a narrow question with the whole index — the
// caller is not told, and the answer looks like a successful search.
func TestUnrecognisedTargetsAreRefusedRatherThanWidened(t *testing.T) {
	intent := spatialIntent(within(`$..geo`))

	query, fatal, _ := discover.MapIntent(intent, beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 1 {
		t.Fatalf("fatal = %s, want exactly one", codesOf(fatal))
	}
	if fatal[0].Code != string(beckn.CodeSchemaInvalidJSONPath) {
		t.Errorf("Code = %q, want SCH_INVALID_JSONPATH", fatal[0].Code)
	}
	if len(query.TargetPaths) != 0 {
		t.Errorf("TargetPaths = %v, want none — a refused pointer must not become a wildcard", query.TargetPaths)
	}
}

// S_TOUCHES and S_CROSSES are legal enum values, so L1 validation lets them
// through. The mapper is the only thing between a caller and a silently wrong
// answer, and it says "not supported" rather than "not yet".
func TestTheTwoUnapproximableOperatorsAreRefused(t *testing.T) {
	for _, op := range []string{beckn.OpSTouches, beckn.OpSCrosses} {
		constraint := within(`$.catalogs[*].provider.availableAt[*].geo`)
		constraint.Op = op

		_, fatal, _ := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, settings())
		if len(fatal) != 1 {
			t.Fatalf("%s: fatal = %s, want exactly one", op, codesOf(fatal))
		}
		if fatal[0].Code != string(beckn.CodeSchemaTypeNotSupported) {
			t.Errorf("%s: Code = %q, want SCH_TYPE_NOT_SUPPORTED", op, fatal[0].Code)
		}
		if !strings.Contains(fatal[0].Message, op) {
			t.Errorf("%s: Message = %q, want it to name the operator", op, fatal[0].Message)
		}
	}
}

// An SRID is never ignored. A caller sending EPSG:3857 has coordinates in
// metres; reading them as degrees puts the query somewhere off the coast of
// Africa and returns an honest-looking empty page.
func TestAnUnknownSRIDIsRefusedAndTheKnownSpellingsAreNot(t *testing.T) {
	accepted := []string{"", "EPSG:4326", "urn:ogc:def:crs:OGC::CRS84", "CRS84"}
	for _, srid := range accepted {
		constraint := within(`$.catalogs[*].provider.availableAt[*].geo`)
		constraint.SRID = srid

		_, fatal, _ := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, settings())
		if len(fatal) != 0 {
			t.Errorf("srid %q: fatal = %s, want none", srid, codesOf(fatal))
		}
	}

	constraint := within(`$.catalogs[*].provider.availableAt[*].geo`)
	constraint.SRID = "EPSG:3857"

	_, fatal, _ := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 1 || fatal[0].Code != string(beckn.CodeSchemaInvalidFormat) {
		t.Fatalf("fatal = %s, want one SCH_INVALID_FORMAT", codesOf(fatal))
	}
}

// A radius past the configured ceiling is a refusal, not a clamp: a clamped
// radius answers a different question from the one asked, and the caller has no
// way to see that it happened.
func TestARadiusOverTheCeilingIsRefused(t *testing.T) {
	over := 200001.0
	constraint := within(`$.catalogs[*].provider.availableAt[*].geo`)
	constraint.Op = beckn.OpSDWithin
	constraint.DistanceMeters = &over

	_, fatal, _ := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 1 || fatal[0].Code != string(beckn.CodeSchemaInvalidFormat) {
		t.Fatalf("fatal = %s, want one SCH_INVALID_FORMAT", codesOf(fatal))
	}
	if !strings.Contains(fatal[0].Message, "200000") {
		t.Errorf("Message = %q, want it to name the boundary", fatal[0].Message)
	}
}

// The spec says distanceMeters is "Ignored for other ops". Ignored is what we
// do — but a caller who sent one believes it is filtering, so it is a PARTIAL
// naming the field rather than silence.
func TestDistanceOnANonDWithinOpIsAPartialNamingTheField(t *testing.T) {
	distance := 500.0
	constraint := within(`$.catalogs[*].provider.availableAt[*].geo`)
	constraint.DistanceMeters = &distance

	query, fatal, partial := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 0 {
		t.Fatalf("fatal = %s, want none — the constraint is still answerable", codesOf(fatal))
	}
	if len(partial) != 1 {
		t.Fatalf("partial = %s, want exactly one", codesOf(partial))
	}
	if !strings.Contains(partial[0].Message, "distanceMeters") {
		t.Errorf("Message = %q, want it to name the field", partial[0].Message)
	}
	if query.Spatial == nil || query.Spatial.RadiusM != 0 {
		t.Errorf("RadiusM = %v, want 0 — the value was ignored, which is what the partial says", query.Spatial)
	}
}

// Each quantifier survives to the domain, and an unrecognised one is refused
// rather than downgraded to ANY.
//
// A silent downgrade is the worst of the three: NONE means "no targeted
// geometry matches" and ANY means "at least one does", so a typo would invert
// the caller's question and answer it confidently.
func TestEachQuantifierArrivesAndAnUnknownOneIsRefused(t *testing.T) {
	cases := map[string]domain.Quantifier{
		"":     domain.QuantifierAny,
		"ANY":  domain.QuantifierAny,
		"ALL":  domain.QuantifierAll,
		"NONE": domain.QuantifierNone,
	}
	for sent, want := range cases {
		constraint := within(`$.catalogs[*].provider.availableAt[*].geo`)
		constraint.Quantifier = sent

		query, fatal, _ := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, settings())
		if len(fatal) != 0 {
			t.Fatalf("quantifier %q: fatal = %s, want none", sent, codesOf(fatal))
		}
		if query.Spatial == nil || query.Spatial.Quantifier != want {
			t.Errorf("quantifier %q mapped to %v, want %v", sent, query.Spatial, want)
		}
	}

	constraint := within(`$.catalogs[*].provider.availableAt[*].geo`)
	constraint.Quantifier = "SOME"

	_, fatal, _ := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 1 {
		t.Fatalf("fatal = %s, want exactly one", codesOf(fatal))
	}
}

// A query geometry that is not one of the seven RFC 7946 types is a 400.
func TestANonRFC7946QueryGeometryIsRefused(t *testing.T) {
	constraint := within(`$.catalogs[*].provider.availableAt[*].geo`)
	constraint.Geometry = &beckn.GeoJSONGeometry{
		Type:        "Circle",
		Coordinates: json.RawMessage(`[77.5946,12.9716]`),
	}

	_, fatal, _ := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 1 || fatal[0].Code != string(beckn.CodeSchemaInvalidFormat) {
		t.Fatalf("fatal = %s, want one SCH_INVALID_FORMAT", codesOf(fatal))
	}
}

// schemaContext is an ENVELOPE field, split on the FIRST '#'.
//
// A URI with no fragment leaves Type empty, which the repository reads as "any
// type under this context" — not as a type literally named "".
func TestSchemaContextSplitsOnTheFirstHash(t *testing.T) {
	envelope := beckn.Context{SchemaContext: []string{
		"https://beckn.org/Agri#SeedLot",
		"https://beckn.org/Agri",
		"https://beckn.org/Agri#Seed#Lot",
	}}

	query, fatal, _ := discover.MapIntent(beckn.Intent{}, envelope, discover.Page{}, settings())
	if len(fatal) != 0 {
		t.Fatalf("fatal = %s, want none", codesOf(fatal))
	}

	want := []domain.SchemaFilter{
		{Context: "https://beckn.org/Agri", Type: "SeedLot"},
		{Context: "https://beckn.org/Agri", Type: ""},
		{Context: "https://beckn.org/Agri", Type: "Seed#Lot"},
	}
	if len(query.Schemas) != len(want) {
		t.Fatalf("Schemas = %v, want %v", query.Schemas, want)
	}
	for i := range want {
		if query.Schemas[i] != want[i] {
			t.Errorf("Schemas[%d] = %v, want %v", i, query.Schemas[i], want[i])
		}
	}
}

// An entry with no context URI faults AND is dropped.
//
// Emitting SchemaFilter{Context: ""} after faulting appends a predicate that
// matches nothing — harmless only for as long as the fault stays fatal, and
// silently emptying every response the day someone softens it to a warning.
func TestASchemaContextEntryWithNoBaseFaultsAndIsDropped(t *testing.T) {
	envelope := beckn.Context{SchemaContext: []string{"#SeedLot", "https://beckn.org/Agri#SeedLot"}}

	query, fatal, _ := discover.MapIntent(beckn.Intent{}, envelope, discover.Page{}, settings())
	if len(fatal) != 1 || fatal[0].Code != string(beckn.CodeContextInvalidField) {
		t.Fatalf("fatal = %s, want one CTX_INVALID_FIELD", codesOf(fatal))
	}
	if len(query.Schemas) != 1 {
		t.Errorf("Schemas = %v, want only the readable entry", query.Schemas)
	}
}

// An absent schemaContext is no predicate at all, not a predicate matching
// nothing. A non-nil empty slice here is the bug that empties every response.
func TestAnAbsentSchemaContextEmitsNoPredicate(t *testing.T) {
	query, fatal, _ := discover.MapIntent(beckn.Intent{}, beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 0 {
		t.Fatalf("fatal = %s, want none", codesOf(fatal))
	}
	if query.Schemas != nil {
		t.Errorf("Schemas = %#v, want nil", query.Schemas)
	}
}

// The one clamp this service performs quietly, and the one it refuses to.
//
// A limit over MaxPageSize still gives the caller the results they asked about,
// so it is clamped. A page past the retrieval depth cannot be answered at all:
// `fused` holds at most MaxCandidatesPerMode ids, so the slice would return
// empty while Total correctly reports thousands — indistinguishable from having
// reached the end.
func TestLimitIsClampedAndAPagePastTheRetrievalDepthIsRefused(t *testing.T) {
	cfg := settings()

	unset, _, _ := discover.MapIntent(beckn.Intent{}, beckn.Context{}, discover.Page{}, cfg)
	if unset.Limit != cfg.Search.DefaultPageSize {
		t.Errorf("Limit = %d, want the default %d", unset.Limit, cfg.Search.DefaultPageSize)
	}

	clamped, fatal, _ := discover.MapIntent(beckn.Intent{}, beckn.Context{}, discover.Page{Limit: 5000}, cfg)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %s, want none — an over-large limit is clamped", codesOf(fatal))
	}
	if clamped.Limit != cfg.Search.MaxPageSize {
		t.Errorf("Limit = %d, want the ceiling %d", clamped.Limit, cfg.Search.MaxPageSize)
	}

	deep := discover.Page{Limit: 100, Offset: cfg.Search.MaxCandidatesPerMode}
	_, fatal, _ = discover.MapIntent(beckn.Intent{}, beckn.Context{}, deep, cfg)
	if len(fatal) != 1 {
		t.Fatalf("fatal = %s, want exactly one", codesOf(fatal))
	}
	if !strings.Contains(fatal[0].Message, "500") {
		t.Errorf("Message = %q, want it to name the boundary", fatal[0].Message)
	}
}

// NetworkID is NOT set here. The service sets it from the envelope, and empty
// means EVERY network; defaulting it in the mapper would quietly put discover
// back to single-network scoping.
func TestTheMapperLeavesNetworkScopingToTheService(t *testing.T) {
	envelope := beckn.Context{SchemaContext: []string{"https://beckn.org/Agri"}}

	query, _, _ := discover.MapIntent(beckn.Intent{}, envelope, discover.Page{}, settings())
	if query.NetworkID != "" {
		t.Errorf("NetworkID = %q, want empty", query.NetworkID)
	}
}

// A negative offset reads as the first page rather than as a caller error:
// unlike an unreadable page (C11's neighbour above), a negative number is not
// ambiguous about what the caller meant.
func TestANegativeOffsetIsClampedToZero(t *testing.T) {
	query, fatal, _ := discover.MapIntent(beckn.Intent{}, beckn.Context{}, discover.Page{Offset: -5}, settings())
	if len(fatal) != 0 {
		t.Fatalf("fatal = %s, want none", codesOf(fatal))
	}
	if query.Offset != 0 {
		t.Errorf("Offset = %d, want 0", query.Offset)
	}
}

// A10 answers exactly one spatial constraint. A second one is refused rather
// than ANDed or ORed, because the spec leaves undefined which of the two this
// service would silently have picked.
func TestMoreThanOneSpatialConstraintIsRefused(t *testing.T) {
	target := `$.catalogs[*].provider.availableAt[*].geo`
	intent := beckn.Intent{Spatial: []beckn.SpatialConstraint{within(target), within(target)}}

	_, fatal, _ := discover.MapIntent(intent, beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 1 || fatal[0].Code != string(beckn.CodeSchemaTypeNotSupported) {
		t.Fatalf("fatal = %s, want one SCH_TYPE_NOT_SUPPORTED", codesOf(fatal))
	}
}

// An operator that is neither answerable nor one of the two named
// unapproximable ones (S_TOUCHES/S_CROSSES) is unknown outright — a typo, not a
// future capability.
func TestAnUnknownSpatialOperatorIsRefused(t *testing.T) {
	constraint := within(`$.catalogs[*].provider.availableAt[*].geo`)
	constraint.Op = "S_NEAR"

	_, fatal, _ := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 1 || fatal[0].Code != string(beckn.CodeSchemaInvalidFormat) {
		t.Fatalf("fatal = %s, want one SCH_INVALID_FORMAT", codesOf(fatal))
	}
	if !strings.Contains(fatal[0].Message, "S_NEAR") {
		t.Errorf("Message = %q, want it to name the operator", fatal[0].Message)
	}
}

// No geometry at all is refused with the same code an unreadable one is, since
// both leave the constraint with nothing to cover against.
func TestAMissingGeometryIsRefused(t *testing.T) {
	constraint := within(`$.catalogs[*].provider.availableAt[*].geo`)
	constraint.Geometry = nil

	_, fatal, _ := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 1 || fatal[0].Code != string(beckn.CodeSchemaInvalidFormat) {
		t.Fatalf("fatal = %s, want one SCH_INVALID_FORMAT", codesOf(fatal))
	}
}

// A geometry of a recognised type whose coordinates cannot be read at all —
// not merely wrong, but not JSON — is refused rather than passed through to a
// backend that will fail on it far from the caller who sent it.
func TestAGeometryThatCannotBeReadIsRefused(t *testing.T) {
	constraint := within(`$.catalogs[*].provider.availableAt[*].geo`)
	constraint.Geometry = &beckn.GeoJSONGeometry{
		Type:        beckn.GeometryPoint,
		Coordinates: json.RawMessage(`{not valid`),
	}

	_, fatal, _ := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 1 || fatal[0].Code != string(beckn.CodeSchemaInvalidFormat) {
		t.Fatalf("fatal = %s, want one SCH_INVALID_FORMAT", codesOf(fatal))
	}
	if !strings.Contains(fatal[0].Message, "cannot be read") {
		t.Errorf("Message = %q, want it to say the geometry could not be read", fatal[0].Message)
	}
}

// f64 turns a literal into the pointer beckn.SpatialConstraint.DistanceMeters
// needs, so a test can state "zero" and "unset" as two different values.
func f64(v float64) *float64 { return &v }

// S_DWITHIN needs a positive radius. Unset and non-positive are the same
// refusal, because both leave the operator with no radius to search.
func TestSDWithinNeedsAPositiveDistance(t *testing.T) {
	for _, distance := range []*float64{nil, f64(0), f64(-5)} {
		constraint := within(`$.catalogs[*].provider.availableAt[*].geo`)
		constraint.Op = beckn.OpSDWithin
		constraint.DistanceMeters = distance

		_, fatal, _ := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, settings())
		if len(fatal) != 1 || fatal[0].Code != string(beckn.CodeSchemaInvalidFormat) {
			t.Fatalf("distance %v: fatal = %s, want one SCH_INVALID_FORMAT", distance, codesOf(fatal))
		}
	}
}

// The success path S_DWITHIN exists for: a radius that survives to the query,
// and — Point-to-Point being the one case the exact haversine refinement
// applies to — a Center for coverConstraint to hand the repository.
func TestSDWithinSetsTheRadiusAndCenterForAPoint(t *testing.T) {
	distance := 5000.0
	constraint := within(`$.catalogs[*].provider.availableAt[*].geo`)
	constraint.Op = beckn.OpSDWithin
	constraint.DistanceMeters = &distance

	query, fatal, partial := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 0 {
		t.Fatalf("fatal = %s, want none", codesOf(fatal))
	}
	if len(partial) != 0 {
		t.Errorf("partial = %s, want none — distanceMeters is exactly what S_DWITHIN uses", codesOf(partial))
	}
	if query.Spatial == nil || query.Spatial.RadiusM != distance {
		t.Fatalf("Spatial = %+v, want RadiusM %g", query.Spatial, distance)
	}
	if query.Spatial.Center == nil || query.Spatial.Center.Lon != 77.5946 || query.Spatial.Center.Lat != 12.9716 {
		t.Errorf("Center = %+v, want the query point", query.Spatial.Center)
	}
}

// No targets at all means every geometry, the same widened answer an
// unrecognised one is refused for — the difference is that this one was never
// sent, rather than sent and unreadable.
func TestNoTargetsMeansEveryGeometry(t *testing.T) {
	constraint := beckn.SpatialConstraint{Op: beckn.OpSIntersects, Geometry: bengaluru()}

	query, fatal, _ := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 0 {
		t.Fatalf("fatal = %s, want none", codesOf(fatal))
	}
	if len(query.TargetPaths) != 0 {
		t.Errorf("TargetPaths = %v, want none", query.TargetPaths)
	}
}

// validateConstraint's own claim — "reports all of them rather than the
// first" — checked as a combination rather than one field at a time: four
// independent faults from one constraint, not the first one short-circuiting
// the rest.
func TestValidateConstraintReportsEveryFaultNotJustTheFirst(t *testing.T) {
	constraint := beckn.SpatialConstraint{
		Op:         "S_NEAR",
		SRID:       "EPSG:3857",
		Quantifier: "SOME",
		Targets:    beckn.Targets{`$.catalogs[*].provider.availableAt[*].geo`},
		// Geometry left nil — a fourth, independent fault.
	}

	_, fatal, _ := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, settings())
	if len(fatal) != 4 {
		t.Fatalf("fatal = %s, want 4 — one per bad field, not just the first", codesOf(fatal))
	}
}

// mapPage's own boundary: `>` refuses, so a page landing EXACTLY on the
// retrieval depth must be answered, not refused. TestLimitIsClampedAndAPagePastTheRetrievalDepthIsRefused
// only asserts the over side of this line.
func TestAPageExactlyAtTheRetrievalDepthIsNotRefused(t *testing.T) {
	cfg := settings()
	page := discover.Page{Limit: 100, Offset: cfg.Search.MaxCandidatesPerMode - 100}

	_, fatal, _ := discover.MapIntent(beckn.Intent{}, beckn.Context{}, page, cfg)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %s, want none — offset+limit lands exactly on the depth, not past it", codesOf(fatal))
	}
}

// validateDistance's own boundary: `>` refuses, so a radius EXACTLY at the
// ceiling must be answered. TestARadiusOverTheCeilingIsRefused only asserts
// the value one metre past it.
func TestARadiusExactlyAtTheCeilingIsNotRefused(t *testing.T) {
	cfg := settings()
	atCeiling := float64(cfg.Search.MaxRadiusMeters)
	constraint := within(`$.catalogs[*].provider.availableAt[*].geo`)
	constraint.Op = beckn.OpSDWithin
	constraint.DistanceMeters = &atCeiling

	_, fatal, _ := discover.MapIntent(spatialIntent(constraint), beckn.Context{}, discover.Page{}, cfg)
	if len(fatal) != 0 {
		t.Fatalf("fatal = %s, want none — the ceiling itself is answerable", codesOf(fatal))
	}
}
