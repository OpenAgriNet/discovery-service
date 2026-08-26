package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"gopkg.in/yaml.v3"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
)

const specFixture = "../../../tests/testdata/beckn-v2.0.0.yaml"

const messageID = "2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11"

// The pin C7 turns on. `Error.details` is additionalProperties:false with
// exactly {path, cause}, so the list of faults a validation pass produces
// cannot be bolted into the body — it has to become a chain. This asserts the
// serialised bytes against the spec's own NackBadRequest node, which reaches
// Error by $ref, so a details carrying anything but path and cause fails here
// rather than at the first consumer strict enough to check.
func TestASerialisedNackValidatesAgainstTheSpec(t *testing.T) {
	fault := apperrors.Chain(
		apperrors.Schema(beckn.CodeSchemaValidationFailed, "resource id is required").
			At("$.message.catalogs[0].resources[0].id"),
		apperrors.Schema(beckn.CodeSchemaInvalidFormat, "not a GeoJSON geometry").
			At("$.message.catalogs[0].resources[1].geometry"),
		apperrors.Context(beckn.CodeContextActionMismatch, "unknown action").At("$.context.action"),
	)

	recorded := writeNack(t, config.Errors{}, fault)

	if recorded.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", recorded.Code)
	}
	if got := recorded.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	validateAgainst(t, "NackBadRequest", recorded.Body.Bytes())
}

// C1. The category is a header and a log field, never a body key, so the
// ordinary NACK is spec-conformant — and the opt-in that puts it back in the
// body is a deliberate violation, which is asserted as one: with the flag on
// the same body no longer validates. A test that only checked the key was
// present would not say what the flag costs.
func TestLegacyTypeIsAViolationTheFlagOptsInTo(t *testing.T) {
	fault := apperrors.Auth(beckn.CodeAuthSignatureMissing, "no signature header")

	conformant := writeNack(t, config.Errors{IncludeLegacyType: false}, fault)
	validateAgainst(t, "NackUnauthorized", conformant.Body.Bytes())
	if got := errorObject(t, conformant.Body.Bytes()); got["type"] != nil {
		t.Errorf("body carries type %v with the flag off", got["type"])
	}

	legacy := writeNack(t, config.Errors{IncludeLegacyType: true}, fault)
	if got := errorObject(t, legacy.Body.Bytes())["type"]; got != apperrors.TypeCore {
		t.Errorf("body type = %v, want %q", got, apperrors.TypeCore)
	}
	if err := validationError(t, "NackUnauthorized", legacy.Body.Bytes()); err == nil {
		t.Error("the legacy body validates; either the spec reopened Error or the validator has no teeth")
	}
}

// The header is on every error response and carries the category the v2.0.0
// body has no room for. 401 rather than 400 because the status comes from the
// code's family, not from the call site.
func TestTheCategoryTravelsAsAHeader(t *testing.T) {
	recorded := writeNack(t, config.Errors{}, apperrors.Auth(beckn.CodeAuthSignatureMissing, "no signature"))

	if got := recorded.Header().Get("X-Beckn-Error-Type"); got != apperrors.TypeCore {
		t.Errorf("X-Beckn-Error-Type = %q, want %q", got, apperrors.TypeCore)
	}
	if recorded.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", recorded.Code)
	}
}

// A4. The one code that puts something on the wire beside the body. Seconds are
// rounded up: a 1.5s back-off reported as 1 invites the caller back before the
// window it was told to wait for has closed.
func TestRateLimitedCarriesItsBackoffHeader(t *testing.T) {
	recorded := writeNack(t, config.Errors{}, apperrors.RateLimited(1500*time.Millisecond, "20 rps"))

	if recorded.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", recorded.Code)
	}
	if got := recorded.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2", got)
	}
	validateAgainst(t, "NackTooManyRequests", recorded.Body.Bytes())
}

// Every other status leaves the header off entirely rather than sending a zero,
// which a caller reads as "retry immediately".
func TestOnlyARateLimitCarriesRetryAfter(t *testing.T) {
	recorded := writeNack(t, config.Errors{}, apperrors.Schema(beckn.CodeSchemaInvalidJSON, "unreadable"))

	if got := recorded.Header().Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q on a 400, want it absent", got)
	}
}

