package errors

import (
	"net/http"
	"strings"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

// The five PRD error categories (C1).
//
// They are not fields on beckn.Error: v2.0.0 closed that schema with
// additionalProperties:false and dropped `type`, so the categories travel as
// the X-Beckn-Error-Type response header and the error_type log field instead.
// They are spelled here and nowhere else, for the same reason the log field
// keys are spelled once in src/platform/logger — a category is a value a
// consumer branches on, and a second spelling of it is a second thing to keep
// true.
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
// A prefix this build does not know is attributed to SYSTEM rather than left
// blank. The header goes out on every error response, and a blank one is worse
// than a wrong one — it is unattributable, and reads to the consumer as a bug
// in the sender rather than as a fault they can categorise. SYSTEM is also the
// honest answer: this receiver could not attribute the code.
//
// DOM_ has no members in the v2.0.0 enum and still has a row, because it is the
// prefix a code relayed from a downstream system arrives with — the one case
// `Error`'s own description names as legitimately non-canonical. It maps, but
// nothing in this service mints it.
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
// AUT_RATE_LIMITED is a 429 rather than the family's 401 — the credentials were
// fine, the pace was not (A4).
//
// NET_CATALOG_SOURCE_UNAVAILABLE is a 400 rather than the family's 500. The
// requested retrieval mode is not configured on this deployment, which is a gap
// in the deployment and not a failure in it: nothing is broken, retrying will
// not help, and the caller fixes it by asking for a mode that exists. A 500
// would tell them to retry something that can only ever fail.
//
// POL_NP_CAPACITY_EXCEEDED is a 413 rather than the family's 403. This service
// mints it for one thing — a body over SERVER_MAX_REQUEST_BODY_BYTES — and 403
// would send the caller looking at their credentials for a fault that is in
// their payload (C14). The spec's other use of the code, an engagement capacity
// limit at 429, has no path in a service that runs no engagements; a deployment
// that grows one must give it a code of its own rather than make this mapping
// mean two statuses.
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
// This is the only conversion from an AppError to a beckn.Error. Serialising
// the result is src/platform/httpx's job; producing it is this package's, and
// keeping the two apart is what makes "one writer" checkable by looking at the
// import graph rather than by remembering it.
//
// cfg carries C1's legacy switch. When ERROR_INCLUDE_LEGACY_TYPE is on the
// category is written into every level of the chain, not just the first: a v1
// client reading a details.cause would otherwise find a body it cannot
// categorise, which is the whole thing the flag exists to prevent.
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
