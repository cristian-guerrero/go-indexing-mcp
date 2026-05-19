// Package embedder provides an HTTP client for llama.cpp's /v1/embeddings endpoint.
// It batches chunk embeddings, reuses HTTP connections, and pools encoding buffers for efficiency.
package embedder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
)

// bufferPool reuses bytes.Buffer instances for JSON encoding to reduce GC pressure.
var bufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

// Embedder sends text chunks to the llama.cpp embedding API and returns float32 vectors.
type Embedder struct {
	BaseURL     string
	BatchSize   int
	Dimensions  int
	QueryPrefix string
	client      *http.Client
}

// New creates an Embedder pointing at the given llama.cpp base URL,
// with the specified vector dimensions, batch size, and optional query prefix
// for models that distinguish query vs document inputs via a text prefix.
func New(baseURL string, dimensions, batchSize int, queryPrefix string) *Embedder {
	return &Embedder{
		BaseURL:     baseURL,
		BatchSize:   batchSize,
		Dimensions:  dimensions,
		QueryPrefix: queryPrefix,
		client: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// embedRequest is the JSON body for the OpenAI-compatible /v1/embeddings endpoint.
type embedRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model,omitempty"`
}

// embedResponse maps the OpenAI-compatible embedding API response.
type embedResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

// EmbedChunks sends all chunks to the embedding API in batches, returning
// a map of chunk ID → embedding vector (converted from float64 API response to float32).
// Truncates input longer than maxInputLength.
func (e *Embedder) EmbedChunks(chunks []chunker.Chunk) (map[string][]float32, error) {
	result := make(map[string][]float32, len(chunks))

	for i := 0; i < len(chunks); i += e.BatchSize {
		end := i + e.BatchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := chunks[i:end]

		texts := make([]string, len(batch))
		for j, ch := range batch {
			texts[j] = ch.Content
		}

		embeddings, err := e.embed(texts)
		if err != nil {
			return nil, fmt.Errorf("batch %d: %w", i/e.BatchSize, err)
		}

		for j, ch := range batch {
			vec64 := embeddings[j]
			vec32 := make([]float32, len(vec64))
			for k, v := range vec64 {
				vec32[k] = float32(v)
			}
			result[ch.ID] = vec32
		}
	}

	return result, nil
}

// maxInputLength truncates embedding inputs to prevent context-length errors from llama.cpp.
const maxInputLength = 1200

// embed sends a batch of texts to the /v1/embeddings endpoint.
// Handles JSON encoding via buffer pool and connection pooling via http.Transport.
func (e *Embedder) embed(texts []string) ([][]float64, error) {
	for i, t := range texts {
		if len(t) > maxInputLength {
			slog.Debug("truncating long input", "original_len", len(t), "truncated_to", maxInputLength)
			texts[i] = t[:maxInputLength]
		}
	}
	body := embedRequest{Input: texts}

	buf := bufferPool.Get().(*bytes.Buffer)
	defer bufferPool.Put(buf)
	buf.Reset()
	if err := json.NewEncoder(buf).Encode(body); err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", e.BaseURL+"/v1/embeddings", buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding API: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var embedResp embedResponse
	if err := json.Unmarshal(respBody, &embedResp); err != nil {
		return nil, err
	}

	result := make([][]float64, len(embedResp.Data))
	for i, d := range embedResp.Data {
		result[i] = d.Embedding
	}

	return result, nil
}

// EmbedQuery embeds a single search query string and returns its vector (converted to float32).
// If QueryPrefix is non-empty it is prepended to the query text (e.g. "search_query: " or
// "Represent this query for searching relevant code: ").
// The vector is not normalized here — normalization happens in storage.SearchHybrid.
func (e *Embedder) EmbedQuery(query string) ([]float32, error) {
	input := query
	if e.QueryPrefix != "" {
		input = e.QueryPrefix + query
	}
	embeddings, err := e.embed([]string{input})
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}
	vec64 := embeddings[0]
	vec32 := make([]float32, len(vec64))
	for i, v := range vec64 {
		vec32[i] = float32(v)
	}
	return vec32, nil
}
