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

// The participant DIDs are the one pair that must NOT be echoed unchanged. They
// name the two ends of a single hop, and catalog/on_publish is the hop back:
// the party that received the publish is the party sending the acknowledgement.
//
// The two ids are deliberately distinct strings. Swapped and echoed are the
// same assertion when both ends carry the same value, so a fixture reusing one
// DID would pass against the very bug this pins.
func TestTheParticipantDIDsSwapOnTheWayBack(t *testing.T) {
	recorder := post(t, mount(t), "/publish", `{"context":{
		"action":"publish","version":"2.0.0",
		"transactionId":"t-1","messageId":"m-1",
		"senderId":"did:example:publisher","receiverId":"did:example:discovery"
	},"message":{"catalogs":[{"id":"c1"}]}}`)

	// Before decoding: a Nack body unmarshals into the same envelope with a zero
	// Context, so a regression that turns this into a 400 would otherwise be
	// reported as an empty senderId — blaming the swap for a rejection.
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body)
	}

	envelope := decodeResponse(t, recorder)
	if envelope.Context.SenderID != "did:example:discovery" {
		t.Errorf("senderId = %q, want the request's receiverId — this service sent the acknowledgement",
			envelope.Context.SenderID)
	}
	if envelope.Context.ReceiverID != "did:example:publisher" {
		t.Errorf("receiverId = %q, want the request's senderId — the publisher receives it",
			envelope.Context.ReceiverID)
	}
}

// The four legacy BAP/BPP fields are accepted and dropped. A publisher still
// sending them gets its catalogs stored rather than a rejection — Context
// declares no additionalProperties:false — and gets none of them back on the
// envelope, because this service no longer models an identity it cannot verify.
//
// `bppId` on the CATALOG is a different field and stays: that one is a
// publisher's data, stored verbatim and rendered back (A17). This asserts on
// the context alone for exactly that reason.
func TestTheLegacyParticipantFieldsAreAcceptedAndNotEchoed(t *testing.T) {
	recorder := post(t, mount(t), "/publish", `{"context":{
		"action":"publish","version":"2.0.0",
		"transactionId":"t-1","messageId":"m-1",
		"bapId":"publisher.example.com","bapUri":"https://publisher.example.com/beckn",
		"bppId":"discovery.example.com","bppUri":"https://discovery.example.com/beckn"
	},"message":{"catalogs":[{"id":"c1"}]}}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a legacy field is ignored, not refused; body: %s",
			recorder.Code, recorder.Body)
	}

	envelope := decodeResponse(t, recorder)
	context, err := json.Marshal(envelope.Context)
	if err != nil {
		t.Fatalf("re-encoding the response context: %v", err)
	}
	for _, field := range []string{"bapId", "bapUri", "bppId", "bppUri"} {
		if strings.Contains(string(context), field) {
			t.Errorf("the response context carries %q; it should have been dropped:\n%s", field, context)
		}
	}
}

// A request that reaches the handler without the Envelope middleware ahead of
// it is a wiring fault, not the caller's — nothing about the request is
// trustworthy enough to echo, since the envelope is what carries the
// messageId a NACK would otherwise correlate against.
func TestAMissingEnvelopeMiddlewareIsAWiringFaultNotTheCallers(t *testing.T) {
	controller := publish.NewController(newService(t, newRepo(), &recordingReplicator{}), config.Errors{})

	request := httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader(
		`{"context":{"action":"publish"},"message":{"catalogs":[{"id":"c1"}]}}`))
	recorder := httptest.NewRecorder()
	controller.Publish(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", recorder.Code, recorder.Body)
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
