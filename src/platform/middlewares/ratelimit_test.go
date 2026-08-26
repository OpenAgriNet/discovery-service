package middlewares

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
)

// One request a second, two in the bucket. Small enough that the third request
// is refused with no dependence on how fast the test host runs, and slow enough
// that nothing refills underneath the assertions.
var tight = config.RateLimit{RPS: 1, Burst: 2}

// limited mounts RateLimit over a handler that answers 200, and reports what
// each request in turn came back as.
func limited(t *testing.T, cfg config.RateLimit, requests []*http.Request) []*httptest.ResponseRecorder {
	t.Helper()

	handler := RateLimit(cfg, config.Errors{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	responses := make([]*httptest.ResponseRecorder, 0, len(requests))
	for _, request := range requests {
		recorded := httptest.NewRecorder()
		handler.ServeHTTP(recorded, request)
		responses = append(responses, recorded)
	}
	return responses
}

// from builds count requests carrying one remote address.
func from(t *testing.T, remoteAddr string, count int) []*http.Request {
	t.Helper()

	requests := make([]*http.Request, 0, count)
	for range count {
		request := httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader(validEnvelope))
		request.RemoteAddr = remoteAddr
		requests = append(requests, request)
	}
	return requests
}

// A4: burst+1 from one caller is refused, with the back-off and the code the
// amendment names. Retry-After is not optional — a 429 with no interval leaves
// the caller to guess, and every caller guesses the same short one.
func TestBurstPlusOneIsRefusedWithTheBackoff(t *testing.T) {
	responses := limited(t, tight, from(t, "203.0.113.7:41000", tight.Burst+1))

	for i, recorded := range responses[:tight.Burst] {
		if recorded.Code != http.StatusOK {
			t.Errorf("request %d = %d, want 200 inside the burst", i+1, recorded.Code)
		}
	}

	refused := responses[tight.Burst]
	if refused.Code != http.StatusTooManyRequests {
		t.Fatalf("request %d = %d, want 429", tight.Burst+1, refused.Code)
	}
	if got := refused.Header().Get(httpx.HeaderRetryAfter); got == "" {
		t.Errorf("%s is absent from the 429", httpx.HeaderRetryAfter)
	}
	if got := nackOf(t, refused).Message.Error.Code; got != beckn.CodeAuthRateLimited {
		t.Errorf("code = %q, want %q", got, beckn.CodeAuthRateLimited)
	}
}

// One caller exhausting their allowance must not refuse anybody else, which is
// the whole difference between a rate limit and an outage.
func TestTwoAddressesDoNotShareABucket(t *testing.T) {
	requests := from(t, "203.0.113.7:41000", tight.Burst+1)
	requests = append(requests, from(t, "198.51.100.4:41000", 1)...)

	responses := limited(t, tight, requests)

	if got := responses[tight.Burst].Code; got != http.StatusTooManyRequests {
		t.Fatalf("the first caller's burst+1 = %d, want 429", got)
	}
	if got := responses[len(responses)-1].Code; got != http.StatusOK {
		t.Errorf("the second caller = %d, want 200 — the buckets are shared", got)
	}
}

// The key is the address, not the address and port. Every connection brings a
// new ephemeral port, so keying on the pair would hand each request its own
// full bucket and the limiter would refuse nothing, ever.
func TestOnePeerIsOneBucketAcrossPorts(t *testing.T) {
	requests := from(t, "203.0.113.7:41000", tight.Burst)
	requests = append(requests, from(t, "203.0.113.7:52001", 1)...)

	responses := limited(t, tight, requests)

	if got := responses[len(responses)-1].Code; got != http.StatusTooManyRequests {
		t.Errorf("a second port from one peer = %d, want 429 — the port is part of the key", got)
	}
}

// X-Forwarded-For is not read, for the same reason X-Request-Id is not: there is
// no trusted-proxy list that would make it safe. Reading it would let any caller
// mint an unlimited supply of buckets by varying a header they control.
func TestTheForwardedForHeaderCannotMintBuckets(t *testing.T) {
	requests := from(t, "203.0.113.7:41000", tight.Burst+1)
	for i, request := range requests {
		request.Header.Set("X-Forwarded-For", "10.0.0."+string(rune('1'+i)))
	}

	responses := limited(t, tight, requests)

	if got := responses[tight.Burst].Code; got != http.StatusTooManyRequests {
		t.Errorf("burst+1 behind a varying X-Forwarded-For = %d, want 429", got)
	}
}

// RateLimit sits below Envelope, so the id it echoes is the parsed one rather
// than C13's salvage out of a body that would not parse.
func TestTheRefusalEchoesTheParsedMessageID(t *testing.T) {
	handler := Envelope(config.Errors{}, roomy)(RateLimit(tight, config.Errors{})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})))

	var refused *httptest.ResponseRecorder
	for range tight.Burst + 1 {
		request := httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader(validEnvelope))
		request.RemoteAddr = "203.0.113.7:41000"
		refused = httptest.NewRecorder()
		handler.ServeHTTP(refused, request)
	}

	if refused.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", refused.Code)
	}
	const sent = "2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11"
	if got := nackOf(t, refused).Message.MessageID; got != sent {
		t.Errorf("messageId = %q, want the parsed %q", got, sent)
	}
}

