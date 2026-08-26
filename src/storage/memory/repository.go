// Package memory is the in-process backend, and the only test double this
// service has for the repository ports.
//
// It exists to prove the port is portable — a seam with one implementation is a
// guess — and to be the double that no hand-rolled mock replaces: a mock
// written by the test that asserts on it proves only that both were written by
// the same person, whereas this passes the same conformance fixtures Postgres
// does.
//
// The write path lives in repository.go and the read path in search.go, which
// is the same split the Postgres adapter makes between its catalog and search
// repositories — one type here, two files, so that the two backends' halves
// stay readable side by side.
package memory

import (
	"context"
	"slices"
	"sync"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
)

// Repository is both ports over one map.
//
// One type rather than two, because a catalog written through the write port
// must be visible through the read port and two types would need a shared store
// between them anyway — at which point the split is a name, not a boundary.
type Repository struct {
	// A RWMutex rather than sync.Map: the write path reads, merges and writes
	// under one lock, which is the row lock Postgres takes and the reason a
	// concurrent republish of one catalog cannot interleave.
	mu       sync.RWMutex
	catalogs map[string]domain.Catalog

	// resolution is the H3 resolution stored geometries are covered at on the
	// READ path. The Postgres side covers once at publish time and keeps the
	// cells in a column; this one has no column, so it covers on demand — and
	// it must do so at the same resolution the query was covered at, or the two
	// cell sets are incomparable and every spatial answer is empty.
	resolution int
}

// New returns an empty store covering geometries at the given H3 resolution.
//
// The resolution is a parameter rather than a package constant for the same
// reason NewCatalogRepository takes one: it is a deployment setting owned by
// the composition root, and a second copy here is a second thing to keep equal
// to config.Indexing — which, being on the other side of the comparison, would
// fail by returning nothing rather than by failing to build.
func New(resolution int) *Repository {
	return &Repository{catalogs: make(map[string]domain.Catalog), resolution: resolution}
}

// UpsertCatalog merges the patch into what is stored and runs derive against
// the result.
//
// The merge itself is domain.MergeCatalog — the same pure function the Postgres
// side calls. That is what keeps the two backends from drifting on the one
// piece of publish semantics that is neither SQL nor I/O; a second merge
// written here would agree with the first only until someone changed one.
func (r *Repository) UpsertCatalog(
	_ context.Context, patch domain.CatalogPatch, mode domain.UpdateMode, derive domain.DeriveFunc,
) ([]domain.Fault, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	merged, touched := domain.MergeCatalog(r.baseFor(patch, mode), patch)

	// The three write-path rules below are domain functions rather than code
	// written here, because the Postgres side has to reach the same end state
	// and a second copy would agree with the first only until someone changed
	// one. What is left in this method is the ORDER, which is the part that is
	// genuinely per-backend.

	// The audience fail-safe first, because the gate copied onto resources two
	// steps down has to carry the FILLED audience, not the empty one.
	merged.EnsureVisibleTo()

	// Then the prune, before derive: an offer that is not going to be stored is
	// not an offer derive should be computing geometry or search text from.
	faults := domain.Faults(domain.PruneOfferReferences(&merged), string(beckn.CodeBusinessItemNotFound))

	// The gate is copied onto EVERY resource, unconditionally, including the
	// ones this payload never mentioned. That unconditional rewrite is the only
	// reason the denormalised copy is safe to read without a join: make it
	// conditional and a resource keeps answering discover after its catalog was
	// withdrawn.
	gate := merged.Gate()
	for index := range merged.Resources {
		gate.ApplyTo(&merged.Resources[index])
	}

	// Derive runs BEFORE the store is updated, but its faults do not abort the
	// write: they are PARTIALS (A8) — the catalog commits and these are the
	// things about it that could not be derived.
	if derive != nil {
		faults = append(faults, derive(&merged, touched)...)
	}

	r.catalogs[patch.ID] = merged
	return faults, nil
}

// baseFor is what the patch merges against, and it is the whole of the
// FULL/MERGE difference (A8).
//
// FULL is MERGE against an EMPTY catalog rather than a separate code path:
// "omissions reset to defaults, and resources and offers the payload omits are
// deleted" is exactly what merging into nothing does. A second branch would be
// a second place for the defaulting rules to live.
func (r *Repository) baseFor(patch domain.CatalogPatch, mode domain.UpdateMode) domain.Catalog {
	if mode == domain.UpdateModeFull {
		return domain.Catalog{ID: patch.ID, NetworkID: patch.NetworkID}
	}
	return r.catalogs[patch.ID]
}

// DeleteCatalog removes a catalog and everything hanging off it.
//
// Idempotent: deleting what is not there is not an error. A publisher retrying
// a delete it already completed is ordinary, and a store that failed the second
// attempt would make the retry the thing that reports a problem.
func (r *Repository) DeleteCatalog(_ context.Context, catalogID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.catalogs, catalogID)
	return nil
}

