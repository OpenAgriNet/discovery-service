package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres/gen"
)

// errUnboundedGeometry is the PARTIAL a shape that produced no bounding box
// earns. The box columns are NOT NULL and, for a shape too big to cover, the
// box is the ENTIRE spatial predicate — so a row without one is not a degraded
// row, it is an undiscoverable one.
var errUnboundedGeometry = errors.New("the geometry produced no bounding box")

// CatalogRepository is the write half of the PostgreSQL adapter.
type CatalogRepository struct {
	pool    *pgxpool.Pool
	queries *gen.Queries

	// resolution is the H3 resolution stored covers are built at. It is a field
	// rather than a package constant because it is configuration
	// (GEO_RESOLUTION_CELLS), and because a store that covered at one
	// resolution while the query covered at another would return nothing and
	// report nothing wrong.
	resolution int
}

// NewCatalogRepository builds the write repository over a pool.
//
// It takes the resolution as well as the pool, which the plan's stated
// signature does not: `geo.CoverGeometry` needs one, the plan's own pseudocode
// elides it, and the alternative — reading config in here — would put a second
// copy of the setting one layer below the composition root that owns it.
func NewCatalogRepository(pool *pgxpool.Pool, resolutionCells int) *CatalogRepository {
	return &CatalogRepository{pool: pool, queries: gen.New(pool), resolution: resolutionCells}
}

// Compile-time proof that this satisfies the port. The conformance suite proves
// it BEHAVES like one; this catches signature drift at the place that can say
// why it matters.
var _ domain.CatalogRepository = (*CatalogRepository)(nil)

// UpsertCatalog is the whole write path, in one transaction.
//
// The order inside is load-bearing and is the plan's "Inside UpsertCatalog":
// the lock-and-load upsert takes the catalog's row lock FIRST, so two
// concurrent republishes of one catalog serialise rather than interleaving two
// read-modify-writes. Everything after it is paid for while that lock is held,
// which is why each loop goes out as one pgx.Batch — the cost of this
// transaction is lock hold time, and a statement per resource makes it linear
// in catalog size.
func (r *CatalogRepository) UpsertCatalog(
	ctx context.Context, patch domain.CatalogPatch, mode domain.UpdateMode, derive domain.DeriveFunc,
) (faults []domain.Fault, err error) {
	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin the publish transaction: %w", err)
	}

	// Unconditional, because a rollback guarded by a condition is one somebody
	// eventually forgets on a new early return, and the failure mode of
	// forgetting is a half-written catalog. After a successful Commit it
	// returns pgx.ErrTxClosed, which is the ordinary path and says nothing went
	// wrong; any OTHER error means the connection could not be put back into a
	// clean state, and reporting the publish as having succeeded on a
	// connection in that condition would be the worse of the two answers.
	defer func() {
		rollbackErr := transaction.Rollback(ctx)
		if err == nil && rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			faults, err = nil, fmt.Errorf("roll back the publish transaction: %w", rollbackErr)
		}
	}()

	faults, err = r.write(ctx, transaction, patch, mode, derive)
	if err != nil {
		return nil, err
	}

	if err = transaction.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit the publish transaction: %w", err)
	}
	return faults, nil
}

// write is the body of the transaction, split out so every failure inside it is
// a plain `return err` and the rollback is stated once.
func (r *CatalogRepository) write(
	ctx context.Context, transaction pgx.Tx,
	patch domain.CatalogPatch, mode domain.UpdateMode, derive domain.DeriveFunc,
) ([]domain.Fault, error) {
	queries := r.queries.WithTx(transaction)

	stored, err := r.loadForMerge(ctx, queries, patch, mode)
	if err != nil {
		return nil, err
	}

	merged, touched, faults := prepare(stored, patch, derive)

	geometryFaults, err := r.persist(ctx, queries, merged, touched, mode)
	if err != nil {
		return nil, err
	}
	return append(faults, geometryFaults...), nil
}

