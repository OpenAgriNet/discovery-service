package beckn

// ErrorCode is a canonical Beckn error code — one of the 76 members of the
// v2.0.0 `ErrorCode` enum, whose prefix names the protocol stack layer the
// fault originated at.
//
// A named type rather than a bare string because `Error.code` is declared
// `type: string`, so L1 accepts an invented code and it would ship green. The
// rule is asymmetric (implementation-plan.md Task 5), so this type
// constrains what this service MINTS while `Error.Code` stays assignable from a
// relayed string — how a downstream `DOM_` code passes through a chain
// untouched.
type ErrorCode string

// The codes this service mints. Deliberately not all 76: a constant no call
// site spends is a guess about which fault a later task will report, and the
// enum is in the fixture for anything missing. One exception is named below.
//
// Six stand in for codes earlier drafts invented; the mapping and the reason for
// each are in Task 5 of the plan. The precision those names carried moves into
// `Error.Message` and `Error.Details.Path`.
const (
	// CTX_ — context and routing. CodeContextActionMismatch answers an action
	// this service indexes no schema for: the envelope declares one thing and
	// the receiver serves another.
	CodeContextActionMismatch ErrorCode = "CTX_ACTION_MISMATCH"

	// The three the envelope rules spend, being the pass L1 cannot make (C6).
	//
	// Missing and invalid stay separate because they send the caller to
	// different places: absent means their sender never set the field,
	// malformed means it set it wrong. Version is a third rather than a second
	// spelling of invalid — it is the one field whose wrong value means this
	// receiver cannot SERVE the request, as opposed to cannot read it.
	CodeContextMissingField       ErrorCode = "CTX_MISSING_FIELD"
	CodeContextInvalidField       ErrorCode = "CTX_INVALID_FIELD"
	CodeContextVersionUnsupported ErrorCode = "CTX_VERSION_UNSUPPORTED"

	// AUT_ — authentication and trust. CodeAuthRateLimited is the one code that
	// carries a header with it (A4): 429 plus Retry-After.
	//
	// CodeAuthSignatureMissing is the exception to the rule above: no production
	// call site spends it, signature verification being out of scope. Declared
	// anyway because once CodeAuthRateLimited overrides itself to 429 it is the
	// only code that can exercise Status()'s TypeCore -> 401 branch, and
	// deleting it would remove the test rather than the branch. So 401 is
	// unreachable in a live deployment until signature verification lands.
	CodeAuthSignatureMissing ErrorCode = "AUT_SIGNATURE_MISSING"
	CodeAuthRateLimited      ErrorCode = "AUT_RATE_LIMITED"

	// SCH_ — core and linked-data schema: a body this service will not accept as
	// written, which is why three of the six mappings land here.
	//
	// InvalidJSON answers every way envelope parsing fails, there being no
	// context yet for a CTX_ code to be about. ValidationFailed carries L1's
	// faults and the duplicate catalog id, InvalidFormat an unreadable geometry,
	// and TypeNotSupported both A1's MASTER refusal and the S_TOUCHES /
	// S_CROSSES one — a value the spec admits and this receiver declines.
	CodeSchemaInvalidJSON      ErrorCode = "SCH_INVALID_JSON"
	CodeSchemaValidationFailed ErrorCode = "SCH_SCHEMA_VALIDATION_FAILED"
	CodeSchemaInvalidFormat    ErrorCode = "SCH_INVALID_FORMAT"
	CodeSchemaInvalidJSONPath  ErrorCode = "SCH_INVALID_JSONPATH"
	CodeSchemaTypeNotSupported ErrorCode = "SCH_TYPE_NOT_SUPPORTED"

	// POL_ — a refusal this deployment's policy requires rather than one the
	// request earned. CodePolicyNPCapacityExceeded answers a request body over
	// SERVER_MAX_REQUEST_BODY_BYTES, the enum naming no payload-size fault
	// (C14). Status maps it to 413, not the spec's 429; a deployment that grows
	// an engagement lifecycle must revisit that rather than give the code a
	// second meaning.
	CodePolicyNPCapacityExceeded ErrorCode = "POL_NP_CAPACITY_EXCEEDED"

	// CodePolicyGenericError answers a per-catalog ceiling the enum has no name
	// for — today, more than MaxGeometriesPerCatalog geometries.
	//
	// Deliberately NOT CodePolicyNPCapacityExceeded: a geometry ceiling is not a
	// byte ceiling, and giving one code two meanings is how a client comes to
	// retry the wrong thing. Appears only as a partial fault inside a 200, so
	// the excess is named and the rest of the catalog still publishes.
	CodePolicyGenericError ErrorCode = "POL_GENERIC_ERROR"

	// BIZ_ — the request is well-formed and asks for something the catalog
	// cannot support. CodeBusinessItemNotFound answers an offer whose
	// `resourceIds` names a resource the merged catalog does not hold
	// (implementation-plan.md §Data Model). An item is what the spec calls a resource,
	// so this member says exactly what happened rather than merely not lying.
	CodeBusinessItemNotFound ErrorCode = "BIZ_ITEM_NOT_FOUND"

	// NET_ — networking and the deployment's own gaps.
	// CodeNetworkCatalogSourceUnavailable answers a retrieval mode the backend
	// cannot run under SEARCH_FAIL_ON_UNAVAILABLE_MODE: a mode is a source of
	// catalogs, and the request succeeds unchanged where one is configured.
	CodeNetworkCatalogSourceUnavailable ErrorCode = "NET_CATALOG_SOURCE_UNAVAILABLE"

	// The fault nobody named: what an error reaching the response writer without
	// a code of its own becomes, so a 500 is still a Beckn error body.
	CodeNetworkInternalError ErrorCode = "NET_INTERNAL_ERROR"
)

