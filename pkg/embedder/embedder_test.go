package embedder

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
)

func TestNew(t *testing.T) {
	e := New("http://localhost:56000", 768, 8, "")
	if e == nil {
		t.Fatal("expected non-nil embedder")
	}
	if e.BaseURL != "http://localhost:56000" {
		t.Errorf("BaseURL = %q, want http://localhost:56000", e.BaseURL)
	}
	if e.BatchSize != 8 {
		t.Errorf("BatchSize = %d, want 8", e.BatchSize)
	}
	if e.Dimensions != 768 {
		t.Errorf("Dimensions = %d, want 768", e.Dimensions)
	}
	if e.client == nil {
		t.Error("expected non-nil HTTP client")
	}
}

func TestEmbed_Texts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected content-type: %s", r.Header.Get("Content-Type"))
		}

		var req embedRequest
		json.NewDecoder(r.Body).Decode(&req)

		resp := embedResponse{Data: make([]struct {
			Embedding []float64 `json:"embedding"`
		}, len(req.Input))}
		for i := range req.Input {
			resp.Data[i].Embedding = []float64{float64(i), 0, 0}
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := New(srv.URL, 3, 2, "")
	embeddings, err := e.embed([]string{"hello", "world"})
	if err != nil {
		t.Fatal(err)
	}
	if len(embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(embeddings))
	}
	if embeddings[0][0] != 0 {
		t.Errorf("expected first embedding[0]=0, got %f", embeddings[0][0])
	}
}

func TestEmbed_TruncatesLongInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Input[0]) > maxInputLength {
			t.Errorf("input was not truncated, len=%d", len(req.Input[0]))
		}
		if len(req.Input[0]) != maxInputLength {
			t.Errorf("expected truncated length %d, got %d", maxInputLength, len(req.Input[0]))
		}
		resp := embedResponse{Data: []struct {
			Embedding []float64 `json:"embedding"`
		}{{[]float64{1, 0, 0}}}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := New(srv.URL, 3, 2, "")
	_, err := e.embed([]string{strings.Repeat("a", maxInputLength+100)})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEmbed_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer srv.Close()

	e := New(srv.URL, 3, 2, "")
	_, err := e.embed([]string{"test"})
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 in error, got %s", err.Error())
	}
}

func TestEmbed_InvalidJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{invalid json}"))
	}))
	defer srv.Close()

	e := New(srv.URL, 3, 2, "")
	_, err := e.embed([]string{"test"})
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestEmbed_ServerUnreachable(t *testing.T) {
	e := New("http://127.0.0.1:1", 3, 2, "")
	_, err := e.embed([]string{"test"})
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestEmbedChunks_Batching(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req embedRequest
		json.NewDecoder(r.Body).Decode(&req)

		resp := embedResponse{Data: make([]struct {
			Embedding []float64 `json:"embedding"`
		}, len(req.Input))}
		for i := range req.Input {
			resp.Data[i].Embedding = []float64{float64(callCount), 0, 0}
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := New(srv.URL, 3, 2, "")
	chunks := []chunker.Chunk{
		{ID: "c1", Content: "a"},
		{ID: "c2", Content: "b"},
		{ID: "c3", Content: "c"},
	}
	embeddings, err := e.EmbedChunks(chunks)
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 batches (batchSize=2), got %d", callCount)
	}
	if len(embeddings) != 3 {
		t.Errorf("expected 3 results, got %d", len(embeddings))
	}
	if _, ok := embeddings["c1"]; !ok {
		t.Error("expected c1 in results")
	}
}

func TestEmbedChunks_EmptyInput(t *testing.T) {
	e := New("http://localhost:56000", 3, 2, "")
	embeddings, err := e.EmbedChunks(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(embeddings) != 0 {
		t.Errorf("expected empty result for nil input, got %d", len(embeddings))
	}

	embeddings, err = e.EmbedChunks([]chunker.Chunk{})
	if err != nil {
		t.Fatal(err)
	}
	if len(embeddings) != 0 {
		t.Errorf("expected empty result for empty input, got %d", len(embeddings))
	}
}

func TestEmbedChunks_BatchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	e := New(srv.URL, 3, 2, "")
	chunks := []chunker.Chunk{
		{ID: "c1", Content: "a"},
		{ID: "c2", Content: "b"},
	}
	_, err := e.EmbedChunks(chunks)
	if err == nil {
		t.Fatal("expected error for failed batch")
	}
}

func TestEmbedQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embedResponse{Data: []struct {
			Embedding []float64 `json:"embedding"`
		}{{[]float64{0.5, 0.5, 0.5}}}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := New(srv.URL, 3, 2, "")
	vec, err := e.EmbedQuery("test query")
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != 3 {
		t.Errorf("expected 3-dim vector, got %d", len(vec))
	}
	if vec[0] != 0.5 {
		t.Errorf("expected 0.5, got %f", vec[0])
	}
}

func TestEmbedQuery_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embedResponse{Data: []struct {
			Embedding []float64 `json:"embedding"`
		}{}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := New(srv.URL, 3, 2, "")
	_, err := e.EmbedQuery("test")
	if err == nil {
		t.Fatal("expected error for empty response")
	}
}
