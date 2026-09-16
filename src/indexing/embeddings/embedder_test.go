package embeddings_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/indexing/embeddings"
)

const dimensions = 768

// embedded fails the test rather than returning the error, so a comparison
// below reads as a comparison instead of six lines of error handling.
func embedded(t *testing.T, embedder embeddings.Embedder, text string) []float32 {
	t.Helper()

	vector, err := embedder.Embed(context.Background(), text)
	if err != nil {
		t.Fatalf("Embed(%q): %v", text, err)
	}
	return vector
}

// noop is the default (A5) and the only provider Phase 1 actually runs. It must
// never fail a publish: semantic search being off is a missing capability, not
// a bad request, and a nil vector is exactly what `embedding IS NULL` — the
// Phase 2 backfill queue — is built to hold.
func TestNoopReturnsNoVectorAndNoError(t *testing.T) {
	embedder := embeddings.NewNoop(dimensions)

	vector, err := embedder.Embed(context.Background(), "sona masuri rice")
	if err != nil {
		t.Errorf("noop failed a publish: %v", err)
	}
	if vector != nil {
		t.Errorf("noop returned %d values; it must return none", len(vector))
	}
	if embedder.Dimensions() != dimensions {
		t.Errorf("Dimensions = %d, want %d", embedder.Dimensions(), dimensions)
	}
}

// An empty text is not an error either. A resource whose name and attributes
// are all empty is odd, not invalid, and refusing it here would turn a thin
// catalog into a failed publish.
func TestNoopAcceptsEmptyText(t *testing.T) {
	if _, err := embeddings.NewNoop(dimensions).Embed(context.Background(), ""); err != nil {
		t.Errorf("noop refused an empty text: %v", err)
	}
}

// hashing is CI's provider: no service, no network, and the same vector for the
// same text on every run and every machine. A provider that drifted would make
// every semantic assertion in the suite a coin toss.
func TestHashingIsStableAcrossCalls(t *testing.T) {
	embedder := embeddings.NewHashing(dimensions)

	first, err := embedder.Embed(context.Background(), "sona masuri rice")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	for range 16 {
		again, err := embedder.Embed(context.Background(), "sona masuri rice")
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if len(again) != len(first) {
			t.Fatalf("length moved between calls: %d then %d", len(first), len(again))
		}
		for index := range first {
			if again[index] != first[index] {
				t.Fatalf("value %d moved between calls: %v then %v", index, first[index], again[index])
			}
		}
	}
}

func TestHashingReturnsTheConfiguredWidth(t *testing.T) {
	for _, width := range []int{8, 768, 1536} {
		vector, err := embeddings.NewHashing(width).Embed(context.Background(), "rice")
		if err != nil {
			t.Fatalf("Embed at width %d: %v", width, err)
		}
		if len(vector) != width {
			t.Errorf("hashing at width %d returned %d values", width, len(vector))
		}
	}
}

// Different texts must not collide into one vector, or every semantic test
// passes against a provider that has stopped reading its input.
func TestHashingSeparatesDifferentTexts(t *testing.T) {
	embedder := embeddings.NewHashing(dimensions)

	rice := embedded(t, embedder, "sona masuri rice")
	tractor := embedded(t, embedder, "tractor hire")

	same := true
	for index := range rice {
		if rice[index] != tractor[index] {
			same = false
			break
		}
	}
	if same {
		t.Error("two different texts embedded identically")
	}
}

// The dimension guard, and the reason it is here rather than at the column: a
// vector of the wrong width reaches pgvector as a failed INSERT inside the
// publish transaction, which rolls back a catalog that was otherwise fine and
// reports a storage error for what is a misconfigured provider.
func TestAWrongWidthVectorIsRejectedWithAClearError(t *testing.T) {
	err := embeddings.CheckDimensions(make([]float32, 512), dimensions)
	if err == nil {
		t.Fatal("a 512-value vector passed a 768 guard")
	}
	if !errors.Is(err, embeddings.ErrDimensions) {
		t.Errorf("error %v is not an ErrDimensions", err)
	}
	// The message has to name both widths. "invalid vector" sends an operator
	// to the wrong service; "512, want 768" names the provider that is wrong.
	if !strings.Contains(err.Error(), "512") || !strings.Contains(err.Error(), "768") {
		t.Errorf("error %q names neither the width it got nor the width it wanted", err)
	}
}

// A nil vector is what noop returns on every publish in Phase 1. Reading it as
// a width violation would make the default provider fail every publish.
func TestTheGuardAcceptsNoVectorAtAll(t *testing.T) {
	if err := embeddings.CheckDimensions(nil, dimensions); err != nil {
		t.Errorf("the guard rejected an absent vector: %v", err)
	}
}

func TestTheGuardAcceptsTheConfiguredWidth(t *testing.T) {
	if err := embeddings.CheckDimensions(make([]float32, dimensions), dimensions); err != nil {
		t.Errorf("the guard rejected a correct vector: %v", err)
	}
}