// Error is the canonical Beckn error body, returned in NACKs and carried in the
// `errors` array of a per-catalog publish result.
//
// The shape is closed on both levels, which is what forces C7's answer to "many
// faults, one Error": a NACK carrying several is a CHAIN, each fault the
// details.cause of the one before it. No fault is dropped.
type Error struct {
	Code    ErrorCode     `json:"code"`
	Message string        `json:"message"`
	Details *ErrorDetails `json:"details,omitempty"`

	// Not in the v2.0.0 schema, and deliberately so: the PRD's five error
	// categories travel as the X-Beckn-Error-Type header and the error_type log
	// field instead (C1). This is the escape hatch for v1-style clients needing
	// the key in the body, written only when ERROR_INCLUDE_LEGACY_TYPE is true —
	// which defaults false, so the ordinary response stays spec-conformant.
	Type string `json:"type,omitempty"`
}

// ErrorDetails is the closed two-key object the spec allows beside a code. Path
// is a JSONPath into the request that failed — `$.message.publishDirectives[1]`
// — and Cause is the self-referencing link C7 builds fault chains out of.
type ErrorDetails struct {
	Path  string `json:"path,omitempty"`
	Cause *Error `json:"cause,omitempty"`
}

// Nack is the synchronous rejection body: the Ack family with `status` pinned
// to NACK and an `error` that is required rather than optional.
//
// One struct serves 400, 401, 403, 429 and 500. The spec names a schema for
// each, but all five declare the same `message` shape, and five identical Go
// structs would be five places for a sixth key to be added to four of them.
// What distinguishes them is the status line and the headers — the writer's.
//
// There is no `context` key, on this or any Ack-family member: a caller
// correlates a NACK by `message.messageId`, which is why the writer needs the
// request's message id and nothing else from the envelope.
type Nack struct {
	Message NackMessage `json:"message"`
}

// NackMessage is the Nack body's single property. Error is a value rather than
// a pointer because every schema pinning `status` to NACK also requires
// `error` — a pointer would make an unschematic rejection constructible.
type NackMessage struct {
	Status    string `json:"status"`
	MessageID string `json:"messageId"`
	Error     Error  `json:"error"`
}

// The two members of the Ack family's status enum.
const (
	StatusAck  = "ACK"
	StatusNack = "NACK"
)
