// Package errors builds the faults this service reports to a caller.
//
// An AppError is a value, not a control-flow device: it carries the Beckn code,
// the JSONPath into the request that failed and — under C7 — the chain of
// further faults hanging off it. It knows how to say what it is, and nothing
// about how to write itself down. Serialising one is src/platform/httpx's job
// and only its job, which is the DRY rule on error construction and response
// writing held as a package boundary rather than restated as a convention.
package errors

import (
	stderrors "errors"
	"fmt"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
)

// The message a fault with no Beckn code of its own is reported as. Fixed text,
// because the alternative is the underlying error's own string, and that string
// is written by a driver or a library for an operator reading a log — a dialled
// host and port, a query, a file path. None of it is the caller's.
const internalMessage = "internal error"

// AppError is one fault, with the code and the path that name it.
//
// Cause is C7's answer to a validation pass that produced several faults.
// `Error.details` is additionalProperties:false with exactly {path, cause}, so
// a list of extra pointers cannot go there and still validate — the faults
// become a chain instead, each one the details.cause of the one before, and
// Chain is the only thing that builds it.
//
// Status and the error_type are not fields. Both are decided by the code's own
// prefix, so two call sites reporting one fault cannot disagree about what it
// is worth on the wire.
type AppError struct {
	Code    beckn.ErrorCode
	Message string

	// A JSONPath into the request — `$.message.publishDirectives[1]` — which is
	// the form the spec's own example uses. Empty where the fault is about the
	// request as a whole rather than about a field in it.
	Path string

	// The next fault in the chain, or nil at the end of it.
	Cause *AppError

	// The back-off A4 requires beside a 429. Zero everywhere else, and the one
	// piece of state the writer turns into a header rather than into the body.
	RetryAfter time.Duration
}

// Context builds a fault in the CTX_ family — context and routing.
func Context(code beckn.ErrorCode, message string) *AppError {
	return &AppError{Code: code, Message: message}
}

// Auth builds a fault in the AUT_ family — authentication and trust.
func Auth(code beckn.ErrorCode, message string) *AppError {
	return &AppError{Code: code, Message: message}
}

// Schema builds a fault in the SCH_ family — core and linked-data schema. This
// is the family for a body this service will not accept as written.
func Schema(code beckn.ErrorCode, message string) *AppError {
	return &AppError{Code: code, Message: message}
}

// Network builds a fault in the NET_ family — networking, and the gaps in a
// deployment's own capabilities.
func Network(code beckn.ErrorCode, message string) *AppError {
	return &AppError{Code: code, Message: message}
}

// Business builds a fault in the BIZ_ family — application and business logic.
func Business(code beckn.ErrorCode, message string) *AppError {
	return &AppError{Code: code, Message: message}
}

// Policy builds a fault in the POL_ family — a refusal this deployment's policy
// requires rather than one the request earned.
func Policy(code beckn.ErrorCode, message string) *AppError {
	return &AppError{Code: code, Message: message}
}

// RateLimited builds the AUT_RATE_LIMITED refusal A4 specifies, carrying the
// back-off the limiter computed.
//
// It has a constructor of its own because it is the only code that puts
// something in a header — Retry-After — and a caller who has to remember to
// set a field after construction is a caller who will eventually not.
func RateLimited(retryAfter time.Duration, message string) *AppError {
	return &AppError{Code: beckn.CodeAuthRateLimited, Message: message, RetryAfter: retryAfter}
}

// Internal is the fault an error with no Beckn code of its own becomes. The
// caller gets a 500 and a fixed message; whatever actually failed goes to the
// log, where it is the operator's to read.
func Internal() *AppError {
	return Network(beckn.CodeNetworkInternalError, internalMessage)
}

// At returns a copy of the fault pointing at path.
//
// A copy, because one fault is frequently built once and reported against
// several paths — a mapper walking an array of directives is the common case —
// and mutating in place would leave every report carrying whichever path was
// set last.
func (e *AppError) At(path string) *AppError {
	copied := *e
	copied.Path = path
	return &copied
}

// Chain folds faults into one, each becoming the details.cause of the one
// before it (C7). It returns nil for no faults, so a caller can hand it the
// result of a validation pass without checking whether the pass found anything.
//
// The faults are copied rather than linked. Chaining the same fault twice would
// otherwise leave the first chain's links hanging off a value the caller still
// holds, and a fault pointed at itself is a serialiser that does not terminate.
//
// The publish path never calls this. `CatalogProcessingResult.errors` is
// natively an array of Error, so there is nothing to pack, and a second
// encoding of "many faults" is a second thing to keep right.
func Chain(faults ...*AppError) *AppError {
	var head, tail *AppError
	for _, fault := range faults {
		if fault == nil {
			continue
		}
		copied := *fault
		copied.Cause = nil
		if head == nil {
			head, tail = &copied, &copied
			continue
		}
		tail.Cause = &copied
		tail = &copied
	}
	return head
}

// FromError returns err as an *AppError, coercing anything without a code of
// its own to Internal.
//
// This is what lets the response writer be total: one function decides what an
// unrecognised error becomes, rather than each handler inventing a status and a
// body for the failures it did not anticipate.
func FromError(err error) *AppError {
	if err == nil {
		return nil
	}

	var fault *AppError
	if stderrors.As(err, &fault) {
		return fault
	}
	return Internal()
}

// Error implements error.
func (e *AppError) Error() string {
	text := fmt.Sprintf("%s: %s", e.Code, e.Message)
	if e.Path != "" {
		text += " at " + e.Path
	}
	if e.Cause != nil {
		text += ": " + e.Cause.Error()
	}
	return text
}

// Unwrap returns the next fault in the chain, so errors.Is and errors.As walk
// a C7 chain the way they walk a %w one.
//
// The nil check is not defensive: returning e.Cause directly would hand back an
// error interface holding a nil *AppError, which is non-nil to errors.As and
// would report a match on a fault that is not there.
func (e *AppError) Unwrap() error {
	if e.Cause == nil {
		return nil
	}
	return e.Cause
}
