package validation

import (
	"errors"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
)

// discoverBody is the smallest request the document accepts: `intent` is
// DiscoverAction's one required property and Intent itself requires nothing.
func discoverBody(message string) string {
	return `{"context":{"action":"discover","version":"2.0.0",` +
		`"messageId":"2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11",` +
		`"transactionId":"a3f0b1c2-5d4e-4f6a-8b9c-0d1e2f3a4b5c",` +
		`"timestamp":"2026-08-26T09:00:00Z"},"message":` + message + `}`
}

// paths lists where the faults landed, for the assertion that says which.
func paths(faults []*apperrors.AppError) []string {
	found := make([]string, 0, len(faults))
	for _, fault := range faults {
		found = append(found, fault.Path)
	}
	return found
}

func hasPath(faults []*apperrors.AppError, path string) bool {
	for _, fault := range faults {
		if fault.Path == path {
			return true
		}
	}
	return false
}

func TestAWellFormedDiscoverBodyValidatesClean(t *testing.T) {
	faults := L1(servingIndex(t), beckn.ActionDiscover, []byte(discoverBody(`{"intent":{"textSearch":"wheat seed"}}`)))

	if len(faults) != 0 {
		t.Errorf("a well-formed discover produced %d faults at %v", len(faults), paths(faults))
	}
}

// C2's promise, kept without editing the published document. The schema
// constrains `context.action` to the const `catalog/publish`, and the const is
// enforced — so a body spelling it `publish`, which the protocol admits and
// this service routes, fails L1 on the very field that identified it. The index
// carries the declared spelling and L1 reconciles the two before validating.
//
// The assertion is against `$.context` rather than `$.context.action` because
// the const sits inside an allOf branch and the visitor reports an allOf
// failure against the composed object, not the leaf that failed. That is the
// path this fault would actually arrive at, so it is the path the test has to
// watch — asserting on the leaf would pass whether or not the reconciliation
// happened.
func TestBothPublishSpellingsClearTheContextSchema(t *testing.T) {
	index := servingIndex(t)

	for _, spelling := range []string{beckn.ActionPublish, beckn.ActionCatalogPublish} {
		body := `{"context":{"action":"` + spelling + `"},"message":{"catalogs":[]}}`

		for _, fault := range L1(index, spelling, []byte(body)) {
			if strings.HasPrefix(fault.Path, "$.context") {
				t.Errorf("%s: the context was rejected at %s (%s); faults at %v",
					spelling, fault.Path, fault.Message, paths(L1(index, spelling, []byte(body))))
			}
		}
	}
}

// The plan's pin: an unknown action NACKs rather than 500s. There is no schema
// to validate against, so the absence is the whole fault — and it is reported
// against the field that carried it, not against the body.
func TestAnUnknownActionIsReportedAsAnActionMismatch(t *testing.T) {
	faults := L1(servingIndex(t), "search", []byte(discoverBody(`{"intent":{}}`)))

	if len(faults) != 1 {
		t.Fatalf("an unknown action produced %d faults at %v, want exactly one", len(faults), paths(faults))
	}
	if faults[0].Code != beckn.CodeContextActionMismatch {
		t.Errorf("code = %q, want %q", faults[0].Code, beckn.CodeContextActionMismatch)
	}
	if faults[0].Path != "$.context.action" {
		t.Errorf("path = %q, want $.context.action", faults[0].Path)
	}
}

// A body that never parses is not a schema fault — there is no document to hold
// against the schema. L1 is reachable in tests and by any later caller without
// Envelope above it, so it answers rather than panicking.
func TestAnUnparseableBodyIsReportedAsInvalidJSON(t *testing.T) {
	faults := L1(servingIndex(t), beckn.ActionDiscover, []byte(`{"context":`))

	if len(faults) != 1 {
		t.Fatalf("an unparseable body produced %d faults at %v, want exactly one", len(faults), paths(faults))
	}
	if faults[0].Code != beckn.CodeSchemaInvalidJSON {
		t.Errorf("code = %q, want %q", faults[0].Code, beckn.CodeSchemaInvalidJSON)
	}
	if faults[0].Path != "$" {
		t.Errorf("path = %q, want $ — nothing narrower is known about a body that did not parse", faults[0].Path)
	}
}

