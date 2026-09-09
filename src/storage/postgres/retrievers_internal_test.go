package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// 23d's retrieval.embedding_ms, tested where the duration is measured.
//
// No database: queryVector talks to an Embedder and to nothing else, so the
// whole of what these two tests need is a context with a record in it.

// slowEmbedder produces a vector after a known delay, so the assertion is about
// a measured duration rather than about however fast the machine ran.
type slowEmbedder struct {
	took   time.Duration
	values []float32
}

func (e slowEmbedder) Embed(context.Context, string) ([]float32, error) {
	time.Sleep(e.took)
	return e.values, nil
}

func (e slowEmbedder) Dimensions() int { return len(e.values) }

// nilEmbedder is the shape embeddings.Noop has: no vector, no error, for any
// text. It is restated here rather than imported so this test states the
// property it depends on — a nil vector — instead of depending on Noop keeping
// it.
type nilEmbedder struct{}

func (nilEmbedder) Embed(context.Context, string) ([]float32, error) { return nil, nil }
func (nilEmbedder) Dimensions() int                                  { return 8 }

// TestTheEmbeddingDurationIsObservedWhenAVectorIsComputed.
//
// The delta between request_info and retrieval_info bundles the embedding call
// and the storage call into one number; this is what splits them, and it is
// worth having precisely because the embedding call is a network hop to Ollama
// on the deployment that turns semantic search on.
func TestTheEmbeddingDurationIsObservedWhenAVectorIsComputed(t *testing.T) {
	const took = 3 * time.Millisecond

	ctx, record := fact.New(t.Context())

	embedder := slowEmbedder{took: took, values: []float32{1, 2, 3, 4}}
	if _, err := queryVector(ctx, embedder, "wheat"); err != nil {
		t.Fatalf("queryVector: %v", err)
	}

	observed, found := record.Lookup(fact.RetrievalEmbeddingMs)
	if !found {
		t.Fatalf("retrieval.embedding_ms was never observed for a computed vector")
	}

	// Only that it measured the embedding rather than measuring nothing: a
	// tighter upper bound would be a test that fails on a loaded CI runner for
	// a reason that has nothing to do with this code.
	if want := float64(took/time.Millisecond) * 0.9; observed.Float < want {
		t.Errorf("retrieval.embedding_ms = %v, want at least %v — the embedder slept %v",
			observed.Float, want, took)
	}
}

// TestTheEmbeddingDurationIsAbsentUnderANilVector — the acceptance criterion in
// opentelemetry.md §How the derivation happens, in the configuration Phase 1
// actually ships as.
//
// Absent and not zero. Zero would be a claim that an embedding was computed in
// no measurable time, which is a different and false statement, and it is the
// one an average over the attribute would take at face value.
func TestTheEmbeddingDurationIsAbsentUnderANilVector(t *testing.T) {
	ctx, record := fact.New(t.Context())

	vector, err := queryVector(ctx, nilEmbedder{}, "wheat")
	if err != nil {
		t.Fatalf("queryVector: %v", err)
	}
	if vector != nil {
		t.Errorf("queryVector = %v for a nil-vector embedder, want nil", vector)
	}

	if observed, found := record.Lookup(fact.RetrievalEmbeddingMs); found {
		t.Errorf("retrieval.embedding_ms = %v; nothing was embedded, so it must be absent",
			observed.Float)
	}
}
