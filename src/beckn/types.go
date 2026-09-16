// Package beckn holds the Beckn v2.0.0 wire types — the shapes that cross the
// network verbatim — together with the protocol's action names and its error
// body. Nothing here does I/O and nothing here knows about publish, discover or
// storage: a spec bump has exactly one package to land in.
//
// Three rules govern how a schema property becomes a Go field:
//
//   - No declared default means a pointer or a json.RawMessage. MERGE is RFC
//     7396 (A8) and encoding/json is the only thing left in the chain that can
//     tell an omitted key from an explicit null.
//   - Stored verbatim means json.RawMessage, not a parsed shape. Parsing
//     `provider` or `resourceAttributes` would only give this package a second,
//     lossier copy of a document it does not interpret.
//   - A Go type that would narrow the schema is not used. `timestamp` stays a
//     string so a malformed one is a CTX_ fault naming its own path rather than
//     a decoder error that takes the whole envelope down.
package beckn

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Context is the Beckn envelope header that accompanies every request and
// callback.
//
// The spec declares no `required` list on it (C6), so the envelope rules that
// reject a missing `transactionId` live in src/platform/validation — and they
// run even when L1 is off, a response context being unbuildable without them.
type Context struct {
	Action  string `json:"action,omitempty"`
	Version string `json:"version,omitempty"`

	// The two participant identities this service reads, and the only two:
	// `bapId`, `bapUri`, `bppId` and `bppUri` are not modelled at all, and a
	// body carrying them is accepted and simply does not get them back (A24,
	// implementation-plan.md). `Catalog.BppID` is unaffected — it describes
	// the provider a catalog belongs to, not who sent the message.
	//
	// NEITHER IS VERIFIED. The controllers build a response by SWAPPING them, so
	// this service's own `senderId` is whatever the caller put in `receiverId`
	// and a caller can be handed a callback asserting a third party's DID. It
	// closes outside this repo — the adopter's layer owns participant signature
	// verification — so do not add a self-DID config here expecting to fix it.
	SenderID   string `json:"senderId,omitempty"`
	ReceiverID string `json:"receiverId,omitempty"`

	TransactionID string `json:"transactionId,omitempty"`
	MessageID     string `json:"messageId,omitempty"`

	// Optional on both paths, which read its absence differently: on publish it
	// means APP_NETWORK_ID, used only to fill an empty visibleTo (C8), and on
	// discover it means no network predicate at all. No shared fallback.
	NetworkID string `json:"networkId,omitempty"`

	// RFC 3339, kept as a string so a bad one comes back as a fault naming
	// `$.context.timestamp` rather than a json.Unmarshal failure that tells the
	// caller only that the body was unreadable.
	Timestamp string `json:"timestamp,omitempty"`

	Key string `json:"key,omitempty"`
	Try *bool  `json:"try,omitempty"`
	TTL string `json:"ttl,omitempty"`

	// A Context field, not an Intent one — Intent's additionalProperties:false
	// forbids it outright. Each entry is a JSON-LD context URI whose optional
	// #fragment names the @type.
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

	// A pointer because `isActive` defaults to true (A9) and the mapper resolves
	// it: a plain bool cannot tell a sent false from nothing sent, so the
	// default would overwrite every deliberate deactivation.
	IsActive *bool `json:"isActive,omitempty"`

	Resources []Resource `json:"resources,omitempty"`
	Offers    []Offer    `json:"offers,omitempty"`

	// Raw rather than *TimePeriod because RFC 7396 (A8) gives `null` a meaning a
	// pointer cannot carry: absent leaves the stored window alone, explicit null
	// clears it, and a *TimePeriod collapses both to nil.
	Validity json.RawMessage `json:"validity,omitempty"`

	// Raw is the catalog exactly as it arrived, and what reaches the
	// catalogs.document column once its two child arrays are lifted off (A17).
	// Same reason as Offer.Raw.
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
// Not the plain verbatim write Offer does: `resources` and `offers` live in
// their own tables (A17), so the bytes are a whole catalog again only once they
// are put back. Splicing here is what lets discover return `descriptor`,
// `bppId`, `bppUri` and `validity` without this service naming them anywhere.
//
// Carries Offer's caveat with one addition — a field edited on a decoded
// Catalog does not reach the output, and only Resources and Offers do.
func (c Catalog) MarshalJSON() ([]byte, error) {
	members, err := c.WithoutChildren()
	if err != nil {
		return nil, err
	}

	// Absent rather than empty when there is nothing: a catalog of offers alone
	// is legal, and an empty array would assert the publisher sent one.
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
// A map rather than bytes because the publish mapper has `isActive` to resolve
// into the document before storing it (A9), and bytes would make that a second
// decode. A Catalog with no Raw was built in Go rather than decoded — the path
// tests and the conformance suite take — and yields whatever the struct holds.
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

	// Raw is the resource exactly as it arrived, and what reaches the
	// resources.document column (A17). Same reason as Offer.Raw.
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

	// Raw is the offer exactly as it arrived, and what reaches the offers.offer
	// column. The spec leaves Offer.additionalProperties unset, so a publisher
	// may send members this struct never named; re-marshalling would drop them
	// and make the column's "verbatim" claim false for whoever relied on it.
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes an offer and keeps the bytes it decoded. The alias
// breaks the recursion; the bytes are copied because encoding/json does not
// retain the caller's slice after the call.
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

// MarshalJSON writes the offer back exactly as it arrived, which is what lets
// the discover response claim to render `offers.offer` verbatim.
//
// Raw is the bytes this value was DECODED from, so editing a field on a decoded
// Offer and marshalling would emit the original. Nothing does — an offer is
// stored verbatim and rendered verbatim, with no step between. A value built in
// Go carries no Raw and marshals from its fields.
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
// Both members are scalar strings and both required (C4), so an array payload
// fails L1 rather than silently having element zero picked for it. The pair and
// nothing else: the container's domain keys stay verbatim on the parent's
// RawMessage, making this a lens over a document, not a replacement for it.
type Attributes struct {
	Context string `json:"@context"`
	Type    string `json:"@type"`
}

// TimePeriod is the validity window on a catalog or an offer. It expands into
// four independent columns, not two: `startDate`/`endDate` are RFC 3339
// instants, `startTime`/`endTime` a recurring daily window, and the two halves
// may appear separately.
//
// All four are raw because RFC 7396 permits a patch that clears `endDate` and
// keeps `startDate`, so each needs three states — absent, null, value — and a
// *string collapses the first two.
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
	// ops", and telling the caller instead of ignoring it silently requires
	// distinguishing a sent 0 from an unsent field.
	DistanceMeters *float64 `json:"distanceMeters,omitempty"`

	Quantifier string `json:"quantifier,omitempty"`
	SRID       string `json:"srid,omitempty"`
}

// The nine CQL2 operators the spec's enum admits. S_TOUCHES and S_CROSSES are
// named because L1 accepts them as legal enum values, and the intent mapper's
// refusal against these two constants is all that stands between a caller and a
// silently wrong answer (A10).
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
// `beckn.yaml` declares a oneOf over a string and an array of strings, and real
// senders use both, so it is resolved here once and everything downstream sees a
// slice — a mapper branching on the wire form would be a second place for the
// two to diverge.
type Targets []string

// UnmarshalJSON accepts the scalar and the array form and refuses everything
// else. A shape read as "no targets" would drop the spatial predicate and answer
// with the whole index, the silently-widened result the plan rejects everywhere.
//
// `null` is checked FIRST because unmarshalling it into a string succeeds as a
// no-op, so reading the arms in order would turn `targets: null` into one empty
// pointer no sender wrote. The array is []*string one level down for the same
// reason: []string would render `["$.a", null]`'s null as "".
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
