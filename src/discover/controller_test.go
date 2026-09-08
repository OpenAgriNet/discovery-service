package discover_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/discover"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
	"github.com/OpenAgriNet/discovery-service/src/platform/middlewares"
)

// mount builds the route table the way Task 20's router will, behind the real
// Envelope middleware.
//
// The real one, not a stub that stuffs a value into the context: the key is
// unexported, so a controller reading it can only be exercised through the
// middleware that writes it — and a test that faked the plumbing would be
// asserting against its own fake.
func mount(t *testing.T, repo domain.SearchRepository, cfg config.Config) http.Handler {
	t.Helper()

	controller := discover.NewController(discover.NewService(repo, cfg), config.Errors{})

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

// postLive sends the request over a real connection.
//
// A ResponseRecorder is not enough for anything about header ORDERING: it
// records Header() whenever it is written, so a header set after the status
// line still shows up there and a test using one would pass against a handler
// that emits it too late to be received. Over a socket, that header is simply
// gone.
func postLive(t *testing.T, handler http.Handler, path, body string) *http.Response {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	response, err := http.Post(server.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("closing the response body: %v", err)
		}
	})

	return response
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder) httpx.Envelope[json.RawMessage] {
	t.Helper()

	var envelope httpx.Envelope[json.RawMessage]
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding the response: %v\nbody: %s", err, recorder.Body)
	}
	return envelope
}

const wheat = `{"context":{"action":"discover"},"message":{"intent":{"textSearch":"wheat"}}}`

// One route, and the action lives in the body rather than in the path — the
// same C2 rule the publish side follows.
func TestDiscoverIsTheOnlyMountAndTheAliasIsNotRouted(t *testing.T) {
	handler := mount(t, &stubRepo{capabilities: everything()}, settings())

	if got := post(t, handler, "/discover", wheat).Code; got != http.StatusOK {
		t.Errorf("POST /discover = %d, want 200", got)
	}
	if got := post(t, handler, "/search", wheat).Code; got != http.StatusNotFound {
		t.Errorf("POST /search = %d, want 404 — there is no alias", got)
	}
}

// C3's twin on the read path: the callback shape, returned inline, with the
// request's correlation handles echoed.
func TestTheResponseIsTheOnDiscoverEnvelope(t *testing.T) {
	handler := mount(t, &stubRepo{
		capabilities: everything(),
		result:       domain.SearchResult{Catalogs: []domain.Catalog{{ID: "c1"}}},
	}, settings())

	recorder := post(t, handler, "/discover", `{"context":{
		"action":"discover","version":"2.0.0",
		"transactionId":"t-1","messageId":"m-1","networkId":"mahavistar"
	},"message":{"intent":{"textSearch":"wheat"}}}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	envelope := decodeResponse(t, recorder)
	if envelope.Context.Action != beckn.ActionOnDiscover {
		t.Errorf("action = %q, want on_discover", envelope.Context.Action)
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

	var message beckn.OnDiscoverAction
	if err := json.Unmarshal(envelope.Message, &message); err != nil {
		t.Fatalf("decoding the message: %v", err)
	}
	if len(message.Catalogs) != 1 || message.Catalogs[0].ID != "c1" {
		t.Errorf("catalogs = %+v, want the one the backend found", message.Catalogs)
	}
}

// The participant DIDs are the one pair that must NOT be echoed unchanged. They
// name the two ends of a single hop, and on_discover is the hop back: the party
// that received the request is the party sending the answer.
//
// The two ids are deliberately distinct strings. Swapped and echoed are the
// same assertion when both ends carry the same value, so a fixture reusing one
// DID would pass against the very bug this pins.
func TestTheParticipantDIDsSwapOnTheWayBack(t *testing.T) {
	handler := mount(t, &stubRepo{
		capabilities: everything(),
		result:       domain.SearchResult{Catalogs: []domain.Catalog{{ID: "c1"}}},
	}, settings())

	recorder := post(t, handler, "/discover", `{"context":{
		"action":"discover","version":"2.0.0",
		"transactionId":"t-1","messageId":"m-1",
		"senderId":"did:example:consumer","receiverId":"did:example:discovery"
	},"message":{"intent":{"textSearch":"wheat"}}}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body)
	}

	envelope := decodeResponse(t, recorder)
	if envelope.Context.SenderID != "did:example:discovery" {
		t.Errorf("senderId = %q, want the request's receiverId — this service sent the answer",
			envelope.Context.SenderID)
	}
	if envelope.Context.ReceiverID != "did:example:consumer" {
		t.Errorf("receiverId = %q, want the request's senderId — the caller receives the answer",
			envelope.Context.ReceiverID)
	}
}

