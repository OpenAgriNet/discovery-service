package middlewares

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
	"github.com/OpenAgriNet/discovery-service/src/platform/validation"
)

// The pinned copy of the protocol, the same one every other conformance test
// reads.
const specFixture = "../../../tests/testdata/beckn-v2.0.0.yaml"

func pinnedIndex(t *testing.T) *validation.SpecIndex {
	t.Helper()

	document, err := os.ReadFile(specFixture)
	if err != nil {
		t.Fatalf("read %s: %v", specFixture, err)
	}
	index, err := validation.NewSpecIndex(document)
	if err != nil {
		t.Fatalf("compile the pinned spec: %v", err)
	}
	return index
}

// discoverRequest is a well-formed discover, with the named context fields
// overridden. An empty override drops the field, which is how the missing-field
// cases are built.
func discoverRequest(overrides map[string]string) string {
	fields := map[string]string{
		"action":        beckn.ActionDiscover,
		"version":       beckn.Version,
		"messageId":     "2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11",
		"transactionId": "a3f0b1c2-5d4e-4f6a-8b9c-0d1e2f3a4b5c",
		"timestamp":     "2026-08-26T09:00:00Z",
	}
	for name, value := range overrides {
		fields[name] = value
	}

	rendered := make([]string, 0, len(fields))
	for _, name := range []string{"action", "version", "messageId", "transactionId", "timestamp"} {
		if fields[name] != "" {
			rendered = append(rendered, `"`+name+`":"`+fields[name]+`"`)
		}
	}
	return `{"context":{` + strings.Join(rendered, ",") + `},"message":{"intent":{"textSearch":"wheat seed"}}}`
}

// validated runs body through Envelope and SchemaValidator, and reports the
// response along with whether the controller below ever ran.
func validated(t *testing.T, rules config.Validation, body string) (*httptest.ResponseRecorder, bool) {
	t.Helper()

	reached := false
	handler := Envelope(config.Errors{}, roomy)(
		SchemaValidator(config.Errors{}, rules, pinnedIndex(t))(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})))

	request := httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader(body))
	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded, request.WithContext(logger.NewContext(request.Context(), zap.NewNop())))
	return recorded, reached
}

func nackBody(t *testing.T, recorded *httptest.ResponseRecorder) beckn.Nack {
	t.Helper()

	var nack beckn.Nack
	if err := json.Unmarshal(recorded.Body.Bytes(), &nack); err != nil {
		t.Fatalf("decode the nack body %q: %v", recorded.Body.String(), err)
	}
	return nack
}

// chainedPaths walks details.cause and lists every path the chain carries, head
// first.
func chainedPaths(fault *beckn.Error) []string {
	var found []string
	for link := fault; link != nil && link.Details != nil; link = link.Details.Cause {
		found = append(found, link.Details.Path)
	}
	return found
}

// l1On and l1Off are the two configurations the whole task turns on: the
// envelope rules run under both, the schema pass under one.
func l1On() config.Validation  { return config.Validation{EnableL1Schema: true} }
func l1Off() config.Validation { return config.Validation{EnableL1Schema: false} }

// The plan's headline pin, and the whole reason C6 exists. `Context` declares
// no `required` list, so this body satisfies the published schema exactly —
// with L1 switched off there is nothing else in the service that could refuse
// it, and a request with no transaction id is a request that cannot be
// correlated to anything for the rest of its life.
func TestAMissingTransactionIDIsRejectedWithL1Off(t *testing.T) {
	recorded, reached := validated(t, l1Off(), discoverRequest(map[string]string{"transactionId": ""}))

	if reached {
		t.Error("the controller ran on a request carrying no transaction id")
	}
	if recorded.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorded.Code)
	}

	fault := nackBody(t, recorded).Message.Error
	if fault.Code != beckn.CodeContextMissingField {
		t.Errorf("code = %q, want %q", fault.Code, beckn.CodeContextMissingField)
	}
	if fault.Details == nil || fault.Details.Path != "$.context.transactionId" {
		t.Errorf("details = %+v, want a path of $.context.transactionId", fault.Details)
	}
}

// The plan's pin: an unknown action NACKs rather than 500s. There is no schema
// indexed under it, and a lookup that returned a zero value would dereference
// nil one line later.
func TestAnUnknownActionNacksRatherThanFailing(t *testing.T) {
	recorded, reached := validated(t, l1On(), discoverRequest(map[string]string{"action": "search"}))

	if reached {
		t.Error("the controller ran on an action this service does not serve")
	}
	if recorded.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — a caller naming an unknown action is not a server failure", recorded.Code)
	}
	if code := nackBody(t, recorded).Message.Error.Code; code != beckn.CodeContextActionMismatch {
		t.Errorf("code = %q, want %q", code, beckn.CodeContextActionMismatch)
	}
}

