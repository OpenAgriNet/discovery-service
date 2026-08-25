package beckn

import "encoding/json"

// The action names this service answers to and speaks back.
//
// `POST /publish` is the only publish route (C2) — there is no /catalog/publish
// alias, because a second path onto one handler is a second thing to route,
// rate-limit, log and document. `context.action` is a different question: it is
// a field inside a body this service did not write, and the L1 schema index is
// keyed by action rather than by URL, so both spellings resolve to the same
// schema and both are accepted.
//
// The two on_ names are response actions, not routes. C3 returns the callback
// shape inline in the 200 body; async callback dispatch is out of scope.
const (
	ActionPublish          = "publish"
	ActionCatalogPublish   = "catalog/publish"
	ActionCatalogOnPublish = "catalog/on_publish"
	ActionDiscover         = "discover"
	ActionOnDiscover       = "on_discover"
)

// CatalogPublishAction is the `message` of a publish request.
type CatalogPublishAction struct {
	Catalogs          []Catalog          `json:"catalogs"`
	PublishDirectives []PublishDirective `json:"publishDirectives,omitempty"`
}

// PublishDirective carries the per-catalog processing instructions, matched to
// a catalog by CatalogID.
//
// Every field but CatalogID has a declared default, and the defaults are
// resolved field-wise before the merge runs (A9): a directive that is absent
// entirely and one naming only `catalogId` must come out the same, because a
// publisher means the same thing by both. That is also why nothing here is a
// pointer — by merge time there is no absence left to represent, and a pointer
// that can never be nil is an invitation to write the branch that makes it nil.
//
// The empty string is what an omitted CatalogType or UpdateMode arrives as, and
// it must not be read as a value. UpdateModeFull deletes the resources a
// payload did not mention, so a zero value reading as FULL would turn every
// directive-less republish into a partial wipe.
type PublishDirective struct {
	CatalogID   string `json:"catalogId"`
	CatalogType string `json:"catalogType,omitempty"`
	UpdateMode  string `json:"updateMode,omitempty"`

	// Omitted or empty resolves to the request's network (C8) — a deliberate
	// deviation from the spec's "visible to all eligible subscribers", because
	// publishing to every network by a typo is the worse of the two failures
	// and a publisher wanting network-wide reach can say so explicitly.
	VisibleTo []string `json:"visibleTo,omitempty"`

	ResourceDirectives []ResourceDirective `json:"resourceDirectives,omitempty"`
}

// ResourceDirective links a resource to a master resource it inherits from.
//
// Phase 1 accepts regular resources only, so any directive carrying a non-empty
// Extends is refused at intake with SCH_TYPE_NOT_SUPPORTED (A1) — refused, not
// partially handled. The field is modelled rather than dropped precisely so the
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
// CatalogTypeRegular and is never inferred from content (C9): the spec would
// have a catalog carrying no offers read as MASTER, which would make the A1
// refusal reject every ordinary resource-only catalog — the common case.
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

// CatalogProcessingResult is one catalog's verdict.
//
// Errors is the spec's own array, which is why the publish path never packs
// faults into a details.cause chain: there is nothing to pack (C7). A NACK is
// the only place the chain is used.
type CatalogProcessingResult struct {
	CatalogID string        `json:"catalogId"`
	Status    string        `json:"status"`
	Errors    []Error       `json:"errors,omitempty"`
	Stats     *CatalogStats `json:"stats,omitempty"`
}

// The three verdicts the spec's status enum admits.
//
// StatusPartial exists for the catalog that landed with one geometry dropped.
// Returning StatusAccepted with a non-empty Errors would tell a publisher whose
// tooling branches on the field the spec made an enum the opposite of what
// happened, with the correction in the array they never read.
const (
	StatusAccepted = "ACCEPTED"
	StatusRejected = "REJECTED"
	StatusPartial  = "PARTIAL"
)

// CatalogStats counts what a publish request landed.
//
// All three are read request-scoped (C12). ItemCount and CategoryCount count
// what this request landed — under MERGE a patch carrying one resource into a
// forty-resource catalog reports 1 — and ProviderCount is 1 because a catalog
// has exactly one provider, so the request-scoped and catalog-scoped readings
// of that one coincide.
//
// CategoryCount is answered as the number of distinct `@type` values, because
// the spec has no category field anywhere (C5) and `@type` is the only grouping
// the schema actually has.
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
// It must not grow a Degraded field. The v2.0.0 schema declares
// additionalProperties:false with `catalogs` as its only property, so an extra
// key here is not an extension — it is a response that fails its own schema at
// the first consumer strict enough to check. `omitempty` would hide that on the
// ordinary path and ship the invalid body on precisely the path that matters:
// the degraded one. The degraded list travels as the X-Beckn-Degraded response
// header instead (C11).
type OnDiscoverAction struct {
	Catalogs []Catalog `json:"catalogs"`
}
