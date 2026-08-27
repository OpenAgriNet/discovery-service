package domain

import (
	"encoding/json"
	"time"
)

// Catalog is one publisher's catalog as this service stores it.
//
// Geometries here are the PROVIDER's locations. They belong to the catalog
// rather than to any one resource, and are stored once with a NULL resource id.
type Catalog struct {
	ID string

	// NetworkID is the publisher's own network, used only to default an empty
	// VisibleTo (C8). It is not stored, because nothing reads it back.
	NetworkID string

	// Document is the Catalog as the publisher sent it, with `resources` and
	// `offers` STRIPPED — those live on Resources and Offers below and are
	// spliced back in when the response is rendered (A17).
	//
	// Every other field on this struct is derived from it. Keeping the document
	// rather than a projection is what lets a discover response return the
	// `descriptor`, `bppId` and `bppUri` the protocol requires and this service
	// invented no column for.
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
	// `context.version`. It is stored rather than derived because it describes
	// the DOCUMENT, not the build: `beckn.Version` says what this binary
	// serves today, and the two agree only until they do not.
	ProtocolVersion string

	Resources  []Resource
	Offers     []Offer
	Geometries []Geometry
}

// Resource is one item in a catalog, carrying the scope gate copied down from
// its catalog.
//
// The gate is copied rather than joined: discover reads validity and visibility
// here and never touches `catalogs`, which is what keeps a count(*) over a
// large result set from probing the parent table once per match.
type Resource struct {
	ID        string
	CatalogID string
	Name      string

	// Document is the Resource verbatim — `{id, descriptor,
	// resourceAttributes}` — and the two members are read back through the
	// accessors below rather than stored beside it.
	Document json.RawMessage

	// Read out of the merged Attributes by `derive`, never carried on a patch —
	// a patch holding them would be a second place they could disagree with the
	// document they describe.
	SchemaContext string
	SchemaType    string

	// The finds the walker made INSIDE this resource. A geometry found in the
	// catalog's own provider block lives on Catalog.Geometries and is shared by
	// every resource in it.
	Geometries []Geometry

	// SearchText is an insert PARAMETER, not a stored column: only the tsvector
	// built from it is kept.
	SearchText string

	Embedding []float32

	// EmbeddingSourceHash is blake2b-256 of SearchText and is the A5 re-embed
	// decision. It is a field of the domain rather than a detail of the store
	// because `derive` compares it and then writes it — a struct that omitted
	// it would push that comparison into the repository, which is the one thing
	// the derive seam exists to keep out.
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
// An empty ResourceIDs means CATALOG-WIDE, not "none". Document is named that
// rather than Offer because `offer.Offer` reads as a mistake at every call site;
// there is no Descriptor or Price field, because the response renders Document
// and a projection the storage layer does not keep is one the domain must not
// pretend to have.
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
// An accessor rather than a field because a field would be a second copy of
// bytes the document already holds, and the two would disagree the first time a
// merge updated one of them. Callers that need it — the geometry walk, and
// nothing else — pay one shallow decode.
func (c Catalog) Provider() json.RawMessage { return member(c.Document, "provider") }

// Descriptor and ResourceAttributes are the two members of a Resource document.
//
// They were columns until A17. As accessors they keep every derivation that
// reads them — the search text, the JSON-LD schema pair, the geometry walk —
// spelled exactly as it was, while there is only one stored copy to keep true.
func (r Resource) Descriptor() json.RawMessage { return member(r.Document, "descriptor") }

// ResourceAttributes is the JSON-LD attribute document, or nil when the
// resource carries none — which the schema permits, since only `id` is
// required.
func (r Resource) ResourceAttributes() json.RawMessage {
	return member(r.Document, "resourceAttributes")
}

// member reads one top-level member of a JSON object, or nil.
//
// Shallow by construction: decoding into map[string]json.RawMessage parses the
// object's own keys and leaves every value as bytes, so reading `descriptor`
// off a resource does not walk its whole attribute tree. A document that is not
// an object has no members, which is the same answer as a member that is
// absent — neither is reachable from a validated publish, and both mean "there
// is nothing here to read".
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
	// `targets`. It is the only one a query compares against.
	TargetPath string

	// SourcePath carries concrete indices, which is what makes two geometries
	// under one wildcard distinguishable — except the catalog's own index,
	// which is wildcarded because it is a property of the request rather than
	// of the catalog. Positional everywhere else, and therefore NOT stable
	// across a republish that reorders an array: geometries are deleted and
	// reinserted rather than merged.
	SourcePath string

	// Owners is empty for a catalog-level geometry, and otherwise carries one
	// id per resource that owns this shape — one for a geometry found inside a
	// resource, N for one found inside an offer covering N of them. Each entry
	// becomes one resource_geometries row.
	Owners []string

	// Type is read out of GeoJSON on the way in and on the way back: a field of
	// the value, not a column of the table.
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
// One type rather than **T, which has four states of which three mean anything
// — and the fourth is the one a reader dereferences by accident. This is the
// only generic in the domain; it exists because absence, deletion and value are
// three answers and Go's zero value is one.
type Nullable[T any] struct {
	Value T
	Set   bool
	Null  bool
}

// ResourceAttributes reads the attribute document off a patch, for the two
// derivations that run before the merge — the JSON-LD pair and the C5 category
// count — and need to see what THIS request sent rather than what is stored.
func (p ResourcePatch) ResourceAttributes() json.RawMessage {
	return member(p.Document, "resourceAttributes")
}

// CatalogPatch is what MapCatalog returns (A8): a catalog-shaped change in
// which absence is a distinct state from the zero value.
//
// This type exists rather than reusing Catalog because encoding/json gives nil
// for a key that was not sent and a non-nil zero for one that was, and MERGE
// turns that distinction into the difference between keeping a publisher's data
// and deleting it.
//
// Active and VisibleTo are deliberately NOT pointers (A9). Active's default is
// resolved in the mapper and VisibleTo arrives already resolved from
// publishOne's applyDirectiveDefaults, so by the time the merge runs there is no
// absence left to represent — and a pointer that can never be nil is an
// invitation to write the branch that makes it nil.
type CatalogPatch struct {
	ID        string
	NetworkID string

	// Document is the catalog document the publisher sent, minus `resources`
	// and `offers`, which the mapper has already lifted onto the two slices
	// below. nil = absent, `null` = delete.
	//
	// It carries a RESOLVED `isActive`: A9 makes an omitted one reset to the
	// default, and RFC 7396 would otherwise keep whatever was stored — the one
	// place the merge rule and the default rule disagree, so the mapper settles
	// it before the merge can see it.
	Document json.RawMessage

	// nil = absent.
	Validity *TimePeriodPatch

	Active    bool
	VisibleTo []string

	// ProtocolVersion is resolved by the mapper and is never empty by the time
	// it reaches a backend — the column is NOT NULL, and an empty string this
	// far down cannot be told apart from a publisher who sent one.
	ProtocolVersion string

	Resources []ResourcePatch
	Offers    []OfferPatch
}

// ResourcePatch is the pure A8 half, with no A9 half at all — which is why it
// is three fields and not eight.
//
// The wire Resource is exactly {id, descriptor, resourceAttributes} (C5) and
// only `id` is required, so the document preserves absence and has no declared
// default. ID is the merge KEY rather than a patchable field:
// resources merge by id, so a patch naming no stored resource is an insert and
// one naming a stored resource is a patch against it. There is no delete —
// under MERGE `null` deletes a key, never a row.
type ResourcePatch struct {
	ID string

	// nil = absent, `null` = delete.
	// Document is the Resource verbatim — `{id, descriptor,
	// resourceAttributes}` — and the two members are read back through the
	// accessors below rather than stored beside it.
	Document json.RawMessage
}

// OfferPatch is the same split as CatalogPatch and for the same reason.
//
// `resourceIds` has a declared default of [] — CATALOG-WIDE, not "none" — so
// the mapper resolves it and it cannot be absent by merge time. Document is the
// WHOLE verbatim offer, merged by RFC 7396 against the stored one, which is why
// it is a RawMessage and not a parsed shape: the `offer` JSONB column keeps what
// the publisher sent.
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
// A *TimePeriod can say "no validity sent" but cannot say "clear the end date
// and keep the start date" — a patch RFC 7396 permits and two independent
// column pairs make meaningful.
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
