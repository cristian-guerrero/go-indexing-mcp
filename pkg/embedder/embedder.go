package embedder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cristian/go-indexing-mcp/pkg/chunker"
)

type Embedder struct {
	BaseURL    string
	BatchSize  int
	Dimensions int
	client     *http.Client
}

func New(baseURL string, dimensions, batchSize int) *Embedder {
	return &Embedder{
		BaseURL:    baseURL,
		BatchSize:  batchSize,
		Dimensions: dimensions,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

type embedRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model,omitempty"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

func (e *Embedder) EmbedChunks(chunks []chunker.Chunk) (map[string][]float64, error) {
	result := make(map[string][]float64, len(chunks))

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
			result[ch.ID] = embeddings[j]
		}
	}

	return result, nil
}

func (e *Embedder) embed(texts []string) ([][]float64, error) {
	body := embedRequest{Input: texts}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", e.BaseURL+"/v1/embeddings", bytes.NewReader(data))
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

func (e *Embedder) EmbedQuery(query string) ([]float64, error) {
	embeddings, err := e.embed([]string{query})
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}
	return embeddings[0], nil
}
