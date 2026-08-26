package publish

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
	"github.com/OpenAgriNet/discovery-service/src/platform/httpx"
	"github.com/OpenAgriNet/discovery-service/src/platform/middlewares"
)

// Controller is the HTTP face of the publish path.
//
// It owns its own route registration rather than being mounted by the router,
// so the mount and the handler cannot drift apart — C2's "one route" is a
// property of this file and is asserted here.
type Controller struct {
	service *Service
	errors  config.Errors
}

// NewController wires the publish route.
func NewController(service *Service, errors config.Errors) *Controller {
	return &Controller{service: service, errors: errors}
}

// Register mounts the publish route, and only it.
//
// There is no `POST /catalog/publish` alias. The action lives in the body (C2),
// so a second path would be a second thing to route, rate-limit, log and
// document for no gain — and nothing would notice it reappearing.
func (c *Controller) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /publish", c.Publish)
}

// Publish answers one publish request with the callback shape, inline (C3).
//
// 200 even when every catalog came back REJECTED: the request was well-formed
// and the per-catalog verdicts ARE the payload. A transport-level NACK is
// reserved for a request that could not be read at all, because a caller
// branching on the HTTP status must not be told "your request failed" about a
// request this service understood and answered.
func (c *Controller) Publish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	envelope, mounted := middlewares.EnvelopeFromContext(ctx)
	if !mounted {
		// The middleware is missing from the chain. A wiring fault, not the
		// caller's, so nothing about the request is echoed.
		httpx.WriteNack(ctx, w, c.errors, "", apperrors.Internal())
		return
	}

	var action beckn.CatalogPublishAction
	if err := json.Unmarshal(envelope.Message, &action); err != nil {
		// There is no catalog here to attach a REJECTED to, so a results array
		// would have to be empty — which reads as "nothing was sent".
		httpx.WriteNack(ctx, w, c.errors, envelope.Context.MessageID,
			apperrors.Schema(beckn.CodeSchemaValidationFailed,
				"message is not a catalog publish action").At(messageRoot))
		return
	}

	results := c.service.Publish(ctx, envelope.Context, action)

	body := httpx.Envelope[beckn.CatalogOnPublishAction]{
		Context: responseContext(envelope.Context),
		Message: beckn.CatalogOnPublishAction{Results: results},
	}
	if err := httpx.WriteJSON(ctx, w, http.StatusOK, body); err != nil {
		httpx.WriteNack(ctx, w, c.errors, envelope.Context.MessageID, err)
	}
}

// responseContext turns the request's envelope into the response's.
//
// The correlation handles are echoed and the action becomes the callback's.
// Echoed rather than regenerated: transactionId and messageId are how the caller
// — and every log downstream of it — ties this answer to the request it made.
func responseContext(request beckn.Context) beckn.Context {
	return beckn.Context{
		Action:  beckn.ActionCatalogOnPublish,
		Version: beckn.Version,

		BapID:  request.BapID,
		BapURI: request.BapURI,
		BppID:  request.BppID,
		BppURI: request.BppURI,

		TransactionID: request.TransactionID,
		MessageID:     request.MessageID,
		NetworkID:     request.NetworkID,

		// This service's own clock. The request's timestamp says when the caller
		// sent; a response repeating it would claim the answer was ready before
		// it was computed.
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}
