package discover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
	"github.com/OpenAgriNet/discovery-service/src/platform/jsonpath"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
)

// Service is the discover request path: one intent, one page of catalogs, and an
// honest account of what could not be run. It holds no request state, so one
// instance serves every caller.
type Service struct {
	repo domain.SearchRepository

	// The whole config rather than config.Search, because MapIntent reads Geo
	// as well.
	cfg config.Config
}

// NewService wires the discover path.
func NewService(repo domain.SearchRepository, cfg config.Config) *Service {
	return &Service{repo: repo, cfg: cfg}
}

// Discover answers one intent with one page of catalogs and the retrieval modes
// that did not contribute. The degraded list is returned beside the catalogs
// rather than inside them, because it reaches the caller as a header (C11).
func (s *Service) Discover(
	ctx context.Context, envelope beckn.Context, intent beckn.Intent, page Page,
) ([]beckn.Catalog, []string, error) {
	query, fatal, partial := MapIntent(intent, envelope, page, s.cfg)
	if len(fatal) > 0 {
		return nil, nil, refusal(fatal)
	}
	reportPartials(ctx, partial)

	// From the ENVELOPE, and NOT defaulted to config.App.Network. Empty means
	// EVERY network and the repository emits no network predicate at all.
	// config.App.Network is publish's default for an empty visibleTo (C8) — a
	// different field answering a different question — and reusing it here puts
	// discover back to single-network scoping (scenario 29).
	query.NetworkID = envelope.NetworkID

	modes, degraded, err := s.negotiate(query)
	if err != nil {
		return nil, nil, err
	}

	result, err := s.repo.Search(ctx, query, modes)
	if err != nil {
		return nil, nil, typedSearchFailure(ctx, err)
	}

	// Both halves, joined once: negotiate's are the modes this deployment cannot
	// run at all, result.Degraded the ones that failed on this request. The
	// header and the span attribute must be the same list.
	degraded = append(degraded, result.Degraded...)
	observeRetrieval(ctx, modes, degraded)

	return render(result.Catalogs), degraded, nil
}

// typedSearchFailure says whose mistake a failed search was.
//
// Two of the store's errors are the CALLER's and get an answer of their own;
// everything else is the deployment's and is a 500 with nothing about the backend
// in it.
func typedSearchFailure(ctx context.Context, err error) error {
	// A page past the retrieval depth. MapIntent is the guard in FRONT and mints
	// the same code at the same path; this is what answers when a caller reaches
	// the repository by another route, because a 500 would invite a retry of a
	// request that cannot succeed.
	if errors.Is(err, domain.ErrRetrievalDepth) {
		return apperrors.
			Schema(beckn.CodeSchemaInvalidFormat, err.Error()).
			At(jsonpath.Dot("$['offset']"))
	}

	// An expression the store's own parser refused is the caller's mistake too,
	// and gets the code the gate mints for one it refuses itself.
	//
	// The sentinel's OWN text, never err.Error(): the backend wraps this with the
	// operation and with PostgreSQL's clause, both internals, and both reach the
	// operator through the log line instead.
	if errors.Is(err, domain.ErrInvalidFilterExpression) {
		logger.FromContext(ctx).Warn("the filter expression was refused by the store",
			zap.Error(err))
		return apperrors.
			Schema(beckn.CodeSchemaInvalidJSONPath, domain.ErrInvalidFilterExpression.Error()).
			At(jsonpath.Dot(filtersPath + "['expression']"))
	}

	logger.FromContext(ctx).Error("searching the catalogue failed", zap.Error(err))

	// Not an empty page. A dead backend and a query that matched nothing read
	// identically at the caller, and only one of them is an answer.
	return apperrors.Internal()
}

// modesFor is the set of retrieval modes an intent asks for.
//
// Text asks for all three ranked modes rather than lexical alone: RRF fuses
// whatever answers, and the wire has no field for naming a mode. Spatial and
// jsonpath are asked for by the presence of the constraint they serve.
func modesFor(query domain.SearchQuery) []domain.Capability {
	modes := make([]domain.Capability, 0, 5)
	if query.Text != "" {
		modes = append(modes,
			domain.CapabilityLexical, domain.CapabilityFuzzy, domain.CapabilitySemantic)
	}
	if query.Spatial != nil {
		modes = append(modes, domain.CapabilitySpatial)
	}
	if len(query.Filters) > 0 {
		modes = append(modes, domain.CapabilityJSONPath)
	}
	return modes
}

// negotiate settles what this backend will actually be asked to run.
//
// Degrade-and-report, or refuse — never silently ignore. A caller who filtered
// for one manufacturer and got every manufacturer has been actively misled.
func (s *Service) negotiate(query domain.SearchQuery) ([]domain.Capability, []string, error) {
	wanted := modesFor(query)
	capabilities := s.repo.Capabilities()

	available := make([]domain.Capability, 0, len(wanted))
	var missing []string
	for _, mode := range wanted {
		if capabilities.Has(mode) {
			available = append(available, mode)
			continue
		}
		missing = append(missing, string(mode))
	}

	if len(missing) == 0 {
		return wanted, nil, nil
	}
	if s.cfg.Search.FailOnUnavailableMode {
		return nil, nil, apperrors.Network(beckn.CodeNetworkCatalogSourceUnavailable,
			fmt.Sprintf("this deployment cannot answer the retrieval mode(s) %s",
				strings.Join(missing, ", ")))
	}
	return available, missing, nil
}

