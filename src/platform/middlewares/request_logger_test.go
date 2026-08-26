package middlewares

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
)

// serveLogged runs below under RequestLogger with an observed logger installed
// above it, the way RequestID installs one in the assembled chain, and reports
// the response and everything the request logged.
func serveLogged(t *testing.T, below http.HandlerFunc) (*httptest.ResponseRecorder, *observer.ObservedLogs) {
	t.Helper()

	core, logged := observer.New(zapcore.DebugLevel)
	handler := RequestLogger(below)

	request := httptest.NewRequest(http.MethodPost, "/publish", nil)
	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded, request.WithContext(logger.NewContext(request.Context(), zap.New(core))))
	return recorded, logged
}

// completionLine returns the one line RequestLogger writes. A rejection also
// logs through WriteNack, so the completion line is found by message rather
// than by position.
func completionLine(t *testing.T, logged *observer.ObservedLogs) observer.LoggedEntry {
	t.Helper()

	found := logged.FilterMessage(requestCompleted).All()
	if len(found) != 1 {
		t.Fatalf("the request produced %d completion lines, want exactly one", len(found))
	}
	return found[0]
}

// The pin the whole wrapper exists for. A header set on the way back out is a
// header that never reaches the wire, and the response snapshot is what proves
// it: httptest captures the header map at WriteHeader, so asserting on
// Result().Header is asserting on what a client would actually have received.
// A handler that writes nothing is the case a late stamp still passes, which is
// why this test's handler writes.
func TestTheResponseTimeReachesAResponseTheHandlerWroteItself(t *testing.T) {
	recorded, _ := serveLogged(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
			t.Errorf("write body: %v", err)
		}
	})

	if got := recorded.Result().Header.Get(HeaderResponseTime); got == "" {
		t.Errorf("%s is absent from the response the handler wrote", HeaderResponseTime)
	}
}

// A handler that writes nothing has not committed the response yet, so the
// header still has somewhere to go. Both cases, because the chain answers
// requests both ways.
func TestTheResponseTimeIsThereWhenTheHandlerWroteNothing(t *testing.T) {
	recorded, _ := serveLogged(t, func(_ http.ResponseWriter, _ *http.Request) {})

	if got := recorded.Result().Header.Get(HeaderResponseTime); got == "" {
		t.Errorf("%s is absent from a response nothing wrote", HeaderResponseTime)
	}
}