func TestAMissingMessageIsReportedAgainstItsOwnPath(t *testing.T) {
	body := `{"context":{"action":"discover","version":"2.0.0"}}`

	faults := L1(servingIndex(t), beckn.ActionDiscover, []byte(body))
	if !hasPath(faults, "$.message") {
		t.Fatalf("a body carrying no message reported faults at %v, want one at $.message", paths(faults))
	}
	for _, fault := range faults {
		if fault.Code != beckn.CodeSchemaValidationFailed {
			t.Errorf("%s: code = %q, want %q", fault.Path, fault.Code, beckn.CodeSchemaValidationFailed)
		}
	}
}

// The path is what a publisher fixes their payload from, so it has to reach the
// leaf. "the message is invalid" against a hundred-resource catalog is a
// rejection nobody can act on.
func TestAFaultInsideTheMessageIsReportedAtItsFullPath(t *testing.T) {
	faults := L1(servingIndex(t), beckn.ActionDiscover, []byte(discoverBody(`{"intent":{"textSearch":42}}`)))

	if !hasPath(faults, "$.message.intent.textSearch") {
		t.Errorf("faults at %v, want one at $.message.intent.textSearch", paths(faults))
	}
}

// An index, not a key. `$.message.intent.spatial.0.op` is not a JSONPath any
// tool a publisher owns will evaluate, and the path exists to be evaluated.
func TestAFaultInsideAnArrayIsRenderedWithAnIndex(t *testing.T) {
	message := `{"intent":{"spatial":[{"op":"S_WITHIN"},{"op":"NOT_AN_OPERATOR"}]}}`

	faults := L1(servingIndex(t), beckn.ActionDiscover, []byte(discoverBody(message)))
	if !hasPath(faults, "$.message.intent.spatial[1].op") {
		t.Errorf("faults at %v, want one at $.message.intent.spatial[1].op", paths(faults))
	}
}

// C7: no fault is dropped. The details chain exists precisely so a caller with
// three mistakes learns about three, and a validator that stopped at the first
// would make the chain unreachable from the one layer that generates the most
// faults.
func TestEveryFaultKeepsItsOwnPath(t *testing.T) {
	message := `{"intent":{"textSearch":42,"spatial":[{"op":"NOT_AN_OPERATOR"}]}}`

	faults := L1(servingIndex(t), beckn.ActionDiscover, []byte(discoverBody(message)))
	for _, want := range []string{"$.message.intent.textSearch", "$.message.intent.spatial[0].op"} {
		if !hasPath(faults, want) {
			t.Errorf("faults at %v, want one at %s", paths(faults), want)
		}
	}
}

// The canonicalisation happens in a map L1 decoded for itself. The buffered
// request body is what the signature layer hashes and what the audit trail
// records, so a validator that rewrote a field in it would make this service's
// copy of the request differ from the one the caller signed.
func TestL1LeavesTheCallersBodyExactlyAsItArrived(t *testing.T) {
	body := []byte(`{"context":{"action":"publish"},"message":{"catalogs":[]}}`)
	original := string(body)

	L1(servingIndex(t), beckn.ActionPublish, body)

	if string(body) != original {
		t.Errorf("body = %q, want it unchanged at %q", body, original)
	}
}

// Every L1 fault is a schema fault, and the message names the path so a log
// line carrying only the message is still actionable.
func TestASchemaFaultNamesWhatItRejected(t *testing.T) {
	faults := L1(servingIndex(t), beckn.ActionDiscover, []byte(discoverBody(`{"intent":{"textSearch":42}}`)))

	if len(faults) == 0 {
		t.Fatal("a message with a wrongly-typed field validated clean")
	}
	if strings.TrimSpace(faults[0].Message) == "" {
		t.Error("the fault carries no message")
	}
}