// Every provider must satisfy the seam. Asserted over the set rather than one
// at a time, because the compiler is the only thing that can catch a provider
// added later that drifts from it.
func TestEveryProviderSatisfiesTheSeam(t *testing.T) {
	providers := map[string]embeddings.Embedder{
		"noop":    embeddings.NewNoop(dimensions),
		"hashing": embeddings.NewHashing(dimensions),
		"fixture": embeddings.NewFixture(dimensions, nil),
	}

	for name, embedder := range providers {
		if embedder.Dimensions() != dimensions {
			t.Errorf("%s reports %d dimensions, want %d", name, embedder.Dimensions(), dimensions)
		}
	}
}

// fixture answers from committed vectors, so an acceptance test can state the
// vector it expects rather than the provider that produced it. An unknown text
// falls back to hashing rather than failing: a fixture file is a set of
// interesting cases, not an exhaustive corpus.
func TestFixtureAnswersFromItsTableAndFallsBackToHashing(t *testing.T) {
	known := make([]float32, dimensions)
	known[0] = 0.5

	embedder := embeddings.NewFixture(dimensions, map[string][]float32{"sona masuri rice": known})

	got, err := embedder.Embed(context.Background(), "sona masuri rice")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != dimensions || got[0] != 0.5 {
		t.Errorf("the fixture vector was not returned: got[0] = %v, len = %d", got[0], len(got))
	}

	unknown, err := embedder.Embed(context.Background(), "tractor hire")
	if err != nil {
		t.Fatalf("Embed on an unlisted text: %v", err)
	}
	if len(unknown) != dimensions {
		t.Errorf("the fallback returned %d values, want %d", len(unknown), dimensions)
	}
}

// A fixture file is committed by hand, so it is exactly where a wrong width
// gets in — and it must be caught when the vector is served, not when Postgres
// refuses the insert three layers away.
func TestAFixtureVectorOfTheWrongWidthIsRefused(t *testing.T) {
	embedder := embeddings.NewFixture(dimensions, map[string][]float32{"rice": make([]float32, 512)})

	_, err := embedder.Embed(context.Background(), "rice")
	if !errors.Is(err, embeddings.ErrDimensions) {
		t.Errorf("a 512-value fixture vector was served under a 768 provider: err = %v", err)
	}
}

// An empty text hashes into no buckets at all, so the vector is all zeros and
// its norm is zero. Scaling it would fill the column with NaN — a row that
// matches nothing, ranks nowhere, and cannot be found again to be repaired.
func TestHashingOnEmptyTextIsZeroRatherThanNaN(t *testing.T) {
	vector, err := embeddings.NewHashing(dimensions).Embed(context.Background(), "   ")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vector) != dimensions {
		t.Fatalf("an empty text returned %d values, want %d", len(vector), dimensions)
	}
	for index, value := range vector {
		if value != 0 {
			t.Fatalf("value %d of an empty text's vector is %v, want 0", index, value)
		}
	}
}

// Unit length, because the index is queried by cosine distance: a vector whose
// magnitude tracked token count would put a long description far from a short
// one saying the same thing.
func TestHashingReturnsAUnitVector(t *testing.T) {
	vector, err := embeddings.NewHashing(dimensions).Embed(context.Background(), "sona masuri rice from raichur")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	sum := 0.0
	for _, value := range vector {
		sum += float64(value) * float64(value)
	}
	if math.Abs(math.Sqrt(sum)-1) > 1e-6 {
		t.Errorf("the vector's length is %v, want 1", math.Sqrt(sum))
	}
}

// Texts sharing tokens must land closer than texts sharing none, or a CI double
// that is merely deterministic will pass every semantic assertion in the suite
// while having no relationship to its input at all.
func TestHashingPutsSharedTokensCloserThanUnsharedOnes(t *testing.T) {
	embedder := embeddings.NewHashing(dimensions)

	rice := embedded(t, embedder, "sona masuri rice")
	similar := embedded(t, embedder, "sona masuri rice bag")
	unrelated := embedded(t, embedder, "tractor hire hourly")

	if dot(rice, similar) <= dot(rice, unrelated) {
		t.Errorf("a text sharing three tokens scored %v, no better than one sharing none at %v",
			dot(rice, similar), dot(rice, unrelated))
	}
}

func dot(left, right []float32) float64 {
	sum := 0.0
	for index := range left {
		sum += float64(left[index]) * float64(right[index])
	}
	return sum
}

// A provider configured with no width has nothing to produce, and must say so.
// Returning a nil vector instead would be indistinguishable from noop — a
// misconfiguration that reads, at every call site, as a deferred feature.
func TestHashingAtZeroWidthIsAnErrorRatherThanNoVector(t *testing.T) {
	vector, err := embeddings.NewHashing(0).Embed(context.Background(), "rice")
	if !errors.Is(err, embeddings.ErrDimensions) {
		t.Errorf("a zero-width provider gave err = %v, want an ErrDimensions", err)
	}
	if vector != nil {
		t.Errorf("a zero-width provider returned %d values", len(vector))
	}
}
