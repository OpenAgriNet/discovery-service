// Package beckn holds the Beckn v2.0.0 wire types — the shapes that cross the
// network verbatim — together with the protocol's action names and its error
// body. Nothing here does I/O and nothing here knows about publish, discover or
// storage: a spec bump has exactly one package to land in.
//
// Three rules govern how a schema property becomes a Go field, and each exists
// to keep a downstream task honest rather than to look tidy here.
//
// A field with no declared default is a pointer or a json.RawMessage, because
// MERGE is RFC 7396 (A8) and encoding/json is the only thing in the chain that
// still knows the difference between a key the publisher omitted and one they
// set to null. Flatten that here and the merge downstream cannot tell "keep
// what is stored" from "delete it".
//
// A field the service stores verbatim is a json.RawMessage rather than a parsed
// shape. `provider`, `descriptor` and `resourceAttributes` land in JSONB
// columns and are rendered back out unchanged, so parsing them would only give
// this package a second, lossier copy of a document it does not interpret.
//
// A field whose Go type would narrow the schema is not narrowed. `timestamp`
// stays a string so a malformed one is a CTX_ fault carrying its own path
// rather than a decoder error that takes the whole envelope down with it, and
// the same reasoning keeps `validity`'s four members raw.
package beckn

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Context is the Beckn envelope header that accompanies every request and
// callback.
//
// The spec declares no `required` list on Context, so L1 schema validation
// alone cannot reject a body missing `transactionId` (C6). The envelope rules
// that do reject it live in src/platform/validation and run even when L1 is
// switched off, because a response context cannot be built without them.
type Context struct {
	Action  string `json:"action,omitempty"`
	Version string `json:"version,omitempty"`

	// The two participant identities this service reads, and the only two.
	// Each is a DID resolving to the document that carries the party's
	// verification keys, so one field answers both "who" and "with what key" —
	// which is what the parked signature layer will need.
	//
	// `bapId`, `bapUri`, `bppId` and `bppUri` are deliberately NOT here. The
	// spec retains them for backward compatibility and a caller may still send
	// them; they are ignored rather than modelled, because a field this service
	// echoes but never reads is one a reader has to check is unused, and one an
	// operator can mistake for an identity that was verified. A body carrying
	// them is still accepted — Context declares no `additionalProperties:
	// false` and the decoder is not strict — it simply does not get them back.
	//
	// NEITHER IS VERIFIED, and on a callback that has a sharp edge. The two
	// controllers build a response by swapping them, so this service's own
	// `senderId` is whatever the caller put in `receiverId` — a caller can name
	// a third party there and be handed a callback asserting that DID as the
	// sender. It is the same "a string the caller chose" hazard the rate
	// limiter refuses to key on, and it closes the same way: the signature
	// layer resolves the DID document, and a configured self-DID replaces the
	// echo. Until then a caller sending neither gets a callback naming neither,
	// which is honest — an absent identity claims less than an unverified one.
	SenderID   string `json:"senderId,omitempty"`
	ReceiverID string `json:"receiverId,omitempty"`

	TransactionID string `json:"transactionId,omitempty"`
	MessageID     string `json:"messageId,omitempty"`

	// Optional on both paths, and the two paths read its absence differently.
	// On publish, absent means APP_NETWORK_ID — used only to fill an empty
	// visibleTo (C8). On discover, absent means no network predicate at all:
	// every network's catalogs match. Same field, two questions, and no shared
	// fallback between them.
	NetworkID string `json:"networkId,omitempty"`

	// RFC 3339, kept as a string. A bad timestamp has to come back as a fault
	// naming `$.context.timestamp`, which a time.Time field cannot do — it
	// fails the whole json.Unmarshal and the caller learns only that the body
	// was unreadable.
	Timestamp string `json:"timestamp,omitempty"`

	Key string `json:"key,omitempty"`
	Try *bool  `json:"try,omitempty"`
	TTL string `json:"ttl,omitempty"`

	// A Context field, not an Intent one. The reference implementation moved it
	// to message.intent, which Intent's additionalProperties:false forbids
	// outright; this plan follows the spec. Each entry is a JSON-LD context URI
	// whose optional #fragment names the @type.
	SchemaContext []string `json:"schemaContext,omitempty"`

	RequestDigest json.RawMessage `json:"requestDigest,omitempty"`
}