// canonicaliseAction is called on whatever json.Unmarshal produced, which for
// a top-level array or scalar body is not a map at all — and for one whose
// `context` isn't an object either. Both must be a no-op rather than a panic;
// L1's own schema check reports the shape fault afterward.
func TestCanonicaliseActionOfANonObjectShapeIsANoop(t *testing.T) {
	canonicaliseAction("not an object", "catalog/publish") // must not panic

	document := map[string]any{"context": "not an object either"}
	canonicaliseAction(document, "catalog/publish")
	if document["context"] != "not an object either" {
		t.Errorf("context = %v, want it untouched", document["context"])
	}
}

// A visitor error that isn't a MultiError — VisitJSON returns one bare error
// for the very first document it can't even start walking — still becomes
// exactly one fault, not zero.
func TestSchemaFaultsOfANonMultiErrorIsOneFault(t *testing.T) {
	faults := schemaFaults(map[string]any{}, errors.New("boom"))
	if len(faults) != 1 {
		t.Fatalf("faults = %d, want 1", len(faults))
	}
}

// faultFrom's own fallback for an error that isn't *openapi3.SchemaError:
// the root path, and the error's own message.
func TestFaultFromOfANonSchemaErrorUsesTheRootPathAndItsMessage(t *testing.T) {
	fault := faultFrom(map[string]any{}, errors.New("boom"))
	if fault.Path != rootPath {
		t.Errorf("Path = %q, want %q", fault.Path, rootPath)
	}
	if !strings.Contains(fault.Message, "boom") {
		t.Errorf("Message = %q, want it to carry the underlying error", fault.Message)
	}
}

// A SchemaError with no Reason — rare, per the code's own comment — still
// names the field the path points at rather than leaving the fault empty.
func TestFaultFromOfAnEmptyReasonFallsBackToAGenericMessage(t *testing.T) {
	fault := faultFrom(map[string]any{}, &openapi3.SchemaError{})
	if fault.Message == "" {
		t.Error("Message is empty, want the fallback reason")
	}
}

// descend against a node that is neither an array nor an object — a schema
// fault reported one level below a leaf, where the walk has nothing left to
// index into. The remaining segment still renders as a member name.
func TestDescendOfANonObjectNodeRendersTheSegmentAnyway(t *testing.T) {
	rendered, next := descend("a leaf value", "field")
	if rendered != ".field" {
		t.Errorf("rendered = %q, want %q", rendered, ".field")
	}
	if next != nil {
		t.Errorf("next = %v, want nil", next)
	}
}

// elementAt's three ways an index can fail to select anything: not a number,
// negative, or past the end.
func TestElementAtOfABadIndexIsNil(t *testing.T) {
	array := []any{"a", "b"}
	for _, segment := range []string{"not-a-number", "-1", "5"} {
		if got := elementAt(array, segment); got != nil {
			t.Errorf("elementAt(array, %q) = %v, want nil", segment, got)
		}
	}
}

// A member name that fails the RFC 9535 shorthand test — `beckn:id`, a real
// key this protocol uses — renders bracket-quoted rather than dotted, which
// is the reason isShorthand exists at all: `$.message.beckn:id` is not a path
// any tool the caller owns would evaluate.
func TestMemberBracketsANameThatIsNotShorthand(t *testing.T) {
	if got := member("beckn:id"); got != `['beckn:id']` {
		t.Errorf("member(%q) = %q, want %q", "beckn:id", got, `['beckn:id']`)
	}
}

// isShorthand's own two refusals: the empty name, and a name whose first
// disqualifying byte is not at position 0 (member's own call always has a
// non-empty name, so this is checked directly against the pure function).
func TestIsShorthandRefusesEmptyAndDisqualifiedNames(t *testing.T) {
	if isShorthand("") {
		t.Error(`isShorthand("") = true, want false`)
	}
	if isShorthand("beckn:id") {
		t.Error(`isShorthand("beckn:id") = true, want false — ":" is not a shorthand byte`)
	}
}