// An error with no Beckn code of its own still comes back as a Beckn body, and
// it says nothing about what failed. The driver's text — a host, a port, a
// query — is the operator's, and it goes to the log line instead.
func TestAnUncodedErrorIsA500ThatLeaksNothing(t *testing.T) {
	recorded := writeNack(t, config.Errors{},
		fmt.Errorf("dial tcp 10.0.0.1:5432: connect: connection refused"))

	if recorded.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", recorded.Code)
	}
	validateAgainst(t, "ServerError", recorded.Body.Bytes())

	body := recorded.Body.String()
	for _, leak := range []string{"10.0.0.1", "5432", "connection refused"} {
		if strings.Contains(body, leak) {
			t.Errorf("body %s leaks %q", body, leak)
		}
	}
}

// The category exists in two places or in neither: the header is what the
// caller branches on and the field is what an operator aggregates over, and a
// category that only ever went out on the wire is one nothing can be counted by.
func TestWriteNackLogsTheCodeAndTheCategoryOnce(t *testing.T) {
	core, recorded := observer.New(zapcore.DebugLevel)
	ctx := loggerContext(zap.New(core))
	fault := apperrors.Network(beckn.CodeNetworkInternalError, "boom")

	WriteNack(ctx, httptest.NewRecorder(), config.Errors{}, messageID, fault)

	entries := recorded.All()
	if len(entries) != 1 {
		t.Fatalf("WriteNack logged %d entries, want exactly one", len(entries))
	}
	fields := entries[0].ContextMap()
	if got := fields["error_type"]; got != apperrors.TypeSystem {
		t.Errorf("error_type = %v, want %q", got, apperrors.TypeSystem)
	}
	if got := fields["error_code"]; got != string(beckn.CodeNetworkInternalError) {
		t.Errorf("error_code = %v, want %q", got, beckn.CodeNetworkInternalError)
	}
}

// A rejected request is not a broken service. A 4xx logged at Error level makes
// every malformed body someone else sends look like an incident here.
func TestAClientFaultLogsBelowError(t *testing.T) {
	core, recorded := observer.New(zapcore.DebugLevel)
	ctx := loggerContext(zap.New(core))

	WriteNack(ctx, httptest.NewRecorder(), config.Errors{}, messageID,
		apperrors.Schema(beckn.CodeSchemaInvalidJSON, "unreadable"))

	if got := recorded.All()[0].Level; got != zapcore.WarnLevel {
		t.Errorf("a 400 logged at %s, want warn", got)
	}
}

// C3: the publish response is the callback shape returned inline, so the 200
// path writes an ordinary envelope through the same function.
func TestWriteJSONWritesTheEnvelope(t *testing.T) {
	recorded := httptest.NewRecorder()
	body := Envelope[beckn.CatalogOnPublishAction]{
		Context: beckn.Context{Action: beckn.ActionCatalogOnPublish, MessageID: messageID},
		Message: beckn.CatalogOnPublishAction{
			Results: []beckn.CatalogProcessingResult{{CatalogID: "cat-1", Status: beckn.StatusAccepted}},
		},
	}

	if err := WriteJSON(context.Background(), recorded, http.StatusOK, body); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if recorded.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", recorded.Code)
	}
	if got := recorded.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var round Envelope[beckn.CatalogOnPublishAction]
	if err := json.Unmarshal(recorded.Body.Bytes(), &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(round.Message.Results) != 1 || round.Message.Results[0].CatalogID != "cat-1" {
		t.Errorf("round-tripped %#v, want one result for cat-1", round.Message.Results)
	}
}

// A body that cannot be encoded is a fault in this service, not in the request,
// and nothing has been written to the wire yet — so the status line is still
// the caller's to set. Returning the error is what lets the handler answer with
// a NACK through the one writer instead of this function inventing a second
// error body of its own.
func TestWriteJSONRefusesAnUnencodableBodyWithoutWriting(t *testing.T) {
	recorded := httptest.NewRecorder()

	err := WriteJSON(context.Background(), recorded, http.StatusOK, map[string]any{"ch": make(chan int)})
	if err == nil {
		t.Fatal("WriteJSON accepted an unencodable body")
	}
	if got := recorded.Body.Len(); got != 0 {
		t.Errorf("wrote %d bytes of a body it could not encode", got)
	}
}