// render turns stored catalogs into the response's.
//
// The whole stored catalog, not a projection of it: since A17 the document is
// verbatim with its two child arrays on their own tables, so rendering is decode,
// put the children back, and let beckn.Catalog's MarshalJSON write it out. The
// next member the protocol adds needs no edit here.
//
// A document that will not decode is dropped rather than half-rendered, for the
// reason renderOffers gives. An EMPTY one is not dropped: the column defaults to
// an empty object, and the id is the row's own primary key.
func render(catalogs []domain.Catalog) []beckn.Catalog {
	rendered := make([]beckn.Catalog, 0, len(catalogs))
	for _, catalog := range catalogs {
		decoded := beckn.Catalog{ID: catalog.ID}
		if len(catalog.Document) > 0 {
			if err := json.Unmarshal(catalog.Document, &decoded); err != nil {
				continue
			}
		}

		decoded.Resources = renderResources(catalog.Resources)
		decoded.Offers = renderOffers(catalog.Offers)
		rendered = append(rendered, decoded)
	}
	return rendered
}

// renderResources gives back each stored Document, which is the resource exactly
// as its publisher wrote it.
//
// An empty document falls back to the row's id: `id` is the only member a
// Resource requires, and a resource that vanished from the response would look
// to the caller exactly like one the gate refused.
func renderResources(resources []domain.Resource) []beckn.Resource {
	rendered := make([]beckn.Resource, 0, len(resources))
	for _, resource := range resources {
		decoded := beckn.Resource{ID: resource.ID}
		if len(resource.Document) > 0 {
			if err := json.Unmarshal(resource.Document, &decoded); err != nil {
				continue
			}
		}
		rendered = append(rendered, decoded)
	}
	return rendered
}

// renderOffers gives back the stored Document, which is the offer exactly as its
// publisher wrote it.
//
// Decoded and not re-projected: beckn.Offer's UnmarshalJSON keeps the bytes it
// decoded and its MarshalJSON writes them back, so a member this service's own
// struct never named survives the round trip — which is the whole claim of
// storing the column verbatim.
//
// A Document that will not decode is dropped rather than half-rendered: it can
// only be a row this service did not write, and an offer whose shape is unknown
// is not one to guess at in a response.
func renderOffers(offers []domain.Offer) []beckn.Offer {
	rendered := make([]beckn.Offer, 0, len(offers))
	for _, offer := range offers {
		var decoded beckn.Offer
		if err := json.Unmarshal(offer.Document, &decoded); err != nil {
			continue
		}
		rendered = append(rendered, decoded)
	}
	return rendered
}

// refusal folds the mapper's fatal faults into the one error the response writer
// renders, each becoming the details.cause of the one before it (C7). The paths
// are in dot form because that is the spelling C7's own example uses.
func refusal(faults []domain.Fault) error {
	chained := make([]*apperrors.AppError, 0, len(faults))
	for _, fault := range faults {
		chained = append(chained, typed(fault.Code, fault.Message).At(jsonpath.Dot(fault.Path)))
	}
	return apperrors.Chain(chained...)
}

// typed turns a mapper fault's code back into a typed fault.
//
// A switch over literals rather than a conversion, because the minted-codes pin
// in src/platform/errors walks for family constructors called with a CONSTANT and
// a code reaching one through a variable is invisible to it. That walk is what
// keeps this mapper's two families apart — a single apperrors.Schema over every
// code would report CTX_INVALID_FIELD as a schema fault.
//
// A code this switch does not know is a fault in THIS file, so it becomes a 500
// rather than a guessed family; TestEveryCodeTheMapperMintsIsTyped fails the day
// the mapper grows one this does not name.
func typed(code, message string) *apperrors.AppError {
	switch beckn.ErrorCode(code) {
	case beckn.CodeContextInvalidField:
		return apperrors.Context(beckn.CodeContextInvalidField, message)
	case beckn.CodeSchemaInvalidFormat:
		return apperrors.Schema(beckn.CodeSchemaInvalidFormat, message)
	case beckn.CodeSchemaInvalidJSONPath:
		return apperrors.Schema(beckn.CodeSchemaInvalidJSONPath, message)
	case beckn.CodeSchemaTypeNotSupported:
		return apperrors.Schema(beckn.CodeSchemaTypeNotSupported, message)
	default:
		return apperrors.Internal()
	}
}

// reportPartials records the faults that qualify a request without refusing it —
// today, only a distanceMeters sent with an operator that ignores it.
//
// The log is the only channel: the response body admits `catalogs` alone (C11)
// and X-Beckn-Degraded names retrieval modes rather than fields. Giving the
// caller one is an open question in docs/design/implementation-prompts.md.
func reportPartials(ctx context.Context, partial []domain.Fault) {
	for _, fault := range partial {
		logger.FromContext(ctx).Warn("part of the intent was not applied",
			zap.String("path", fault.Path),
			zap.String("code", fault.Code),
			zap.String("reason", fault.Message))
	}
}
