package domain

import (
	"context"
	"errors"
)

// SearchRepository is the read side of the store.
//
// Capabilities is part of the port, not a build-time fact: the negotiation in
// front of Search reads it to choose modes and to fill Degraded.
type SearchRepository interface {
	Search(ctx context.Context, query SearchQuery, modes []Capability) (SearchResult, error)
	Capabilities() Capabilities
}

// Store failures the request path must tell apart from a dead pool.
//
// Declared on the port, not in the adapter that raises them: tests/architecture
// forbids src/discover from importing src/storage/postgres, so a sentinel
// declared there is one the request path can only report as a 500.
var (
	// ErrRetrievalDepth reports a page past what the retrievers reached.
	ErrRetrievalDepth = errors.New("the requested page is past the retrieval depth")

	// ErrInvalidFilterExpression reports an attribute filter the store's own
	// expression parser refused. The text is what the caller is told, so it
	// names the field and nothing about PostgreSQL.
	ErrInvalidFilterExpression = errors.New("filters.expression is not a valid SQL/JSON path")
)
