package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/blake2b"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/embeddings"
	"github.com/OpenAgriNet/discovery-service/src/platform/jsonpath"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
)

// Service is the publish request path: it turns one wire action into one
// verdict per catalog.
//
// It holds no request state, so one instance serves every caller.
type Service struct {
	repo       domain.CatalogRepository
	replicator domain.CatalogReplicator
	embedder   embeddings.Embedder

	// network is APP_NETWORK_ID, the fallback when the envelope names none. It
	// is read only to fill an empty visibleTo (C8).
	network string

	// zone is APP_DEFAULT_TIMEZONE, used to resolve a bare clock in `validity`.
	zone *time.Location
}

// NewService wires the publish path.
//
// The replicator is a required collaborator rather than an optional one: a
// deployment that fans out to nothing passes a no-op, which is a decision
// visible at the composition root instead of a nil check repeated here.
func NewService(
	repo domain.CatalogRepository,
	replicator domain.CatalogReplicator,
	embedder embeddings.Embedder,
	network string,
	zone *time.Location,
) *Service {
	return &Service{repo: repo, replicator: replicator, embedder: embedder, network: network, zone: zone}
}

// Publish processes every catalog in the action and answers one result each, in
// the order they were sent.
//
// Each catalog is its own transaction. A request-wide transaction would make one
// publisher's refused catalog an outage for the catalogs beside it, and the
// per-catalog `status` enum the spec defines would have nothing to say.
func (s *Service) Publish(
	ctx context.Context, envelope beckn.Context, action beckn.CatalogPublishAction,
) []beckn.CatalogProcessingResult {
	network := envelope.NetworkID
	if network == "" {
		network = s.network
	}

	results := make([]beckn.CatalogProcessingResult, 0, len(action.Catalogs))
	seen := make(map[string]bool, len(action.Catalogs))

	for index, catalog := range action.Catalogs {
		// The same id twice in one request. Left unchecked both come back
		// ACCEPTED and the stored catalog is the second, so one of the two
		// success verdicts describes a document that no longer exists.
		if seen[catalog.ID] {
			results = append(results, rejected(catalog.ID, beckn.Error{
				Code:    beckn.CodeSchemaValidationFailed,
				Message: "this catalog id appears earlier in the same request; only the first entry was stored",
				Details: &beckn.ErrorDetails{Path: catalogPath(index)},
			}))
			continue
		}
		seen[catalog.ID] = true

		directive, directiveIndex := directiveFor(action, catalog.ID)
		results = append(results, s.publishOne(ctx, request{
			catalogIndex:   index,
			catalog:        catalog,
			directive:      applyDirectiveDefaults(directive, catalog.ID, network),
			directiveIndex: directiveIndex,
			network:        network,
			version:        envelope.Version,
		}))
	}
	return results
}

// request is one catalog together with everything about where in the payload it
// came from. The two indices are carried because a fault has to name the value
// the publisher sent, and only the caller knows which slot that was.
type request struct {
	catalogIndex   int
	catalog        beckn.Catalog
	directive      beckn.PublishDirective
	directiveIndex int
	network        string

	// version is context.version, carried per-request rather than read from
	// the service because it describes the envelope, not the deployment.
	version string
}

// publishOne runs the whole path for one catalog.
func (s *Service) publishOne(ctx context.Context, req request) beckn.CatalogProcessingResult {
	if refusal := intakeRefusal(req); refusal != nil {
		return rejected(req.catalog.ID, *refusal)
	}

	patch, fatal, partial := MapCatalog(req.catalog, req.directive, req.network, s.zone, req.version)
	if len(fatal) > 0 {
		return rejected(req.catalog.ID, catalogRelative(fatal, req.catalogIndex)...)
	}

	mode := domain.UpdateMode(req.directive.UpdateMode)
	derived, err := s.repo.UpsertCatalog(ctx, patch, mode, s.derive(ctx, req.catalogIndex))
	if err != nil {
		logger.FromContext(ctx).Error("storing the catalog failed",
			zap.String("catalog_id", req.catalog.ID), zap.Error(err))

		return rejected(req.catalog.ID, beckn.Error{
			Code:    beckn.CodeNetworkInternalError,
			Message: "the catalog could not be stored",
			Details: &beckn.ErrorDetails{Path: catalogPath(req.catalogIndex)},
		})
	}
	// The two families stay apart all the way to the wire, because they are
	// rooted differently and only the caller knows which is which.
	faults := append(
		catalogRelative(partial, req.catalogIndex),
		requestRelative(derived)...,
	)

	// A7. AFTER the write returns, never inside the closure: a fan-out that ran
	// before commit would announce a catalog that then rolled back, and no
	// response anywhere would show it. The converse is a stale replica, which
	// the next publish repairs.
	if err := s.replicator.Replicate(ctx, req.catalog.ID); err != nil {
		// Logged, not reported. The catalog IS stored; a REJECTED here would ask
		// the publisher to send again what the store already holds.
		logger.FromContext(ctx).Warn("announcing the catalog failed",
			zap.String("catalog_id", req.catalog.ID), zap.Error(err))
	}

	status := beckn.StatusAccepted
	if len(faults) > 0 {
		status = beckn.StatusPartial
	}
	return beckn.CatalogProcessingResult{
		CatalogID: req.catalog.ID,
		Status:    status,
		Errors:    faults,
		Stats:     statsFor(patch),
	}
}

