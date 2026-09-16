// Package embeddings is the seam between a resource's derived text and the
// vector stored beside it.
//
// The vector is optional. A5 defers semantic search, so the default provider
// returns nothing and the column stays NULL — which is what the Phase 2 backfill
// scans for. Everything here is built so "no vector" is an ordinary outcome and
// never an error a publisher sees.
package embeddings

import (
	"context"
	"errors"
	"fmt"
)

// Embedder turns derived search text into the vector stored on the resource.
//
// A nil vector is not an error: a publish whose semantic index is deferred
// succeeded with a missing capability, and failing it here would take a catalog
// offline because a model was unreachable.
type Embedder interface {
	// Embed returns the vector for text, or nil when this provider produces
	// none. The context carries the provider's write deadline.
	Embed(ctx context.Context, text string) ([]float32, error)

	// Dimensions is the width every non-nil vector has, and the width the column
	// was created with. Callers guard against it rather than a constant, so a
	// redeployment at a new width is a config change and not a code change.
	Dimensions() int
}

// ErrDimensions reports a vector whose width is not the one the column holds.
//
// A sentinel because the publish path has to tell this apart from a transport
// failure: a wrong width is a misconfigured provider, permanent under retry,
// while a timeout is worth another attempt.
var ErrDimensions = errors.New("embedding has the wrong number of dimensions")

// CheckDimensions rejects a vector the resource's column cannot hold.
//
// At the provider rather than at the INSERT: pgvector's width check fires inside
// the publish transaction and rolls back an otherwise correct catalog, reporting
// a storage failure for a configuration mistake three layers up. Caught here it
// names the provider instead.
//
// A nil vector passes — that is what noop returns on every Phase 1 publish, and
// reading it as a width violation would fail them all.
func CheckDimensions(vector []float32, dimensions int) error {
	if vector == nil || len(vector) == dimensions {
		return nil
	}
	return fmt.Errorf("%w: got %d, want %d", ErrDimensions, len(vector), dimensions)
}
