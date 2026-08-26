package middlewares

import (
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
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
}

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

		next.ServeHTTP(recorder, r)

		elapsed := time.Since(recorder.started)
		if !recorder.wrote {
			// Nothing has committed the response, so the header still has
			// somewhere to go. This is the case a stamp placed only here would
			// pass on, which is why it is not the only place it happens.
			w.Header().Set(HeaderResponseTime, responseTime(elapsed))
		}

		fields := []zap.Field{logger.Status(recorder.status), logger.DurationMS(elapsed)}
		if category := w.Header().Get(httpx.HeaderErrorType); category != "" {
			// Only when something was rejected. A field that is blank on every
			// successful request is a field nothing can be filtered by.
			fields = append(fields, logger.ErrorType(category))
		}
		logger.FromContext(r.Context()).Info(requestCompleted, fields...)
	})
}

// responseTime renders the elapsed time for the header: milliseconds to
// microsecond precision, with the unit. Integer milliseconds would report every
// request this service is built to serve — the 20 ms budget — as one of twenty
// indistinguishable values, and a sub-millisecond one as zero.
func responseTime(elapsed time.Duration) string {
	return strconv.FormatFloat(float64(elapsed.Microseconds())/1000, 'f', 3, 64) + "ms"
}