// prepare is everything between the load and the first write: the merge, the
// three pure write-path rules and the post-merge derive.
//
// The rules are domain functions called in this order rather than code written
// here, because the memory backend has to reach the same end state and two
// pieces of code reaching it would agree only until someone changed one. The
// ORDER is what is genuinely per-backend, and it is the only thing this
// function contributes.
func prepare(
	stored domain.Catalog, patch domain.CatalogPatch, derive domain.DeriveFunc,
) (domain.Catalog, []string, []domain.Fault) {
	merged, touched := domain.MergeCatalog(stored, patch)

	merged.EnsureVisibleTo()
	faults := domain.Faults(domain.PruneOfferReferences(&merged), string(beckn.CodeBusinessItemNotFound))

	gate := merged.Gate()
	for index := range merged.Resources {
		gate.ApplyTo(&merged.Resources[index])
	}

	// POST-merge (A8). Derive writes what it computes onto the merged catalog
	// through the pointer and returns only faults; those faults are PARTIALS —
	// the transaction still commits, and only a storage error rolls it back.
	if derive != nil {
		faults = append(faults, derive(&merged, touched)...)
	}
	return merged, touched, faults
}

// persist is the SQL half, in the order the plan's "Inside UpsertCatalog" sets
// out. Every statement in it runs under the catalog row lock the load already
// took, which is why each per-entity loop leaves as one batch.
func (r *CatalogRepository) persist(
	ctx context.Context, queries *gen.Queries,
	merged domain.Catalog, touched []string, mode domain.UpdateMode,
) ([]domain.Fault, error) {
	if err := queries.UpdateCatalogRow(ctx, catalogRowParams(merged)); err != nil {
		return nil, fmt.Errorf("write the catalog row: %w", err)
	}

	if mode == domain.UpdateModeFull {
		if err := deleteOmitted(ctx, queries, merged); err != nil {
			return nil, err
		}
	}

	if err := clearGeometries(ctx, queries, merged.ID, touched); err != nil {
		return nil, err
	}

	// Covering happens before the resource upserts because a cover can FAIL,
	// and a failed cover is a fault rather than an abort; inserting happens
	// after them, because a resource-level geometry row has a foreign key onto
	// (catalog_id, resource_id) and a resource this publish is creating does
	// not exist yet.
	inserts, coverFaults := r.coverGeometries(merged, touched)

	if err := writeResources(ctx, queries, merged, touched); err != nil {
		return nil, err
	}
	if err := runBatch(queries.InsertGeometry(ctx, inserts)); err != nil {
		return nil, fmt.Errorf("write the geometries: %w", err)
	}

	if err := queries.PropagateGate(ctx, gateParams(merged.ID, merged.Gate(), touched)); err != nil {
		return nil, fmt.Errorf("propagate the scope gate: %w", err)
	}

	if err := writeOffers(ctx, queries, merged); err != nil {
		return nil, err
	}

	if mode == domain.UpdateModeFull {
		if err := pruneOrphanedOffers(ctx, queries, merged); err != nil {
			return nil, err
		}
	}

	// LAST, and deliberately: filter_doc projects the three documents this
	// transaction has just settled (A18). RebuildFilterDocs names the three
	// publishes an earlier derivation gets wrong.
	if err := queries.RebuildFilterDocs(ctx, merged.ID); err != nil {
		return nil, fmt.Errorf("rebuild the filter composites: %w", err)
	}
	return coverFaults, nil
}

// loadForMerge takes the row lock and returns what the patch merges against.
//
// Under FULL that is an EMPTY catalog rather than a second code path:
// "omissions reset to defaults, and resources and offers the payload omits are
// deleted" is exactly what merging into nothing does. The lock is still taken —
// the upsert runs in both modes — because a FULL republish races a MERGE one
// just as readily.
func (r *CatalogRepository) loadForMerge(
	ctx context.Context, queries *gen.Queries, patch domain.CatalogPatch, mode domain.UpdateMode,
) (domain.Catalog, error) {
	row, err := queries.LockAndLoadCatalog(ctx, patch.ID)
	if err != nil {
		return domain.Catalog{}, fmt.Errorf("lock the catalog row: %w", err)
	}

	if mode == domain.UpdateModeFull {
		return domain.Catalog{ID: patch.ID, NetworkID: patch.NetworkID}, nil
	}

	stored := storedCatalog(gen.GetCatalogRowRow(row))

	resources, err := queries.ListStoredResources(ctx, patch.ID)
	if err != nil {
		return domain.Catalog{}, fmt.Errorf("load the stored resources: %w", err)
	}
	for _, resource := range resources {
		stored.Resources = append(stored.Resources, storedResource(resource))
	}

	offers, err := queries.ListStoredOffers(ctx, patch.ID)
	if err != nil {
		return domain.Catalog{}, fmt.Errorf("load the stored offers: %w", err)
	}
	for _, offer := range offers {
		stored.Offers = append(stored.Offers, storedOffer(offer))
	}

	// Geometries are deliberately NOT loaded. They have no id to key an
	// identity merge on: the merge happens one level up, on `provider` and on
	// each resource's document, and the rows are rebuilt from whatever the
	// walker finds afterwards.
	return stored, nil
}

