package middlewares

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

const validEnvelope = `{"context":{"action":"catalog/publish","messageId":"2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11"},"message":{"catalogs":[]}}`

// serve runs body through Envelope and reports what the response was and what
// the handler below it saw. A nil downstream request means the middleware
// answered the request itself.
func serve(t *testing.T, body string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()

	var seen *http.Request
	handler := Envelope(config.Errors{})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r
	}))

	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded, httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader(body)))
	return recorded, seen
}

func nackOf(t *testing.T, recorded *httptest.ResponseRecorder) beckn.Nack {
	t.Helper()

	var nack beckn.Nack
	if err := json.Unmarshal(recorded.Body.Bytes(), &nack); err != nil {
		t.Fatalf("decode nack %s: %v", recorded.Body.String(), err)
	}
	return nack
}

// The pin the rest of the chain is built on. Signature verification hashes the
// bytes and schema validation re-parses them, so a body consumed once and gone
// is a failure that surfaces two tasks later as an empty request.
func TestTheBodyIsReReadableDownstream(t *testing.T) {
	recorded, seen := serve(t, validEnvelope)

	if seen == nil {
		t.Fatalf("Envelope answered the request itself: %d %s", recorded.Code, recorded.Body)
	}
	reread, err := io.ReadAll(seen.Body)
	if err != nil {
		t.Fatalf("re-read body: %v", err)
	}
	if string(reread) != validEnvelope {
		t.Errorf("downstream read %q, want the original body", reread)
	}
}

// Both stashes, because they answer different questions: the raw body is what a
// digest is computed over without consuming r.Body, and the parsed envelope is
// what routes on `context.action` without parsing it a second time.
func TestTheEnvelopeAndTheRawBodyReachTheContext(t *testing.T) {
	_, seen := serve(t, validEnvelope)
	if seen == nil {
		t.Fatal("Envelope answered a valid request itself")
	}

	envelope, ok := EnvelopeFromContext(seen.Context())
	if !ok {
		t.Fatal("no envelope in the downstream context")
	}
	if envelope.Context.Action != "catalog/publish" {
		t.Errorf("context.action = %q, want catalog/publish", envelope.Context.Action)
	}
	if got := string(envelope.Message); got != `{"catalogs":[]}` {
		t.Errorf("message = %s, want the undecoded message object", got)
	}

	raw, ok := RawBodyFromContext(seen.Context())
	if !ok {
		t.Fatal("no raw body in the downstream context")
	}
	if string(raw) != validEnvelope {
		t.Errorf("raw body = %q, want the original body", raw)
	}
}

// One code for every way the body is not a readable JSON object: there is no
// `context` yet for a CTX_ code to be about.
func TestAMalformedBodyNacksInvalidJSON(t *testing.T) {
	bodies := map[string]string{
		"an empty body":        "",
		"a JSON null":          "null",
		"an array":             `[{"context":{}}]`,
		"trailing content":     validEnvelope + `{"context":{}}`,
		"a non-object context": `{"context":5,"message":{}}`,
		"truncated JSON":       `{"context":{"action":"catalog/publish"`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			recorded, seen := serve(t, body)

			if seen != nil {
				t.Fatal("a malformed body reached the handler below Envelope")
			}
			if recorded.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", recorded.Code)
			}
			if got := nackOf(t, recorded).Message.Error.Code; got != beckn.CodeSchemaInvalidJSON {
				t.Errorf("code = %q, want %q", got, beckn.CodeSchemaInvalidJSON)
			}
		})
	}
}

// C13. The id is lifted out and handed to the writer before anything judges it,
// so the caller with the least other means of working out which request was
// refused gets the handle they sent — a uuid or not. The body here is a
// well-formed envelope with a second document glued to it, which is the case
// where the salvage has something to find.
func TestTheMessageIDIsEchoedBeforeItIsJudged(t *testing.T) {
	recorded, seen := serve(t, `{"context":{"messageId":"not-a-uuid"},"message":{}}{"context":{}}`)

	if seen != nil {
		t.Fatal("a body with trailing content reached the handler below Envelope")
	}
	if got := nackOf(t, recorded).Message.MessageID; got != "not-a-uuid" {
		t.Errorf("messageId = %q, want the value the caller sent echoed verbatim", got)
	}
}

// The other half of C13, and the half that is a rule rather than an omission: a
// body too broken to yield an id comes back empty, never a minted uuid. An id
// the caller never sent looks like an answer and correlates to nothing.
func TestABodyThatYieldsNoMessageIDEchoesEmpty(t *testing.T) {
	for _, body := range []string{"", "null", `{"context":{"action":"catalog/publish"`} {
		recorded, _ := serve(t, body)

		if got := nackOf(t, recorded).Message.MessageID; got != "" {
			t.Errorf("body %q echoed messageId %q, want empty", body, got)
		}
	}
}