// The four legacy BAP/BPP fields are accepted and dropped. A caller still
// sending them gets a 200 rather than a rejection — Context declares no
// additionalProperties:false — and gets none of them back, because this service
// no longer models an identity it cannot verify.
//
// The assertion is scoped to the CONTEXT, not the whole body, and the stub
// returns a catalog document that would trip a body-wide scan. `bppId` on a
// catalog is a different field: documents are stored and rendered back verbatim
// (A17), so a publisher's own `bppId` reaching the response is correct — it is
// the publisher's data, not this service's echo of an identity claim.
func TestTheLegacyParticipantFieldsAreAcceptedAndNotEchoed(t *testing.T) {
	handler := mount(t, &stubRepo{
		capabilities: everything(),
		result: domain.SearchResult{Catalogs: []domain.Catalog{{
			ID:       "c1",
			Document: []byte(`{"id":"c1","bppId":"weather.example.org"}`),
		}}},
	}, settings())

	recorder := post(t, handler, "/discover", `{"context":{
		"action":"discover","version":"2.0.0",
		"transactionId":"t-1","messageId":"m-1",
		"bapId":"consumer.example.com","bapUri":"https://consumer.example.com/beckn",
		"bppId":"discovery.example.com","bppUri":"https://discovery.example.com/beckn"
	},"message":{"intent":{"textSearch":"wheat"}}}`)

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

// C11, both halves. The degraded list is a HEADER, and the body it travels
// beside carries no `degraded` key — OnDiscoverAction declares
// additionalProperties:false with `catalogs` as its only property, so a body
// field would be a response that fails its own schema.
func TestTheDegradedListIsAHeaderAndNotABodyKey(t *testing.T) {
	handler := mount(t, &stubRepo{
		capabilities: phase1(),
		result:       domain.SearchResult{Catalogs: []domain.Catalog{{ID: "c1"}}},
	}, settings())

	recorder := post(t, handler, "/discover", wheat)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — degrading is not failing; body: %s", recorder.Code, recorder.Body)
	}
	if got := recorder.Header().Get("X-Beckn-Degraded"); got != "semantic" {
		t.Errorf("X-Beckn-Degraded = %q, want semantic", got)
	}

	var message map[string]json.RawMessage
	if err := json.Unmarshal(decodeResponse(t, recorder).Message, &message); err != nil {
		t.Fatalf("decoding the message: %v", err)
	}
	if _, present := message["degraded"]; present {
		t.Errorf("message = %v, want no degraded key — the schema admits only catalogs (C11)", message)
	}
	if _, present := message["catalogs"]; !present {
		t.Errorf("message = %v, want catalogs", message)
	}
}

// The degraded header reaches a real client, which is a stronger claim than
// "the handler called Header().Set": WriteJSON writes the status line, and
// anything added to the header map after that is never sent.
func TestTheDegradedHeaderSurvivesTheWire(t *testing.T) {
	handler := mount(t, &stubRepo{
		capabilities: phase1(),
		result:       domain.SearchResult{Catalogs: []domain.Catalog{{ID: "c1"}}},
	}, settings())

	response := postLive(t, handler, "/discover", wheat)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if got := response.Header.Get("X-Beckn-Degraded"); got != "semantic" {
		t.Errorf("X-Beckn-Degraded = %q, want semantic — a header set after the status line is never sent", got)
	}
}

// Absent, not empty. A header carrying "" says something degraded and declines
// to name it.
func TestTheDegradedHeaderIsAbsentWhenNothingDegraded(t *testing.T) {
	handler := mount(t, &stubRepo{capabilities: everything()}, settings())

	recorder := post(t, handler, "/discover", wheat)
	if _, present := recorder.Header()["X-Beckn-Degraded"]; present {
		t.Errorf("X-Beckn-Degraded present as %q, want the header absent",
			recorder.Header().Get("X-Beckn-Degraded"))
	}
}

