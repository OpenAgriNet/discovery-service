package middlewares

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// What Trace passes through, and what it adds.
//
// This test used to assert the handler saw the *same* *http.Request value,
// because a link with nothing to put in the context has no reason to copy one.
// 23b gave it something — the fact record 23c's span reads its attributes off —
// and r.WithContext is a shallow copy by construction, so the pointer cannot
// stay the same and asserting that it does would only pin the allocation out
// again.
//
// The claim that survives is the one the pointer was standing in for: the
// request arrives at the handler as it arrived here, with the context the single
// difference and the record the single thing in it. Asserting the URL, header
// and body are the SAME values rather than merely equal ones is what shows the
// copy was WithContext's shallow one and not a request rebuilt somewhere along
// the way.
func TestTracePassesTheRequestThroughUnmodified(t *testing.T) {
	var seen *http.Request
	handler := Trace(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r
	}))

	request := httptest.NewRequest(http.MethodPost, "/publish", nil)
	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded, request)

	if seen == nil {
		t.Fatal("Trace never called the handler below it")
	}
	sameHeaderMap := reflect.ValueOf(seen.Header).Pointer() == reflect.ValueOf(request.Header).Pointer()
	if seen.Method != request.Method || seen.URL != request.URL ||
		seen.Body != request.Body || !sameHeaderMap {
		t.Errorf("the handler saw %s %v, want the request Trace was given, %s %v",
			seen.Method, seen.URL, request.Method, request.URL)
	}

	if fact.From(seen.Context()) == nil {
		t.Error("no record below Trace; 23c's span would have nothing to read attributes off")
	}
	if fact.From(request.Context()) != nil {
		t.Error("Trace put the record on the request it was given rather than on a copy, " +
			"which leaks it back up to whatever mounted the chain")
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
