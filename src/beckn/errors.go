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
// enum is in the fixture for anything this list is missing.
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

	// AUT_ — authentication and trust. CodeAuthRateLimited is the one code
	// that carries a header with it (A4): 429 plus Retry-After.
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
