package beckn

// ErrorCode is a canonical Beckn error code — one of the 76 members of the
// v2.0.0 `ErrorCode` enum, whose prefix names the protocol stack layer the
// fault originated at.
//
// It is a named type and not a bare string because the schema cannot enforce
// the constraint it states: `Error.code` is declared `type: string` rather than
// `$ref: ErrorCode`, so L1 validation accepts anything and an invented code
// would ship green. The rule the type stands in for is in `Error`'s own
// description, and it is asymmetric — the topmost Error MUST carry a canonical
// code, while a `details.cause` MAY carry a domain-specific or non-canonical
// one from downstream. So this type constrains what this service mints;
// `Error.Code` stays assignable from a relayed string, which is how a `DOM_`
// code arrives in a chain and is passed through untouched.
type ErrorCode string

// The codes this service mints. Deliberately not all 76: a constant no call
// site spends is a guess about which fault a later task will report, and the
// enum is in the fixture for anything this list is missing. One exception is
// declared and named as such below, with the reason it earns its place.
//
// Six of them stand in for codes earlier drafts invented — the mapping and the
// reason for each are in Task 5 of the plan. The precision those names carried
// moves into `Error.Message` and `Error.Details.Path`, which is where a human
// reads it; what would have been lost instead is a consumer's ability to
// branch on `code` at all.
const (
	// CTX_ — context and routing. CodeContextActionMismatch answers an action
	// this service indexes no schema for: the envelope declares one thing and
	// the receiver serves another.
	CodeContextActionMismatch ErrorCode = "CTX_ACTION_MISMATCH"

	// The three the envelope rules spend (C6). `Context` declares no `required`
	// list, so L1 cannot reject a body missing `transactionId` and these are the
	// codes for the pass that can.
	//
	// Missing and invalid are separate members in the enum and are kept
	// separate here, because they send the caller to different places: absent
	// means their sender never set the field, malformed means it set it wrong.
	// Collapsing them into one code would make a typo and an unimplemented
	// field indistinguishable in the only signal a consumer branches on.
	//
	// CodeContextVersionUnsupported is the third rather than a second spelling
	// of invalid: `version` is the one context field whose wrong value means
	// this receiver cannot serve the request at all, as opposed to cannot read
	// it.
	CodeContextMissingField       ErrorCode = "CTX_MISSING_FIELD"
	CodeContextInvalidField       ErrorCode = "CTX_INVALID_FIELD"
	CodeContextVersionUnsupported ErrorCode = "CTX_VERSION_UNSUPPORTED"

	// AUT_ — authentication and trust. CodeAuthRateLimited is the one code
	// that carries a header with it (A4): 429 plus Retry-After.
	//
	// CodeAuthSignatureMissing is the exception to the rule above: no
	// production call site spends it, because signature verification is out of
	// scope for this phase and the layer that would mint it does not exist. It
	// is declared anyway because it is the only AUT_ member left once
	// CodeAuthRateLimited overrides itself to 429, and so the only code that
	// can exercise `Status()`'s TypeCore -> 401 branch at all. Deleting it
	// would not remove the branch, only the one test that proves it. Note what
	// that means for a reader of a live deployment: 401 is unreachable today,
	// and the day signature verification lands is the day this constant stops
	// being test-only.
	CodeAuthSignatureMissing ErrorCode = "AUT_SIGNATURE_MISSING"
	CodeAuthRateLimited      ErrorCode = "AUT_RATE_LIMITED"

	// SCH_ — core and linked-data schema. This is the family for a body this
	// service will not accept as written, which is why three of the six
	// mappings land here.
	//
	// CodeSchemaInvalidJSON answers every way envelope parsing fails, because
	// each of them means "this is not a readable JSON object" and there is no
	// context yet for a CTX_ code to be about. CodeSchemaValidationFailed
	// carries L1's faults and the duplicate catalog id. CodeSchemaInvalidFormat
	// carries an unreadable geometry. CodeSchemaTypeNotSupported carries both
	// A1's MASTER refusal and the S_TOUCHES / S_CROSSES refusal — the same
	// species of fault, a value the spec admits and this receiver declines.
	CodeSchemaInvalidJSON      ErrorCode = "SCH_INVALID_JSON"
	CodeSchemaValidationFailed ErrorCode = "SCH_SCHEMA_VALIDATION_FAILED"
	CodeSchemaInvalidFormat    ErrorCode = "SCH_INVALID_FORMAT"
	CodeSchemaInvalidJSONPath  ErrorCode = "SCH_INVALID_JSONPATH"
	CodeSchemaTypeNotSupported ErrorCode = "SCH_TYPE_NOT_SUPPORTED"

	// POL_ — a refusal this deployment's policy requires rather than one the
	// request earned. CodePolicyNPCapacityExceeded answers a request body over
	// SERVER_MAX_REQUEST_BODY_BYTES.
	//
	// The enum names no payload-size fault, and this is the nearest member that
	// is not a lie: the request is well-formed and would succeed against a
	// deployment configured to accept it, which is what POL_ means. The spec
	// pairs this code with 429 in NackTooManyRequests, for an *engagement*
	// capacity limit; this service has no engagement lifecycle to run out of, so
	// the code is unambiguous here and Status maps it to 413. A deployment that
	// grows one must revisit that mapping rather than add a second meaning to
	// it — see C14.
	CodePolicyNPCapacityExceeded ErrorCode = "POL_NP_CAPACITY_EXCEEDED"

	// CodePolicyGenericError answers a per-catalog ceiling this deployment
	// imposes and the enum has no name for — today, a catalog carrying more than
	// MaxGeometriesPerCatalog geometries.
	//
	// It is deliberately NOT CodePolicyNPCapacityExceeded. C14 pins that member
	// to a request body over SERVER_MAX_REQUEST_BODY_BYTES and to Status 413; a
	// geometry ceiling is not a byte ceiling, and giving one code two meanings
	// is how a client comes to retry the wrong thing. This one appears only as a
	// partial fault inside a 200 response, so the excess geometries are named
	// rather than silently dropped and the rest of the catalog still publishes.
	CodePolicyGenericError ErrorCode = "POL_GENERIC_ERROR"

	// BIZ_ — the request is well-formed and asks for something the catalog
	// cannot support.
	//
	// CodeBusinessItemNotFound answers an offer whose `resourceIds` names a
	// resource the merged catalog does not hold. `resource_ids` carries no
	// foreign key — PostgreSQL cannot declare one into an array — so a typo
	// would otherwise store an offer attached to nothing and report success.
	// An item is what the spec calls a resource, which makes this the member
	// that says exactly what happened rather than the nearest one that does not
	// lie.
	CodeBusinessItemNotFound ErrorCode = "BIZ_ITEM_NOT_FOUND"

	// NET_ — networking and the deployment's own gaps.
	//
	// CodeNetworkCatalogSourceUnavailable answers a retrieval mode the backend
	// cannot run under SEARCH_FAIL_ON_UNAVAILABLE_MODE. A retrieval mode is a
	// source of catalogs, and NET_ attributes it correctly: the request is
	// valid and succeeds unchanged on a deployment that configured the mode.
	CodeNetworkCatalogSourceUnavailable ErrorCode = "NET_CATALOG_SOURCE_UNAVAILABLE"

	// The fault nobody named. It is what an error reaching the response writer
	// without a code of its own becomes, so that a 500 is still a Beckn error
	// body rather than an empty one.
	CodeNetworkInternalError ErrorCode = "NET_INTERNAL_ERROR"
)

