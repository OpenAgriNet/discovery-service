package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
)

// HeaderErrorType carries the PRD error category (C1).
//
// v2.0.0 closed `Error` with additionalProperties:false and dropped the `type`
// key the PRD's five categories used to live in, so the category travels beside
// the body instead of inside it. It goes out on every error response — a
// consumer that branches on the category must not have to know which faults
// this service happened to categorise.
const HeaderErrorType = "X-Beckn-Error-Type"

// HeaderRetryAfter is the back-off A4 requires beside a 429.
const HeaderRetryAfter = "Retry-After"

const contentTypeJSON = "application/json"

// WriteJSON writes body as the JSON response at status.
//
// It encodes before it touches the ResponseWriter, so a body that cannot be
// encoded leaves the response untouched and the status line still the caller's
// to set. That is why the error comes back rather than being answered here: the
// handler answers it with WriteNack, through the one writer, instead of this
// function inventing a second error body beside it.
func WriteJSON(ctx context.Context, w http.ResponseWriter, status int, body any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode response body: %w", err)
	}

	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)

	if _, err := w.Write(encoded); err != nil {
		// The status line is already gone and there is no second response to
		// send, so this is the log's to carry and nothing else's. A caller that
		// hung up mid-response is the ordinary cause.
		logger.FromContext(ctx).Warn("write response body", zap.Error(err))
	}
	return nil
}

// WriteNack writes err as the Beckn NACK: the status its code implies, the
// category on the X-Beckn-Error-Type header, the chain of faults in the body,
// and one log line carrying both the category and whatever the error actually
// was.
//
// This is the only place in the service that assembles a rejection body. A
// second one is a second wire shape to keep true, and the two diverge on the
// day one of them grows a header.
//
// messageID is the request's `context.messageId`, which the Ack family echoes
// as the caller's only correlation handle — no member of that family carries a
// `context`. An envelope too broken to yield one leaves it empty rather than
// inventing a uuid the caller never sent.
//
// It reports nothing. WriteNack is the last resort on the request path, so
// there is nothing left to escalate a failure to, and a returned error would
// only be discarded at every call site.
func WriteNack(ctx context.Context, w http.ResponseWriter, cfg config.Errors, messageID string, err error) {
	fault := apperrors.FromError(err)
	if fault == nil {
		fault = apperrors.Internal()
	}
	status := fault.Status()

	w.Header().Set(HeaderErrorType, fault.Type())
	if retryAfter := fault.RetryAfter; retryAfter > 0 {
		// Whole seconds, rounded up: a 1.5s window reported as 1 invites the
		// caller back before it has closed. Absent rather than "0" everywhere
		// else, because a zero reads as "retry immediately".
		w.Header().Set(HeaderRetryAfter, strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
	}

	logNack(ctx, fault, status, err)

	body := beckn.Nack{Message: beckn.NackMessage{
		Status:    beckn.StatusNack,
		MessageID: messageID,
		Error:     fault.Beckn(cfg),
	}}
	if writeErr := WriteJSON(ctx, w, status, body); writeErr != nil {
		logger.FromContext(ctx).Error("encode nack body", zap.Error(writeErr))
	}
}

// logNack writes the operator's copy of the rejection.
//
// The original error goes here and only here: a driver's text names a host, a
// port or a query, and none of that is the caller's — which is why the body
// carries the coerced fault's fixed message instead.
//
// A 4xx logs below Error. A malformed body someone else sent is not an incident
// in this service, and logging it as one is how an error rate stops meaning
// anything.
func logNack(ctx context.Context, fault *apperrors.AppError, status int, original error) {
	log := logger.FromContext(ctx).With(
		logger.ErrorType(fault.Type()),
		logger.ErrorCode(string(fault.Code)),
		zap.Error(original),
	)

	if status >= http.StatusInternalServerError {
		log.Error("rejected request")
		return
	}
	log.Warn("rejected request")
}
