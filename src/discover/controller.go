package discover

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
	"github.com/OpenAgriNet/discovery-service/src/platform/middlewares"
)

// degradedHeader carries the retrieval modes that did not contribute.
//
// A header and not a body key (C11): OnDiscoverAction is
// additionalProperties:false with `catalogs` as its only property, so a
// `degraded` member inside `message` is not an extension but a response that
// fails its own schema.
const degradedHeader = "X-Beckn-Degraded"

// messageRoot is where a fault about the action as a whole points.
const messageRoot = "$.message"

// Controller is the HTTP face of the discover path. It owns its own route
// registration, so the mount and the handler cannot drift apart.
type Controller struct {
	service *Service
	errors  config.Errors
}

// NewController wires the discover route.
func NewController(service *Service, errors config.Errors) *Controller {
	return &Controller{service: service, errors: errors}
}

// Register mounts the discover route, and only it. There is no `/search` alias:
// the action lives in the body, so a second path is a second thing to route,
// rate-limit, log and document for no gain.
func (c *Controller) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /discover", c.Discover)
}

// Discover answers one intent with the callback shape, inline.
func (c *Controller) Discover(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	envelope, mounted := middlewares.EnvelopeFromContext(ctx)
	if !mounted {
		// The middleware is missing from the chain. A wiring fault, not the
		// caller's, so nothing about the request is echoed.
		httpx.WriteNack(ctx, w, c.errors, "", apperrors.Internal())
		return
	}

	var action beckn.DiscoverAction
	if err := json.Unmarshal(envelope.Message, &action); err != nil {
		httpx.WriteNack(ctx, w, c.errors, envelope.Context.MessageID,
			apperrors.Schema(beckn.CodeSchemaValidationFailed,
				"message is not a discover action").At(messageRoot))
		return
	}

	observeIntent(ctx, envelope.Context, action.Intent)

	page, err := pageFrom(r.URL.Query(), action)
	if err != nil {
		httpx.WriteNack(ctx, w, c.errors, envelope.Context.MessageID, err)
		return
	}

	answer, degraded, err := c.service.Discover(ctx, envelope.Context, action.Intent, page)
	if err != nil {
		httpx.WriteNack(ctx, w, c.errors, envelope.Context.MessageID, err)
		return
	}

	observeResult(ctx, answer.Catalogs)

	// Before the body: WriteJSON writes the status line, and a header set after
	// that is a header nobody receives.
	if len(degraded) > 0 {
		w.Header().Set(degradedHeader, strings.Join(degraded, ","))
	}

	body := httpx.Envelope[beckn.OnDiscoverAction]{
		Context: responseContext(envelope.Context),
		Message: answer,
	}
	if err := httpx.WriteJSON(ctx, w, http.StatusOK, body); err != nil {
		httpx.WriteNack(ctx, w, c.errors, envelope.Context.MessageID, err)
	}
}

// pageFrom reads the page from the body; ?limit= is refused (ADR-0017).
func pageFrom(query url.Values, action beckn.DiscoverAction) (Page, error) {
	if query.Has("limit") || query.Has("offset") {
		return Page{}, apperrors.Schema(beckn.CodeSchemaInvalidFormat,
			"pagination moved from the query string to message.limit and message.offset").At(messageRoot)
	}
	return Page{Limit: action.Limit, Offset: action.Offset}, nil
}

// responseContext turns the request's envelope into the response's. The
// correlation handles are echoed rather than regenerated: they are how the caller
// ties this answer to the request it made.
func responseContext(request beckn.Context) beckn.Context {
	return beckn.Context{
		Action:  beckn.ActionOnDiscover,
		Version: beckn.Version,

		// The legs reverse (A24): on the request this service was the receiver,
		// so echoing these unswapped puts the caller's DID on a message the
		// caller did not send. Neither is verified; beckn.Context says what
		// that costs.
		SenderID:   request.ReceiverID,
		ReceiverID: request.SenderID,

		TransactionID: request.TransactionID,
		MessageID:     request.MessageID,
		NetworkID:     request.NetworkID,

		// This service's own clock: the request's timestamp says when the caller
		// sent, and repeating it claims the answer was ready before it existed.
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}