// deleteOmitted is the FULL half of A8 — MERGE runs neither statement, which is
// the whole difference between an update and a silent data loss.
func deleteOmitted(ctx context.Context, queries *gen.Queries, merged domain.Catalog) error {
	kept := make([]string, 0, len(merged.Resources))
	for _, resource := range merged.Resources {
		kept = append(kept, resource.ID)
	}
	if err := queries.DeleteResourcesNotIn(ctx, gen.DeleteResourcesNotInParams{
		CatalogID: merged.ID, Kept: kept,
	}); err != nil {
		return fmt.Errorf("delete the resources this FULL republish omitted: %w", err)
	}

	keptOffers := make([]string, 0, len(merged.Offers))
	for _, offer := range merged.Offers {
		keptOffers = append(keptOffers, offer.ID)
	}
	if err := queries.DeleteOffersNotIn(ctx, gen.DeleteOffersNotInParams{
		CatalogID: merged.ID, Kept: keptOffers,
	}); err != nil {
		return fmt.Errorf("delete the offers this FULL republish omitted: %w", err)
	}
	return nil
}

// pruneOrphanedOffers is the delete-then-prune pair, in that order.
//
// Two statements rather than one because an offer that ARRIVES empty means
// catalog-wide and must be kept, while one PRUNED to empty must go.
//
// It is the SECOND of the three defences the missing foreign key on
// `resource_ids` needs, and today it never fires: domain.PruneOfferReferences
// has already removed every dangling reference from the merge result before
// anything reaches SQL, and the merge result is what both this statement and
// that function measure against. What it covers is drift neither can see — a
// row written by an older build, or by hand — which is exactly the case a
// foreign key would have covered. That also makes the plan's "the delete runs
// before the prune" unobservable through the ports; the order is still the one
// stated, because it is the order that stays correct if the domain-side prune
// is ever removed.
func pruneOrphanedOffers(ctx context.Context, queries *gen.Queries, merged domain.Catalog) error {
	if err := queries.PruneOfferResourceIDs(ctx, merged.ID); err != nil {
		return fmt.Errorf("prune the orphaned offer references: %w", err)
	}

	// Only offers that arrived carrying ids are candidates for deletion.
	// Sweeping every offer would take one a publisher deliberately sent
	// catalog-wide, which is a meaning and not an absence.
	candidates := make([]string, 0, len(merged.Offers))
	for _, offer := range merged.Offers {
		if len(offer.ResourceIDs) > 0 {
			candidates = append(candidates, offer.ID)
		}
	}
	if err := queries.DeleteOffersPrunedToEmpty(ctx, gen.DeleteOffersPrunedToEmptyParams{
		CatalogID: merged.ID, Candidates: candidates,
	}); err != nil {
		return fmt.Errorf("delete the offers the prune emptied: %w", err)
	}
	return nil
}

// clearGeometries removes the rows the covers below will replace.
//
// Geometry rows are REPLACED, never merged: a geometry has no id, so there is
// nothing to key an identity merge on. Catalog-level and resource-level rows
// are cleared by two separate statements so neither wipes the other, and only
// TOUCHED resources are cleared — an untouched resource's shapes are still
// current.
func clearGeometries(ctx context.Context, queries *gen.Queries, catalogID string, touched []string) error {
	if err := queries.DeleteCatalogGeometries(ctx, catalogID); err != nil {
		return fmt.Errorf("clear the catalog-level geometries: %w", err)
	}

	deletes := make([]gen.DeleteResourceGeometriesParams, 0, len(touched))
	for _, resourceID := range touched {
		deletes = append(deletes, gen.DeleteResourceGeometriesParams{
			CatalogID: catalogID, ResourceID: owner(resourceID),
		})
	}
	if err := runBatch(queries.DeleteResourceGeometries(ctx, deletes)); err != nil {
		return fmt.Errorf("clear the resource geometries: %w", err)
	}
	return nil
}

// coverCache memoizes an H3 fill for the length of one publish.
//
// Keyed on SourcePath, which is unique per shape within a catalog — it is half
// of the unique index the geometry rows carry. An offer's shape sits on the
// list of every resource that offer covers, so without this the identical fill
// would run once per owner, and the fill is the expensive half of a publish.
type coverCache map[string]geo.Cover

