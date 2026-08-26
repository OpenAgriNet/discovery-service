package discover

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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
// A header and not a body key (C11): OnDiscoverAction declares
// additionalProperties:false with `catalogs` as its only property, so a
// `degraded` member inside `message` is not an extension — it is a response
// that fails its own schema, and it would ship on precisely the path that
// matters.
const degradedHeader = "X-Beckn-Degraded"

// messageRoot is where a fault about the action as a whole points. The action
// lives in the body, so `$.message`.
const messageRoot = "$.message"

// Controller is the HTTP face of the discover path.
//
// It owns its own route registration rather than being mounted by the router,
// so the mount and the handler cannot drift apart.
type Controller struct {
	service *Service
	errors  config.Errors
}

// NewController wires the discover route.
func NewController(service *Service, errors config.Errors) *Controller {
	return &Controller{service: service, errors: errors}
}

// Register mounts the discover route, and only it. There is no `/search`
// alias: the action lives in the body, so a second path would be a second thing
// to route, rate-limit, log and document for no gain.
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

	page, err := pageFrom(r.URL.Query())
	if err != nil {
		httpx.WriteNack(ctx, w, c.errors, envelope.Context.MessageID, err)
		return
	}

	catalogs, degraded, err := c.service.Discover(ctx, envelope.Context, action.Intent, page)
	if err != nil {
		httpx.WriteNack(ctx, w, c.errors, envelope.Context.MessageID, err)
		return
	}

	// Before the body: WriteJSON writes the status line, and a header set after
	// that is a header nobody receives.
	if len(degraded) > 0 {
		w.Header().Set(degradedHeader, strings.Join(degraded, ","))
	}

	body := httpx.Envelope[beckn.OnDiscoverAction]{
		Context: responseContext(envelope.Context),
		Message: beckn.OnDiscoverAction{Catalogs: catalogs},
	}
	if err := httpx.WriteJSON(ctx, w, http.StatusOK, body); err != nil {
		httpx.WriteNack(ctx, w, c.errors, envelope.Context.MessageID, err)
	}
}

// pageFrom reads pagination off the query string, where it lives rather than
// inside the intent.
//
// An unreadable value is refused, not defaulted. Zero already means "the
// default page size" to the mapper, so reading `limit=twenty` as zero would
// answer a page the caller never asked for and say nothing about it — the same
// silent widening every branch of the intent mapper refuses.
func pageFrom(query url.Values) (Page, error) {
	limit, err := intParam(query, "limit")
	if err != nil {
		return Page{}, err
	}
	offset, err := intParam(query, "offset")
	if err != nil {
		return Page{}, err
	}
	return Page{Limit: limit, Offset: offset}, nil
}

// intParam reads one numeric query parameter. Absent is zero, which the mapper
// resolves to its own default; present and unreadable is a refusal.
func intParam(query url.Values, name string) (int, error) {
	raw := query.Get(name)
	if raw == "" {
		return 0, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apperrors.Schema(beckn.CodeSchemaInvalidFormat,
			fmt.Sprintf("%s is not a whole number", name)).At("$." + name)
	}
	return value, nil
}

// responseContext turns the request's envelope into the response's.
//
// The correlation handles are echoed rather than regenerated: transactionId and
// messageId are how the caller — and every log downstream of it — ties this
// answer to the request it made.
func responseContext(request beckn.Context) beckn.Context {
	return beckn.Context{
		Action:  beckn.ActionOnDiscover,
		Version: beckn.Version,

		BapID:  request.BapID,
		BapURI: request.BapURI,
		BppID:  request.BppID,
		BppURI: request.BppURI,

		TransactionID: request.TransactionID,
		MessageID:     request.MessageID,
		NetworkID:     request.NetworkID,

		// This service's own clock. The request's timestamp says when the
		// caller sent; a response repeating it would claim the answer was ready
		// before it was computed.
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}
