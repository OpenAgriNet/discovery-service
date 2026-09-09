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

// SpatialOp is a CQL2 spatial operator. All nine are named, including the two
// this service refuses — a refusal has to name what it is refusing.
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

// The two refused as unapproximable at any resolution (A10): a cell
// decomposition has no measure-zero boundary.
const (
	OpTouches SpatialOp = "S_TOUCHES"
	OpCrosses SpatialOp = "S_CROSSES"
)

// Quantifier says how a constraint applies across the geometries a resource can
// be found by.
//
// A string and not a bool: ALL is NOT EXISTS over the negated predicate, which
// no value of a bool honestly represents.
type Quantifier string

// The three quantifiers.
const (
	QuantifierAny  Quantifier = "ANY"
	QuantifierAll  Quantifier = "ALL"
	QuantifierNone Quantifier = "NONE"
)

// SpatialFilter is one spatial constraint, already reduced to cells.
//
// One type for all seven answered operators, which differ only in the set
// relation the repository applies.
type SpatialFilter struct {
	Op SpatialOp

	// The query geometry's two covers: CellsFull is a guaranteed subset and
	// proves positives, CellsCover a guaranteed superset and proves negatives.
	// They are nil TOGETHER — a declined cover disables the cell predicate and
	// leaves Bounds to decide.
	CellsFull  []uint64
	CellsCover []uint64

	// nil means the cover declined to produce a box, not a box matching
	// everything.
	Bounds *BBox

	// Populated ONLY for Point-to-Point S_DWITHIN, the one case the exact
	// haversine refinement applies to.
	Center  *GeoPoint
	RadiusM float64

	Quantifier Quantifier
}

// SchemaFilter is one entry of the schema predicate. Type == "" means any type
// under this context.
//
// The two halves are compared together, not as independent IN lists: a request
// for [schema.org#GroceryItem, beckn.org/Mobility#RideService] must not match
// schema.org + RideService.
type SchemaFilter struct {
	Context string
	Type    string
}

// AttributeFilter is a structured predicate over the composite the store keeps
// for each resource (Task 22).
//
// Expression is PostgreSQL SQL/JSON path (C10), already validated — the store
// may cast and run it, never interpret it. There is no Root (A18). A store that
// cannot run it must narrow nothing and say so in Degraded.
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
// Degraded names the retrieval modes that did not contribute and reaches the
// caller as the X-Beckn-Degraded header, not a body key (C11). There is no
// Total (A19).
type SearchResult struct {
	Catalogs []Catalog
	Degraded []string
}

// Scope is the request-wide gate every retrieval mode reads (A6).
//
// Now is captured once per request so every mode in a concurrent search agrees
// on it. Postgres ignores it and calls now(); it exists for the backends that
// have none — the memory store, and tests that ask what was live at 23:00.
//
// NetworkID == "" means UNSCOPED, which every backend must read as "emit no
// network predicate" — never as a literal id, and never by falling back to
// APP_NETWORK_ID, which is publish's visibleTo default (C8) answering a
// different question.
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
// Spatial and jsonpath are filters, carried by the predicate each ranked mode
// already applies. The distinction decides that an applied filter is never
// reported in X-Beckn-Degraded, and that an intent naming only filters is still
// a query.
func (c Capability) Ranked() bool {
	return c != CapabilitySpatial && c != CapabilityJSONPath
}

// Capabilities is the set a backend declares.
//
// A set rather than a struct of bools, so a backend gaining a capability does
// not recompile the ones that have not.
type Capabilities map[Capability]bool

// Has reports whether the backend declared this capability. A nil Capabilities
// declares nothing.
func (c Capabilities) Has(capability Capability) bool {
	return c[capability]
}