// cover answers for a shape, computing it at most once.
//
// Only successes are cached: a shape that will not cover is the rare path, and
// caching the failure would buy nothing while making the cache hold two kinds
// of thing.
func (c coverCache) cover(shape domain.Geometry, resolution int) (geo.Cover, error) {
	if hit, ok := c[shape.SourcePath]; ok {
		return hit, nil
	}

	computed, err := geo.CoverGeometry(shape, resolution)
	if err != nil {
		return geo.Cover{}, err
	}
	// Bounds is nil only for a shape geo could not bound, which the error above
	// already covers; the columns are NOT NULL, so there is nothing to write.
	if computed.Bounds == nil {
		return geo.Cover{}, errUnboundedGeometry
	}

	c[shape.SourcePath] = computed
	return computed, nil
}

// coverGeometries turns every shape on the merged catalog into insert
// parameters, and a shape that will not cover into a PARTIAL.
//
// The catalog's own provider locations are covered ONCE for the catalog: three
// shapes across forty resources are three rows with a NULL resource_id, not
// 120 rows and 120 H3 fills.
func (r *CatalogRepository) coverGeometries(
	merged domain.Catalog, touched []string,
) ([]gen.InsertGeometryParams, []domain.Fault) {
	var (
		inserts []gen.InsertGeometryParams
		faults  []domain.Fault
	)
	covers := coverCache{}

	add := func(ownerID string, shapes []domain.Geometry) {
		for _, shape := range shapes {
			cover, err := covers.cover(shape, r.resolution)
			if err != nil {
				faults = append(faults, geometryFault(shape, err))
				continue
			}
			inserts = append(inserts, geometryParams(merged.ID, ownerID, shape, cover))
		}
	}

	// The catalog's own provider locations, owned by nobody.
	add("", merged.Geometries)

	// Resource-level shapes, for the touched resources only — the rows of the
	// untouched ones were never cleared, so re-inserting them would collide
	// with themselves. An offer geometry cannot go stale on an untouched
	// resource, because `touched` follows offers: patching the offer that
	// carries the shape touches every resource it covers.
	for _, resource := range merged.Resources {
		if slices.Contains(touched, resource.ID) {
			add(resource.ID, resource.Geometries)
		}
	}
	return inserts, faults
}

// geometryFault names the shape that could not be stored.
//
// By SourcePath, which carries concrete indices, rather than by TargetPath: a
// publisher fixing a bad polygon needs to know WHICH `availableAt` entry it
// was, and the wildcard form names all of them.
func geometryFault(shape domain.Geometry, err error) domain.Fault {
	return domain.Fault{
		Path:    shape.SourcePath,
		Code:    string(beckn.CodeSchemaInvalidFormat),
		Message: fmt.Sprintf("the geometry at %s could not be indexed: %v", shape.SourcePath, err),
	}
}

// batchResults is what all four generated *BatchResults types have in common.
//
// sqlc emits a distinct named type per :batchexec query with no shared
// interface, so this states the shape once instead of at four call sites. It is
// declared structurally: nothing in `gen` has to be aware of it, which is what
// keeps it correct across a regeneration.
type batchResults interface {
	Exec(f func(int, error))
	Close() error
}

// runBatch sends a batch and returns the FIRST statement error.
//
// First, not last: the statements in one batch write one catalog, so the second
// failure is usually a consequence of the first and the first is the one that
// says why. Every statement is still drained — Exec walks the whole batch — so
// the connection is left usable whatever happened.
func runBatch(results batchResults) error {
	var first error
	results.Exec(func(index int, err error) {
		if err != nil && first == nil {
			first = fmt.Errorf("statement %d of the batch: %w", index, err)
		}
	})
	if closeErr := results.Close(); closeErr != nil && first == nil {
		first = closeErr
	}
	return first
}

// writeResources upserts every TOUCHED resource, whole-row, in one batch.
//
// `touched` only. A resource the patch never named is already byte-identical to
// what is stored, and rewriting it would burn a row version, a WAL record and
// an embedding for nothing.
func writeResources(ctx context.Context, queries *gen.Queries, merged domain.Catalog, touched []string) error {
	upserts := make([]gen.UpsertResourceParams, 0, len(touched))
	for _, resource := range merged.Resources {
		if !slices.Contains(touched, resource.ID) {
			continue
		}
		upserts = append(upserts, resourceParams(merged.ID, resource))
	}
	if err := runBatch(queries.UpsertResource(ctx, upserts)); err != nil {
		return fmt.Errorf("write the resources: %w", err)
	}
	return nil
}

