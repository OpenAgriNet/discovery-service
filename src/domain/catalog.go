package domain

import (
	"encoding/json"
	"time"
)

// Catalog is one publisher's catalog as this service stores it.
type Catalog struct {
	ID string

	// NetworkID is the publisher's own network, used only to default an empty
	// VisibleTo (C8). Not stored — nothing reads it back.
	NetworkID string

	// Document is the Catalog as the publisher sent it, with `resources` and
	// `offers` STRIPPED — those live on the slices below and are spliced back in
	// when the response is rendered (A17). Every other field here is derived
	// from it.
	Document json.RawMessage

	ValidFrom time.Time
	ValidTo   time.Time

	// The daily window, orthogonal to the calendar range above and ANDed with
	// it. Pointers because nil is "no window" and 00:00:00 is a real bound.
	ValidTimeFrom *TimeOfDay
	ValidTimeTo   *TimeOfDay

	VisibleTo []string
	Active    bool

	// ProtocolVersion is the Beckn version the publisher declared in
	// `context.version`. It describes the DOCUMENT, not the build, so it is
	// stored rather than read off beckn.Version.
	ProtocolVersion string

	Resources []Resource
	Offers    []Offer

	// Geometries are the PROVIDER's locations — the catalog's, not any one
	// resource's, stored once with a NULL resource id.
	Geometries []Geometry
}

// Resource is one item in a catalog, carrying the scope gate copied down from
// its catalog.
//
// Copied rather than joined: discover reads validity and visibility here and
// never touches `catalogs`.
type Resource struct {
	ID        string
	CatalogID string
	Name      string

	// Document is the Resource verbatim — {id, descriptor, resourceAttributes}.
	// The members are read back through the accessors below.
	Document json.RawMessage

	// Read out of the merged Attributes by `derive`, never carried on a patch.
	SchemaContext string
	SchemaType    string

	// The finds the walker made INSIDE this resource. A geometry in the
	// catalog's provider block lives on Catalog.Geometries instead.
	Geometries []Geometry

	// SearchText is an insert PARAMETER, not a stored column: only the tsvector
	// built from it is kept.
	SearchText string

	Embedding []float32

	// EmbeddingSourceHash is blake2b-256 of SearchText and is the A5 re-embed
	// decision. On the domain rather than in the store because `derive` compares
	// it and then writes it.
	EmbeddingSourceHash []byte

	VisibleTo     []string
	Active        bool
	ValidFrom     time.Time
	ValidTo       time.Time
	ValidTimeFrom *TimeOfDay
	ValidTimeTo   *TimeOfDay
}

// Offer is a priced or promoted thing over some of a catalog's resources.
//
// An empty ResourceIDs means CATALOG-WIDE, not "none". There is no Descriptor
// or Price field: the response renders Document.
type Offer struct {
	ID          string
	CatalogID   string
	ResourceIDs []string
	Document    json.RawMessage

	ValidFrom     time.Time
	ValidTo       time.Time
	ValidTimeFrom *TimeOfDay
	ValidTimeTo   *TimeOfDay
}

// Provider is the catalog's provider block, read out of the document.
//
// An accessor and not a field, so there is only one copy of the bytes. Callers
// that need it — the geometry walk — pay one shallow decode.
func (c Catalog) Provider() json.RawMessage { return member(c.Document, "provider") }

// Descriptor is the resource's descriptor member. A column until A17.
func (r Resource) Descriptor() json.RawMessage { return member(r.Document, "descriptor") }

// ResourceAttributes is the JSON-LD attribute document, or nil when the
// resource carries none — which the schema permits, since only `id` is
// required.
func (r Resource) ResourceAttributes() json.RawMessage {
	return member(r.Document, "resourceAttributes")
}

