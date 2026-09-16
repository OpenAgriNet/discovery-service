package beckn

import "encoding/json"

// Version is the one protocol version this build serves.
//
// Not left as the literal inside the schema because L1 cannot reach the case it
// is spent on: `Context` has no `required` list (C6), so an envelope OMITTING
// version passes L1 clean and only the envelope rules can refuse it.
const Version = "2.0.0"

// The action names this service answers to and speaks back.
//
// `POST /publish` is the only publish ROUTE (C2), but both spellings are
// accepted as `context.action`: that is a field inside a body this service did
// not write, and the L1 schema index is keyed by action rather than by URL.
//
// The two on_ names are response actions, not routes (C3).
const (
	ActionPublish          = "publish"
	ActionCatalogPublish   = "catalog/publish"
	ActionCatalogOnPublish = "catalog/on_publish"
	ActionDiscover         = "discover"
	ActionOnDiscover       = "on_discover"
)

// NormalizeAction folds the two spellings of publish into the one this service
// reports, and leaves every other action alone.
//
// For telemetry rather than routing — it is what keeps beckn.action Bounded
// (fact/registry.go, BecknAction). Deliberately not applied to what goes back on
// the wire: a caller who said `catalog/publish` is answered in their own terms.
func NormalizeAction(action string) string {
	if action == ActionCatalogPublish {
		return ActionPublish
	}
	return action
}

// CatalogPublishAction is the `message` of a publish request.
type CatalogPublishAction struct {
	Catalogs          []Catalog          `json:"catalogs"`
	PublishDirectives []PublishDirective `json:"publishDirectives,omitempty"`
}

// PublishDirective carries the per-catalog processing instructions, matched to
// a catalog by CatalogID.
//
// Every field but CatalogID has a declared default, resolved field-wise before
// the merge runs (A9) — which is why nothing here is a pointer: by merge time
// there is no absence left to represent.
//
// An omitted CatalogType or UpdateMode arrives as the empty string and must not
// be read as a value. UpdateModeFull deletes the resources a payload did not
// mention, so a zero value reading as FULL would turn every directive-less
// republish into a partial wipe.
type PublishDirective struct {
	CatalogID   string `json:"catalogId"`
	CatalogType string `json:"catalogType,omitempty"`
	UpdateMode  string `json:"updateMode,omitempty"`

	// Omitted or empty resolves to the request's network, a deliberate deviation
	// from the spec's "visible to all eligible subscribers" (C8).
	VisibleTo []string `json:"visibleTo,omitempty"`

	ResourceDirectives []ResourceDirective `json:"resourceDirectives,omitempty"`
}

// ResourceDirective links a resource to a master resource it inherits from.
//
// Phase 1 accepts regular resources only, so a non-empty Extends is refused at
// intake with SCH_TYPE_NOT_SUPPORTED (A1). Modelled rather than dropped so the
// refusal can name it: a shape this service does not read is one it cannot
// reject on purpose.
type ResourceDirective struct {
	ResourceID string          `json:"resourceId"`
	Extends    *Extends        `json:"extends,omitempty"`
	Variant    json.RawMessage `json:"variant,omitempty"`
}

// Extends declares which master resource a provider resource inherits from.
type Extends struct {
	MasterResourceID string `json:"masterResourceId"`
}

// The catalog types the spec's enum admits. An absent directive is
// CatalogTypeRegular and is never inferred from content (C9).
const (
	CatalogTypeRegular = "REGULAR"
	CatalogTypeMaster  = "MASTER"
)

// The two update modes. MERGE is the default and is RFC 7396 JSON Merge Patch
// against the stored documents; FULL replaces the catalog outright, deleting
// the resources and offers the payload omits (A8).
const (
	UpdateModeMerge = "MERGE"
	UpdateModeFull  = "FULL"
)

// CatalogOnPublishAction is the `message` of the publish response — the spec's
// callback shape, returned inline in the 200 body (C3).
type CatalogOnPublishAction struct {
	Results []CatalogProcessingResult `json:"results"`
}

// CatalogProcessingResult is one catalog's verdict. Errors is the spec's own
// array, which is why the publish path never packs faults into a details.cause
// chain — a NACK is the only place that chain is used (C7).
type CatalogProcessingResult struct {
	CatalogID string        `json:"catalogId"`
	Status    string        `json:"status"`
	Errors    []Error       `json:"errors,omitempty"`
	Stats     *CatalogStats `json:"stats,omitempty"`
}

// The three verdicts the spec's status enum admits. StatusPartial is for the
// catalog that landed with a geometry dropped, and is never reported as
// StatusAccepted with a non-empty Errors (implementation-plan.md
// §Publish — How It Works).
const (
	StatusAccepted = "ACCEPTED"
	StatusRejected = "REJECTED"
	StatusPartial  = "PARTIAL"
)

// CatalogStats counts what a publish request landed. All three are read
// request-scoped (C12), so under MERGE a patch carrying one resource into a
// forty-resource catalog reports 1. CategoryCount is the number of distinct
// `@type` values, the spec having no category field anywhere (C5).
type CatalogStats struct {
	ItemCount     int `json:"itemCount"`
	ProviderCount int `json:"providerCount"`
	CategoryCount int `json:"categoryCount"`
}

// DiscoverAction is the `message` of a discover request.
type DiscoverAction struct {
	Intent Intent `json:"intent"`
}

// OnDiscoverAction is the `message` of the discover response, and it is exactly
// one field.
//
// It must NOT grow a Degraded field. The v2.0.0 schema is
// additionalProperties:false with `catalogs` as its only property, so an extra
// key is a response that fails its own schema — and `omitempty` would hide that
// on the ordinary path while shipping the invalid body on the degraded one, the
// path that matters. The degraded list travels as X-Beckn-Degraded (C11).
type OnDiscoverAction struct {
	Catalogs []Catalog `json:"catalogs"`
}
