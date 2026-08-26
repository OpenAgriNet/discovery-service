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

// A ceiling no test body comes near, so a test about parsing is not also a test
// about the size limit.
const roomy = 1 << 20

// serve runs body through Envelope and reports what the response was and what
// the handler below it saw. A nil downstream request means the middleware
// answered the request itself.
func serve(t *testing.T, body string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	return serveReader(t, strings.NewReader(body), roomy)
}

// serveLimited is serve with the body ceiling as the subject rather than the
// backdrop.
func serveLimited(t *testing.T, body string, maxBodyBytes int64) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	return serveReader(t, strings.NewReader(body), maxBodyBytes)
}

func serveReader(t *testing.T, body io.Reader, maxBodyBytes int64) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()

	var seen *http.Request
	handler := Envelope(config.Errors{}, maxBodyBytes)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r
	}))

	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded, httptest.NewRequest(http.MethodPost, "/publish", body))
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

// The shape C13's salvage exists for. A body cut off mid-flight — a caller that
// hung up, a proxy that gave up — is the malformed body that actually arrives in
// production, and the id is at the front of the envelope precisely because
// everything downstream needs it. Reaching it must not depend on the bytes after
// it parsing.
func TestATruncatedBodyStillEchoesTheMessageIDItReached(t *testing.T) {
	bodies := map[string]string{
		"cut off inside message":   `{"context":{"messageId":"2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11","action":"catalog/publish"},"message":{"catal`,
		"cut off just past the id": `{"context":{"action":"catalog/publish","messageId":"2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11"`,
		"cut off after the comma":  `{"context":{"messageId":"2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11",`,
		"a non-uuid, cut off":      `{"context":{"messageId":"2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11"},"messag`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			recorded, seen := serve(t, body)

			if seen != nil {
				t.Fatal("a truncated body reached the handler below Envelope")
			}
			const want = "2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11"
			if got := nackOf(t, recorded).Message.MessageID; got != want {
				t.Errorf("messageId = %q, want %q", got, want)
			}
		})
	}
}

// The salvage reads `$.context.messageId` and nothing that merely looks like it.
// A body broken enough to need salvaging is a body whose structure is not to be
// trusted, so a key found at some other depth is not evidence of anything — and
// echoing it would hand the caller a correlation id they did not send under this
// name, which C13 rules out for the same reason it rules out minting one.
func TestOnlyTheTopLevelContextMessageIDIsEchoed(t *testing.T) {
	bodies := map[string]string{
		"nested inside context":                        `{"context":{"location":{"messageId":"wrong"}},"message"`,
		"under message":                                `{"message":{"messageId":"wrong"},"context":{"action":"x"`,
		"a context key by that name on another object": `{"other":{"context":{"messageId":"wrong"}},"context":{`,
		"not a string":                                 `{"context":{"messageId":42},"message":{`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			recorded, _ := serve(t, body)

			if got := nackOf(t, recorded).Message.MessageID; got != "" {
				t.Errorf("echoed messageId %q, want empty", got)
			}
		})
	}
}

// countingReader reports how much of itself was actually consumed.
type countingReader struct {
	remaining int
	read      int
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.remaining)
	for i := range p[:n] {
		p[i] = 'x'
	}
	r.remaining -= n
	r.read += n
	return n, nil
}

// The ceiling is a boundary, so it is tested as one. Envelope is the first thing
// in the chain that reads the body — it runs before RateLimit, so the limiter
// never sees the bytes — and the endpoint is unauthenticated in Phase 1. Without
// a ceiling here there is none anywhere.
func TestTheBodyCeilingIsExactlyWhereItSays(t *testing.T) {
	size := int64(len(validEnvelope))

	if _, seen := serveLimited(t, validEnvelope, size); seen == nil {
		t.Error("a body exactly at the ceiling was refused")
	}

	recorded, seen := serveLimited(t, validEnvelope, size-1)
	if seen != nil {
		t.Fatal("a body over the ceiling reached the handler below Envelope")
	}
	if recorded.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", recorded.Code)
	}
	if got := nackOf(t, recorded).Message.Error.Code; got != beckn.CodePolicyNPCapacityExceeded {
		t.Errorf("code = %q, want %q", got, beckn.CodePolicyNPCapacityExceeded)
	}
}

// The pin that makes the ceiling worth having: the refusal must cost the
// service the ceiling, not the body. A limit enforced after buffering is a
// limit that has already paid for what it is refusing.
func TestAnOversizedBodyIsNeverFullyBuffered(t *testing.T) {
	source := &countingReader{remaining: 8 << 20}

	recorded, seen := serveReader(t, source, 1024)
	if seen != nil {
		t.Fatal("an oversized body reached the handler below Envelope")
	}
	if recorded.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", recorded.Code)
	}
	if source.read > 64<<10 {
		t.Errorf("read %d bytes of an 8 MiB body against a 1 KiB ceiling, want the read bounded near the ceiling", source.read)
	}
}

// C13 still holds at the ceiling. The prefix that was read is the prefix the
// caller sent, so an id at the front of it is as good a correlation handle as
// any — and a caller whose request was refused unread is exactly the caller with
// no other way to tell which one it was.
func TestAnOversizedBodyStillEchoesAMessageIDFromItsPrefix(t *testing.T) {
	const id = "2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11"
	body := `{"context":{"messageId":"` + id + `"},"message":{"pad":"` + strings.Repeat("x", 4096) + `"}}`

	recorded, _ := serveLimited(t, body, 512)
	if got := nackOf(t, recorded).Message.MessageID; got != id {
		t.Errorf("messageId = %q, want %q", got, id)
	}
}