// writeOffers writes the offers the prune left, whole-row, in one batch.
func writeOffers(ctx context.Context, queries *gen.Queries, merged domain.Catalog) error {
	upserts := make([]gen.UpsertOfferParams, 0, len(merged.Offers))
	for _, offer := range merged.Offers {
		upserts = append(upserts, offerParams(merged.ID, offer))
	}
	if err := runBatch(queries.UpsertOffer(ctx, upserts)); err != nil {
		return fmt.Errorf("write the offers: %w", err)
	}
	return nil
}

// gateParams is the propagate's arguments, gathered here so the six gate
// columns and the touched list travel together.
func gateParams(catalogID string, gate domain.ScopeGate, touched []string) gen.PropagateGateParams {
	return gen.PropagateGateParams{
		CatalogID:     catalogID,
		VisibleTo:     list(gate.VisibleTo),
		Active:        gate.Active,
		ValidFrom:     timestamp(gate.ValidFrom),
		ValidTo:       timestamp(gate.ValidTo),
		ValidTimeFrom: clock(gate.ValidTimeFrom),
		ValidTimeTo:   clock(gate.ValidTimeTo),
		Touched:       list(touched),
	}
}

// DeleteCatalog removes a catalog and, by cascade, everything under it.
//
// Idempotent: deleting what is not there is not an error. A publisher retrying
// a delete it already completed is ordinary, and a store that failed the second
// attempt would make the retry the thing that reports a problem.
func (r *CatalogRepository) DeleteCatalog(ctx context.Context, catalogID string) error {
	if err := r.queries.DeleteCatalog(ctx, catalogID); err != nil {
		return fmt.Errorf("delete catalog %q: %w", catalogID, err)
	}
	return nil
}

// GetCatalog reads a whole catalog back — row, resources, offers and
// geometries.
//
// Through GetCatalogRow and not the lock-and-load upsert: that statement
// CREATES the row it does not find, so a read routed through it would answer
// "found" for a catalog nobody published and leave it behind.
func (r *CatalogRepository) GetCatalog(ctx context.Context, catalogID string) (domain.Catalog, error) {
	row, err := r.queries.GetCatalogRow(ctx, catalogID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Catalog{}, domain.ErrCatalogNotFound
	}
	if err != nil {
		return domain.Catalog{}, fmt.Errorf("read catalog %q: %w", catalogID, err)
	}
	catalog := storedCatalog(row)

	resources, err := r.ListCatalogResources(ctx, catalogID)
	if err != nil {
		return domain.Catalog{}, err
	}
	catalog.Resources = resources

	offers, err := r.queries.ListStoredOffers(ctx, catalogID)
	if err != nil {
		return domain.Catalog{}, fmt.Errorf("read the offers of catalog %q: %w", catalogID, err)
	}
	for _, offer := range offers {
		catalog.Offers = append(catalog.Offers, storedOffer(offer))
	}

	geometryRows, err := r.queries.ListStoredGeometries(ctx, catalogID)
	if err != nil {
		return domain.Catalog{}, fmt.Errorf("read the geometries of catalog %q: %w", catalogID, err)
	}
	catalogLevel, byResource := geometriesFrom(geometryRows)
	catalog.Geometries = catalogLevel
	for index := range catalog.Resources {
		catalog.Resources[index].Geometries = byResource[catalog.Resources[index].ID]
	}
	return catalog, nil
}

// ListCatalogResources returns the catalog's resources.
//
// It does NOT report a missing catalog: an empty catalog and an absent one both
// hold no resources, and distinguishing them would cost a second query for a
// caller that has no different answer for the two. GetCatalog is where that
// distinction lives.
func (r *CatalogRepository) ListCatalogResources(ctx context.Context, catalogID string) ([]domain.Resource, error) {
	rows, err := r.queries.ListStoredResources(ctx, catalogID)
	if err != nil {
		return nil, fmt.Errorf("list the resources of catalog %q: %w", catalogID, err)
	}
	resources := make([]domain.Resource, 0, len(rows))
	for _, row := range rows {
		resources = append(resources, storedResource(row))
	}
	return resources, nil
}