// Error is the canonical Beckn error body, returned in NACKs and carried in the
// `errors` array of a per-catalog publish result.
//
// The shape is closed on both levels — `Error` is additionalProperties:false
// with exactly {code, message, details}, and `details` is additionalProperties:
// false with exactly {path, cause}. That is what forces C7's answer to "many
// faults, one Error": a list of extra pointers cannot go in `details` and still
// validate, so a NACK carrying several faults is a chain, each fault the
// details.cause of the one before it. No fault is dropped.
type Error struct {
	Code    ErrorCode     `json:"code"`
	Message string        `json:"message"`
	Details *ErrorDetails `json:"details,omitempty"`

	// Not in the v2.0.0 schema, and deliberately so. The PRD's five error
	// categories have no home in a body the spec closed, so they travel as the
	// X-Beckn-Error-Type response header and the error_type log field (C1).
	// This field is the escape hatch for v1-style clients that require the key
	// in the body, written only when ERROR_INCLUDE_LEGACY_TYPE is true — which
	// defaults to false, so the ordinary response stays spec-conformant.
	Type string `json:"type,omitempty"`
}

// ErrorDetails is the closed two-key object the spec allows beside a code.
//
// Path is a JSONPath into the request that failed — `$.message.
// publishDirectives[1]` — which is the form the spec's own example uses. Cause
// is the self-referencing link C7 builds fault chains out of.
type ErrorDetails struct {
	Path  string `json:"path,omitempty"`
	Cause *Error `json:"cause,omitempty"`
}

// Nack is the synchronous rejection body: the Ack family with `status` pinned
// to NACK and an `error` that is required rather than optional.
//
// One struct serves 400, 401, 403, 429 and 500. The spec names a separate
// schema for each — NackBadRequest, NackUnauthorized, NackForbidden,
// NackTooManyRequests, ServerError — but all five declare the same `message`
// shape, and five identical Go structs would be five places for a sixth key to
// be added to four of them. What actually distinguishes the five is the status
// line and the headers, and those are the response writer's.
//
// There is no `context` key, on this or on any member of the Ack family. A
// caller correlates a NACK by `message.messageId`, which is why the writer
// needs the request's message id and needs nothing else from the envelope.
type Nack struct {
	Message NackMessage `json:"message"`
}

// NackMessage is the Nack body's single property.
//
// Error is a value and not a pointer because every schema that pins `status` to
// NACK also lists `error` in its required set: a rejection with no error is a
// body that fails its own schema, and a pointer would make that shape
// constructible.
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
