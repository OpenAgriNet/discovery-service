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

// HeaderResponseTime reports how long this service took to answer, in
// milliseconds. The unit is in the value rather than in the name because the
// name has nowhere to put it, and a bare number is a number the next consumer
// guesses the unit of.
const HeaderResponseTime = "X-Response-Time"

// requestCompleted is the completion line's message. Fixed, and named here, so
// the line is findable by message rather than by position — a rejected request
// logs through WriteNack as well, and which of the two comes first is not
// something a query should have to know.
const requestCompleted = "request completed"

// responseRecorder is the wrapper that stamps the elapsed time and captures the
// status.
//
// Both halves have to happen here rather than in RequestLogger. A header set
// after the handler has written is a header that never reaches the wire, so
// WriteHeader — the last moment the header map is still mutable — is where the
// stamp goes; and the status is only knowable from inside, because a handler
// that answered its own request never tells the middleware what it answered.
type responseRecorder struct {
	http.ResponseWriter

	// When the request reached RequestLogger. Held on the recorder rather than
	// closed over, so the value the header reports and the value the log line
	// reports are measured from one origin.
	started time.Time

	status int
	wrote  bool

	// The request's facts. Allocated by Trace above, or here when RequestLogger
	// is mounted without it, and written by Envelope below. See fact.Record.
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

	// Delegated every time, including a second call. net/http answers a
	// superfluous WriteHeader with a warning of its own, and swallowing the
	// call here would swallow the warning with it — a handler writing its
	// status twice is a bug worth hearing about.
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
// The timer starts before everything below it, authentication included, because
// what a caller experiences is the whole chain and not the part of it that ran
// after they were let in.
//
// The category is read back off the X-Beckn-Error-Type header that
// httpx.WriteNack already set (C1) rather than derived here. Deriving it a
// second time would be a second place that decides what family a fault belongs
// to, and having exactly one is the whole of C1.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 200 up front: a handler that returns without writing has answered
		// 200, and that is the status net/http will send.
		recorder := &responseRecorder{ResponseWriter: w, started: time.Now(), status: http.StatusOK}

		// The one thing that has to travel back up the chain: Envelope, below, is
		// what learns which transaction this request belongs to. Trace above has
		// normally allocated the record already; see recordFor for why both links
		// adopt-or-allocate rather than one of them owning it.
		ctx, record := recordFor(r.Context())
		recorder.record = record

		// Deferred, so a panic unwinding through here does not take the line
		// with it. Recover sits below and answers the ordinary panic, but the
		// committed-response case re-panics with http.ErrAbortHandler by
		// design — and a request that ended by having its connection dropped is
		// not one to leave unaccounted for.
		defer recorder.complete(ctx)

		next.ServeHTTP(recorder, r.WithContext(ctx))
	})
}

// complete stamps the response time where nothing has yet and writes the one
// completion line.
func (w *responseRecorder) complete(ctx context.Context) {
	elapsed := time.Since(w.started)
	if !w.wrote {
		// Nothing has committed the response, so the header still has somewhere
		// to go. This is the case a stamp placed only here would pass on, which
		// is why it is not the only place it happens.
		w.Header().Set(HeaderResponseTime, responseTime(elapsed))
	}

	// What the request cost, observed onto the record rather than appended to the
	// line directly, so the span and 23e's metrics read the same numbers from the
	// same place. The correlators Envelope observed are already on it and were
	// observed first, which is what keeps a line reading as what the request was
	// before what it cost — Record.All yields first-observed order, and the
	// projection preserves it.
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
// number itself comes from logger.Millis, which is also what the log field and
// the fact record carry — three answers to "how long did this take" that a
// caller can reconcile only if they agree, and they can only be relied on to
// agree if they are one computation.
func responseTime(elapsed time.Duration) string {
	return strconv.FormatFloat(logger.Millis(elapsed), 'f', 3, 64) + "ms"
}