// The header joins every degraded mode with a bare comma — checked with two,
// since TestTheDegradedListIsAHeaderAndNotABodyKey's single mode cannot tell a
// Join from a bare concatenation.
func TestTheDegradedHeaderJoinsMultipleModesWithACommaAndNoSpace(t *testing.T) {
	handler := mount(t, &stubRepo{
		capabilities: domain.Capabilities{domain.CapabilityLexical: true, domain.CapabilityFuzzy: true},
		result:       domain.SearchResult{Catalogs: []domain.Catalog{{ID: "c1"}}},
	}, settings())

	recorder := post(t, handler, "/discover", `{"context":{"action":"discover"},
		"message":{"intent":{"textSearch":"wheat","filters":{
			"type":"jsonpath","expression":"$.catalogs[*].resources[*] ? (@.grade == \"A\")"}}}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body)
	}
	if got := recorder.Header().Get("X-Beckn-Degraded"); got != "semantic,jsonpath" {
		t.Errorf("X-Beckn-Degraded = %q, want %q", got, "semantic,jsonpath")
	}
}

// The same request under SEARCH_FAIL_ON_UNAVAILABLE_MODE=true is a 400 naming
// the mode, not a 200 with a header.
func TestAnUnavailableModeIsA400WhenTheDeploymentAsksToBe(t *testing.T) {
	cfg := settings()
	cfg.Search.FailOnUnavailableMode = true

	recorder := post(t, mount(t, &stubRepo{capabilities: phase1()}, cfg), "/discover", wheat)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), string(beckn.CodeNetworkCatalogSourceUnavailable)) {
		t.Errorf("body = %s, want NET_CATALOG_SOURCE_UNAVAILABLE", recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "semantic") {
		t.Errorf("body = %s, want the missing mode named", recorder.Body)
	}
}

// Pagination arrives as query parameters rather than inside the intent, and an
// oversize limit is clamped rather than refused (C11's neighbour: the caller
// still gets the results they asked about).
func TestLimitAndOffsetAreReadFromTheQueryString(t *testing.T) {
	repo := &stubRepo{capabilities: everything()}
	handler := mount(t, repo, settings())

	if got := post(t, handler, "/discover?limit=1000&offset=40", wheat).Code; got != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an oversize limit is clamped, not refused", got)
	}
	if repo.gotQuery.Limit != settings().Search.MaxPageSize {
		t.Errorf("Limit = %d, want it clamped to %d", repo.gotQuery.Limit, settings().Search.MaxPageSize)
	}
	if repo.gotQuery.Offset != 40 {
		t.Errorf("Offset = %d, want 40", repo.gotQuery.Offset)
	}
}

// A limit that is not a number is refused rather than read as zero. Zero means
// "the default page size", so a typo would be answered confidently with a page
// the caller did not ask for.
func TestAPaginationParameterThatIsNotANumberIsRefused(t *testing.T) {
	repo := &stubRepo{capabilities: everything()}

	recorder := post(t, mount(t, repo, settings()), "/discover?limit=twenty", wheat)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", recorder.Code, recorder.Body)
	}
	if repo.calls != 0 {
		t.Errorf("the backend was searched %d times; an unreadable page runs no query", repo.calls)
	}
}

// The same refusal for offset, which pageFrom reads with its own call to
// intParam rather than reusing limit's — a valid limit must not mask an
// unreadable offset.
func TestAnUnreadableOffsetIsRefused(t *testing.T) {
	repo := &stubRepo{capabilities: everything()}

	recorder := post(t, mount(t, repo, settings()), "/discover?limit=10&offset=twenty", wheat)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", recorder.Code, recorder.Body)
	}
	if repo.calls != 0 {
		t.Errorf("the backend was searched %d times; an unreadable page runs no query", repo.calls)
	}
}

// A request that reaches the handler without the Envelope middleware ahead of
// it is a wiring fault, not the caller's — nothing about the request is
// trustworthy enough to echo, since the envelope is what carries the
// messageId a NACK would otherwise correlate against.
func TestAMissingEnvelopeMiddlewareIsAWiringFaultNotTheCallers(t *testing.T) {
	controller := discover.NewController(
		discover.NewService(&stubRepo{capabilities: everything()}, settings()), config.Errors{})

	request := httptest.NewRequest(http.MethodPost, "/discover", strings.NewReader(wheat))
	recorder := httptest.NewRecorder()
	controller.Discover(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", recorder.Code, recorder.Body)
	}
}

// A `message` this route cannot read is a transport failure, and the caller's
// messageId is echoed back so they can tie the refusal to what they sent (C13).
func TestAnUnreadableMessageIsANack(t *testing.T) {
	recorder := post(t, mount(t, &stubRepo{capabilities: everything()}, settings()), "/discover",
		`{"context":{"action":"discover","messageId":"m-1"},"message":["not","an","object"]}`)

	if recorder.Code == http.StatusOK {
		t.Fatalf("status = 200 for a message that could not be decoded; body: %s", recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "m-1") {
		t.Errorf("body = %s, want the caller's messageId echoed back (C13)", recorder.Body)
	}
}

// Scenario 32 at the edge: a refused operator is a 400 naming it, not a wider
// answer.
func TestARefusedOperatorIsA400(t *testing.T) {
	repo := &stubRepo{capabilities: everything()}

	recorder := post(t, mount(t, repo, settings()), "/discover", `{"context":{"action":"discover"},
		"message":{"intent":{"spatial":[{
			"op":"S_CROSSES",
			"targets":"$.catalogs[*].provider.availableAt[*].geo",
			"geometry":{"type":"Point","coordinates":[77.5946,12.9716]}}]}}}`)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), string(beckn.CodeSchemaTypeNotSupported)) {
		t.Errorf("body = %s, want SCH_TYPE_NOT_SUPPORTED", recorder.Body)
	}
	if repo.calls != 0 {
		t.Errorf("the backend was searched %d times; a refused intent runs no query", repo.calls)
	}
}
