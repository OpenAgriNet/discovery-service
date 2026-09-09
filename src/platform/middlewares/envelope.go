// Package middlewares holds the chain the protocol routes are wrapped in, in
// the fixed order the plan sets out:
//
//	RequestID → Trace → RequestLogger → Recover → Envelope
//	          → RateLimit → Signature → SchemaValidator → controller
//
// Signature is not in this package and is not a mounted no-op: it is parked for
// Phase 2, and the flag that would switch it on refuses the boot instead — see
// config.validateAuth.
package middlewares

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"

	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// RawEnvelope is the shape Envelope parses off the wire: the context every
// route reads, and the message left as raw bytes for the route's own controller.
// One middleware serves both routes because it does not decode the half that
// differs between them.
type RawEnvelope = httpx.Envelope[json.RawMessage]

// The two values Envelope installs. Unexported key types, so no other package
// can name them or collide with them.
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
// The ceiling belongs HERE and nowhere further down: this is the only place in
// the service that reads a request body, and it runs before RateLimit, so a
// bound set later is one set after the allocation it exists to prevent.
//
// The buffering is likewise load-bearing rather than incidental — signature
// verification hashes the bytes and schema validation re-parses them — so r.Body
// is replaced with a reader over the same bytes rather than left drained.
func Envelope(cfg config.Errors, maxBodyBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, envelope, err := readEnvelope(http.MaxBytesReader(w, r.Body, maxBodyBytes))
			if err != nil {
				// C13: the id is echoed as sent and lifted before anything
				// judges it.
				httpx.WriteNack(r.Context(), w, cfg, echoedMessageID(body), err)
				return
			}

			r.Body = io.NopCloser(bytes.NewReader(body))
			ctx := correlate(stash(r.Context(), body, envelope), envelope.Context)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// readEnvelope drains and parses the body. Every way of failing means "this is
// not a readable JSON object" and earns the one fault; over the ceiling is the
// exception, because such a body may be perfectly well-formed. The
// partially-read bytes still come back, so C13's salvage has the prefix.
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
		// The parser's own text stays in the log and out of the body: it names a
		// byte offset in a document the caller already has.
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
// to be an envelope, for C13's echo. It yields empty rather than failing.
//
// It walks TOKENS rather than decoding a value, which is the whole point: both
// json.Unmarshal and Decoder.Decode read a complete value before yielding any of
// it, so both return nothing on a body cut off mid-flight — the malformed body
// that actually arrives, and the one where the id is intact and at the front.
//
// The PATH is matched, not the key. A messageId under some other parent is not
// the caller's correlation handle, and echoing it is the fault C13 rules out
// when it refuses to mint one: an id that looks like an answer and correlates to
// nothing.
func echoedMessageID(body []byte) string {
	decoder := json.NewDecoder(bytes.NewReader(body))

	var stack []jsonFrame
	for {
		token, err := decoder.Token()
		if err != nil {
			// Including io.ErrUnexpectedEOF — the truncation this function
			// exists for. Nothing the walk reached matched.
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

// shift opens or closes a container, reporting done at the close of the
// top-level document: a second document glued behind the first is a different
// message, and its ids are not this caller's.
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

// scalar records one non-delimiter token against the innermost container and
// reports the echo when that token is the value at `$.context.messageId`. It
// mutates the frame in place through the shared backing array, which is why it
// takes the stack by value and returns none.
func scalar(stack []jsonFrame, token json.Token) (string, bool) {
	depth := len(stack)
	if depth == 0 {
		// A bare scalar document — `null`, a number, a string.
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

// correlate adds the correlators the envelope carries to the request-scoped
// logger, so everything below this point joins up without threading them
// through its own signature. This is the first point in the chain at which they
// are known.
//
// A correlator the envelope did not carry is LEFT OFF rather than logged empty:
// transactionId is optional, and a field blank on every request that omits it is
// a field nothing can filter by.
func correlate(ctx context.Context, envelope beckn.Context) context.Context {
	fields := make([]zap.Field, 0, 3)

	for _, correlator := range correlators(envelope) {
		if correlator.value == "" {
			continue
		}

		// Down to everything below, as fields on the request-scoped logger.
		if correlator.field != nil {
			fields = append(fields, correlator.field(correlator.value))
		}

		// And back UP: to RequestLogger's completion line and to Trace's span,
		// both written by middlewares that ran before any of this was known.
		fact.ObserveString(ctx, correlator.key, correlator.value)
	}

	return logger.With(ctx, fields...)
}

// correlator is one envelope field under both of its spellings.
type correlator struct {
	value string
	key   fact.Key
	field func(string) zap.Field
}

// correlators is the table, and it is ONE table rather than two. Three of these
// reach both the log and the span, four only the span, and a nil field
// constructor is what says so — two loops would be two places to add a
// correlator, one of which gets forgotten.
//
// The action is normalised here: `catalog/publish` and `publish` are one action
// under two names (C2), and observing both would split every publish query in
// two. It changes the log field as well as the span, which is the point.
func correlators(envelope beckn.Context) []correlator {
	return []correlator{
		{envelope.TransactionID, fact.BecknTransactionID, logger.TransactionID},
		{envelope.MessageID, fact.BecknMessageID, logger.MessageID},
		{beckn.NormalizeAction(envelope.Action), fact.BecknAction, logger.Action},

		// Span-only: on the completion line these would be four fields
		// identical on every request from a given caller.
		//
		// receiverId is who the CALLER addressed. It is not recipient.id, which
		// Trace observes from APP_SUBSCRIBER_ID — the two agree whenever a
		// request is correctly addressed, so one field holding both would be
		// undetectably wrong (opentelemetry.md:524).
		{envelope.SenderID, fact.SenderID, nil},
		{envelope.Version, fact.BecknVersion, nil},
		{envelope.NetworkID, fact.BecknNetworkID, nil},
		{envelope.ReceiverID, fact.BecknReceiverID, nil},
	}
}

func stash(ctx context.Context, body []byte, envelope RawEnvelope) context.Context {
	return context.WithValue(context.WithValue(ctx, envelopeKey{}, envelope), rawBodyKey{}, body)
}

// EnvelopeFromContext returns the envelope Envelope parsed off the request. The
// second result is false where Envelope did not RUN, which is not the same as an
// envelope whose fields are all empty.
func EnvelopeFromContext(ctx context.Context) (RawEnvelope, bool) {
	envelope, ok := ctx.Value(envelopeKey{}).(RawEnvelope)
	return envelope, ok
}

// RawBodyFromContext returns the request body Envelope buffered — the copy to
// read when the bytes are needed exactly as they arrived and must not be
// consumed. r.Body is restored for everything that would rather just read it.
func RawBodyFromContext(ctx context.Context) ([]byte, bool) {
	body, ok := ctx.Value(rawBodyKey{}).([]byte)
	return body, ok
}