// C13. The NACK that reports a malformed message id is the one NACK the caller
// cannot correlate any other way, so whatever they sent goes back as sent —
// read out before it is judged, not after.
func TestARejectedMessageIDComesBackEchoedRatherThanBlanked(t *testing.T) {
	recorded, _ := validated(t, l1Off(), discoverRequest(map[string]string{"messageId": "not-a-uuid"}))

	if recorded.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorded.Code)
	}
	if echoed := nackBody(t, recorded).Message.MessageID; echoed != "not-a-uuid" {
		t.Errorf("messageId = %q, want it echoed as %q", echoed, "not-a-uuid")
	}
}

// The other half of C13: a body too broken to yield an id echoes empty. A
// minted uuid would look like an answer and correlate to nothing, which is
// worse than no answer at all.
func TestABodyTooBrokenToParseEchoesNoMessageID(t *testing.T) {
	recorded, reached := validated(t, l1On(), `{"context":`)

	if reached {
		t.Error("the controller ran on a body that never parsed")
	}
	if echoed := nackBody(t, recorded).Message.MessageID; echoed != "" {
		t.Errorf("messageId = %q, want it empty rather than invented", echoed)
	}
}

// C7. `Error.details` is closed to exactly {path, cause}, so several faults are
// a chain rather than an array — and the point of building one is that nothing
// is dropped. A caller with five mistakes learns about five.
func TestAMultiFaultEnvelopeChainsEveryPath(t *testing.T) {
	recorded, _ := validated(t, l1Off(), `{"context":{},"message":{"intent":{}}}`)

	fault := nackBody(t, recorded).Message.Error
	got := chainedPaths(&fault)

	for _, want := range []string{
		"$.context.action",
		"$.context.version",
		"$.context.messageId",
		"$.context.transactionId",
		"$.context.timestamp",
	} {
		if !contains(got, want) {
			t.Errorf("the chain carries %v, missing %s", got, want)
		}
	}
}

// L1 is what refuses this, so switching it off has to actually switch it off —
// otherwise the flag is decoration and the deployment that sets it gets
// rejections it was configured not to get.
func TestASchemaFaultPassesWhenL1IsOff(t *testing.T) {
	body := `{"context":{"action":"discover","version":"2.0.0",` +
		`"messageId":"2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11",` +
		`"transactionId":"a3f0b1c2-5d4e-4f6a-8b9c-0d1e2f3a4b5c",` +
		`"timestamp":"2026-08-26T09:00:00Z"},"message":{"intent":{"textSearch":42}}}`

	if _, reached := validated(t, l1Off(), body); !reached {
		t.Error("a schema fault was raised with L1 switched off")
	}
}

func TestASchemaFaultIsRejectedAtItsPathWhenL1IsOn(t *testing.T) {
	body := `{"context":{"action":"discover","version":"2.0.0",` +
		`"messageId":"2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11",` +
		`"transactionId":"a3f0b1c2-5d4e-4f6a-8b9c-0d1e2f3a4b5c",` +
		`"timestamp":"2026-08-26T09:00:00Z"},"message":{"intent":{"textSearch":42}}}`

	recorded, reached := validated(t, l1On(), body)
	if reached {
		t.Fatal("the controller ran on a message that fails its own schema")
	}

	fault := nackBody(t, recorded).Message.Error
	if fault.Code != beckn.CodeSchemaValidationFailed {
		t.Errorf("code = %q, want %q", fault.Code, beckn.CodeSchemaValidationFailed)
	}
	if got := chainedPaths(&fault); !contains(got, "$.message.intent.textSearch") {
		t.Errorf("the chain carries %v, want $.message.intent.textSearch", got)
	}
}

func TestAWellFormedRequestReachesTheController(t *testing.T) {
	recorded, reached := validated(t, l1On(), discoverRequest(nil))

	if !reached {
		t.Fatalf("a well-formed discover was refused with %d: %s", recorded.Code, recorded.Body)
	}
	if recorded.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", recorded.Code)
	}
}

// A chain assembled without Envelope above SchemaValidator has no envelope
// and no buffered body to check anything against — letting the request past
// would disable L1 and C6 at once and answer 200 while doing it, so this is a
// panic (which Recover, mounted above both in the real chain, turns into a
// logged 500) rather than a silent pass-through.
func TestSchemaValidatorWithoutEnvelopeAbovePanics(t *testing.T) {
	handler := SchemaValidator(config.Errors{}, l1Off(), pinnedIndex(t))(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("the controller ran despite no envelope being mounted")
		}))

	defer func() {
		if recovered := recover(); recovered != notMounted {
			t.Errorf("recovered %v, want the panic to be %q", recovered, notMounted)
		}
	}()

	request := httptest.NewRequest(http.MethodPost, "/publish", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request.WithContext(logger.NewContext(request.Context(), zap.NewNop())))
	t.Fatal("SchemaValidator did not panic with Envelope missing above it")
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
