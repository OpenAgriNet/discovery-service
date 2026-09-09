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
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// HeaderErrorType carries the PRD error category (C1).
//
// v2.0.0 closed `Error` and dropped the `type` key the PRD's five categories
// lived in, so the category travels beside the body instead of inside it. It
// goes out on every error response — a consumer branching on the category must
// not have to know which faults this service happened to categorise.
const HeaderErrorType = "X-Beckn-Error-Type"

// HeaderRetryAfter is the back-off A4 requires beside a 429.
const HeaderRetryAfter = "Retry-After"

const contentTypeJSON = "application/json"

// maxEchoedMessageIDBytes bounds the message id C13 echoes back.
//
// 128 is well past any uuid, which is the point: what it excludes is not a long
// id but a caller who noticed this field comes back and started putting things
// in it. Over the cap the value is dropped rather than truncated — a truncated id
// still looks like an id and correlates to nothing.
const maxEchoedMessageIDBytes = 128

// WriteJSON writes body as the JSON response at status.
//
// It encodes before touching the ResponseWriter, so a body that cannot be
// encoded leaves the status line still the caller's to set — which is why the
// error comes back rather than being answered here. The handler answers it with
// WriteNack, through the one writer, instead of this function inventing a second
// error body beside it.
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
// messageID is the request's `context.messageId`, echoed VERBATIM under C13 —
// including a value this service is in the middle of rejecting as malformed, and
// bounded by maxEchoedMessageIDBytes. An envelope too broken to yield one leaves
// it empty; a minted uuid would look like an answer and correlate to nothing.
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
	observeFault(ctx, fault)

	if retryAfter := fault.RetryAfter; retryAfter > 0 {
		// Whole seconds, rounded up: a 1.5s window reported as 1 invites the
		// caller back before it has closed. Absent rather than "0" everywhere
		// else, because a zero reads as "retry immediately".
		w.Header().Set(HeaderRetryAfter, strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
	}

	logNack(ctx, fault, status, err)

	if len(messageID) > maxEchoedMessageIDBytes {
		messageID = ""
	}

	body := beckn.Nack{Message: beckn.NackMessage{
		Status:    beckn.StatusNack,
		MessageID: messageID,
		Error:     fault.Beckn(cfg),
	}}
	if writeErr := WriteJSON(ctx, w, status, body); writeErr != nil {
		logger.FromContext(ctx).Error("encode nack body", zap.Error(writeErr))
	}
}

// observeFault puts the rejection on the record, as 23d's error event.
//
// It calls fault.Type() rather than deriving the category a second way, because
// 23d requires the event's type and X-Beckn-Error-Type to match byte for byte
// and two calls to one method cannot drift.
//
// Here rather than in a controller because every refusal on every path passes
// through WriteNack, including the ones no controller sees — a body over the size
// ceiling, a rate-limit refusal, a panic caught by Recover.
//
// The COERCED fault's fields, never the original error: a span leaves this
// machine, and a driver's text names a host, a port or a query. The original
// stays in logNack, which does not leave.
//
// Path is omitted when there is none. "" is not the root of anything, and
// writing it would put every fault that names no field at a location that reads
// like one.
func observeFault(ctx context.Context, fault *apperrors.AppError) {
	fact.ObserveString(ctx, fact.ErrorEventType, fault.Type())
	fact.ObserveString(ctx, fact.ErrorCode, string(fault.Code))
	fact.ObserveString(ctx, fact.ErrorMessage, fault.Message)

	if fault.Path != "" {
		fact.ObserveString(ctx, fact.ErrorPath, fault.Path)
	}
}

// logNack writes the operator's copy of the rejection.
//
// The original error goes here and only here: a driver's text names a host, a
// port or a query, none of which is the caller's.
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