// Catalog is a provider's resources and offers as one publishable unit. It is
// the payload of both directions: a publisher sends it, and discover renders
// the matched subset of it back.
type Catalog struct {
	ID     string `json:"id"`
	BppID  string `json:"bppId,omitempty"`
	BppURI string `json:"bppUri,omitempty"`

	Descriptor json.RawMessage `json:"descriptor,omitempty"`
	Provider   json.RawMessage `json:"provider,omitempty"`

	// A pointer because `isActive` has a declared default of true (A9) and the
	// mapper is what resolves it. A plain bool cannot say whether the publisher
	// sent false or sent nothing, so the default would silently overwrite every
	// deliberate deactivation.
	IsActive *bool `json:"isActive,omitempty"`

	Resources []Resource `json:"resources,omitempty"`
	Offers    []Offer    `json:"offers,omitempty"`

	// Raw rather than *TimePeriod because RFC 7396 (A8) gives `null` a meaning
	// a pointer cannot carry: absent means "leave the stored window alone",
	// while an explicit null means "clear it". A *TimePeriod collapses both to
	// nil, so a publisher trying to clear a validity would be answered with
	// silence and a window that never went away.
	Validity json.RawMessage `json:"validity,omitempty"`

	// Raw is the catalog exactly as it arrived, and it is what reaches the
	// catalogs.document column once its two child arrays are lifted off (A17).
	//
	// The same reason Offer has one: re-marshalling the struct would emit the
	// members this file happens to name and drop the rest, and a column that
	// claims to hold what the publisher sent would be lying to exactly the
	// publishers who checked.
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes a catalog and keeps the bytes it decoded.
//
// The alias breaks the recursion. The captured bytes are the caller's slice,
// which encoding/json does not retain after the call, so they are copied.
func (c *Catalog) UnmarshalJSON(data []byte) error {
	type wire Catalog

	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	*c = Catalog(decoded)
	c.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON writes the stored document back with the two child arrays
// spliced in.
//
// Not the plain verbatim write Offer does, because a catalog's document is the
// one that had members REMOVED before storage: `resources` and `offers` live in
// their own tables (A17), so the bytes are only a whole catalog again once they
// are put back. Splicing here rather than at the caller is what lets discover
// return `descriptor`, `bppId`, `bppUri` and `validity` without this service
// naming them anywhere — they ride along in Raw.
//
// It carries Offer's caveat and one of its own. A field edited on a decoded
// Catalog does not reach the output; only Resources and Offers do. Nothing
// edits one — publish stores the document and discover renders it — and the two
// exceptions are exactly the two members that must be assembled.
func (c Catalog) MarshalJSON() ([]byte, error) {
	members, err := c.WithoutChildren()
	if err != nil {
		return nil, err
	}

	// Absent rather than empty when there is nothing: `resources` is not in
	// Catalog's required list, so a catalog of offers alone is legal, and an
	// empty array would be this service asserting the publisher sent one.
	if len(c.Resources) > 0 {
		if members["resources"], err = json.Marshal(c.Resources); err != nil {
			return nil, err
		}
	}
	if len(c.Offers) > 0 {
		if members["offers"], err = json.Marshal(c.Offers); err != nil {
			return nil, err
		}
	}

	return json.Marshal(members)
}

// WithoutChildren is the catalog's own members: everything the publisher sent
// except `resources` and `offers`.
//
// A map rather than bytes because the publish mapper has a member of its own to
// resolve into the document before storing it — `isActive`, which A9 defaults
// and RFC 7396 would otherwise leave to the merge — and handing back bytes
// would make that a second decode of the same document.
//
// A Catalog with no Raw was built in Go rather than decoded, so its members are
// whatever the struct holds. That path exists for tests and for the conformance
// suite; every catalog that arrives over the wire has Raw.
func (c Catalog) WithoutChildren() (map[string]json.RawMessage, error) {
	raw := c.Raw
	if len(raw) == 0 {
		type wire Catalog

		bare := wire(c)
		bare.Resources, bare.Offers = nil, nil

		encoded, err := json.Marshal(bare)
		if err != nil {
			return nil, err
		}
		raw = encoded
	}

	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, err
	}
	if members == nil {
		members = map[string]json.RawMessage{}
	}

	delete(members, "resources")
	delete(members, "offers")
	return members, nil
}

// Resource is a referenceable unit of value in a catalog. The spec gives it
// exactly three properties — there is no `category` field anywhere in v2.0.0,
// which is why this service has no category column, index or derivation (C5).
type Resource struct {
	ID                 string          `json:"id"`
	Descriptor         json.RawMessage `json:"descriptor,omitempty"`
	ResourceAttributes json.RawMessage `json:"resourceAttributes,omitempty"`

	// Raw is the resource exactly as it arrived, and it is what reaches the
	// resources.document column (A17). Same reason as Offer.Raw: the spec
	// leaves additionalProperties unset, so re-marshalling the three named
	// members would drop whatever else a publisher sent.
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes a resource and keeps the bytes it decoded.
func (r *Resource) UnmarshalJSON(data []byte) error {
	type wire Resource

	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	*r = Resource(decoded)
	r.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON writes the resource back exactly as it arrived, or from its
// fields when it was built in Go rather than decoded.
func (r Resource) MarshalJSON() ([]byte, error) {
	if len(r.Raw) > 0 {
		return r.Raw, nil
	}

	type wire Resource
	return json.Marshal(wire(r))
}

// Offer is the commercial terms under which resources may be committed.
//
// An empty or absent ResourceIDs means CATALOG-WIDE, not "no resources" — the
// same meaning the offers.resource_ids column carries — so an offer's geometry
// with no ids attaches to the whole catalog rather than to nothing.
type Offer struct {
	ID          string          `json:"id"`
	Descriptor  json.RawMessage `json:"descriptor,omitempty"`
	Provider    json.RawMessage `json:"provider,omitempty"`
	ResourceIDs []string        `json:"resourceIds,omitempty"`

	AddOns         json.RawMessage `json:"addOns,omitempty"`
	Considerations json.RawMessage `json:"considerations,omitempty"`

	// Raw for the same reason Catalog.Validity is (A8): `null` clears the
	// stored window and absence leaves it.
	Validity        json.RawMessage `json:"validity,omitempty"`
	OfferAttributes json.RawMessage `json:"offerAttributes,omitempty"`

	// Raw is the offer exactly as it arrived, and it is what reaches the
	// offers.offer column.
	//
	// The spec leaves Offer.additionalProperties unset, so a publisher may send
	// members this struct never named. Re-marshalling the struct would drop
	// them, and the column's whole claim — "the offer document, verbatim" —
	// would be false for precisely the publishers who needed it to be true.
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes an offer and keeps the bytes it decoded.
//
// The alias breaks the recursion; without it this method calls itself. The
// captured bytes are the caller's slice, which encoding/json does not retain
// after the call, so they are copied.
func (o *Offer) UnmarshalJSON(data []byte) error {
	type wire Offer

	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	*o = Offer(decoded)
	o.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON writes the offer back exactly as it arrived.
//
// The counterpart to UnmarshalJSON above, and the reason the discover response
// can claim to render `offers.offer` verbatim: the spec leaves
// Offer.additionalProperties unset, so a publisher may send members this struct
// never named, and re-marshalling the fields alone would drop them for
// precisely the publishers who relied on the column keeping them.
//
// Raw is the bytes this value was DECODED from, so a caller that decodes an
// offer and then edits a field would marshal the original. Nothing does — an
// offer is stored verbatim and rendered verbatim, and there is no step between
// that rewrites one. A value built in Go carries no Raw and marshals from its
// fields, which is what keeps a hand-constructed Offer honest.
func (o Offer) MarshalJSON() ([]byte, error) {
	if len(o.Raw) > 0 {
		return o.Raw, nil
	}

	type wire Offer
	return json.Marshal(wire(o))
}

// Attributes reads the JSON-LD pair out of an extensibility container —
// `resourceAttributes`, `offerAttributes` and their kin.
//
// Both members are scalar strings and both are required (C4). Typed that way,
// an array payload fails L1 validation instead of silently having element zero
// picked for it, which is how two publishers come to disagree about the shape
// of the field discover filters on.
//
// It reads the pair and nothing else. The container is additionalProperties:
// true and its domain keys are stored verbatim on the parent's RawMessage, so
// this type is a lens over a document rather than a replacement for it.
type Attributes struct {
	Context string `json:"@context"`
	Type    string `json:"@type"`
}

// TimePeriod is the validity window on a catalog or an offer. It expands into
// four independent columns, not two: `startDate`/`endDate` are RFC 3339
// instants and `startTime`/`endTime` are a recurring daily window, and the two
// halves may appear separately.
//
// The four members are raw for the same reason the catalog's optional documents
// are. RFC 7396 permits a patch that clears `endDate` and keeps `startDate`, so
// each member needs three states — absent, explicit null, value — and a *string
// collapses the first two into nil.
type TimePeriod struct {
	StartDate json.RawMessage `json:"startDate,omitempty"`
	EndDate   json.RawMessage `json:"endDate,omitempty"`
	StartTime json.RawMessage `json:"startTime,omitempty"`
	EndTime   json.RawMessage `json:"endTime,omitempty"`
}

// GeoJSONGeometry is an RFC 7946 geometry. All seven types are carried and all
// seven are cell-indexed; Coordinates is raw because its nesting depth is a
// function of Type, and Geometries is populated only for a GeometryCollection.
type GeoJSONGeometry struct {
	Type        string            `json:"type"`
	Coordinates json.RawMessage   `json:"coordinates,omitempty"`
	Geometries  []GeoJSONGeometry `json:"geometries,omitempty"`
	BBox        []float64         `json:"bbox,omitempty"`
}

// The seven RFC 7946 geometry type names, which are also this service's test
// for whether an object encountered during the catalog walk is a geometry.
const (
	GeometryPoint              = "Point"
	GeometryLineString         = "LineString"
	GeometryPolygon            = "Polygon"
	GeometryMultiPoint         = "MultiPoint"
	GeometryMultiLineString    = "MultiLineString"
	GeometryMultiPolygon       = "MultiPolygon"
	GeometryGeometryCollection = "GeometryCollection"
)

// Intent is the structured expression of what a caller is searching for.
//
// It carries no `schemaContext`: the schema filter is a Context field, and
// Intent is additionalProperties:false, so a sender that puts it here produces
// a body that fails its own schema.
type Intent struct {
	TextSearch  string              `json:"textSearch,omitempty"`
	Filters     *Filters            `json:"filters,omitempty"`
	Spatial     []SpatialConstraint `json:"spatial,omitempty"`
	MediaSearch json.RawMessage     `json:"mediaSearch,omitempty"`
}

// Filters is Intent's attribute filter. Only PostgreSQL SQL/JSON path is
// executed: an RFC 9535 expression — the grammar of the spec's own example — is
// a 400 rather than an attempt, because a filter that matches nothing is
// indistinguishable from an honest empty result (C10).
type Filters struct {
	Type       string `json:"type"`
	Expression string `json:"expression"`
}

// SpatialConstraint is one OGC CQL2 spatial predicate over the geometry fields
// a `targets` pointer resolves to.
type SpatialConstraint struct {
	Op      string  `json:"op"`
	Targets Targets `json:"targets"`

	Geometry *GeoJSONGeometry `json:"geometry,omitempty"`

	// A pointer because the spec documents distanceMeters as "Ignored for other
	// ops", and ignoring it silently is the one thing this service will not do:
	// a caller who sent one believes it is filtering. Telling them requires
	// distinguishing a sent 0 from an unsent field.
	DistanceMeters *float64 `json:"distanceMeters,omitempty"`

	Quantifier string `json:"quantifier,omitempty"`
	SRID       string `json:"srid,omitempty"`
}

// The nine CQL2 operators the spec's enum admits. S_TOUCHES and S_CROSSES are
// named here because L1 validation accepts them — they are legal enum values —
// and the only thing standing between a caller and a silently wrong answer is
// the refusal the intent mapper raises against these two constants.
const (
	OpSIntersects = "S_INTERSECTS"
	OpSDisjoint   = "S_DISJOINT"
	OpSWithin     = "S_WITHIN"
	OpSContains   = "S_CONTAINS"
	OpSOverlaps   = "S_OVERLAPS"
	OpSEquals     = "S_EQUALS"
	OpSDWithin    = "S_DWITHIN"
	OpSTouches    = "S_TOUCHES"
	OpSCrosses    = "S_CROSSES"
)

// How a constraint is evaluated when `targets` resolves to more than one
// geometry. Omitted reads as QuantifierAny.
const (
	QuantifierAny  = "ANY"
	QuantifierAll  = "ALL"
	QuantifierNone = "NONE"
)

// Targets is SpatialConstraint's `targets`: one JSONPath pointer or several.
//
// `beckn.yaml` declares a oneOf over a string and an array of strings and real
// senders use both, so the oneOf is resolved here, once, and everything
// downstream sees a slice. A mapper that had to branch on the wire form would
// be a second place for the two forms to diverge.
type Targets []string

// UnmarshalJSON accepts the scalar and the array form and refuses everything
// else.
//
// Refusing matters more than it looks: a shape read as "no targets" would drop
// the spatial predicate and answer with the whole index, which is exactly the
// silently-widened result the plan rejects on every branch of the spatial path.
//
// `null` is checked before anything else because encoding/json hands it to this
// method like any other value, and unmarshalling it into a string succeeds as a
// no-op — so reading the arms in order would turn `targets: null` into one
// empty pointer that no sender wrote, with no error to show for it. The array
// is read as []*string for the same reason one level down: `["$.a", null]` is
// not an array of strings, and []string would quietly render that null as "".
// Neither shape satisfies the oneOf, so neither is this package's to interpret.
func (t *Targets) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("targets is null, which is neither a string nor an array of strings")
	}

	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*t = Targets{one}
		return nil
	}

	var many []*string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("targets is neither a string nor an array of strings: %w", err)
	}

	out := make(Targets, len(many))
	for i, pointer := range many {
		if pointer == nil {
			return fmt.Errorf("targets[%d] is null, which is not a JSONPath pointer", i)
		}
		out[i] = *pointer
	}

	*t = out
	return nil
}
