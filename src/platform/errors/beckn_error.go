package errors

import (
	"net/http"
	"strings"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

// The five PRD error categories (C1).
//
// Not fields on beckn.Error: v2.0.0 closed that schema and dropped `type`, so
// the categories travel as the X-Beckn-Error-Type header and the error_type log
// field. Spelled here and nowhere else — a category is a value a consumer
// branches on, and a second spelling is a second thing to keep true.
const (
	TypeContext = "CONTEXT"
	TypeCore    = "CORE"
	TypeDomain  = "DOMAIN"
	TypePolicy  = "POLICY"
	TypeSystem  = "SYSTEM"
)

// TypeOf maps a code to its PRD category by its prefix, which is the only
// signal the enum carries about the layer a fault came from.
//
// An unknown prefix is attributed to SYSTEM rather than left blank. The header
// goes out on every error response, and a blank one is unattributable — it reads
// as a bug in the sender rather than a fault a consumer can categorise. SYSTEM
// is also the honest answer: this receiver could not attribute the code.
//
// DOM_ has no members in the v2.0.0 enum and still has a row, because it is the
// prefix a code relayed from a downstream system arrives with — the one case
// `Error`'s own description names as legitimately non-canonical. Nothing in this
// service mints it.
func TypeOf(code beckn.ErrorCode) string {
	prefix, _, _ := strings.Cut(string(code), "_")
	switch prefix {
	case "CTX":
		return TypeContext
	case "AUT":
		return TypeCore
	case "SCH", "BIZ", "DOM":
		return TypeDomain
	case "POL":
		return TypePolicy
	default:
		return TypeSystem
	}
}

// Type returns the fault's PRD category.
func (e *AppError) Type() string {
	return TypeOf(e.Code)
}

// Status returns the HTTP status this fault answers with.
//
// Derived from the code, never stored, so the status cannot drift from the code
// a caller reads beside it. Three codes need more than their family says:
//
//   - AUT_RATE_LIMITED, 429 rather than 401: the credentials were fine, the pace
//     was not (A4).
//   - NET_CATALOG_SOURCE_UNAVAILABLE, 400 rather than 500: the mode is not
//     configured here, a gap in the deployment rather than a failure in it, and
//     a 500 would tell the caller to retry what can only ever fail.
//   - POL_NP_CAPACITY_EXCEEDED, 413 rather than 403: this service mints it only
//     for a body over SERVER_MAX_REQUEST_BODY_BYTES, and 403 sends the caller to
//     their credentials for a fault in their payload (C14).
//
// A deployment that grows an engagement lifecycle must give that refusal a code
// of its own; the one thing it must not do is make this mapping carry two
// statuses.
func (e *AppError) Status() int {
	switch e.Code {
	case beckn.CodeAuthRateLimited:
		return http.StatusTooManyRequests
	case beckn.CodeNetworkCatalogSourceUnavailable:
		return http.StatusBadRequest
	case beckn.CodePolicyNPCapacityExceeded:
		return http.StatusRequestEntityTooLarge
	}

	switch e.Type() {
	case TypeContext, TypeDomain:
		return http.StatusBadRequest
	case TypeCore:
		return http.StatusUnauthorized
	case TypePolicy:
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

// Beckn renders the fault — and everything chained behind it — as the wire
// Error.
//
// The only conversion from an AppError to a beckn.Error. Serialising the result
// is src/platform/httpx's job, which is what makes "one writer" checkable from
// the import graph rather than from memory.
//
// cfg carries C1's legacy switch. When ERROR_INCLUDE_LEGACY_TYPE is on the
// category is written into every level of the chain, not just the first: a v1
// client reading a details.cause would otherwise find a body it cannot
// categorise, which is the thing the flag exists to prevent.
func (e *AppError) Beckn(cfg config.Errors) beckn.Error {
	rendered := beckn.Error{Code: e.Code, Message: e.Message}
	if cfg.IncludeLegacyType {
		rendered.Type = e.Type()
	}

	if e.Path == "" && e.Cause == nil {
		return rendered
	}

	rendered.Details = &beckn.ErrorDetails{Path: e.Path}
	if e.Cause != nil {
		cause := e.Cause.Beckn(cfg)
		rendered.Details.Cause = &cause
	}
	return rendered
}
