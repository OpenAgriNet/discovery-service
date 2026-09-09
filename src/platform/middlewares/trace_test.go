package middlewares

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

// Trace is a pass-through until Task 23 puts otelhttp inside it, so what is
// pinned here is that it passes the request through *as it arrived* — the same
// request value, not a copy carrying a context of its own — and that the one
// thing it does add is its chain entry.
func TestTracePassesTheRequestThroughUnmodified(t *testing.T) {
	var seen *http.Request
	handler := Trace(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r
	}))

	request := httptest.NewRequest(http.MethodPost, "/publish", nil)
	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded, request)

	if seen != request {
		t.Errorf("the handler saw %p, want the request Trace was given, %p", seen, request)
	}
	if got := recorded.Result().Header; len(got) != 1 {
		t.Errorf("Trace set %v, want only %s", got, HeaderChain)
	}
	if got := recorded.Header().Values(HeaderChain); !reflect.DeepEqual(got, []string{"trace"}) {
		t.Errorf("%s = %v, want [trace]", HeaderChain, got)
	}
}

// The pair is what makes the order testable rather than merely visible.
// Header().Add preserves insertion order, so Values reads back as the order the
// two links actually ran — and because both stamp *before* calling next, a
// single presence marker would survive a recovered panic under either nesting
// and prove nothing about which wrapped which. This is the assertion Task 20
// makes over the assembled chain; it is pinned here because the two Add calls
// that carry it are this task's.
func TestTheChainHeaderReadsBackInTheOrderTheLinksRan(t *testing.T) {
	panics := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("the route blew up")
	})

	arrangements := map[string]struct {
		handler http.Handler
		want    []string
	}{
		"Trace outside Recover": {Trace(Recover(config.Errors{})(panics)), []string{"trace", "recover"}},
		"Recover outside Trace": {Recover(config.Errors{})(Trace(panics)), []string{"recover", "trace"}},
	}

	for name, arrangement := range arrangements {
		t.Run(name, func(t *testing.T) {
			recorded := httptest.NewRecorder()
			arrangement.handler.ServeHTTP(recorded, httptest.NewRequest(http.MethodPost, "/publish", nil))

			if got := recorded.Result().Header.Values(HeaderChain); !reflect.DeepEqual(got, arrangement.want) {
				t.Errorf("%s = %v, want %v", HeaderChain, got, arrangement.want)
			}
		})
	}
}
