package domain

import (
	"context"
	"errors"
)

// ErrCatalogNotFound is what GetCatalog returns when nothing is stored under
// the id.
//
// A sentinel because publish is a read-modify-write, where "nothing stored yet"
// is the ordinary first case rather than a failure.
var ErrCatalogNotFound = errors.New("catalog not found")

// DeriveFunc is the post-merge seam (A8): everything computed from a catalog
// after the merge has run and before the write commits.
//
// It returns faults and not error because an unreadable geometry is a partial —
// the catalog still commits. `merged` is a pointer and must stay one (A14):
// derive delivers everything it computes by writing onto it, and a value
// parameter would silently drop the catalog-level fields.
type DeriveFunc func(merged *Catalog, touched []string) []Fault

// CatalogRepository is the write side of the store.
//
// The faults UpsertCatalog returns are partials: the catalog committed, and
// these are the things about it that could not be derived. An error means
// nothing committed.
type CatalogRepository interface {
	UpsertCatalog(ctx context.Context, patch CatalogPatch, mode UpdateMode, derive DeriveFunc) ([]Fault, error)
	DeleteCatalog(ctx context.Context, catalogID string) error
	GetCatalog(ctx context.Context, catalogID string) (Catalog, error)
	ListCatalogResources(ctx context.Context, catalogID string) ([]Resource, error)
}

// CatalogReplicator is the write fan-out seam (A7).
//
// It takes an id and not a catalog, so a second store re-reads through
// GetCatalog and this never becomes a second definition of what a catalog is.
// Phase 1 ships the no-op.
type CatalogReplicator interface {
	Replicate(ctx context.Context, catalogID string) error
}
