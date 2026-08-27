package domain

import "time"

// GeoPoint is a WGS 84 coordinate.
type GeoPoint struct {
	Lat float64
	Lon float64
}

// BBox is an axis-aligned bounding box in WGS 84 degrees.
type BBox struct {
	MinLat float64
	MaxLat float64
	MinLon float64
	MaxLon float64
}

// SpatialOp is a CQL2 spatial operator.
//
// All nine are named, including the two this service refuses. A refusal has to
// name the operator it is refusing, and it can only do that if the operator has
// a name here — recognising S_TOUCHES in order to reject it is not the same as
// pretending it does not exist.
type SpatialOp string

// The seven operators answered as cell-set algebra (A10).
const (
	OpIntersects SpatialOp = "S_INTERSECTS"
	OpDisjoint   SpatialOp = "S_DISJOINT"
	OpWithin     SpatialOp = "S_WITHIN"
	OpContains   SpatialOp = "S_CONTAINS"
	OpOverlaps   SpatialOp = "S_OVERLAPS"
	OpEquals     SpatialOp = "S_EQUALS"
	OpDWithin    SpatialOp = "S_DWITHIN"
)

// The two refused as unapproximable at any resolution (A10). A cell
// decomposition has no measure-zero boundary, so no resolution answers them;
// listing them as "later" would be a promise nothing in this design can keep.
const (
	OpTouches SpatialOp = "S_TOUCHES"
	OpCrosses SpatialOp = "S_CROSSES"
)

// Quantifier says how a constraint applies across the geometries a resource can
// be found by.
//
// Not a bool. `Negate bool` held two of the three and there is no honest value
// for ALL, which is NOT EXISTS over the negated predicate rather than over the
// predicate. A third state added to a bool becomes a second bool, and then a
// pair with an unrepresentable-but-constructible combination.
type Quantifier string

// The three quantifiers.
const (
	QuantifierAny  Quantifier = "ANY"
	QuantifierAll  Quantifier = "ALL"
	QuantifierNone Quantifier = "NONE"
)

// SpatialFilter is one spatial constraint, already reduced to cells.
//
// ONE type for all seven answered operators, because they differ only in which
// set relation the repository applies. Seven filter types would be seven places
// for the quantifier handling to drift.
type SpatialFilter struct {
	Op SpatialOp

	// The query geometry's two covers: CellsFull is a guaranteed subset and
	// proves positives, CellsCover a guaranteed superset and proves negatives.
	//
	// They are nil TOGETHER. A cover that declined — antimeridian, over budget —
	// disables the cell predicate entirely and leaves Bounds to decide. One
	// without the other is a state the repository has no branch for, because
	// CoverQuery cannot produce it.
	CellsFull  []uint64
	CellsCover []uint64

	// nil means the cover declined to produce a box, not a box that matches
	// everything.
	Bounds *BBox

	// Populated ONLY for Point-to-Point S_DWITHIN, the single case the exact
	// haversine refinement applies to. A non-nil Center on any other operator
	// would silently narrow that operator's answer.
	Center  *GeoPoint
	RadiusM float64

	Quantifier Quantifier
}

// SchemaFilter is one entry of the schema predicate.
//
// Type == "" means "any type under this context". The two halves are compared
// together rather than as a pair of independent IN lists, because a request for
// [schema.org#GroceryItem, beckn.org/Mobility#RideService] must not match a
// resource that is schema.org + RideService.
type SchemaFilter struct {
	Context string
	Type    string
}

// AttributeFilter is a structured predicate over the composite the store keeps
// for each resource (Task 22).
//
// Expression is PostgreSQL SQL/JSON path (C10), ALREADY validated — the store
// is handed an expression it may cast and run, never one it must interpret.
//
// There is no Root, and its absence is A18. Root named the column an expression
// was rebased onto, which per-column routing needed and which per-column
// routing cannot survive: `catalog.x == 1 || offer.y == 2` is a ROW-level
// disjunction with no decomposition into per-table results. One composite
// column answers it, one root reaches that column, and a field that can hold
// only one value is a field two code paths will eventually disagree about.
//
// A store that cannot run it must narrow NOTHING and say so in Degraded,
// because a wrongly narrowed page and a correctly narrowed one are the same page
// at the caller.
type AttributeFilter struct {
	Expression string
}