// A map keyed on the remote address that only ever grows is a leak an
// unauthenticated caller drives, one bucket per address they can reach us from.
// Past the horizon a bucket has refilled to full and is indistinguishable from a
// bucket that was never created, so dropping it loses nothing.
func TestABucketIdlePastItsHorizonIsEvicted(t *testing.T) {
	at := time.Date(2026, time.August, 26, 9, 0, 0, 0, time.UTC)
	limiter := newLimiter(tight, func() time.Time { return at })

	limiter.allow("203.0.113.7")
	at = at.Add(limiter.horizon + time.Second)
	limiter.allow("198.51.100.4")

	if _, present := limiter.buckets["203.0.113.7"]; present {
		t.Error("a bucket idle past the horizon is still held")
	}
	if len(limiter.buckets) != 1 {
		t.Errorf("the limiter holds %d buckets, want only the live one", len(limiter.buckets))
	}
}

// The other half of eviction: a sweep drops the idle and keeps the live. A sweep
// that took the live ones too would hand every caller their allowance back at
// whatever interval it ran on.
func TestABucketTouchedInsideItsHorizonSurvivesTheSweep(t *testing.T) {
	at := time.Date(2026, time.August, 26, 9, 0, 0, 0, time.UTC)
	limiter := newLimiter(tight, func() time.Time { return at })

	limiter.allow("203.0.113.7")
	at = at.Add(limiter.horizon * 3 / 4)
	limiter.allow("203.0.113.7")

	// Past a horizon since the last sweep, so one runs — but only a
	// three-quarter horizon since this caller was last seen.
	at = at.Add(limiter.horizon / 2)
	limiter.allow("198.51.100.4")

	if _, present := limiter.buckets["203.0.113.7"]; !present {
		t.Error("the sweep dropped a bucket touched inside its horizon")
	}
}

// Spent allowance is remembered. A bucket rebuilt full on the next request is
// not a limit, and it is what an eviction horizon set too short would produce.
func TestSpentAllowanceIsNotHandedBack(t *testing.T) {
	at := time.Date(2026, time.August, 26, 9, 0, 0, 0, time.UTC)
	limiter := newLimiter(tight, func() time.Time { return at })

	for range tight.Burst {
		limiter.allow("203.0.113.7")
	}
	at = at.Add(time.Millisecond)

	allowed, retryAfter := limiter.allow("203.0.113.7")
	if allowed {
		t.Error("a caller who spent their burst got it back a millisecond later")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter = %s, want the interval until a token returns", retryAfter)
	}
}

// One key is one bucket under concurrent callers, and the -race build is what
// makes this a test of the locking rather than of the arithmetic.
func TestConcurrentCallersShareOneBucket(t *testing.T) {
	at := time.Date(2026, time.August, 26, 9, 0, 0, 0, time.UTC)
	limiter := newLimiter(config.RateLimit{RPS: 100, Burst: 200}, func() time.Time { return at })

	var wait sync.WaitGroup
	for range 50 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			limiter.allow("203.0.113.7")
		}()
	}
	wait.Wait()

	if len(limiter.buckets) != 1 {
		t.Errorf("50 concurrent requests from one address made %d buckets", len(limiter.buckets))
	}
}
