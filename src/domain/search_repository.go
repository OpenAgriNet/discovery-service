package domain

import "context"

// SearchRepository is the read side of the store.
//
// Capabilities is part of the port rather than a build-time fact, because the
// negotiation in front of Search reads it to decide which modes to run and what
// to report in Degraded. A backend that cannot answer a mode says so here; it
// never answers the query badly instead.
type SearchRepository interface {
	Search(ctx context.Context, query SearchQuery, modes []Capability) (SearchResult, error)
	Capabilities() Capabilities
}
