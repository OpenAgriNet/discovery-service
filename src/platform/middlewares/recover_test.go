package middlewares

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
)

// The text the panicking handler carries, so a body that leaked it is a body
// this test can name.
const panicDetail = "dial tcp 10.0.0.7:5432: connection refused"

// serveRecover runs below under Recover with log installed above it, the way
// RequestID installs one in the assembled chain.
func serveRecover(t *testing.T, log *zap.Logger, below http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()

	handler := Recover(config.Errors{})(below)
	request := httptest.NewRequest(http.MethodPost, "/publish", nil)
	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded, request.WithContext(logger.NewContext(request.Context(), log)))
	return recorded
}

// A panic is a 500 and nothing else. A stack trace names this service's
// internal paths and whatever the panicking value happened to be carrying — a
// dialled host and port in the common case — and none of that is the caller's.
func TestAPanicIsA500ThatLeaksNoStack(t *testing.T) {
	recorded := serveRecover(t, zap.NewNop(), func(_ http.ResponseWriter, _ *http.Request) {
		panic(panicDetail)
	})

	if recorded.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", recorded.Code)
	}

	body := recorded.Body.String()
	for _, leak := range []string{panicDetail, "goroutine", "middlewares."} {
		if strings.Contains(body, leak) {
			t.Errorf("body %s carries %q", body, leak)
		}
	}
	if got := nackOf(t, recorded).Message.Error.Code; got != beckn.CodeNetworkInternalError {
		t.Errorf("code = %q, want %q", got, beckn.CodeNetworkInternalError)
	}
}

// The trace is not discarded, it is moved: the operator needs it and the caller
// must not have it. It rides on the error handed to WriteNack, which logs the
// original and puts only the fixed message in the body — so there is one entry
// for the fault and one place that decides what the caller sees.
func TestTheStackReachesTheLog(t *testing.T) {
	core, logged := observer.New(zapcore.DebugLevel)

	serveRecover(t, zap.New(core), func(_ http.ResponseWriter, _ *http.Request) {
		panic(panicDetail)
	})

	entries := logged.All()
	if len(entries) != 1 {
		t.Fatalf("a recovered panic produced %d log entries, want exactly one", len(entries))
	}
	if entries[0].Level != zapcore.ErrorLevel {
		t.Errorf("logged at %s, want error", entries[0].Level)
	}

	recordedError, ok := entries[0].ContextMap()["error"].(string)
	if !ok {
		t.Fatalf("error = %v, want the panic and its stack as a string",
			entries[0].ContextMap()["error"])
	}
	if !strings.Contains(recordedError, panicDetail) {
		t.Errorf("error = %q, want the panic value", recordedError)
	}
	if !strings.Contains(recordedError, "middlewares.") {
		t.Errorf("error = %q, want the stack trace", recordedError)
	}
}

// Every request, not only the ones it catches. The entry is what places Recover
// against Trace in the chain, and a marker that appeared only on a panicking
// route would leave the ordinary route's chain unobservable.
func TestRecoverStampsItsChainEntryOnARequestItDoesNotCatch(t *testing.T) {
	recorded := serveRecover(t, zap.NewNop(), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	if recorded.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", recorded.Code)
	}
	if got := recorded.Result().Header.Values(HeaderChain); !reflect.DeepEqual(got, []string{"recover"}) {
		t.Errorf("%s = %v, want [recover]", HeaderChain, got)
	}
}

// A panicked value must not steer the response. A panic is a bug in this
// service, so it is a 500 whatever it happens to be carrying — and an
// *apperrors.AppError is exactly the value that would slip past a Recover
// wrapping with %w, because errors.As would find the caller's fault inside the
// panic and answer with that status instead. The panic goes in with %v for that
// reason; this test is what keeps it there.
func TestAPanickedFaultIsStillA500(t *testing.T) {
	recorded := serveRecover(t, zap.NewNop(), func(_ http.ResponseWriter, _ *http.Request) {
		panic(apperrors.Schema(beckn.CodeSchemaInvalidJSON, "unreadable"))
	})

	if recorded.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 — the panicked fault chose the status", recorded.Code)
	}
	if got := nackOf(t, recorded).Message.Error.Code; got != beckn.CodeNetworkInternalError {
		t.Errorf("code = %q, want %q", got, beckn.CodeNetworkInternalError)
	}
}

// A panic after the handler has already answered is the one case Recover must
// not answer. The status line has gone and bytes are on the wire, so a second
// WriteNack appends a NACK document to a half-written body and leaves it under
// whatever status was already sent — a 200 carrying two documents, neither of
// them valid. What is true at that point is that the response is incomplete,
// and http.ErrAbortHandler is how net/http is told to drop the connection and
// say so, without printing a stack of its own. A truncated body served as a
// clean 200 is the worse failure: it is the one the caller cannot detect.
func TestAPanicAfterTheResponseIsCommittedAbortsRatherThanWritingTwice(t *testing.T) {
	const answered = `{"message":{"status":"ACK"`

	core, logged := observer.New(zapcore.DebugLevel)
	handler := RequestLogger(Recover(config.Errors{})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte(answered)); err != nil {
				t.Errorf("write body: %v", err)
			}
			panic(panicDetail)
		})))

	request := httptest.NewRequest(http.MethodPost, "/publish", nil)
	recorded := httptest.NewRecorder()

	aborted := serveExpectingAbort(t, handler, recorded, request.WithContext(
		logger.NewContext(request.Context(), zap.New(core))))

	if aborted != http.ErrAbortHandler {
		t.Errorf("panicked with %v, want http.ErrAbortHandler", aborted)
	}
	if got := recorded.Body.String(); got != answered {
		t.Errorf("body = %s, want the half-written response untouched", got)
	}
	if entries := logged.FilterLevelExact(zapcore.ErrorLevel).All(); len(entries) != 1 {
		t.Fatalf("the abort produced %d error lines, want exactly one", len(entries))
	}
}

// serveExpectingAbort runs handler and returns the value it panicked with, or
// nil where it returned normally.
func serveExpectingAbort(t *testing.T, handler http.Handler,
	w http.ResponseWriter, r *http.Request) (panicked any) {
	t.Helper()

	defer func() { panicked = recover() }()
	handler.ServeHTTP(w, r)
	return nil
}