// member reads one top-level member of a JSON object, or nil.
//
// Shallow by construction: map[string]json.RawMessage parses the object's own
// keys and leaves the values as bytes, so reading `descriptor` does not walk
// the whole attribute tree.
func member(document json.RawMessage, name string) json.RawMessage {
	if len(document) == 0 {
		return nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(document, &members); err != nil {
		return nil
	}
	return members[name]
}

// Geometry is one geographic shape found somewhere in a published document.
type Geometry struct {
	// TargetPath is the wildcard form, byte-identical to a spatial constraint's
	// `targets`. The only one a query compares against.
	TargetPath string

	// SourcePath carries concrete indices, which is what distinguishes two
	// geometries under one wildcard. Positional, and therefore NOT stable across
	// a republish that reorders an array — geometries are deleted and
	// reinserted rather than merged.
	SourcePath string

	// Owners is empty for a catalog-level geometry, and otherwise one id per
	// resource that owns this shape — N for a geometry inside an offer covering
	// N of them. Each entry becomes one resource_geometries row.
	Owners []string

	// Type is read out of GeoJSON both ways: a field of the value, not a column.
	Type string

	// GeoJSON is kept VERBATIM. Parsing at publish time is how the reference
	// implementation loses five of seven types and every polygon hole.
	GeoJSON json.RawMessage
}

// Nullable distinguishes the three answers a JSON Merge Patch can give about a
// scalar column.
//
//	Set == false   ABSENT: keep whatever is stored.
//	Set && Null    an explicit JSON null: clear the column.
//	Set && !Null   Value.
//
// One type rather than **T, whose fourth state is the one a reader dereferences
// by accident. The only generic in the domain.
type Nullable[T any] struct {
	Value T
	Set   bool
	Null  bool
}

// ResourceAttributes reads the attribute document off a patch, for the two
// derivations that run before the merge and need what THIS request sent.
func (p ResourcePatch) ResourceAttributes() json.RawMessage {
	return member(p.Document, "resourceAttributes")
}

// CatalogPatch is what MapCatalog returns (A8): a catalog-shaped change in
// which absence is a distinct state from the zero value.
//
// Not Catalog reused, because encoding/json gives nil for an unsent key and a
// non-nil zero for one that was sent, and MERGE turns that into the difference
// between keeping a publisher's data and deleting it.
//
// Active and VisibleTo are deliberately NOT pointers (A9): both are resolved
// before the merge runs, so no absence is left to represent.
type CatalogPatch struct {
	ID        string
	NetworkID string

	// Document is the catalog document minus `resources` and `offers`, which the
	// mapper has lifted onto the slices below. nil = absent, `null` = delete.
	//
	// It carries a RESOLVED `isActive`: A9 resets an omitted one to the default
	// where RFC 7396 would keep what is stored, so the mapper settles it before
	// the merge sees it.
	Document json.RawMessage

	// nil = absent.
	Validity *TimePeriodPatch

	Active    bool
	VisibleTo []string

	// ProtocolVersion is resolved by the mapper and is never empty this far
	// down: the column is NOT NULL, and an empty string here cannot be told
	// apart from a publisher who sent one.
	ProtocolVersion string

	Resources []ResourcePatch
	Offers    []OfferPatch
}

// ResourcePatch is the pure A8 half, with no A9 half — which is why it is two
// fields and not eight.
//
// ID is the merge KEY, not a patchable field: a patch naming no stored resource
// is an insert. There is no delete — under MERGE `null` deletes a key, never a
// row.
type ResourcePatch struct {
	ID string

	// nil = absent, `null` = delete. The Resource verbatim.
	Document json.RawMessage
}

// OfferPatch is the same split as CatalogPatch and for the same reason.
//
// `resourceIds` has a declared default of [] — CATALOG-WIDE, not "none" — so
// the mapper resolves it before merge time. Document is the whole verbatim
// offer, merged by RFC 7396 against the stored one.
type OfferPatch struct {
	ID string

	// nil = absent, `null` = delete.
	Document json.RawMessage

	ResourceIDs []string
	Validity    *TimePeriodPatch
}

// TimePeriodPatch is four independent tri-states, because `validity` expands
// into four independent columns.
//
// A *TimePeriod cannot say "clear the end date and keep the start date", which
// RFC 7396 permits.
type TimePeriodPatch struct {
	StartDate Nullable[time.Time]
	EndDate   Nullable[time.Time]
	StartTime Nullable[TimeOfDay]
	EndTime   Nullable[TimeOfDay]
}

// UpdateMode is how a CatalogPatch applies to what is stored (A8).
type UpdateMode string

// The two update modes.
const (
	// UpdateModeMerge is RFC 7396 against the stored documents, with resources
	// and offers matched by id rather than by array position.
	UpdateModeMerge UpdateMode = "MERGE"

	// UpdateModeFull replaces the catalog outright, its own columns included:
	// omissions reset to defaults, and resources and offers the payload omits
	// are deleted.
	UpdateModeFull UpdateMode = "FULL"
)
