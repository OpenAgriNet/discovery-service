package errors

import (
	stderrors "errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
)

// One constructor per code family, and the status each family answers with.
// The status is derived from the code rather than passed in, so two call sites
// reporting one fault cannot disagree about what it is worth on the wire.
func TestEachFamilyCarriesItsStatus(t *testing.T) {
	cases := []struct {
		name  string
		fault *AppError
		want  int
	}{
		{"context", Context(beckn.CodeContextActionMismatch, "unknown action"), http.StatusBadRequest},
		{"auth", Auth(beckn.CodeAuthSignatureMissing, "no signature"), http.StatusUnauthorized},
		{"schema", Schema(beckn.CodeSchemaInvalidJSON, "unreadable"), http.StatusBadRequest},
		{"network", Network(beckn.CodeNetworkInternalError, "boom"), http.StatusInternalServerError},
		{"business", Business(beckn.ErrorCode("BIZ_NO_RESULTS_FOUND"), "nothing"), http.StatusBadRequest},
		{"policy", Policy(beckn.ErrorCode("POL_GEO_RESTRICTED"), "not here"), http.StatusForbidden},
	}
	for _, c := range cases {
		if got := c.fault.Status(); got != c.want {
			t.Errorf("%s: Status() = %d, want %d", c.name, got, c.want)
		}
	}
}

// A4: the rate limiter's refusal is a 429 and carries the back-off it computed.
// It is the one code with a header attached, which is why it has a constructor
// of its own rather than a caller remembering to set a field.
func TestRateLimitedIs429WithItsBackoff(t *testing.T) {
	fault := RateLimited(3*time.Second, "20 requests per second")

	if fault.Code != beckn.CodeAuthRateLimited {
		t.Errorf("Code = %q, want %q", fault.Code, beckn.CodeAuthRateLimited)
	}
	if got := fault.Status(); got != http.StatusTooManyRequests {
		t.Errorf("Status() = %d, want %d", got, http.StatusTooManyRequests)
	}
	if fault.RetryAfter != 3*time.Second {
		t.Errorf("RetryAfter = %s, want 3s", fault.RetryAfter)
	}
}

// One of the two codes whose family status is wrong for it. A missing retrieval
// mode is a NET_ fault — the deployment's gap, not the caller's — but the caller
// can fix it by asking for a different mode, so it is a 400 rather than a 500.
func TestAnUnavailableRetrievalModeIsA400(t *testing.T) {
	fault := Network(beckn.CodeNetworkCatalogSourceUnavailable, "semantic is not configured")

	if got := fault.Status(); got != http.StatusBadRequest {
		t.Errorf("Status() = %d, want %d", got, http.StatusBadRequest)
	}
	if got := fault.Type(); got != TypeSystem {
		t.Errorf("Type() = %q, want %q", got, TypeSystem)
	}
}

// The other one, and the reason it is worth its own test rather than a row in
// the family table: POL_NP_CAPACITY_EXCEEDED is the family's status everywhere
// in the spec except here (C14). 403 would send a caller whose body was too
// large to go and look at their credentials.
func TestAnOversizedBodyIsA413(t *testing.T) {
	fault := Policy(beckn.CodePolicyNPCapacityExceeded, "request body exceeds the 1024 byte limit this deployment accepts")

	if got := fault.Status(); got != http.StatusRequestEntityTooLarge {
		t.Errorf("Status() = %d, want %d", got, http.StatusRequestEntityTooLarge)
	}
	if got := fault.Type(); got != TypePolicy {
		t.Errorf("Type() = %q, want %q", got, TypePolicy)
	}
}

// At returns a copy, so a fault built once and pointed at two paths does not
// end up as one fault whose path is whichever call ran last.
func TestAtDoesNotMutateTheFaultItCopies(t *testing.T) {
	base := Schema(beckn.CodeSchemaInvalidFormat, "not a geometry")

	first := base.At("$.message.catalogs[0]")
	second := base.At("$.message.catalogs[1]")

	if base.Path != "" {
		t.Errorf("At wrote onto its receiver: Path = %q", base.Path)
	}
	if first.Path == second.Path {
		t.Errorf("both copies carry %q; At is returning the same fault twice", first.Path)
	}
}

// AppError is an error, and the chain is what Unwrap walks — so errors.Is and
// errors.As reach a cause that arrived three hops down without every caller
// between here and there having to know about details.cause.
func TestTheChainIsWalkableAsAGoError(t *testing.T) {
	deep := Policy(beckn.ErrorCode("POL_CONSENT_REQUIRED"), "consent required")
	chain := Chain(Schema(beckn.CodeSchemaValidationFailed, "top"), deep)

	var found *AppError
	if !stderrors.As(stderrors.Unwrap(chain), &found) {
		t.Fatalf("Unwrap(chain) is not an *AppError: %v", stderrors.Unwrap(chain))
	}
	if found.Code != deep.Code {
		t.Errorf("unwrapped code = %q, want %q", found.Code, deep.Code)
	}
	if got := chain.Error(); got == "" {
		t.Errorf("Error() is empty")
	}
}

// The last fault in a chain unwraps to nil rather than to a typed nil pointer,
// which errors.As would report as a match and hand the caller a nil *AppError.
func TestTheEndOfAChainUnwrapsToNil(t *testing.T) {
	if got := stderrors.Unwrap(Schema(beckn.CodeSchemaInvalidJSON, "unreadable")); got != nil {
		t.Errorf("Unwrap of an uncaused fault = %#v, want nil", got)
	}
}

// FromError is what makes the response writer total. An error with no code of
// its own becomes a 500 that says nothing about the failure: the text of a
// database or driver error is not something to hand a caller, and the original
// goes to the log instead.
func TestFromErrorCoercesAnUncodedError(t *testing.T) {
	coerced := FromError(fmt.Errorf("dial tcp 10.0.0.1:5432: connect: connection refused"))

	if coerced.Code != beckn.CodeNetworkInternalError {
		t.Errorf("Code = %q, want %q", coerced.Code, beckn.CodeNetworkInternalError)
	}
	if got := coerced.Status(); got != http.StatusInternalServerError {
		t.Errorf("Status() = %d, want 500", got)
	}
	if got := coerced.Message; got == "" {
		t.Errorf("Message is empty")
	}
	for _, leak := range []string{"10.0.0.1", "5432", "connection refused"} {
		if strings.Contains(coerced.Message, leak) {
			t.Errorf("Message %q leaks %q from the underlying error", coerced.Message, leak)
		}
	}
}

// An AppError wrapped in %w is still an AppError, so a fault that crossed a
// package boundary the way the Errors constraint requires is not flattened to
// a 500 at the writer.
func TestFromErrorFindsAWrappedAppError(t *testing.T) {
	fault := Schema(beckn.CodeSchemaTypeNotSupported, "S_TOUCHES is not approximable")
	wrapped := fmt.Errorf("map intent: %w", fault)

	if got := FromError(wrapped); got.Code != fault.Code {
		t.Errorf("Code = %q, want %q", got.Code, fault.Code)
	}
	if got := FromError(nil); got != nil {
		t.Errorf("FromError(nil) = %+v, want nil", got)
	}
}
