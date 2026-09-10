package domain

import "context"

// Retriever answers one retrieval mode (A6).
//
// One implementation per mode rather than one interface with a mode parameter,
// so modes run concurrently under one deadline without sharing a code path. It
// returns ranked ids and not rows: fusion compares orders, and most candidates
// are discarded before anything is hydrated.
type Retriever interface {
	Retrieve(ctx context.Context, query SearchQuery, scope Scope) ([]string, error)
}

// Hydrator turns the fused id page into the rows a response is rendered from.
//
// ScopeFilter narrows a set of ids to the ones the scope admits, which a
// retriever cannot always do for itself — a vector index has no notion of
// validity or visibility. There is no Count (A19).
type Hydrator interface {
	ScopeFilter(ctx context.Context, ids []string, scope Scope) ([]string, error)
	Hydrate(ctx context.Context, ids []string, scope Scope) ([]Catalog, error)
}