// directiveFor finds the directive that names a catalog, and says where it sat.
//
// The index is the reason this is not a method on the action returning only the
// directive: a refusal has to point at `$.message.publishDirectives[1]`, and a
// literal `i` in a response is a placeholder that shipped.
func directiveFor(action beckn.CatalogPublishAction, catalogID string) (beckn.PublishDirective, int) {
	for index, directive := range action.PublishDirectives {
		if directive.CatalogID == catalogID {
			return directive, index
		}
	}
	return beckn.PublishDirective{}, -1
}

// applyDirectiveDefaults fills a missing directive FIELD-WISE (A9).
//
// Field-wise rather than all-or-nothing because a directive naming only
// catalogId means the same thing as no directive at all. The updateMode default
// is the one that is a data-loss bug the other way round: a zero value reading
// as FULL turns every directive-less republish into a partial wipe of everything
// the payload did not mention.
func applyDirectiveDefaults(
	directive beckn.PublishDirective, catalogID, network string,
) beckn.PublishDirective {
	if directive.CatalogID == "" {
		directive.CatalogID = catalogID
	}
	if directive.CatalogType == "" {
		directive.CatalogType = beckn.CatalogTypeRegular
	}
	if directive.UpdateMode == "" {
		directive.UpdateMode = beckn.UpdateModeMerge
	}
	if len(directive.VisibleTo) == 0 {
		directive.VisibleTo = []string{network}
	}
	if directive.ResourceDirectives == nil {
		directive.ResourceDirectives = []beckn.ResourceDirective{}
	}
	return directive
}

// intakeRefusal is A1: the two things Phase 1 refuses before doing any work.
//
// Refused at intake and not partially handled, so nothing downstream has to
// carry a half-implemented inheritance path that no test exercises.
func intakeRefusal(req request) *beckn.Error {
	if req.catalog.ID == "" {
		return &beckn.Error{
			Code:    beckn.CodeSchemaValidationFailed,
			Message: "a catalog needs an id; catalogs merge by it",
			Details: &beckn.ErrorDetails{Path: catalogPath(req.catalogIndex)},
		}
	}

	if req.directive.CatalogType == beckn.CatalogTypeMaster {
		return &beckn.Error{
			Code:    beckn.CodeSchemaTypeNotSupported,
			Message: "master catalogs are not supported; publish this catalog as REGULAR",
			Details: &beckn.ErrorDetails{Path: req.directivePath()},
		}
	}

	for k, resourceDirective := range req.directive.ResourceDirectives {
		if resourceDirective.Extends == nil {
			continue
		}
		return &beckn.Error{
			Code:    beckn.CodeSchemaTypeNotSupported,
			Message: "resource inheritance is not supported; publish this resource in full",
			Details: &beckn.ErrorDetails{Path: req.resourceDirectivePath(k)},
		}
	}
	return nil
}

// derive is the closure the repository runs inside the write transaction, on the
// MERGED document.
//
// It is built here rather than passed in as a repository port because it needs
// two things a port has no business knowing: the catalog's index in THIS request,
// so a geometry fault names the value the publisher sent, and the embedder.
func (s *Service) derive(ctx context.Context, catalogIndex int) domain.DeriveFunc {
	return func(merged *domain.Catalog, touched []string) []domain.Fault {
		found, faults := ExtractGeometries(catalogIndex, *merged)
		assignGeometries(merged, found)

		inPatch := domain.NewTouchedSet(touched)
		for index := range merged.Resources {
			if !inPatch.Has(merged.Resources[index].ID) {
				continue
			}
			faults = append(faults, s.deriveResource(ctx, &merged.Resources[index], catalogIndex, index)...)
		}
		return faults
	}
}

