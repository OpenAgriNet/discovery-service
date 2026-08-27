package domain

import "context"

// Retriever answers one retrieval mode (A6).
//
// One per mode — lexical, fuzzy, semantic — rather than one interface with a
// mode parameter, so the modes run concurrently under one deadline without
// sharing a code path, and so adding a mode is a new type rather than a new arm
// in an existing switch.
//
// It returns ranked ids and not rows: fusing four modes means comparing their
// orders, and a mode that hydrated its own rows would do that work four times
// over for candidates the fusion is about to discard.
type Retriever interface {
	Retrieve(ctx context.Context, query SearchQuery, scope Scope) ([]string, error)
}

// Hydrator turns the fused id page into the rows a response is rendered from,
// and answers the two questions that need the store but not the ranking.
//
// ScopeFilter narrows a set of ids to the ones the scope admits. It exists
// because a retriever may come from an index that has no notion of validity or
// visibility — a vector index is one — and the gate has to be applied
// somewhere that does.
//
// There is no Count (A19). It was the one method here that answered a question
// no response could carry, and the only one whose cost was not bounded by the
// page.
type Hydrator interface {
	ScopeFilter(ctx context.Context, ids []string, scope Scope) ([]string, error)
	Hydrate(ctx context.Context, ids []string, scope Scope) ([]Catalog, error)
}
