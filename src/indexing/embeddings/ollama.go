package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry"
)

// embedPath is Ollama's embedding route.
const embedPath = "/api/embed"

// Ollama embeds against a local model server, `nomic-embed-text` by default.
type Ollama struct {
	// endpoint comes from configuration and nowhere else. A URL out of a request
	// body would make this a fetch any publisher can aim, which is the
	// distinction EXT_ALLOW_NETWORK_FETCH draws — and it holds here even though
	// nothing today can write to this field.
	endpoint   string
	model      string
	dimensions int
	client     *http.Client
}

// NewOllama returns a provider posting to endpoint, with timeout as the ceiling
// on one embedding call.
//
// The timeout lives on the client as well as the caller's context because this
// call sits inside the publish path: a model server that accepts a connection
// and then stops writing would otherwise hold a publish open for as long as the
// request lives.
func NewOllama(endpoint, model string, dimensions int, timeout time.Duration) *Ollama {
	return &Ollama{
		endpoint:   strings.TrimSuffix(endpoint, "/"),
		model:      model,
		dimensions: dimensions,
		client:     &http.Client{Timeout: timeout},
	}
}

// Embed asks the model server for the vector of text.
//
// An empty text is answered without a request. Servers disagree about whether
// an empty input is an error, which would turn a resource that merely has no
// descriptor into a failed publish.
func (o *Ollama) Embed(ctx context.Context, text string) ([]float32, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}

	body, err := json.Marshal(map[string]string{"model": o.model, "input": text})
	if err != nil {
		return nil, fmt.Errorf("encode the embedding request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint+embedPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build the embedding request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	// Continues the trace into the embedding service, if it is instrumented.
	// retrieval.embedding_ms says how long the call took; the traceparent is what
	// says where inside the model server it went. A no-op when no span is in
	// flight, which is why it is unconditional rather than guarded.
	telemetry.Inject(ctx, request.Header)

	vector, err := o.send(request)
	if err != nil {
		return nil, err
	}
	if err := CheckDimensions(vector, o.dimensions); err != nil {
		return nil, fmt.Errorf("model %q: %w", o.model, err)
	}
	return vector, nil
}

// send performs the request and reads the first vector out of the response.
func (o *Ollama) send(request *http.Request) ([]float32, error) {
	response, err := o.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("reach the embedding service: %w", err)
	}
	return o.readAndClose(response)
}

// readAndClose closes the body whatever the read did, mirroring writeAndClose in
// platform/validation.
//
// A close error is reported only when the read succeeded: when both fail, the
// read is the one that says what went wrong.
func (o *Ollama) readAndClose(response *http.Response) ([]float32, error) {
	vector, readErr := o.firstVector(response)
	closeErr := response.Body.Close()

	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close the embedding response: %w", closeErr)
	}
	return vector, nil
}

// firstVector reads one vector out of a response the caller will close.
func (o *Ollama) firstVector(response *http.Response) ([]float32, error) {
	if response.StatusCode != http.StatusOK {
		// The body is not quoted into the error: it is a remote service's
		// output, and a log field built from it is a log field an operator of
		// that service controls.
		return nil, fmt.Errorf("embedding service answered %s", response.Status)
	}

	var decoded struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode the embedding response: %w", err)
	}
	if len(decoded.Embeddings) == 0 {
		return nil, fmt.Errorf("embedding service returned no vector for model %q", o.model)
	}
	return decoded.Embeddings[0], nil
}

// Dimensions is the configured width of the vector column.
func (o *Ollama) Dimensions() int {
	return o.dimensions
}