// assignGeometries replaces the catalog's covers with the walk's finds, split by
// who owns them.
//
// Replaces rather than appends: derive runs on every publish, and under MERGE
// the merged document already carries the geometries the last one derived.
// Appending would double them at each republish.
func assignGeometries(merged *domain.Catalog, found []domain.Geometry) {
	merged.Geometries = nil
	for index := range merged.Resources {
		merged.Resources[index].Geometries = nil
	}

	// Walk each geometry's OWNERS and look the resource up, rather than walking
	// every resource and scanning the owners. The two read alike and cost
	// differently: a catalog's resources outnumber one shape's owners by orders
	// of magnitude, so the scan is geometries x resources where this is
	// geometries x owners. A resource id an owner names but the catalog does
	// not hold is skipped — ExtractGeometries takes owners from the resources
	// it walked and from offer.ResourceIDs, and only the second can name a
	// resource that is not there.
	at := make(map[string]int, len(merged.Resources))
	for index := range merged.Resources {
		at[merged.Resources[index].ID] = index
	}

	for _, geometry := range found {
		// Empty Owners is CATALOG-WIDE — the shape is shared by every resource
		// rather than owned by none.
		if len(geometry.Owners) == 0 {
			merged.Geometries = append(merged.Geometries, geometry)
			continue
		}
		for position, owner := range geometry.Owners {
			// Owners come from offer.ResourceIDs, which is publisher-supplied
			// and may name the same resource twice. Scanning the resources and
			// testing membership — which this replaced — appended once per
			// RESOURCE and so absorbed the duplicate silently; walking the
			// owners does not, and a shape stored twice against one resource is
			// a duplicate row in the geometry table.
			if slices.Contains(geometry.Owners[:position], owner) {
				continue
			}
			index, ok := at[owner]
			if !ok {
				continue
			}
			merged.Resources[index].Geometries = append(merged.Resources[index].Geometries, geometry)
		}
	}
}

// deriveResource fills the columns that are read OFF the merged document rather
// than sent: the two C4 filter columns, the search text, and the A5 pair.
//
// Nothing else in the service writes SchemaContext or SchemaType, so a discover
// filtering on either matches nothing without this.
func (s *Service) deriveResource(
	ctx context.Context, resource *domain.Resource, catalogIndex, resourceIndex int,
) []domain.Fault {
	resource.Name = descriptorName(resource.Descriptor())
	resource.SchemaContext, resource.SchemaType = schemaOf(resource.ResourceAttributes())
	resource.SearchText = deriveSearchText(*resource)

	hash := blake2b.Sum256([]byte(resource.SearchText))
	changed := !bytes.Equal(hash[:], resource.EmbeddingSourceHash)

	// A5: written UNCONDITIONALLY, outside the branch. It records what the
	// derived text currently IS, which is true whether or not an embedder ran —
	// and the Phase 2 backfill selects on a NULL embedding, not on this, so a
	// failed embed is still picked up.
	resource.EmbeddingSourceHash = hash[:]

	if !changed {
		return nil
	}

	vector, err := s.embedder.Embed(ctx, resource.SearchText)
	if err != nil {
		// PARTIAL, not fatal. Failing a publish because a model was unreachable
		// would take a whole catalog offline over a feature that is deferred.
		return []domain.Fault{{
			Path:    resourcePath(catalogIndex, resourceIndex),
			Code:    string(beckn.CodeNetworkInternalError),
			Message: fmt.Sprintf("this resource was stored without a vector: %v", err),
		}}
	}
	if err := embeddings.CheckDimensions(vector, s.embedder.Dimensions()); err != nil {
		return []domain.Fault{{
			Path:    resourcePath(catalogIndex, resourceIndex),
			Code:    string(beckn.CodeNetworkInternalError),
			Message: fmt.Sprintf("this resource was stored without a vector: %v", err),
		}}
	}

	resource.Embedding = vector
	return nil
}

// descriptorName reads `descriptor.name`, or the empty string.
func descriptorName(descriptor json.RawMessage) string {
	var shape struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(descriptor, &shape) != nil {
		return ""
	}
	return shape.Name
}

// schemaOf reads the JSON-LD pair off a resource's attributes.
func schemaOf(attributes json.RawMessage) (schemaContext, schemaType string) {
	var shape struct {
		Context string `json:"@context"`
		Type    string `json:"@type"`
	}
	if json.Unmarshal(attributes, &shape) != nil {
		return "", ""
	}
	return shape.Context, shape.Type
}

