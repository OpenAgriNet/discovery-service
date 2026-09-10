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

// Controller is the HTTP face of the publish path. It owns its own route
// registration rather than being mounted by the router, so C2's "one route" is a
// property of this file.
type Controller struct {
	service *Service
	errors  config.Errors
}

// NewController wires the publish route.
func NewController(service *Service, errors config.Errors) *Controller {
	return &Controller{service: service, errors: errors}
}

// Register mounts the publish route, and only it. There is no
// `POST /catalog/publish` alias — the action lives in the body (C2).
func (c *Controller) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /publish", c.Publish)
}

// Publish answers one publish request with the callback shape, inline (C3).
//
// 200 even when every catalog came back REJECTED: the request was well-formed
// and the per-catalog verdicts ARE the payload. A transport-level NACK is
// reserved for a request that could not be read at all.
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

	observePublish(ctx, envelope.Context, action)

	results := c.service.Publish(ctx, envelope.Context, action)

	body := httpx.Envelope[beckn.CatalogOnPublishAction]{
		Context: responseContext(envelope.Context),
		Message: beckn.CatalogOnPublishAction{Results: results},
	}
	if err := httpx.WriteJSON(ctx, w, http.StatusOK, body); err != nil {
		httpx.WriteNack(ctx, w, c.errors, envelope.Context.MessageID, err)
	}
}

// responseContext turns the request's envelope into the response's: the
// correlation handles are echoed, the action becomes the callback's, and the
// participant legs reverse.
func responseContext(request beckn.Context) beckn.Context {
	return beckn.Context{
		Action:  beckn.ActionCatalogOnPublish,
		Version: beckn.Version,

		// Echoing these unswapped would put the caller's DID on a message the
		// caller did not send. Neither is verified; beckn.Context says what
		// that costs.
		SenderID:   request.ReceiverID,
		ReceiverID: request.SenderID,

		TransactionID: request.TransactionID,
		MessageID:     request.MessageID,
		NetworkID:     request.NetworkID,

		// This service's own clock, not the request's: a repeated timestamp
		// would claim the answer was ready before it was computed.
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}
