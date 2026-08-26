package publish_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
	"github.com/OpenAgriNet/discovery-service/src/platform/middlewares"
	"github.com/OpenAgriNet/discovery-service/src/publish"
)

// mount builds the route table the way Task 20's router will, behind the real
// Envelope middleware.
//
// The real one, not a stub that stuffs a value into the context: the key is
// unexported, so a controller reading it can only be exercised through the
// middleware that writes it — and a test that faked the plumbing would be
// asserting against its own fake.
func mount(t *testing.T) http.Handler {
	t.Helper()

	repo := newRepo()
	controller := publish.NewController(newService(t, repo, &recordingReplicator{}), config.Errors{})

	mux := http.NewServeMux()
	controller.Register(mux)

	return middlewares.Envelope(config.Errors{}, 1<<20)(mux)
}

func post(t *testing.T, handler http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	return recorder
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder) httpx.Envelope[beckn.CatalogOnPublishAction] {
	t.Helper()

	var envelope httpx.Envelope[beckn.CatalogOnPublishAction]
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding the response: %v\nbody: %s", err, recorder.Body)
	}
	return envelope
}

// C2: one route, and the action lives in the body rather than in the path.
//
// The alias is asserted to 404 rather than merely left unregistered, because a
// second path onto one handler is a second thing to route, rate-limit, log and
// document — and nothing else would notice it reappearing.
func TestPublishIsTheOnlyMountAndTheAliasIsNotRouted(t *testing.T) {
	handler := mount(t)
	body := `{"context":{"action":"publish"},"message":{"catalogs":[{"id":"c1"}]}}`

	if got := post(t, handler, "/publish", body).Code; got != http.StatusOK {
		t.Errorf("POST /publish = %d, want 200", got)
	}
	if got := post(t, handler, "/catalog/publish", body).Code; got != http.StatusNotFound {
		t.Errorf("POST /catalog/publish = %d, want 404 — there is no alias (C2)", got)
	}
}

// 200 even when every catalog was REJECTED.
//
// The request was well-formed; the per-catalog verdicts are the payload, not
// the transport status. A transport-level NACK is reserved for a request that
// could not be read at all.
func TestTheStatusIsOKEvenWhenEveryCatalogIsRejected(t *testing.T) {
	recorder := post(t, mount(t), "/publish", `{"context":{"action":"publish"},"message":{
		"catalogs":[{"id":"c1"},{"id":"c2"}],
		"publishDirectives":[
			{"catalogId":"c1","catalogType":"MASTER"},
			{"catalogId":"c2","catalogType":"MASTER"}
		]}}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body)
	}

	results := decodeResponse(t, recorder).Message.Results
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for _, result := range results {
		if result.Status != beckn.StatusRejected {
			t.Errorf("%s came back %q, want REJECTED", result.CatalogID, result.Status)
		}
	}
}

// C3: the callback shape, returned inline. The response is an envelope, and its
// context is the request's correlation handles with the response action.
func TestTheResponseIsTheCallbackEnvelope(t *testing.T) {
	recorder := post(t, mount(t), "/publish", `{"context":{
		"action":"publish","version":"2.0.0",
		"transactionId":"t-1","messageId":"m-1","networkId":"mahavistar"
	},"message":{"catalogs":[{"id":"c1"}]}}`)

	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	envelope := decodeResponse(t, recorder)
	if envelope.Context.Action != beckn.ActionCatalogOnPublish {
		t.Errorf("action = %q, want catalog/on_publish", envelope.Context.Action)
	}
	if envelope.Context.TransactionID != "t-1" || envelope.Context.MessageID != "m-1" {
		t.Errorf("correlation = %q / %q, want the request's own",
			envelope.Context.TransactionID, envelope.Context.MessageID)
	}
	if envelope.Context.NetworkID != "mahavistar" {
		t.Errorf("networkId = %q, want it echoed", envelope.Context.NetworkID)
	}
	if envelope.Context.Version != beckn.Version {
		t.Errorf("version = %q, want %q", envelope.Context.Version, beckn.Version)
	}
	if len(envelope.Message.Results) != 1 {
		t.Fatalf("results = %+v, want one", envelope.Message.Results)
	}
}

// A `message` this route cannot read is a transport failure, not a verdict.
// There is no catalog to attach a REJECTED to, so a results array would have to
// be empty — which reads as "nothing was sent".
func TestAnUnreadableMessageIsANack(t *testing.T) {
	recorder := post(t, mount(t), "/publish",
		`{"context":{"action":"publish","messageId":"m-1"},"message":["not","an","object"]}`)

	if recorder.Code == http.StatusOK {
		t.Fatalf("status = 200 for a message that could not be decoded; body: %s", recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "m-1") {
		t.Errorf("body = %s, want the caller's messageId echoed back (C13)", recorder.Body)
	}
}
