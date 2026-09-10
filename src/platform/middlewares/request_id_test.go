package middlewares

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
)

// serveRequestID runs one request through RequestID against log, and reports
// the response and the request the handler below it saw.
func serveRequestID(t *testing.T, log *zap.Logger, inbound string,
	below http.HandlerFunc) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()

	var seen *http.Request
	handler := RequestID(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		if below != nil {
			below(w, r)
		}
	}))

	request := httptest.NewRequest(http.MethodPost, "/publish", nil)
	if inbound != "" {
		request.Header.Set(HeaderRequestID, inbound)
	}
	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded, request)
	return recorded, seen
}

func TestRequestIDMintsAnIDAndEchoesIt(t *testing.T) {
	recorded, seen := serveRequestID(t, zap.NewNop(), "", nil)

	if seen == nil {
		t.Fatal("RequestID did not call the handler below it")
	}
	if recorded.Header().Get(HeaderRequestID) == "" {
		t.Errorf("%s is absent, want the minted id", HeaderRequestID)
	}
}

// Two requests are two ids. One id shared by two requests is two requests
// nothing can tell apart in the log, which is the whole of what the field buys.
func TestEachRequestMintsItsOwnID(t *testing.T) {
	first, _ := serveRequestID(t, zap.NewNop(), "", nil)
	second, _ := serveRequestID(t, zap.NewNop(), "", nil)

	if first.Header().Get(HeaderRequestID) == second.Header().Get(HeaderRequestID) {
		t.Errorf("two requests share the id %q", first.Header().Get(HeaderRequestID))
	}
}

// Phase 1 is unauthenticated, so an inbound id is a value the caller chose:
// honouring it lets them collide two requests' log lines, or put control
// characters in a log field. Propagating a gateway's id needs a trusted-proxy
// list first, and that is a Phase 2 decision.
func TestAnInboundRequestIDIsNeverTrusted(t *testing.T) {
	const chosen = "chosen-by-the-caller\nlevel=fatal"

	recorded, _ := serveRequestID(t, zap.NewNop(), chosen, nil)

	if got := recorded.Header().Get(HeaderRequestID); got == chosen {
		t.Errorf("%s = %q, want a minted id rather than the inbound one", HeaderRequestID, got)
	}
}

// The pin that the middleware installed a logger rather than merely minting an
// id: a request id in a field nothing writes to is not an id anyone can search
// for. Observed through a WriteNack from below, because that is the one log line
// a rejected request produces and it is written by a package that was never
// handed the id.
func TestTheLoggerBelowRequestIDCarriesTheMintedID(t *testing.T) {
	core, logged := observer.New(zapcore.DebugLevel)

	recorded, _ := serveRequestID(t, zap.New(core), "", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteNack(r.Context(), w, config.Errors{}, "", apperrors.Internal())
	})

	entries := logged.All()
	if len(entries) != 1 {
		t.Fatalf("the request produced %d log entries, want exactly one", len(entries))
	}
	minted := recorded.Header().Get(HeaderRequestID)
	if got := entries[0].ContextMap()["request_id"]; got != minted {
		t.Errorf("request_id = %v, want the echoed id %q", got, minted)
	}
}