// GetCatalog returns the stored catalog, or domain.ErrCatalogNotFound.
func (r *Repository) GetCatalog(_ context.Context, catalogID string) (domain.Catalog, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stored, held := r.catalogs[catalogID]
	if !held {
		return domain.Catalog{}, domain.ErrCatalogNotFound
	}
	return cloned(stored), nil
}

// ListCatalogResources returns the catalog's resources, or
// domain.ErrCatalogNotFound.
func (r *Repository) ListCatalogResources(_ context.Context, catalogID string) ([]domain.Resource, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stored, held := r.catalogs[catalogID]
	if !held {
		return nil, domain.ErrCatalogNotFound
	}
	return slices.Clone(stored.Resources), nil
}

// cloned copies the slices a caller could append into.
//
// Shallow, not deep: this is not a general defence against a caller that
// mutates what it was handed, it is the one that stops `append` to a returned
// slice from writing into the store's own backing array. Postgres cannot alias
// its rows to a caller, so a memory backend that did would disagree with it in
// a way no fixture asserts and every fixture depends on.
func cloned(catalog domain.Catalog) domain.Catalog {
	catalog.Resources = slices.Clone(catalog.Resources)
	catalog.Offers = slices.Clone(catalog.Offers)
	catalog.Geometries = slices.Clone(catalog.Geometries)
	catalog.VisibleTo = slices.Clone(catalog.VisibleTo)
	return catalog
}

// matchesSpatial is this backend's spatial stage for one stored geometry: the
// bounding box, then the cell algebra, then the one exact refinement.
//
// The Go twin of Postgres's `CASE spatial_op` predicate, written from the same
// table in the plan's Geospatial Design rather than from it. geo.MatchesOp
// takes no box on purpose, which is what puts the S_DISJOINT exception HERE,
// where the SQL's own version of the decision is visible beside it.
//
// geometries is the set the distance refinement below looks at. Search passes
// ONE — the geometry `stored` was covered from — because that is how the SQL
// evaluates it: box, cells and refinement all sit inside one EXISTS over one
// `resource_geometries` row. Handing the resource's whole set in would let a
// nearby Polygon rescue a distant Point, which no EXISTS over rows can do.
func matchesSpatial(stored geo.Cover, geometries []domain.Geometry, filter domain.SpatialFilter) bool {
	// Six of the seven operators need the two shapes to MEET, so a box that
	// misses pre-rejects them cheaply. S_DISJOINT is the seventh and it
	// inverts: two shapes whose boxes miss entirely ARE disjoint, so ANDing the
	// box in would return exactly the rows near the query — the complement of
	// the truth, with no error to notice.
	if filter.Op != domain.OpDisjoint && !boxesMeet(stored.Bounds, filter.Bounds) {
		return false
	}
	if !cellsAdmit(stored, filter) {
		return false
	}
	return refinedByDistance(geometries, filter)
}

// cellsAdmit is the cell predicate alone, kept separate so the refinement below
// can be shown to only ever narrow what it admitted.
func cellsAdmit(stored geo.Cover, filter domain.SpatialFilter) bool {
	return geo.MatchesOp(filter.Op, stored.CellsFull, stored.CellsCover, filter.CellsFull, filter.CellsCover)
}

// boxesMeet reports whether two boxes overlap, treating a NIL box as "no box"
// rather than as an empty one.
//
// A nil box is a cover that declined — antimeridian, over budget — and a
// declined box cannot reject anything. Reading it as empty would make every
// oversize geometry unfindable, which is the opposite of why the box columns
// survived the redesign.
func boxesMeet(stored, query *domain.BBox) bool {
	if stored == nil || query == nil {
		return true
	}
	return stored.MinLat <= query.MaxLat && stored.MaxLat >= query.MinLat &&
		stored.MinLon <= query.MaxLon && stored.MaxLon >= query.MinLon
}

// refinedByDistance is the Point-to-Point S_DWITHIN refinement, and the ONLY
// place an exact distance decides anything here.
//
// A refinement, never a widening: it can only remove a resource the cells
// already admitted, so the superset guarantee survives it, and it applies to
// exactly one geometry type so no other shape's answer moves.
//
// NearestGeometryM reporting false means "no refinement applies" — nothing in
// the set is a Point — and NOT "no match". Returning false there would drop
// every resource whose only geometry is a Polygon out of an S_DWITHIN, which is
// the inversion this design corrected.
func refinedByDistance(geometries []domain.Geometry, filter domain.SpatialFilter) bool {
	if filter.Op != domain.OpDWithin || filter.Center == nil {
		return true
	}
	metres, refinable := geo.NearestGeometryM(*filter.Center, geometries)
	if !refinable {
		return true
	}
	return metres <= filter.RadiusM
}
