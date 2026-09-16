// Package validation holds the two schema layers a request passes through: the
// L1 pass, which validates a body against the published beckn.yaml, and the
// envelope rules, which enforce the handful of context requirements the
// document does not state.
//
// The split is not a style choice: `Context` in v2.0.0 declares no `required`
// list (C6), so an envelope carrying no transactionId satisfies the schema
// exactly. L1 can be switched off by config; the envelope rules cannot, and they
// run first.
package validation

import (
	"time"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
)

// ValidateEnvelope reports every C6 requirement the context fails, one fault
// per field, against the JSONPath the field sits at.
//
// Every rule runs: stopping at the first fault would send a caller with five
// mistakes through five round trips, which is why C7 gives Error.details a cause
// chain to hang the rest off (apperrors.Chain).
//
// Faults come back as values rather than wrapped into an error because
// aggregation is the point — each carries its own Path and Code, and a %w-chain
// of five is a chain a caller has to take apart before it can answer "which
// field". The plan's Global Constraints carve this out by name.
func ValidateEnvelope(envelope beckn.Context) []*apperrors.AppError {
	rules := envelopeRules()

	faults := make([]*apperrors.AppError, 0, len(rules))
	for _, rule := range rules {
		if fault := rule.check(envelope); fault != nil {
			faults = append(faults, fault)
		}
	}
	if len(faults) == 0 {
		// nil rather than an empty non-nil slice: "nothing was wrong" is the
		// answer, and a caller writing `if faults != nil` should get it right.
		return nil
	}
	return faults
}

// envelopeRule is one field's requirement: it has to be there, and if it is it
// has to be readable.
type envelopeRule struct {
	// The field's name under `$.context` — both what the fault's path is built
	// from and what its message names, so a rename cannot report the old name in
	// one and the new one in the other.
	field string

	// read lifts the field off the context. A function per rule rather than
	// reflection over the json tags, because C6 requires a fixed five and a rule
	// deriving itself from the struct would silently start requiring whatever
	// field someone adds next.
	read func(beckn.Context) string

	// wellFormed judges a value that is present. nil where being present is the
	// whole of the requirement — `action` is checked against the served set by
	// the spec index, not here, because only the index knows what is served.
	wellFormed func(string) bool

	// malformed builds the fault a present-but-unusable value earns, and expected
	// is the phrase saying what would have been usable.
	//
	// A constructor rather than a bare code, so every code this package mints is a
	// literal constant at the call site — what the minted-codes pin in
	// src/platform/errors walks for. The six family constructors have identical
	// bodies, so a SCH_ code reported through Context() changes nothing here and
	// everything on the wire, where the status and error_type come from the code.
	malformed func(message string) *apperrors.AppError
	expected  string
}

// The three faults these rules mint, one function per code.
func missingField(message string) *apperrors.AppError {
	return apperrors.Context(beckn.CodeContextMissingField, message)
}

func invalidField(message string) *apperrors.AppError {
	return apperrors.Context(beckn.CodeContextInvalidField, message)
}

func unsupportedVersion(message string) *apperrors.AppError {
	return apperrors.Context(beckn.CodeContextVersionUnsupported, message)
}

// envelopeRules is the C6 required list. A function rather than a package var:
// no package-level mutable state, and a table nothing can reach is a table
// nothing can quietly reorder.
//
// networkId, senderId and receiverId are deliberately absent: networkId is a
// filter rather than an identity claim and defaults when omitted, and the two
// participant DIDs belong to the signature layer this phase parks — requiring a
// field this build cannot verify reads as a check and is not one. C6 and A24 name
// this exact set, so a fourth entry contradicts a written rule.
func envelopeRules() []envelopeRule {
	return []envelopeRule{
		{field: "action", read: func(c beckn.Context) string { return c.Action }},
		{
			field: "version", read: func(c beckn.Context) string { return c.Version },
			wellFormed: func(value string) bool { return value == beckn.Version },
			// Not CTX_INVALID_FIELD: a version this build does not serve is not
			// an unreadable field, it is a readable one saying the caller wants
			// a protocol that is not here — a different thing to go and fix.
			malformed: unsupportedVersion, expected: beckn.Version,
		},
		idRule("messageId", func(c beckn.Context) string { return c.MessageID }),
		idRule("transactionId", func(c beckn.Context) string { return c.TransactionID }),
		{
			field: "timestamp", read: func(c beckn.Context) string { return c.Timestamp },
			wellFormed: isRFC3339,
			malformed:  invalidField, expected: "an RFC 3339 timestamp",
		},
	}
}

// idRule builds the rule the two correlation ids share.
func idRule(field string, read func(beckn.Context) string) envelopeRule {
	return envelopeRule{
		field: field, read: read,
		wellFormed: isCanonicalUUID,
		malformed:  invalidField, expected: "a canonical UUID",
	}
}

// check reports the one fault this field earns, or nil.
//
// Absent and malformed are separate codes because they send the caller to
// separate places: never populated versus populated wrongly. Collapsing them
// makes a typo and an unimplemented field indistinguishable in the only signal a
// consumer branches on.
func (rule envelopeRule) check(envelope beckn.Context) *apperrors.AppError {
	path := "$.context." + rule.field

	value := rule.read(envelope)
	if value == "" {
		return missingField(rule.field + " is required").At(path)
	}
	if rule.wellFormed != nil && !rule.wellFormed(value) {
		return rule.malformed(rule.field + " must be " + rule.expected).At(path)
	}
	return nil
}

// isCanonicalUUID reports whether value is the 8-4-4-4-12 hyphenated hex form,
// and nothing else.
//
// Hand-rolled rather than delegated, because every UUID library in reach is more
// permissive than `format: uuid` — the urn:uuid: prefix, the braced form and the
// 32 unhyphenated digits all name one number in three strings, and a correlation
// handle is compared as a string by every hop that carries it. This service mints
// no UUIDs of its own (request_id uses rand.Text), so the library would be a
// dependency held for this predicate alone.
//
// The version and variant nibbles are not checked (A13): narrowing to v4 would
// refuse a conformant v7 id the protocol admits.
func isCanonicalUUID(value string) bool {
	const canonicalLength = 36
	if len(value) != canonicalLength {
		return false
	}

	for offset, char := range []byte(value) {
		switch offset {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if !isHexDigit(char) {
				return false
			}
		}
	}
	return true
}

func isHexDigit(char byte) bool {
	return (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')
}

// isRFC3339 reports whether value is the `date-time` the schema declares.
//
// time.RFC3339 is that grammar: it requires the date, the T, the time and an
// offset, so a bare date and a space-separated stamp are both refused. Freshness
// and replay windows are measured against this value, and one that parses under
// some other layout is one every such window measures wrong.
func isRFC3339(value string) bool {
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}
