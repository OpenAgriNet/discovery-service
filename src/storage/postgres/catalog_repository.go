package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres/gen"
)

// errUnboundedGeometry reports a shape that produced no bounding box. The box
// columns are NOT NULL, so the row cannot be written.
var errUnboundedGeometry = errors.New("the geometry produced no bounding box")

// CatalogRepository is the write half of the PostgreSQL adapter.
type CatalogRepository struct {
	pool    *pgxpool.Pool
	queries *gen.Queries

	// resolution is the H3 resolution stored covers are built at
	// (GEO_RESOLUTION_CELLS). A store that covered at one resolution while the
	// query covered at another would return nothing and report nothing wrong.
	resolution int
}

// NewCatalogRepository builds the write repository over a pool.
func NewCatalogRepository(pool *pgxpool.Pool, resolutionCells int) *CatalogRepository {
	return &CatalogRepository{pool: pool, queries: gen.New(pool), resolution: resolutionCells}
}

var _ domain.CatalogRepository = (*CatalogRepository)(nil)

// UpsertCatalog is the whole write path, in one transaction.
//
// The order inside is load-bearing (the plan's "Inside UpsertCatalog"): the
// lock-and-load upsert takes the catalog's row lock FIRST, so two concurrent
// republishes of one catalog serialise instead of interleaving two
// read-modify-writes. Everything after it is paid for under that lock, which is
// why each loop leaves as one pgx.Batch.
func (r *CatalogRepository) UpsertCatalog(
	ctx context.Context, patch domain.CatalogPatch, mode domain.UpdateMode, derive domain.DeriveFunc,
) (faults []domain.Fault, err error) {
	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin the publish transaction: %w", err)
	}

	// Unconditional: a rollback guarded by a condition is one a new early return
	// eventually skips, and the failure mode is a half-written catalog. After a
	// successful Commit this returns pgx.ErrTxClosed, the ordinary path; any
	// other error means the connection is not in a clean state, so the publish
	// is not reported as having succeeded.
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
// The rules are domain functions rather than code written here, because the
// memory backend has to reach the same end state. Only the ORDER is
// per-backend, and it is all this function contributes.
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

	// POST-merge (A8). Derive writes through the pointer and returns only
	// faults, which are PARTIALS: the transaction still commits.
	if derive != nil {
		faults = append(faults, derive(&merged, touched)...)
	}
	return merged, touched, faults
}

// persist is the SQL half, in the order the plan's "Inside UpsertCatalog" sets
// out. Every statement runs under the row lock the load already took.
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

	// Covering runs before the resource upserts because a failed cover is a
	// fault rather than an abort; the INSERT runs after them, because a
	// resource-level geometry row has a foreign key onto (catalog_id,
	// resource_id) and a resource this publish creates does not exist yet.
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
	// transaction has just settled (A18).
	if err := queries.RebuildFilterDocs(ctx, merged.ID); err != nil {
		return nil, fmt.Errorf("rebuild the filter composites: %w", err)
	}
	return coverFaults, nil
}

