// Package middlewares holds the chain the protocol routes are wrapped in, in
// the fixed order the plan sets out:
//
//	RequestID → Trace → Recover → RequestLogger → Envelope
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
// below. A body it cannot read at all is a NACK carrying SCH_INVALID_JSON.
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
func Envelope(cfg config.Errors) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, envelope, err := readEnvelope(r.Body)
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
func readEnvelope(source io.Reader) ([]byte, RawEnvelope, error) {
	body, err := io.ReadAll(source)
	if err != nil {
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

// echoedMessageID salvages `context.messageId` out of a body already known not
// to be an envelope, for C13's echo. Best-effort by construction: it runs only
// after parsing has failed, so it yields empty rather than failing, and empty
// is what a body too broken to read produces.
//
// A Decoder rather than json.Unmarshal, because Unmarshal validates the whole
// input before it decodes any of it — which would yield nothing on the one
// shape this salvage can actually help with, a well-formed envelope with a
// second document glued to it.
func echoedMessageID(body []byte) string {
	var probe struct {
		Context struct {
			MessageID string `json:"messageId"`
		} `json:"context"`
	}

	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&probe); err != nil {
		return ""
	}
	return probe.Context.MessageID
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
