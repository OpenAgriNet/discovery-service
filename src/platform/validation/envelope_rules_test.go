package validation

import (
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
)

// valid is the envelope every test below breaks one field of. A function
// rather than a package var, because the No globals constraint holds in tests
// too — and because a shared value one test mutates is a failure the next test
// reports.
func valid() beckn.Context {
	return beckn.Context{
		Action:        beckn.ActionCatalogPublish,
		Version:       beckn.Version,
		MessageID:     "2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11",
		TransactionID: "a3f0b1c2-5d4e-4f6a-8b9c-0d1e2f3a4b5c",
		Timestamp:     "2026-08-26T09:00:00Z",
	}
}

// faultAt returns the one fault reported against path, and fails when the pass
// reported none or several — a rule that fires twice for one field is as wrong
// as one that does not fire at all.
func faultAt(t *testing.T, faults []*apperrors.AppError, path string) *apperrors.AppError {
	t.Helper()

	var found []*apperrors.AppError
	for _, fault := range faults {
		if fault.Path == path {
			found = append(found, fault)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d faults at %s, want exactly one; got %v", len(found), path, faults)
	}
	return found[0]
}

func TestAWellFormedEnvelopePassesTheRules(t *testing.T) {
	if faults := ValidateEnvelope(valid()); len(faults) != 0 {
		t.Errorf("a well-formed envelope produced %d faults: %v", len(faults), faults)
	}
}

// The pin C6 exists for. `Context` declares no `required` list, so L1 accepts
// this envelope however strict it is — this pass is the only thing that does
// not, which is why it runs even when L1 is switched off.
func TestAMissingTransactionIDIsRejected(t *testing.T) {
	envelope := valid()
	envelope.TransactionID = ""

	fault := faultAt(t, ValidateEnvelope(envelope), "$.context.transactionId")
	if fault.Code != beckn.CodeContextMissingField {
		t.Errorf("code = %q, want %q", fault.Code, beckn.CodeContextMissingField)
	}
}

// Every required field, each reported against its own path. A pass that
// stopped at the first fault would send a caller with five mistakes through
// five round trips (C7).
func TestEveryRequiredFieldIsNamedByItsOwnPath(t *testing.T) {
	faults := ValidateEnvelope(beckn.Context{})

	for _, path := range []string{
		"$.context.action",
		"$.context.version",
		"$.context.messageId",
		"$.context.transactionId",
		"$.context.timestamp",
	} {
		if fault := faultAt(t, faults, path); fault.Code != beckn.CodeContextMissingField {
			t.Errorf("%s: code = %q, want %q", path, fault.Code, beckn.CodeContextMissingField)
		}
	}
	if len(faults) != 5 {
		t.Errorf("an empty context produced %d faults, want 5", len(faults))
	}
}

// Absent and malformed are different codes because they send the caller to
// different places: absent means their sender never set the field, malformed
// means it set it wrong. One code for both makes a typo and an unimplemented
// field indistinguishable in the only signal a consumer branches on.
func TestAMalformedIDIsAnInvalidFieldRatherThanAMissingOne(t *testing.T) {
	envelope := valid()
	envelope.MessageID = "not-a-uuid"

	fault := faultAt(t, ValidateEnvelope(envelope), "$.context.messageId")
	if fault.Code != beckn.CodeContextInvalidField {
		t.Errorf("code = %q, want %q", fault.Code, beckn.CodeContextInvalidField)
	}
}

// The canonical 8-4-4-4-12 form and nothing else. The schema says
// `format: uuid`, and the other spellings a permissive parser accepts — the
// urn: prefix, braces, the 32 hex digits with no hyphens — are not that
// format. Accepting them would make two senders' ids differ as strings while
// naming one message, which is the whole of what a correlation handle must not
// do.
func TestANonCanonicalUUIDSpellingIsRefused(t *testing.T) {
	for _, spelling := range []string{
		"urn:uuid:2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11",
		"{2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11}",
		"2f6b3f7e4c1a4a5e9d3f6b1f0d2a7c11",
	} {
		envelope := valid()
		envelope.TransactionID = spelling

		fault := faultAt(t, ValidateEnvelope(envelope), "$.context.transactionId")
		if fault.Code != beckn.CodeContextInvalidField {
			t.Errorf("%s: code = %q, want %q", spelling, fault.Code, beckn.CodeContextInvalidField)
		}
	}
}

// version has a code of its own. A wrong value there does not mean this
// receiver could not read the request, it means it does not serve that
// protocol — and the enum has the member that says so.
func TestAnUnservedVersionIsItsOwnCode(t *testing.T) {
	envelope := valid()
	envelope.Version = "1.1.0"

	fault := faultAt(t, ValidateEnvelope(envelope), "$.context.version")
	if fault.Code != beckn.CodeContextVersionUnsupported {
		t.Errorf("code = %q, want %q", fault.Code, beckn.CodeContextVersionUnsupported)
	}
}

func TestATimestampThatIsNotRFC3339IsRejected(t *testing.T) {
	for _, stamp := range []string{"26/08/2026", "2026-08-26", "2026-08-26 09:00:00"} {
		envelope := valid()
		envelope.Timestamp = stamp

		fault := faultAt(t, ValidateEnvelope(envelope), "$.context.timestamp")
		if fault.Code != beckn.CodeContextInvalidField {
			t.Errorf("%s: code = %q, want %q", stamp, fault.Code, beckn.CodeContextInvalidField)
		}
	}
}

// The three C6 leaves optional stay optional. networkId in particular is a
// filter and not an identity claim, and a publish carrying only the five
// required fields is a valid request — it is what publishers send.
//
// Both directions, because "optional" is two claims: absent is accepted, and so
// is present. A rule table that had started rejecting an unrecognised value
// would pass the absent case on its own.
func TestTheOptionalContextFieldsAreNotRequired(t *testing.T) {
	populated := valid()
	populated.NetworkID = "mahavistar"
	populated.SenderID, populated.ReceiverID = "did:example:sender", "did:example:receiver"

	cleared := valid()
	cleared.NetworkID, cleared.SenderID, cleared.ReceiverID = "", "", ""

	for _, testcase := range []struct {
		name     string
		envelope beckn.Context
	}{
		{"naming a network, a sender and a receiver", populated},
		{"naming none of them", cleared},
	} {
		if faults := ValidateEnvelope(testcase.envelope); len(faults) != 0 {
			t.Errorf("an envelope %s produced %d faults: %v", testcase.name, len(faults), faults)
		}
	}
}