// SearchQuery is a discover request reduced to what a backend needs.
type SearchQuery struct {
	Text string

	// "" means UNSCOPED — every network. See Scope.
	NetworkID string

	// Empty means no schema predicate at all, NOT a predicate matching nothing.
	Schemas []SchemaFilter

	Spatial *SpatialFilter

	// The spatial constraint's `targets`, already canonicalised. Empty means
	// every geometry the resource can be found by — its own, plus its
	// catalog's.
	TargetPaths []string

	Filters []AttributeFilter

	Limit  int
	Offset int
}

// SearchResult is one page of an answered query.
//
// Degraded names the retrieval modes that did not contribute, and reaches the
// caller as the X-Beckn-Degraded response header rather than a body key:
// OnDiscoverAction is additionalProperties:false with exactly `catalogs`, so a
// `degraded` key would be a response that fails validation at the first
// consumer strict enough to check (C11).
// There is NO `Total` (A19). OnDiscoverAction is additionalProperties:false
// with `catalogs` as its only property, so a count has nowhere on the wire to
// go — and it was computed on every request anyway. Measured on PG16 over 100k
// rows: retrieval with its scope gate, text predicate and spatial join answers
// in 1.5 ms under LIMIT 200, while the matching counter costs 150.6 ms, because
// it cannot take a LIMIT without making the number wrong in the one state a
// caller cannot detect. 100x the query it accompanied, for a value the service
// discarded. Removed rather than deferred: a deferral reads as "we will use
// this later", and nothing in Phase 1 can. Should a header ever carry a count,
// it returns a CAPPED one.
type SearchResult struct {
	Catalogs []Catalog
	Degraded []string
}

// Scope is the request-wide gate every retrieval mode reads (A6).
//
// One instant, captured once per request, so that every mode in a concurrent
// search agrees on "now". Postgres does not read it — its gate calls now(), the
// transaction's own instant. It exists for the backends that have no now(): the
// memory store, and the tests, which must be able to ask what was live at 23:00
// without waiting until 23:00.
//
// NetworkID == "" means UNSCOPED. Every backend must read that as "emit no
// network predicate", never as a literal network id that matches nothing and
// never by falling back to APP_NETWORK_ID — that fallback belongs to publish's
// visibleTo default (C8), a different field answering a different question.
type Scope struct {
	NetworkID string
	Now       time.Time
}

// Capability is one thing a backend can do. A backend that cannot do one
// declares it missing rather than answering the query badly.
type Capability string

// The capabilities a backend may declare.
const (
	CapabilityLexical  Capability = "lexical"
	CapabilityFuzzy    Capability = "fuzzy"
	CapabilitySemantic Capability = "semantic"
	CapabilitySpatial  Capability = "spatial"
	CapabilityJSONPath Capability = "jsonpath"
)

// Ranked reports whether this capability produces an ordered list of its own,
// as opposed to narrowing the rows every list is drawn from.
//
// Spatial and jsonpath are filters: they are carried by the predicate each
// ranked mode already applies, so a backend satisfies them by running the
// search at all. The distinction decides two things neither backend can be
// trusted to remember on its own — that a filter is never reported in
// X-Beckn-Degraded when it WAS applied, and that an intent naming only filters
// is still a query rather than a request for nothing. It lives here, in the
// package both backends import, because a copy in each is a copy that drifts,
// and the drift shows up as an empty page with nothing to explain it.
func (c Capability) Ranked() bool {
	return c != CapabilitySpatial && c != CapabilityJSONPath
}

// Capabilities is the set a backend declares.
//
// A set rather than a struct of bools: a backend that gains a capability should
// not force a recompile of every backend that has not, and the negotiation
// reads this by asking rather than by switching.
type Capabilities map[Capability]bool

// Has reports whether the backend declared this capability. A nil Capabilities
// declares nothing, which is the right reading for a zero value: a backend that
// says nothing about itself gets asked for nothing.
func (c Capabilities) Has(capability Capability) bool {
	return c[capability]
}
