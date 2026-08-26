// Package middlewares holds the chain the protocol routes are wrapped in, in
// the fixed order the plan sets out:
//
//	RequestID → Trace → RequestLogger → Recover → Envelope
//	          → RateLimit → Signature → SchemaValidator → controller
//
// Signature is not in this package. It is parked with the Ed25519 primitives
// it needs and the slot above is where it goes when Phase 2 builds it — not a
// no-op waiting to be filled in, because a middleware that is mounted and does
// nothing is indistinguishable from a working one at every call site that
// matters, and this one is named for a security control. The flag that would
// switch it on refuses the boot instead; see config.validateAuth.
package middlewares

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
)

// RawEnvelope is the shape Envelope parses off the wire: the context every
// route reads, and the message left as the bytes the route's own controller
// decodes. One middleware serves both routes precisely because it does not
// decode the half that differs between them.
type RawEnvelope = httpx.Envelope[json.RawMessage]

// The two values Envelope installs. Unexported key types, so nothing outside
// this package can name them and no other package's value can collide with
// them.
type (
	envelopeKey struct{}
	rawBodyKey  struct{}
)

// Envelope buffers the request body, parses the {context, message} pair off it
// and puts both in the request context, leaving r.Body readable for everything
// below. A body it cannot read at all is a NACK carrying SCH_INVALID_JSON; a
// body over maxBodyBytes is a NACK carrying POL_NP_CAPACITY_EXCEEDED at 413,
// refused before it has been read (C14).
//
// The ceiling lives here and not in Task 20's server because this is the only
// place in the service that reads a request body, and it runs before RateLimit
// — the limiter never sees these bytes, so a bound set anywhere later is a bound
// set after the allocation it was meant to prevent. Buffering an unbounded body
// from an unauthenticated caller is the one thing this middleware could do that
// no downstream task could undo.
//
// The buffering is the point, not an implementation detail: signature
// verification hashes the bytes and schema validation re-parses them, so a body
// consumed once and gone is a failure that surfaces as an empty request two
// middlewares down. r.Body is replaced with a reader over the same bytes rather
// than left drained.
//
// It takes config.Errors because it rejects, and every rejection in this
// service goes out through httpx.WriteNack, which shapes the body from that
// config (C1). A middleware that answered a request without it would be the one
// error body in the service that ignores ERROR_INCLUDE_LEGACY_TYPE.
func Envelope(cfg config.Errors, maxBodyBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, envelope, err := readEnvelope(http.MaxBytesReader(w, r.Body, maxBodyBytes))
			if err != nil {
				// C13: whatever the body held is echoed as sent, and it is
				// lifted here — before anything judges it — because a caller
				// whose messageId this service is in the middle of rejecting
				// has no other handle on which request came back.
				httpx.WriteNack(r.Context(), w, cfg, echoedMessageID(body), err)
				return
			}

			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r.WithContext(stash(r.Context(), body, envelope)))
		})
	}
}

// readEnvelope drains and parses the body, reporting the one fault every way of
// failing earns: each of them means "this is not a readable JSON object", and
// there is no context yet for a CTX_ code to be about.
//
// Over the ceiling is the exception, and it is a different fault rather than a
// louder version of the same one: the body may be perfectly well-formed, and
// telling the caller their JSON is unreadable would send them to inspect a
// document that is fine. The partially-read bytes still come back, so C13's
// salvage has the prefix to work with.
func readEnvelope(source io.Reader) ([]byte, RawEnvelope, error) {
	body, err := io.ReadAll(source)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if stderrors.As(err, &tooLarge) {
			fault := apperrors.Policy(beckn.CodePolicyNPCapacityExceeded,
				fmt.Sprintf("request body exceeds the %d byte limit this deployment accepts", tooLarge.Limit))
			return body, RawEnvelope{}, fmt.Errorf("read request body: %w: %w", fault, err)
		}

		fault := apperrors.Schema(beckn.CodeSchemaInvalidJSON, "request body could not be read")
		return body, RawEnvelope{}, fmt.Errorf("read request body: %w: %w", fault, err)
	}

	envelope, err := httpx.ParseEnvelope[json.RawMessage](body)
	if err != nil {
		// The parser's own text stays in the log and out of the body: it names
		// a byte offset in a document the caller already has, and the code and
		// message are what a consumer branches on.
		fault := apperrors.Schema(beckn.CodeSchemaInvalidJSON, "request body is not a readable Beckn envelope")
		return body, RawEnvelope{}, fmt.Errorf("parse request envelope: %w: %w", fault, err)
	}
	return body, envelope, nil
}

