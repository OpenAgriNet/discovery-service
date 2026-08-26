package domain

import (
	"context"
	"errors"
)

// ErrCatalogNotFound is what GetCatalog returns when nothing is stored under
// the id.
//
// A sentinel rather than a bare error, because publish is a read-modify-write
// and "nothing stored yet" is its ordinary first case, not a failure: an insert
// and a lookup that genuinely broke must be told apart, and a string comparison
// on the message is how that goes wrong quietly.
var ErrCatalogNotFound = errors.New("catalog not found")

// DeriveFunc is the post-merge seam (A8): everything computed from a catalog
// after the merge has run and before the write commits.
//
// NAMED rather than passed as an untyped parameter, because Task 15 must accept
// it and Task 18 must construct it, and the two are written by different people
// who never read each other's task.
//
// It returns faults and not `error` because an unreadable geometry is a PARTIAL
// — the catalog still commits — so a signature returning `error` would make
// every caller re-invent that distinction.
//
// `touched` is the id set MergeCatalog returned, passed through rather than
// re-derived: a second computation of "which resources did the patch name" is a
// second chance to re-embed a catalog nobody patched.
type DeriveFunc func(merged Catalog, touched []string) []Fault

// CatalogRepository is the write side of the store.
//
// UpsertCatalog takes DeriveFunc as a parameter rather than the repository
// holding an embedder, because the domain must not know that an embedder exists
// and the repository must not own one.
//
// The faults it returns are PARTIALS: the catalog committed, and these are the
// things about it that could not be derived. An error means nothing committed.
type CatalogRepository interface {
	UpsertCatalog(ctx context.Context, patch CatalogPatch, mode UpdateMode, derive DeriveFunc) ([]Fault, error)
	DeleteCatalog(ctx context.Context, catalogID string) error
	GetCatalog(ctx context.Context, catalogID string) (Catalog, error)
	ListCatalogResources(ctx context.Context, catalogID string) ([]Resource, error)
}

// CatalogReplicator is the write fan-out seam (A7).
//
// It takes an id and not a catalog, so a second store re-reads through
// GetCatalog and this interface never becomes a second definition of what a
// catalog is.
//
// Phase 1 ships the no-op. A queue table arrives with the second store that
// needs one, because a queue with no consumer is the `pending_targets` column
// again — the debt A7 removed.
type CatalogReplicator interface {
	Replicate(ctx context.Context, catalogID string) error
}