// loadForMerge takes the row lock and returns what the patch merges against.
//
// Under FULL that is an EMPTY catalog rather than a second code path: merging
// into nothing is what "omissions reset to defaults" means. The lock is taken
// in both modes, because a FULL republish races a MERGE one just as readily.
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

	// Geometries are deliberately NOT loaded: they have no id to key an
	// identity merge on. The merge happens one level up, on `provider` and each
	// resource's document, and the rows are rebuilt from what the walker finds.
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
// catalog-wide and is kept, while one PRUNED to empty must go. The second of
// the three defences the missing foreign key on `resource_ids` needs; it covers
// drift the domain-side prune cannot see.
func pruneOrphanedOffers(ctx context.Context, queries *gen.Queries, merged domain.Catalog) error {
	if err := queries.PruneOfferResourceIDs(ctx, merged.ID); err != nil {
		return fmt.Errorf("prune the orphaned offer references: %w", err)
	}

	// Only offers that arrived carrying ids are candidates. Sweeping every offer
	// would take one a publisher deliberately sent catalog-wide.
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
// Geometry rows are REPLACED, never merged, since a geometry has no id. The two
// statements keep catalog-level and resource-level rows from wiping each other,
// and only TOUCHED resources are cleared — an untouched one's shapes are still
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

// coverCache memoizes an H3 fill for the length of one publish, keyed on
// SourcePath — unique per shape within a catalog. An offer's shape sits on
// every resource that offer covers, so without this the identical fill runs
// once per owner.
type coverCache map[string]geo.Cover

// cover answers for a shape, computing it at most once. Only successes are
// cached.
func (c coverCache) cover(shape domain.Geometry, resolution int) (geo.Cover, error) {
	if hit, ok := c[shape.SourcePath]; ok {
		return hit, nil
	}

	computed, err := geo.CoverGeometry(shape, resolution)
	if err != nil {
		return geo.Cover{}, err
	}
	// The box columns are NOT NULL, so a shape with no bounds has no row.
	if computed.Bounds == nil {
		return geo.Cover{}, errUnboundedGeometry
	}

	c[shape.SourcePath] = computed
	return computed, nil
}

// coverGeometries turns every shape on the merged catalog into insert
// parameters, and a shape that will not cover into a PARTIAL.
//
// The catalog's own provider locations are covered ONCE for the catalog, as
// rows with a NULL resource_id, not once per resource.
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

	// Touched resources only: the untouched ones' rows were never cleared, so
	// re-inserting them would collide with themselves. An offer geometry cannot
	// go stale on an untouched resource, because `touched` follows offers.
	inPatch := domain.NewTouchedSet(touched)
	for _, resource := range merged.Resources {
		if inPatch.Has(resource.ID) {
			add(resource.ID, resource.Geometries)
		}
	}
	return inserts, faults
}

// geometryFault names the shape that could not be stored, by SourcePath rather
// than TargetPath: the concrete indices say WHICH `availableAt` entry it was.
func geometryFault(shape domain.Geometry, err error) domain.Fault {
	return domain.Fault{
		Path:    shape.SourcePath,
		Code:    string(beckn.CodeSchemaInvalidFormat),
		Message: fmt.Sprintf("the geometry at %s could not be indexed: %v", shape.SourcePath, err),
	}
}

// batchResults is what the generated *BatchResults types have in common. sqlc
// emits a distinct named type per :batchexec query with no shared interface;
// this is structural, so a regeneration cannot break it.
type batchResults interface {
	Exec(f func(int, error))
	Close() error
}

// runBatch sends a batch and returns the FIRST statement error — later ones are
// usually a consequence of it. Every statement is still drained, so the
// connection is left usable.
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

// writeResources upserts every TOUCHED resource, whole-row, in one batch. A
// resource the patch never named is already byte-identical to what is stored.
func writeResources(ctx context.Context, queries *gen.Queries, merged domain.Catalog, touched []string) error {
	inPatch := domain.NewTouchedSet(touched)
	upserts := make([]gen.UpsertResourceParams, 0, len(touched))
	for _, resource := range merged.Resources {
		if !inPatch.Has(resource.ID) {
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

// gateParams is the propagate's arguments — the six gate columns and the
// touched list.
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
// Idempotent: deleting what is not there is not an error.
func (r *CatalogRepository) DeleteCatalog(ctx context.Context, catalogID string) error {
	if err := r.queries.DeleteCatalog(ctx, catalogID); err != nil {
		return fmt.Errorf("delete catalog %q: %w", catalogID, err)
	}
	return nil
}

// GetCatalog reads a whole catalog back — row, resources, offers and
// geometries.
//
// Through GetCatalogRow and never the lock-and-load upsert: that statement
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

// ListCatalogResources returns the catalog's resources. It does NOT report a
// missing catalog — an empty one and an absent one both hold no resources.
// GetCatalog is where that distinction lives.
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