// statsFor counts what THIS request landed (C5, C12).
//
// Request-scoped, not catalog-scoped: a MERGE carrying one resource into a
// forty-resource catalog reports 1. Read back off the stored rows instead, a
// re-publish of a single resource would report 40 and the publisher would have
// no way to tell what their request actually did.
func statsFor(patch domain.CatalogPatch) *beckn.CatalogStats {
	// Distinct @type, because the spec has no category field anywhere (C5).
	categories := make(map[string]bool, len(patch.Resources))
	for _, resource := range patch.Resources {
		if _, schemaType := schemaOf(resource.ResourceAttributes()); schemaType != "" {
			categories[schemaType] = true
		}
	}

	return &beckn.CatalogStats{
		ItemCount:     len(patch.Resources),
		ProviderCount: 1,
		CategoryCount: len(categories),
	}
}

// rejected is the verdict that stores nothing.
func rejected(catalogID string, faults ...beckn.Error) beckn.CatalogProcessingResult {
	return beckn.CatalogProcessingResult{
		CatalogID: catalogID,
		Status:    beckn.StatusRejected,
		Errors:    faults,
	}
}

// catalogRelative renders faults the MAPPER produced. It walks one catalog, so
// its paths are relative to that catalog: `$['resources'][0]['id']`.
func catalogRelative(faults []domain.Fault, catalogIndex int) []beckn.Error {
	return rebase(faults, func(dotted string) string {
		return catalogPath(catalogIndex) + dotted[len("$"):]
	}, catalogIndex)
}

// requestRelative renders faults the GEOMETRY WALK produced. It walks the
// catalogs array, so its paths already name the catalog: `$['catalogs'][2]…`.
//
// It takes no index precisely because it needs none — which is the whole reason
// this is a second function rather than a `strings.HasPrefix` inside one. A
// sniff would hold only while no catalog field is ever called `catalogs`, and
// that is an invariant nothing states and nothing checks.
func requestRelative(faults []domain.Fault) []beckn.Error {
	return rebase(faults, func(dotted string) string {
		return messageRoot + dotted[len("$"):]
	}, -1)
}

// rebase renders each fault's path onto the request body in the dot form C7's
// example uses, and copies the rest of the fault across.
//
// Neither producer is wrong about its own root; only here is it known that both
// sit under `message`, and a publisher needs a path they can run against the
// body they actually sent.
func rebase(faults []domain.Fault, onto func(dotted string) string, catalogIndex int) []beckn.Error {
	if len(faults) == 0 {
		return nil
	}

	out := make([]beckn.Error, 0, len(faults))
	for _, fault := range faults {
		path := ""
		if dotted := jsonpath.Dot(fault.Path); dotted != "" {
			path = onto(dotted)
		} else if catalogIndex >= 0 {
			// Unreadable, so there is nothing honest to say about where inside
			// the catalog it was. Naming the catalog is still true and useful.
			path = catalogPath(catalogIndex)
		}

		out = append(out, beckn.Error{
			Code:    beckn.ErrorCode(fault.Code),
			Message: fault.Message,
			Details: &beckn.ErrorDetails{Path: path},
		})
	}
	return out
}

// messageRoot is where every path in a publish fault is rooted. The action lives
// in the body, so `$.message` and not `$.catalogs`.
const messageRoot = "$.message"

func catalogPath(index int) string {
	return fmt.Sprintf("%s.catalogs[%d]", messageRoot, index)
}

func resourcePath(catalogIndex, resourceIndex int) string {
	return fmt.Sprintf("$['catalogs'][%d]['resources'][%d]", catalogIndex, resourceIndex)
}

// directivePath names the directive this catalog was published under.
//
// A catalog with no directive of its own falls back to naming the catalog: the
// defaults A9 filled are not in the payload, so pointing at
// `publishDirectives[-1]` would name a value the publisher never sent.
func (r request) directivePath() string {
	if r.directiveIndex < 0 {
		return catalogPath(r.catalogIndex)
	}
	return fmt.Sprintf("%s.publishDirectives[%d]", messageRoot, r.directiveIndex)
}

func (r request) resourceDirectivePath(index int) string {
	if r.directiveIndex < 0 {
		return catalogPath(r.catalogIndex)
	}
	return fmt.Sprintf("%s.resourceDirectives[%d]", r.directivePath(), index)
}
