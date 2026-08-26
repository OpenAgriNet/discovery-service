package embeddings

import "context"

// Noop is the default provider (A5): it produces no vector and never fails.
//
// Phase 1 ships with semantic search deferred, so this is what production
// actually runs. Every resource it embeds leaves `embedding` NULL, which is
// both the honest representation of "not computed" and the predicate the Phase
// 2 backfill selects on — so turning semantic search on later is a backfill,
// not a republish of the whole corpus.
type Noop struct {
	dimensions int
}

// NewNoop returns the no-vector provider.
//
// It takes a width even though it never produces one, because a caller sizing a
// buffer or asserting against the column reads Dimensions from whichever
// provider is configured and must not have to special-case this one.
func NewNoop(dimensions int) *Noop {
	return &Noop{dimensions: dimensions}
}

// Embed returns no vector and no error, for any text including an empty one.
func (n *Noop) Embed(context.Context, string) ([]float32, error) {
	return nil, nil
}

// Dimensions is the configured width of the vector column.
func (n *Noop) Dimensions() int {
	return n.dimensions
}
