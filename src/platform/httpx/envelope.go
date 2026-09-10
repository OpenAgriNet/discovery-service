// Package httpx holds the HTTP-shaped plumbing every protocol route shares:
// reading a Beckn envelope off the wire and writing one back. Envelope parsing
// lives here and only here, so the two request paths cannot drift in how they
// read a body.
package httpx

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
)

// Envelope is the {context, message} pair every Beckn request and response
// carries.
//
// T is the action the `message` holds — beckn.CatalogPublishAction on publish,
// beckn.DiscoverAction on discover — which is what lets one parser serve both
// routes without an `any` and a type assertion at each controller.
type Envelope[T any] struct {
	Context beckn.Context `json:"context"`
	Message T             `json:"message"`
}

// ParseEnvelope decodes a request body into an Envelope[T].
//
// It does not reject unknown fields. The spec permits them, and a v2.0.x sender
// with a key this build never heard of must reach the L1 validator — which knows
// which schemas close additionalProperties — rather than be turned away by a
// decoder that knows only this build's structs.
//
// It does reject a body that is not a single JSON object. Everything it rejects
// is a transport-level failure — the request could not be read at all — which is
// the one case the plan reserves a NACK for. A well-formed request whose
// contents are wrong is answered by the validator and the per-catalog verdicts.
func ParseEnvelope[T any](body []byte) (Envelope[T], error) {
	var envelope Envelope[T]

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return envelope, fmt.Errorf("read envelope: body is empty")
	}

	// Every other non-object — an array, a number, a bare string — fails the
	// decode below with a type error. `null` does not: encoding/json treats it as
	// a no-op against a struct, so it is the one unreadable body that would come
	// back as a zero envelope and a nil error, answered as a context fault rather
	// than the transport failure it is.
	if bytes.Equal(trimmed, []byte("null")) {
		return envelope, fmt.Errorf("read envelope: body is JSON null, not an object")
	}

	// A Decoder rather than json.Unmarshal, for More(): Unmarshal silently
	// ignores whatever follows the leading value, so two concatenated documents
	// would parse as the first. Reading half a request nobody meant to send is
	// worse than refusing all of it.
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope[T]{}, fmt.Errorf("read envelope: %w", err)
	}
	if decoder.More() {
		return Envelope[T]{}, fmt.Errorf("read envelope: trailing content after the JSON object")
	}

	return envelope, nil
}
