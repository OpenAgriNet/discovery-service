package middlewares

import (
	"net/http"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
	"github.com/OpenAgriNet/discovery-service/src/platform/validation"
)

// notMounted is what a chain assembled without Envelope above this middleware
// panics with. A panic rather than a pass-through: with no envelope and no
// buffered body, letting the request past would disable L1 and C6 at once and
// report 200 while doing it. Recover turns it into a logged 500.
const notMounted = "SchemaValidator is mounted without Envelope above it"

// SchemaValidator refuses a request that does not satisfy the protocol, and
// reports every way in which it does not.
//
// Two passes, in this order. The envelope rules run first and unconditionally
// (C6) — the published Context declares no `required` list, so L1 cannot refuse
// a body carrying no transaction id however strict it is. L1 runs second and
// only when configured on.
//
// Faults within a pass are chained through details.cause (C7) so a caller with
// five mistakes learns about five. The two passes are NOT merged: once the
// context is unreadable every schema fault below it is a consequence, and
// reporting both would bury the one the caller has to act on.
//
// The message id is echoed as sent (C13), including the value being rejected as
// malformed — that NACK is the one the caller cannot correlate any other way.
func SchemaValidator(cfg config.Errors, rules config.Validation, index *validation.SpecIndex) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			envelope, mounted := EnvelopeFromContext(r.Context())
			body, buffered := RawBodyFromContext(r.Context())
			if !mounted || !buffered {
				panic(notMounted)
			}

			faults := validateRequest(rules, index, envelope.Context, body)
			if len(faults) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			httpx.WriteNack(r.Context(), w, cfg, envelope.Context.MessageID, apperrors.Chain(faults...))
		})
	}
}

// validateRequest runs the two passes and reports the faults of the first one
// that found any.
func validateRequest(
	rules config.Validation,
	index *validation.SpecIndex,
	envelope beckn.Context,
	body []byte,
) []*apperrors.AppError {
	if faults := validation.ValidateEnvelope(envelope); len(faults) > 0 {
		return faults
	}
	if !rules.EnableL1Schema {
		return nil
	}

	// Safe by the pass above: the envelope rules require `action`, so by here it
	// is present — an absent one would otherwise be looked up as the empty
	// string and reported as an action mismatch, which names the wrong problem.
	return validation.L1(index, envelope.Action, body)
}
