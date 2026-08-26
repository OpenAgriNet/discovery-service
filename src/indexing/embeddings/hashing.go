package embeddings

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// Hashing is CI's provider: the same vector for the same text, on every run and
// every machine, with no model and no network.
//
// It is a hashing vectoriser rather than a digest reshaped into floats, so that
// two texts sharing tokens come out closer than two that share none. Tests that
// assert a semantic ordering therefore assert something, instead of passing
// against a provider whose output is uncorrelated with its input.
//
// It approximates nothing about meaning — "paddy" and "rice" are as far apart
// here as "paddy" and "tractor". It is a test double, and the only claim it
// makes is determinism.
type Hashing struct {
	dimensions int
}

// NewHashing returns the deterministic provider at the given width.
func NewHashing(dimensions int) *Hashing {
	return &Hashing{dimensions: dimensions}
}

// Embed hashes each token of text into one bucket with one sign, then scales
// the result to unit length.
//
// Unit length because cosine distance is what the index will be queried with:
// leaving the magnitude proportional to token count would make a long
// description look distant from a short one that says the same thing.
func (h *Hashing) Embed(_ context.Context, text string) ([]float32, error) {
	// Stated rather than routed through CheckDimensions, which would read a
	// zero-width provider's empty vector as the right width and hand back a
	// silent nil — the one outcome a misconfiguration must not look like.
	if h.dimensions <= 0 {
		return nil, fmt.Errorf("%w: hashing is configured for %d", ErrDimensions, h.dimensions)
	}

	vector := make([]float32, h.dimensions)
	for _, token := range strings.Fields(strings.ToLower(text)) {
		digest := sha256.Sum256([]byte(token))
		bucket := binary.BigEndian.Uint64(digest[:8]) % uint64(h.dimensions)
		if digest[8]&1 == 0 {
			vector[bucket]++
		} else {
			vector[bucket]--
		}
	}
	return normalised(vector), nil
}

// Dimensions is the configured width of the vector column.
func (h *Hashing) Dimensions() int {
	return h.dimensions
}

// normalised scales a vector to unit length, in place.
//
// A vector of all zeros — which an empty text produces, and an empty text is an
// ordinary resource with no descriptor — is returned unchanged. Dividing by its
// zero norm would fill the column with NaN, and NaN reaches pgvector as a row
// that matches nothing and cannot be found again to be repaired.
func normalised(vector []float32) []float32 {
	sum := 0.0
	for _, value := range vector {
		sum += float64(value) * float64(value)
	}
	if sum == 0 {
		return vector
	}

	norm := float32(math.Sqrt(sum))
	for index := range vector {
		vector[index] /= norm
	}
	return vector
}
