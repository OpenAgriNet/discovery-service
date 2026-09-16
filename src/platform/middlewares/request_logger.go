package middlewares

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// HeaderResponseTime reports how long this service took to answer. The unit is
// in the VALUE, since the header name has nowhere to put it and a bare number is
// one the next consumer guesses the unit of.
const HeaderResponseTime = "X-Response-Time"

// requestCompleted is the completion line's message, fixed so the line is
// findable by message rather than by position — a rejected request logs through
// WriteNack as well.
const requestCompleted = "request completed"

// responseRecorder stamps the elapsed time and captures the status.
//
// Both halves have to happen HERE and not in RequestLogger: a header set after
// the handler has written never reaches the wire, so WriteHeader is the last
// moment the header map is mutable, and the status is knowable only from inside
// — a handler that answered its own request never says what it answered.
type responseRecorder struct {
	http.ResponseWriter

	// When the request reached RequestLogger. On the recorder rather than closed
	// over, so the header and the log line are measured from one origin.
	started time.Time

	status int
	wrote  bool

	// The request's facts. Allocated by Trace above, or here when RequestLogger
	// is mounted without it, and written by Envelope below.
	record *fact.Record
}

// committed reports whether the response has gone. Recover asks, because a
// panic raised after this point has no second response to write.
func (w *responseRecorder) committed() bool { return w.wrote }

// WriteHeader stamps the response time and records the status, then commits.
func (w *responseRecorder) WriteHeader(status int) {
	if !w.wrote {
		w.wrote = true
		w.status = status
		w.Header().Set(HeaderResponseTime, responseTime(time.Since(w.started)))
	}

	// Delegated every time, including a second call: net/http warns about a
	// superfluous WriteHeader, and swallowing the call would swallow the
	// warning about a real bug with it.
	w.ResponseWriter.WriteHeader(status)
}

// Write commits the response the way net/http would, through WriteHeader, so a
// handler that writes a body without setting a status still gets the header
// stamped.
func (w *responseRecorder) Write(body []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

// RequestLogger times the request, stamps X-Response-Time and writes the one
// completion line carrying the status, the duration and — on a rejection — the
// error category.
//
// The timer starts above everything below it, authentication included: what a
// caller experiences is the whole chain. The category is read back off the
// X-Beckn-Error-Type header WriteNack already set rather than derived a second
// time, which is the whole of C1.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 200 up front: a handler that returns without writing has answered
		// 200, and that is the status net/http will send.
		recorder := &responseRecorder{ResponseWriter: w, started: time.Now(), status: http.StatusOK}

		ctx, record := recordFor(r.Context())
		recorder.record = record

		// Deferred, so a panic unwinding through here does not take the line
		// with it — including Recover's committed-response case, which drops the
		// connection with http.ErrAbortHandler.
		defer recorder.complete(ctx)

		next.ServeHTTP(recorder, r.WithContext(ctx))
	})
}

// complete stamps the response time where nothing has yet and writes the one
// completion line.
func (w *responseRecorder) complete(ctx context.Context) {
	elapsed := time.Since(w.started)
	if !w.wrote {
		// Nothing committed the response, so the header still has somewhere to
		// go. WriteHeader covers the case this one cannot.
		w.Header().Set(HeaderResponseTime, responseTime(elapsed))
	}

	// Onto the record rather than appended to the line directly, so the span,
	// the line and the metrics read one set of numbers. Observed AFTER
	// Envelope's correlators, which is what keeps the line reading as what the
	// request was before what it cost — Record.All yields first-observed order.
	w.record.ObserveInt64(fact.HTTPStatusCode, int64(w.status))
	w.record.ObserveFloat64(fact.DurationMS, logger.Millis(elapsed))
	if category := w.Header().Get(httpx.HeaderErrorType); category != "" {
		// Only when something was rejected. A field that is blank on every
		// successful request is a field nothing can be filtered by.
		w.record.ObserveString(fact.ErrorType, category)
	}

	logger.FromContext(ctx).Info(requestCompleted, logger.Fields(w.record)...)
}

// responseTime renders the elapsed time for the header, with the unit. The
// number comes from logger.Millis, which is what the log field and the record
// carry too: three answers to "how long did this take" agree only by being one
// computation.
func responseTime(elapsed time.Duration) string {
	return strconv.FormatFloat(logger.Millis(elapsed), 'f', 3, 64) + "ms"
}
