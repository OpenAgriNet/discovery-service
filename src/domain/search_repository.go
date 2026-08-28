package domain

import (
	"context"
	"errors"
)

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

// ErrRetrievalDepth reports a page that lies past what the retrievers reached.
//
// A sentinel, because the caller has to tell this apart from a query that
// legitimately ran out of results: a fused list holds at most
// MaxCandidatesPerMode ids, so an offset beyond it can only slice past the end,
// and the empty slice it would otherwise answer with is indistinguishable from
// the end of the results.
//
// It lives on the PORT rather than in the adapter that raises it, for a reason
// the import graph makes concrete: tests/architecture forbids src/discover from
// importing src/storage/postgres, so a sentinel declared there is one the
// request path cannot match — and an error it cannot match is an error it
// reports as a 500. Here, any backend may raise it and the one caller that has
// to tell it from a dead pool can.
//
// It is the guard BEHIND the guard. The plan makes the discover mapper the
// owner of this refusal, and the mapper checks the same bound against the same
// config.Search, so a request over HTTP is turned away before a query runs.
// This exists for the paths that do not come through the mapper.
var ErrRetrievalDepth = errors.New("the requested page is past the retrieval depth")
