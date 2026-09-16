package embeddings_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/indexing/embeddings"
)

const model = "nomic-embed-text"

// serving stands in for a model server, and records what it was asked. Against
// a real Ollama these tests would be a network dependency in `make test`, which
// is the reason `hashing` is CI's provider in the first place.
func serving(t *testing.T, handler http.HandlerFunc) (*embeddings.Ollama, *[]*http.Request) {
	t.Helper()

	var seen []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = append(seen, request)
		handler(writer, request)
	}))
	t.Cleanup(server.Close)

	return embeddings.NewOllama(server.URL, model, dimensions, time.Second), &seen
}

func answering(t *testing.T, vector []float32) http.HandlerFunc {
	t.Helper()

	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(map[string]any{"embeddings": [][]float32{vector}}); err != nil {
			t.Errorf("encode the stub response: %v", err)
		}
	}
}

func TestOllamaReturnsTheServersVector(t *testing.T) {
	want := make([]float32, dimensions)
	want[7] = 0.25

	embedder, requests := serving(t, answering(t, want))

	got, err := embedder.Embed(context.Background(), "sona masuri rice")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != dimensions || got[7] != 0.25 {
		t.Errorf("got a vector of %d values with got[7] = %v", len(got), got[7])
	}
	if len(*requests) != 1 {
		t.Fatalf("the server saw %d requests, want 1", len(*requests))
	}
	if path := (*requests)[0].URL.Path; path != "/api/embed" {
		t.Errorf("posted to %q, want %q", path, "/api/embed")
	}
}

// The model and the text both have to reach the server. A request that dropped
// the input would still decode a vector, and every test above would pass.
func TestOllamaSendsTheModelAndTheText(t *testing.T) {
	var body struct {
		Model string `json:"model"`
		Input string `json:"input"`
	}

	embedder, _ := serving(t, func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode the request: %v", err)
		}
		answering(t, make([]float32, dimensions))(writer, request)
	})

	if _, err := embedder.Embed(context.Background(), "sona masuri rice"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if body.Model != model {
		t.Errorf("sent model %q, want %q", body.Model, model)
	}
	if body.Input != "sona masuri rice" {
		t.Errorf("sent input %q, want the derived text", body.Input)
	}
}

// A server configured with the wrong model answers with the wrong width, and it
// must be caught here rather than at the INSERT — where it would roll back a
// publish and report a storage error for a configuration mistake.
func TestOllamaRejectsAVectorOfTheWrongWidth(t *testing.T) {
	embedder, _ := serving(t, answering(t, make([]float32, 512)))

	_, err := embedder.Embed(context.Background(), "rice")
	if !errors.Is(err, embeddings.ErrDimensions) {
		t.Errorf("a 512-value response was accepted under a 768 provider: err = %v", err)
	}
}

func TestOllamaReportsAFailedStatus(t *testing.T) {
	embedder, _ := serving(t, func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "model not found", http.StatusNotFound)
	})

	if _, err := embedder.Embed(context.Background(), "rice"); err == nil {
		t.Error("a 404 from the model server was read as a successful embedding")
	}
}

// A body that decodes but holds no vector is not a vector. Reading it as one
// panics on the first index; reading it as nil silently writes NULL and hides a
// broken model server behind a column the backfill will never revisit.
func TestOllamaReportsAnEmptyAnswer(t *testing.T) {
	embedder, _ := serving(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if _, err := writer.Write([]byte(`{"embeddings":[]}`)); err != nil {
			t.Errorf("write the stub response: %v", err)
		}
	})

	if _, err := embedder.Embed(context.Background(), "rice"); err == nil {
		t.Error("an empty embeddings array was read as a successful embedding")
	}
}

// An empty text is answered without a request at all: there is nothing to
// embed, and servers disagree about whether an empty input is an error — which
// would turn a resource with no descriptor into a failed publish.
func TestOllamaDoesNotCallTheServerForAnEmptyText(t *testing.T) {
	embedder, requests := serving(t, answering(t, make([]float32, dimensions)))

	vector, err := embedder.Embed(context.Background(), "  \n ")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if vector != nil {
		t.Errorf("an empty text produced %d values; it must produce none", len(vector))
	}
	if len(*requests) != 0 {
		t.Errorf("the server saw %d requests for an empty text, want 0", len(*requests))
	}
}

// A cancelled context must stop the call, not be discarded in favour of the
// client's own timeout. The publish path cancels on client disconnect, and a
// provider that ignored it would hold the request open for the full deadline.
func TestOllamaHonoursACancelledContext(t *testing.T) {
	embedder, _ := serving(t, answering(t, make([]float32, dimensions)))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := embedder.Embed(ctx, "rice"); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context gave err = %v, want a context.Canceled", err)
	}
}

// A response that answers 200 but is not valid JSON — a server on the
// endpoint that isn't Ollama at all, or a proxy's own error page dressed as
// a success — must be caught by the decode rather than misread as a vector.
func TestOllamaReportsAnUnreadableBody(t *testing.T) {
	embedder, _ := serving(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if _, err := writer.Write([]byte("<html>not json</html>")); err != nil {
			t.Errorf("write the stub response: %v", err)
		}
	})

	if _, err := embedder.Embed(context.Background(), "rice"); err == nil {
		t.Error("an unparseable body was read as a successful embedding")
	}
}

// An endpoint that cannot become a valid request URL is refused before any
// network call, the same way a malformed request never reaches send.
func TestOllamaRefusesAnEndpointThatCannotBuildARequest(t *testing.T) {
	embedder := embeddings.NewOllama("http://\x7f", model, dimensions, time.Second)

	if _, err := embedder.Embed(context.Background(), "rice"); err == nil {
		t.Error("an unbuildable request URL was silently sent")
	}
}

// Ollama satisfies the seam. Asserted here rather than in the provider table,
// because constructing it there would stand a live http.Client up in a test
// that never makes a request.
func TestOllamaSatisfiesTheSeam(t *testing.T) {
	var embedder embeddings.Embedder = embeddings.NewOllama("http://localhost:11434", model, dimensions, time.Second)

	if embedder.Dimensions() != dimensions {
		t.Errorf("Dimensions = %d, want %d", embedder.Dimensions(), dimensions)
	}
}