// jsonFrame is one open container in the token walk below.
type jsonFrame struct {
	// An object rather than an array. Only objects alternate key, value.
	object bool

	// The key this container was the value of, in its parent.
	openedBy string

	// The key the next value in this object belongs to.
	key string

	// The next token in this object is a key rather than a value.
	atKey bool
}

// echoedMessageID salvages `$.context.messageId` out of a body already known not
// to be an envelope, for C13's echo. Best-effort by construction: it runs only
// after parsing has failed, so it yields empty rather than failing, and empty is
// what a body too broken to reach the id produces.
//
// It walks tokens rather than decoding a value, and that is the whole point.
// Both json.Unmarshal and Decoder.Decode read a complete value before they yield
// any of it, so both return nothing on a body cut off mid-flight — which is the
// malformed body that actually arrives, and the one where the id is present,
// intact and at the front. Walking stops at the first `$.context.messageId` it
// reaches, so every byte after it is free to be garbage.
//
// The path is matched, not the key: a `messageId` nested deeper, or hanging off
// some other object, is not the caller's correlation handle. A body broken
// enough to need salvaging is a body whose structure is not evidence of
// anything, and echoing a value found under the wrong parent is the same fault
// C13 rules out when it refuses to mint one — an id that looks like an answer
// and correlates to nothing.
func echoedMessageID(body []byte) string {
	decoder := json.NewDecoder(bytes.NewReader(body))

	var stack []jsonFrame
	for {
		token, err := decoder.Token()
		if err != nil {
			// Including io.ErrUnexpectedEOF, which is the truncation this
			// function exists for: whatever the walk had not reached by then is
			// not recoverable, and nothing it did reach matched.
			return ""
		}

		if delim, ok := token.(json.Delim); ok {
			next, done := shift(stack, delim)
			if done {
				return ""
			}
			stack = next
			continue
		}

		if id, done := scalar(stack, token); done {
			return id
		}
	}
}

// shift opens or closes a container. It reports done at the close of the
// top-level document: a second document glued behind the first is a different
// message and its ids are not this caller's, so the walk ends rather than
// reading on.
func shift(stack []jsonFrame, delim json.Delim) ([]jsonFrame, bool) {
	if delim == '}' || delim == ']' {
		if len(stack) == 0 {
			return stack, true
		}
		stack = stack[:len(stack)-1]
		return stack, len(stack) == 0
	}

	opened := jsonFrame{object: delim == '{', atKey: delim == '{'}
	if depth := len(stack); depth > 0 {
		opened.openedBy = stack[depth-1].key
		// The parent has now had its value; whatever follows the close is its
		// next key.
		stack[depth-1].atKey = stack[depth-1].object
	}
	return append(stack, opened), false
}

// scalar records one non-delimiter token against the innermost container, and
// reports the echo when that token is the value at `$.context.messageId` — a
// value keyed messageId, in an object opened under "context", which is itself
// opened at the root. It mutates the frame in place through the shared backing
// array, which is why it takes the stack by value and returns none.
func scalar(stack []jsonFrame, token json.Token) (string, bool) {
	depth := len(stack)
	if depth == 0 {
		// A bare scalar document — `null`, a number, a string. There is no
		// context in it to find.
		return "", true
	}

	top := &stack[depth-1]
	if top.object && top.atKey {
		key, ok := token.(string)
		if !ok {
			return "", true // Unreachable from a valid token stream.
		}
		top.key, top.atKey = key, false
		return "", false
	}
	if top.object {
		top.atKey = true
	}

	if depth != 2 || top.openedBy != "context" || top.key != "messageId" {
		return "", false
	}
	id, ok := token.(string)
	if !ok {
		return "", true // Present, but not a string: there is nothing to echo.
	}
	return id, true
}

func stash(ctx context.Context, body []byte, envelope RawEnvelope) context.Context {
	return context.WithValue(context.WithValue(ctx, envelopeKey{}, envelope), rawBodyKey{}, body)
}

// EnvelopeFromContext returns the envelope Envelope parsed off the request.
//
// The second result is false where Envelope did not run, which is a different
// thing from an envelope whose fields are all empty — a handler mounted outside
// the chain must not read the latter as the former.
func EnvelopeFromContext(ctx context.Context) (RawEnvelope, bool) {
	envelope, ok := ctx.Value(envelopeKey{}).(RawEnvelope)
	return envelope, ok
}

// RawBodyFromContext returns the request body Envelope buffered.
//
// This is the copy to read when the body is needed but must not be consumed —
// a digest over the bytes as they arrived. r.Body is restored for everything
// that would rather just read it.
func RawBodyFromContext(ctx context.Context) ([]byte, bool) {
	body, ok := ctx.Value(rawBodyKey{}).([]byte)
	return body, ok
}