// The status the wrapper captured, not the one a wrapper that only ever assumed
// 200 would report.
func TestTheCompletionLineCarriesTheStatusTheHandlerWrote(t *testing.T) {
	_, logged := serveLogged(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	if got := completionLine(t, logged).ContextMap()["status"]; got != int64(http.StatusAccepted) {
		t.Errorf("status = %v, want %d", got, http.StatusAccepted)
	}
}

func TestACompletedRequestIsTimedInTheLogAsWellAsTheHeader(t *testing.T) {
	_, logged := serveLogged(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	duration, ok := completionLine(t, logged).ContextMap()["duration_ms"].(float64)
	if !ok {
		t.Fatalf("duration_ms = %v, want a number of milliseconds",
			completionLine(t, logged).ContextMap()["duration_ms"])
	}
	if duration < 0 {
		t.Errorf("duration_ms = %v, want a non-negative elapsed time", duration)
	}
}

// C1: the category is decided in exactly one place. RequestLogger reads it back
// off the header WriteNack already set rather than deriving it a second time —
// two derivations are two things that can disagree about what a fault is.
func TestTheCompletionLineCarriesTheCategoryTheHeaderNamed(t *testing.T) {
	recorded, logged := serveLogged(t, func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteNack(r.Context(), w, config.Errors{}, "",
			apperrors.Schema(beckn.CodeSchemaInvalidJSON, "unreadable"))
	})

	header := recorded.Result().Header.Get(httpx.HeaderErrorType)
	if header == "" {
		t.Fatal("WriteNack set no error category to read back")
	}
	if got := completionLine(t, logged).ContextMap()["error_type"]; got != header {
		t.Errorf("error_type = %v, want the %s the header named, %q", got, httpx.HeaderErrorType, header)
	}
}

// A field that is blank on every successful request is a field an operator
// cannot filter on. Absent says "nothing was rejected"; empty says nothing.
func TestASuccessfulRequestLogsNoCategory(t *testing.T) {
	_, logged := serveLogged(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	if got, present := completionLine(t, logged).ContextMap()["error_type"]; present {
		t.Errorf("error_type = %v on a request nothing rejected, want the field absent", got)
	}
}

// The request an operator most needs timed is the one that ended in a 500, and
// a panic is how that 500 arrives. RequestLogger sits *above* Recover so the
// answer Recover writes goes through this recorder and the status the line
// reports is the status the caller got — and the line is written from a defer,
// so a panic unwinding through here does not take it with it. Without both,
// every panicked request is missing from the completion log entirely and any
// count of requests by status silently under-counts exactly the failures.
func TestAPanickingRequestIsStillTimedAndLogged(t *testing.T) {
	core, logged := observer.New(zapcore.DebugLevel)
	handler := RequestLogger(Recover(config.Errors{})(
		http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			panic("the route blew up")
		})))

	request := httptest.NewRequest(http.MethodPost, "/publish", nil)
	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded, request.WithContext(logger.NewContext(request.Context(), zap.New(core))))

	result := recorded.Result()
	if result.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", result.StatusCode)
	}
	if got := result.Header.Get(HeaderResponseTime); got == "" {
		t.Errorf("%s is absent from the 500 a panic produced", HeaderResponseTime)
	}

	line := completionLine(t, logged)
	if got := line.ContextMap()["status"]; got != int64(http.StatusInternalServerError) {
		t.Errorf("status = %v, want 500 — the recorder did not see Recover's answer", got)
	}
	if got := line.ContextMap()["error_type"]; got != "SYSTEM" {
		t.Errorf("error_type = %v, want SYSTEM", got)
	}
}

// The completion line is the one line per request that carries the status and
// the latency, so it is the line an operator answers "what happened to
// transaction X" from — and it is written by the middleware that sits *above*
// the one that learns X. Context enrichment only ever flows down, so without a
// channel back up, transaction_id reaches every line about a request except the
// one that says how it ended.
func TestTheCompletionLineCarriesTheCorrelatorsEnvelopeParsed(t *testing.T) {
	const correlating = `{"context":{"action":"catalog/publish","transactionId":"a3f0",` +
		`"messageId":"2f6b"},"message":{"catalogs":[]}}`

	core, logged := observer.New(zapcore.DebugLevel)
	handler := RequestLogger(Envelope(config.Errors{}, roomy)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})))

	request := httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader(correlating))
	handler.ServeHTTP(httptest.NewRecorder(),
		request.WithContext(logger.NewContext(request.Context(), zap.New(core))))

	fields := completionLine(t, logged).ContextMap()
	for key, want := range map[string]string{
		"transaction_id": "a3f0",
		"message_id":     "2f6b",
		"action":         "catalog/publish",
	} {
		if got := fields[key]; got != want {
			t.Errorf("%s = %v, want %q", key, got, want)
		}
	}
}

// A request Envelope refused never had correlators to record, and the
// completion line still has to be written — it is the only record that the
// request happened at all.
func TestTheCompletionLineSurvivesABodyThatNeverParsed(t *testing.T) {
	core, logged := observer.New(zapcore.DebugLevel)
	handler := RequestLogger(Envelope(config.Errors{}, roomy)(
		http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			t.Error("the handler ran below a body Envelope should have refused")
		})))

	request := httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader("not json"))
	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded, request.WithContext(logger.NewContext(request.Context(), zap.New(core))))

	fields := completionLine(t, logged).ContextMap()
	if got := fields["status"]; got != int64(http.StatusBadRequest) {
		t.Errorf("status = %v, want 400", got)
	}
	if got, present := fields["transaction_id"]; present {
		t.Errorf("transaction_id = %v on a body that never parsed, want the field absent", got)
	}
}
