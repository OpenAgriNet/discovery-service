package discover

import (
	"context"
	"encoding/json"
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

// Service is the discover request path: one intent, one page of catalogs, and
// an honest account of what could not be run.
//
// It holds no request state, so one instance serves every caller.
type Service struct {
	repo domain.SearchRepository

	// The whole config rather than config.Search, because MapIntent reads Geo
	// as well and a second struct threaded beside it would be a second thing to
	// keep in step.
	cfg config.Config
}

// NewService wires the discover path.
func NewService(repo domain.SearchRepository, cfg config.Config) *Service {
	return &Service{repo: repo, cfg: cfg}
}

// Discover answers one intent with one page of catalogs and the retrieval modes
// that did not contribute.
//
// The degraded list is returned beside the catalogs rather than inside them:
// OnDiscoverAction is additionalProperties:false with `catalogs` as its only
// property, so it reaches the caller as the X-Beckn-Degraded header (C11).
func (s *Service) Discover(
	ctx context.Context, envelope beckn.Context, intent beckn.Intent, page Page,
) ([]beckn.Catalog, []string, error) {
	query, fatal, partial := MapIntent(intent, envelope, page, s.cfg)
	if len(fatal) > 0 {
		return nil, nil, refusal(fatal)
	}
	reportPartials(ctx, partial)

	// From the ENVELOPE, and NOT defaulted to config.App.Network. Empty means
	// EVERY network: the repository emits no network predicate at all, the same
	// way an empty schemaContext emits no schema predicate. config.App.Network
	// is publish's default for an empty visibleTo (C8) — a different field
	// answering a different question — and reusing it here would quietly put
	// discover back to single-network scoping under a name that suggests it is
	// unscoped (scenario 29).
	query.NetworkID = envelope.NetworkID

	modes, degraded, err := s.negotiate(query)
	if err != nil {
		return nil, nil, err
	}

	result, err := s.repo.Search(ctx, query, modes)
	if err != nil {
		logger.FromContext(ctx).Error("searching the catalogue failed", zap.Error(err))

		// Not an empty page. A dead backend and a query that matched nothing
		// read identically at the caller, and only one of them is an answer.
		return nil, nil, apperrors.Internal()
	}

	return render(result.Catalogs), append(degraded, result.Degraded...), nil
}

// modesFor is the set of retrieval modes an intent asks for.
//
// Text asks for all three ranked modes rather than for lexical alone: RRF
// fuses whatever answers, and a deployment that has fuzzy or semantic should
// use them without the caller naming a mode the wire has no field for. Spatial
// and jsonpath are asked for by the presence of the constraint they serve —
// they are filters rather than ranked modes, and a backend that cannot run one
// must say so rather than answer a narrower question than it was asked.
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
// for one manufacturer and got every manufacturer has been actively misled, so
// silence is the one option that is never taken.
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
// Four members — id, provider, resources, offers — and nothing this service
// would have to invent. `isActive` is not among them: every catalog that
// survives the scope gate is live, so rendering it would be a constant true
// dressed as information.
//
// `descriptor` is missing, and the schema requires it. Catalog in beckn.yaml is
// required:[id, descriptor, provider] with additionalProperties:false, so what
// this emits does not satisfy its own response shape. Nothing here can fix
// that: domain.Catalog has no Descriptor, no column stores one and the publish
// mapper never read one, so the value does not exist to be rendered. The gap is
// in the plan's Deferred table with what closing it costs — it is a schema and
// write-path change, not a projection this function is declining to make.
//
// SearchResult.Total is dropped for the neighbouring reason: OnDiscoverAction
// admits `catalogs` alone. That one is not free — the repository issues a count
// query to produce it — and it is in the same table.
func render(catalogs []domain.Catalog) []beckn.Catalog {
	rendered := make([]beckn.Catalog, 0, len(catalogs))
	for _, catalog := range catalogs {
		rendered = append(rendered, beckn.Catalog{
			ID:        catalog.ID,
			Provider:  catalog.Provider,
			Resources: renderResources(catalog.Resources),
			Offers:    renderOffers(catalog.Offers),
		})
	}
	return rendered
}

// renderResources projects the three members the wire Resource has (C5). There
// is no category field anywhere in v2.0.0, so there is none to render.
func renderResources(resources []domain.Resource) []beckn.Resource {
	rendered := make([]beckn.Resource, 0, len(resources))
	for _, resource := range resources {
		rendered = append(rendered, beckn.Resource{
			ID:                 resource.ID,
			Descriptor:         resource.Descriptor,
			ResourceAttributes: resource.Attributes,
		})
	}
	return rendered
}

// renderOffers gives back the stored Document, which is the offer exactly as
// its publisher wrote it.
//
// Decoded and not re-projected: beckn.Offer's UnmarshalJSON keeps the bytes it
// decoded, and its MarshalJSON writes them back, so a member this service's own
// struct never named survives the round trip. The `offer` JSONB column is
// stored verbatim for precisely this, and a response that dropped those members
// would make that column's whole claim false for the publishers who needed it
// to be true.
//
// A Document that will not decode is dropped rather than half-rendered. It can
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

// refusal folds the mapper's fatal faults into the one error the response
// writer renders, each becoming the details.cause of the one before it (C7).
//
// The paths are rendered in dot form because that is the spelling C7's own
// example uses and the one a human comparing it against the body they sent will
// recognise.
func refusal(faults []domain.Fault) error {
	chained := make([]*apperrors.AppError, 0, len(faults))
	for _, fault := range faults {
		chained = append(chained, typed(fault.Code, fault.Message).At(jsonpath.Dot(fault.Path)))
	}
	return apperrors.Chain(chained...)
}

// typed turns a mapper fault's code back into a typed fault, and it is a switch
// over literals rather than a conversion for one reason: the minted-codes pin in
// src/platform/errors walks for family constructors called with a CONSTANT, and
// a code that reaches one through a variable is invisible to that walk.
//
// The walk is what keeps a SCH_ code from being reported as a CTX_ one, and
// this mapper mints both families — an unreadable schemaContext entry is a
// context fault, and everything else here is a schema one. A single
// apperrors.Schema over every code would have shipped CTX_INVALID_FIELD as a
// DOMAIN error with a 400 that categorises wrongly.
//
// A code this switch does not know is a fault in THIS file rather than in the
// request, so it becomes a 500 rather than a guessed family. Nothing has to
// remember to extend it: TestEveryCodeTheMapperMintsIsTyped fails the day the
// mapper grows a code this does not name.
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

// reportPartials records the faults that qualify a request without refusing it
// — today, only a distanceMeters sent with an operator that ignores it.
//
// The log is the only channel: OnDiscoverAction is additionalProperties:false
// with `catalogs` as its only property, and X-Beckn-Degraded names retrieval
// modes rather than fields. Giving the caller one is an open question against
// this task, recorded in docs/design/implementation-prompts.md rather than left
// as a comment here.
func reportPartials(ctx context.Context, partial []domain.Fault) {
	for _, fault := range partial {
		logger.FromContext(ctx).Warn("part of the intent was not applied",
			zap.String("path", fault.Path),
			zap.String("code", fault.Code),
			zap.String("reason", fault.Message))
	}
}