// "The single writer." A NACK assembled anywhere else is a second wire shape to
// keep true, and the two would diverge on the day one of them grew a header.
// The rule is checkable, so it is checked rather than left to a reviewer's
// attention on the day.
func TestNoPackageOutsideHttpxAssemblesANack(t *testing.T) {
	const repoRoot = "../../.."

	var offenders []string
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.HasPrefix(filepath.ToSlash(path), repoRoot+"/src/platform/httpx/") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "beckn.Nack{") || strings.Contains(string(body), "beckn.NackMessage{") {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", repoRoot, err)
	}
	sort.Strings(offenders)
	for _, path := range offenders {
		t.Errorf("%s assembles a Nack; WriteNack is the only place that may", path)
	}
}

func writeNack(t *testing.T, cfg config.Errors, err error) *httptest.ResponseRecorder {
	t.Helper()

	recorded := httptest.NewRecorder()
	WriteNack(loggerContext(zap.NewNop()), recorded, cfg, messageID, err)
	return recorded
}

func loggerContext(log *zap.Logger) context.Context {
	return logger.NewContext(context.Background(), log)
}

func errorObject(t *testing.T, body []byte) map[string]any {
	t.Helper()

	var nack struct {
		Message struct {
			Error map[string]any `json:"error"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &nack); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return nack.Message.Error
}

func validateAgainst(t *testing.T, schemaName string, body []byte) {
	t.Helper()

	if err := validationError(t, schemaName, body); err != nil {
		t.Errorf("%s does not validate against %s: %v", body, schemaName, err)
	}
}

// validationError returns what the walk found, so a test can assert that a body
// fails as well as that one passes. The whole document is loaded, not just the
// named node, because Error reaches itself by $ref and the chain has to be
// followable out of the node under test.
//
// Loaded per call rather than cached in a package var: the No globals
// constraint holds in tests too.
func validationError(t *testing.T, schemaName string, body []byte) error {
	t.Helper()

	blob, err := os.ReadFile(specFixture)
	if err != nil {
		t.Fatalf("read %s: %v", specFixture, err)
	}
	var spec map[string]any
	if parseErr := yaml.Unmarshal(blob, &spec); parseErr != nil {
		t.Fatalf("parse %s: %v", specFixture, parseErr)
	}

	node, err := followRef(spec, "#/components/schemas/"+schemaName)
	if err != nil {
		t.Fatalf("%s: %v", specFixture, err)
	}

	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return validateNode(spec, node, value, "$")
}

// A JSON Schema validator over exactly the keywords the Ack family and Error
// use: $ref, type, required, properties, additionalProperties:false, const and
// enum. Hand-rolled rather than pulled in, because the only full validator in
// this service's plan is kin-openapi and it arrives with Task 9 — adding it
// here to assert on one body would put a dependency on the critical path a task
// early, and the subset is short enough to read.
//
// What it does not check is `format`, and that gap is now load-bearing rather
// than incidental: under C13 WriteNack echoes `messageId` verbatim, so a
// rejected non-uuid — and the empty string, when the envelope yielded nothing —
// goes out against seven variants that declare `format: uuid`. The decision is
// recorded in C13, not here. So "validates against the spec" in this package
// means the structure C7 is about, the closed objects and the chain, and not
// every assertion the document makes. When Task 9 swaps this for kin-openapi
// the `messageId` format has to be excluded deliberately, with C13 named, or
// the conformance test will fail on behaviour the plan asked for.
func validateNode(spec, node map[string]any, value any, where string) error {
	if ref, ok := node["$ref"].(string); ok {
		target, err := followRef(spec, ref)
		if err != nil {
			return err
		}
		return validateNode(spec, target, value, where)
	}

	if want, ok := node["const"]; ok && fmt.Sprint(want) != fmt.Sprint(value) {
		return fmt.Errorf("%s = %v, want the constant %v", where, value, want)
	}
	if members, ok := node["enum"].([]any); ok && !isMember(members, value) {
		return fmt.Errorf("%s = %v, which is not one of %v", where, value, members)
	}

	switch node["type"] {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s = %v, want a string", where, value)
		}
		return nil
	case "object":
		return validateObject(spec, node, value, where)
	default:
		return nil
	}
}

func validateObject(spec, node map[string]any, value any, where string) error {
	object, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s = %v, want an object", where, value)
	}

	properties, declared := node["properties"].(map[string]any)
	if !declared {
		properties = map[string]any{}
	}
	for _, name := range names(node["required"]) {
		if _, present := object[name]; !present {
			return fmt.Errorf("%s is missing the required property %q", where, name)
		}
	}
	if closed, ok := node["additionalProperties"].(bool); ok && !closed {
		for name := range object {
			if _, declared := properties[name]; !declared {
				return fmt.Errorf("%s carries %q, which the schema does not declare", where, name)
			}
		}
	}

	for name, child := range object {
		schema, ok := properties[name].(map[string]any)
		if !ok {
			continue
		}
		if err := validateNode(spec, schema, child, where+"."+name); err != nil {
			return err
		}
	}
	return nil
}

func followRef(spec map[string]any, ref string) (map[string]any, error) {
	node := any(spec)
	for _, step := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		object, ok := node.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: %q is not an object", ref, step)
		}
		if node, ok = object[step]; !ok {
			return nil, fmt.Errorf("%s: no such node", ref)
		}
	}
	target, ok := node.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: not a schema object", ref)
	}
	return target, nil
}

func isMember(members []any, value any) bool {
	for _, member := range members {
		if fmt.Sprint(member) == fmt.Sprint(value) {
			return true
		}
	}
	return false
}

func names(raw any) []string {
	listed, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(listed))
	for _, name := range listed {
		out = append(out, fmt.Sprint(name))
	}
	return out
}

// C13. The Ack family disagrees with itself: seven variants declare
// `messageId` as `format: uuid`, three drop the format and describe it as
// "Echoes the messageId from the triggering request's Context". Echo is the
// reading that holds — by C6 the spec never establishes a uuid was sent — so a
// message id this service is about to reject as malformed still goes back
// exactly as it arrived. That NACK is the one its caller has no other way to
// correlate.
func TestWriteNackEchoesTheMessageIDItWasGiven(t *testing.T) {
	for _, given := range []string{"not-a-uuid", "", "   ", "42"} {
		recorded := httptest.NewRecorder()
		WriteNack(loggerContext(zap.NewNop()), recorded, config.Errors{}, given,
			apperrors.Schema(beckn.CodeSchemaValidationFailed, "messageId is not a uuid"))

		if got := writtenMessageID(t, recorded.Body.Bytes()); got != given {
			t.Errorf("echoed messageId = %q, want %q verbatim", got, given)
		}
	}
}

// The cap C13 puts on the echo. Past it the value is not a correlation handle
// the caller can do anything with, it is a payload they chose our error body to
// carry — so it is dropped rather than truncated. A truncated id is worse than
// none: it still looks like an id, and it correlates to nothing.
func TestWriteNackDropsAnOverlongMessageID(t *testing.T) {
	atCap := strings.Repeat("a", maxEchoedMessageIDBytes)
	overCap := atCap + "a"

	recorded := httptest.NewRecorder()
	WriteNack(loggerContext(zap.NewNop()), recorded, config.Errors{}, atCap,
		apperrors.Schema(beckn.CodeSchemaInvalidFormat, "at the cap"))
	if got := writtenMessageID(t, recorded.Body.Bytes()); got != atCap {
		t.Errorf("a messageId exactly at the cap was not echoed: len %d", len(got))
	}

	recorded = httptest.NewRecorder()
	WriteNack(loggerContext(zap.NewNop()), recorded, config.Errors{}, overCap,
		apperrors.Schema(beckn.CodeSchemaInvalidFormat, "over the cap"))
	if got := writtenMessageID(t, recorded.Body.Bytes()); got != "" {
		t.Errorf("messageId = %q (len %d), want it dropped to empty", got, len(got))
	}
}

func writtenMessageID(t *testing.T, body []byte) string {
	t.Helper()

	var nack struct {
		Message struct {
			MessageID string `json:"messageId"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &nack); err != nil {
		t.Fatalf("decode nack: %v", err)
	}
	return nack.Message.MessageID
}
