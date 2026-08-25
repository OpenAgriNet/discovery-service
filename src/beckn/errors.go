package beckn

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
	Code    string        `json:"code"`
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
