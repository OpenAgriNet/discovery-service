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
// panics with.
//
// A panic rather than a pass-through, because the alternative is a service that
// silently validates nothing: there is no envelope to check and no buffered
// body to check it against, so letting the request past would disable the whole
// of L1 and C6 at once and report 200 while doing it. Recover sits above and
// turns this into a logged 500 — which is what a wiring bug is.
const notMounted = "SchemaValidator is mounted without Envelope above it"

// SchemaValidator refuses a request that does not satisfy the protocol, and
// reports every way in which it does not.
//
// Two passes, and the order matters. The envelope rules run first and run
// unconditionally (C6): the published Context declares no `required` list, so
// L1 cannot refuse a body carrying no transaction id however strict it is, and
// a response context cannot be built without those fields at all. L1 runs
// second and only when configured on, because that flag is the one an operator
// reaches for when a protocol point release outruns the spec they have cached.
//
// Faults from a pass are aggregated and chained through details.cause (C7) so a
// caller with five mistakes learns about five. The two passes are not merged
// into one chain: once the context is unreadable, every schema fault below it
// is a consequence of that rather than a separate thing to fix, and reporting
// both would bury the one the caller has to act on.
//
// The message id is lifted off the envelope before anything judges it (C13) and
// handed to WriteNack as sent — including the value this middleware is in the
// middle of rejecting as malformed, because the NACK reporting a bad message id
// is the one NACK the caller cannot correlate any other way.
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
