package embeddings

import "context"

// Fixture answers from vectors committed alongside the tests that expect them.
//
// It lets an acceptance test state the vector a resource has rather than the
// provider that produced it. Asserting a ranking against Hashing pins the
// assertion to the hashing scheme; asserting it against two written-down vectors
// pins it to the ranking.
type Fixture struct {
	dimensions int
	vectors    map[string][]float32
	fallback   *Hashing
}

// NewFixture returns a provider serving vectors, falling back to Hashing for
// any text the table does not list.
//
// The fallback is deliberate: a fixture file is a handful of interesting cases,
// not a corpus. A test publishing fifty resources to exercise paging should not
// have to write fifty vectors.
func NewFixture(dimensions int, vectors map[string][]float32) *Fixture {
	return &Fixture{dimensions: dimensions, vectors: vectors, fallback: NewHashing(dimensions)}
}

// Embed returns the committed vector for text, or the hashed one if there is
// none.
//
// The width is checked on the way out: a fixture file is written by hand, which
// makes it where a wrong width gets in, and the failure has to name the fixture
// rather than surface as a rejected INSERT three layers away.
func (f *Fixture) Embed(ctx context.Context, text string) ([]float32, error) {
	vector, listed := f.vectors[text]
	if !listed {
		return f.fallback.Embed(ctx, text)
	}
	if err := CheckDimensions(vector, f.dimensions); err != nil {
		return nil, err
	}

	// Copied, because the table outlives the call. A caller that normalised the
	// slice it was handed would rewrite the fixture for every later test in the
	// run, and the failure would land in whichever ran second.
	served := make([]float32, len(vector))
	copy(served, vector)
	return served, nil
}

// Dimensions is the configured width of the vector column.
func (f *Fixture) Dimensions() int {
	return f.dimensions
}
