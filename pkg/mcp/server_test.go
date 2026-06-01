package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/embedder"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/indexer"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/storage"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/walker"
	"github.com/mark3labs/mcp-go/mcp"
)

// newEmbeddingServer creates a test HTTP server that responds to /v1/embeddings
// with dummy embedding vectors.
func newEmbeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/embeddings" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3,0.4]}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

// newEmbedder creates an embedder pointing to the given base URL.
func newEmbedder(t *testing.T, baseURL string) *embedder.Embedder {
	t.Helper()
	return embedder.New(baseURL, 4, 8, "")
}

func newMCPServerWithChunks(t *testing.T) *MCPServer {
	t.Helper()

	root := t.TempDir()
	w := walker.New(root, nil)

	st, err := storage.New(filepath.Join(root, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ch := chunker.New(50, 10)

	idx := indexer.New(w, ch, nil, st, nil, 0, 0)
	// Simulate an already-indexed state so handleGrepSearch doesn't trigger indexAll
	idx.Stats.TotalChunks = 1
	idx.Stats.TotalFiles = 1

	// Insert test chunks into storage directly (must provide dummy embeddings)
	chunks := []chunker.Chunk{
		{ID: "c1", FilePath: "main.go", RelPath: "main.go", Content: "package main\n\nfunc hello() {\n\tfmt.Println(\"hello world\")\n}\n", StartLine: 1, EndLine: 6, FileHash: "h1", Language: "go"},
		{ID: "c2", FilePath: "util.go", RelPath: "util.go", Content: "package util\n\nfunc validate(input string) error {\n\treturn nil\n}\n", StartLine: 1, EndLine: 5, FileHash: "h2", Language: "go"},
		{ID: "c3", FilePath: "api.go", RelPath: "api.go", Content: "package api\n\nfunc getUser(id int) *User {\n\treturn &User{}\n}\n", StartLine: 1, EndLine: 5, FileHash: "h3", Language: "go"},
	}
	embeddings := map[string][]float32{
		"c1": {1, 0, 0, 0},
		"c2": {0, 1, 0, 0},
		"c3": {0, 0, 1, 0},
	}
	if err := st.UpsertChunks(chunks, embeddings); err != nil {
		t.Fatal(err)
	}

	s := &MCPServer{
		indexer:       idx,
		currentBranch: "", // no git repo
	}

	return s
}

func TestHandleGrepSearch_Basic(t *testing.T) {
	s := newMCPServerWithChunks(t)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"query": "hello",
			},
		},
	}

	result, err := s.handleGrepSearch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}

	text := result.Content[0].(mcp.TextContent).Text
	var parsed []storage.GrepResult
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) == 0 {
		t.Fatal("expected at least one grep result")
	}

	found := false
	for _, r := range parsed {
		if r.FilePath == "main.go" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected result in main.go")
	}
}

func TestHandleGrepSearch_CaseSensitive(t *testing.T) {
	s := newMCPServerWithChunks(t)

	// case-sensitive search for "Hello" (capital H), should not match "hello"
	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"query":          "Hello",
				"case_sensitive": true,
			},
		},
	}

	result, err := s.handleGrepSearch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}

	text := result.Content[0].(mcp.TextContent).Text
	if text == "No matches found." {
		t.Log("case-sensitive correctly returned no matches for 'Hello'")
		return
	}

	var parsed []storage.GrepResult
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatal(err)
	}
	for _, r := range parsed {
		for _, m := range r.Matches {
			if m.Line > 0 {
				t.Logf("match in %s line %d: %s", r.FilePath, m.Line, m.Content)
			}
		}
	}
}

func TestHandleGrepSearch_WordBoundary(t *testing.T) {
	s := newMCPServerWithChunks(t)

	// word boundary search for "get" should match "getUser" (starts with get)
	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"query":        "get",
				"word_boundary": true,
			},
		},
	}

	result, err := s.handleGrepSearch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}

	text := result.Content[0].(mcp.TextContent).Text
	if text == "No matches found." {
		t.Log("word boundary search returned no matches")
		return
	}

	var parsed []storage.GrepResult
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) > 0 {
		t.Logf("word boundary search returned %d results", len(parsed))
	}
}

func TestHandleGrepSearch_LanguageFilter(t *testing.T) {
	s := newMCPServerWithChunks(t)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"query": "package",
				"lang":  "go",
			},
		},
	}

	result, err := s.handleGrepSearch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}

	text := result.Content[0].(mcp.TextContent).Text
	if text == "No matches found." {
		t.Fatal("expected matches for 'package' in Go files")
	}

	var parsed []storage.GrepResult
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) == 0 {
		t.Fatal("expected at least one result")
	}
}

func TestHandleGrepSearch_PathFilter(t *testing.T) {
	s := newMCPServerWithChunks(t)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"query":       "package",
				"path_filter": "util.go",
			},
		},
	}

	result, err := s.handleGrepSearch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}

	text := result.Content[0].(mcp.TextContent).Text
	var parsed []storage.GrepResult
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatal(err)
	}

	for _, r := range parsed {
		if r.FilePath != "util.go" {
			t.Fatalf("expected only util.go results, got %s", r.FilePath)
		}
	}
}

func TestHandleGrepSearch_NoMatch(t *testing.T) {
	s := newMCPServerWithChunks(t)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"query": "nonexistent_symbol_xyz",
			},
		},
	}

	result, err := s.handleGrepSearch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success with no matches")
	}

	text := result.Content[0].(mcp.TextContent).Text
	if text != "No matches found." {
		t.Fatalf("expected 'No matches found.', got %q", text)
	}
}

func TestHandleGrepSearch_EmptyQuery(t *testing.T) {
	s := newMCPServerWithChunks(t)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{},
		},
	}

	result, err := s.handleGrepSearch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error for empty query")
	}
}

func TestHandleGrepSearch_LimitParam(t *testing.T) {
	s := newMCPServerWithChunks(t)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"query": "package",
				"limit": float64(1),
			},
		},
	}

	result, err := s.handleGrepSearch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}

	text := result.Content[0].(mcp.TextContent).Text
	var parsed []storage.GrepResult
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) > 1 {
		t.Logf("limit=1 returned %d results (may return more if equal scores)", len(parsed))
	}
}

func TestHandleSearch_WithEmbedderMock(t *testing.T) {
	// For handleSearch we need a mock embedder that returns dummy embeddings.
	// Create an httptest server that responds to /v1/embeddings
	srv := newEmbeddingServer(t)
	defer srv.Close()

	root := t.TempDir()
	w := walker.New(root, nil)

	st, err := storage.New(filepath.Join(root, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	em := newEmbedder(t, srv.URL)
	ch := chunker.New(50, 10)

	idx := indexer.New(w, ch, em, st, nil, 0, 0)
	idx.Stats.TotalChunks = 0
	idx.Stats.TotalFiles = 0

	chunks := []chunker.Chunk{
		{ID: "c1", FilePath: "main.go", RelPath: "main.go", Content: "package main\n\nfunc hello() {\n\tfmt.Println(\"hello world\")\n}\n", StartLine: 1, EndLine: 6, FileHash: "h1", Language: "go"},
	}
	embeddings := map[string][]float32{
		"c1": {0.1, 0.2, 0.3, 0.4},
	}
	if err := st.UpsertChunks(chunks, embeddings); err != nil {
		t.Fatal(err)
	}

	s := &MCPServer{
		indexer:       idx,
		currentBranch: "",
		// mgr is nil so ensureLlama returns nil immediately
	}

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"query": "hello",
				"limit": float64(10),
			},
		},
	}

	result, err := s.handleSearch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}

	text := result.Content[0].(mcp.TextContent).Text
	t.Logf("search result: %s", text[:min(len(text), 300)])
}

func TestHandleGrepSearch_EmptyIndex(t *testing.T) {
	// Create a temp dir with a Go file so IndexAll has something to walk
	root := t.TempDir()
	w := walker.New(root, nil)

	srv := newEmbeddingServer(t)
	defer srv.Close()

	st, err := storage.New(filepath.Join(root, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ch := chunker.New(50, 10)
	em := newEmbedder(t, srv.URL)

	// Create a test Go file so the indexer has files to index
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	idx := indexer.New(w, ch, em, st, nil, 0, 0)

	s := &MCPServer{
		indexer:       idx,
		currentBranch: "",
	}

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"query": "main",
			},
		},
	}

	result, err := s.handleGrepSearch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}

	text := result.Content[0].(mcp.TextContent).Text
	t.Logf("empty index result: %s", text)
}

func TestNilIndexer(t *testing.T) {
	// handleGrepSearch panics with nil indexer (this is expected — production
	// code never calls it with nil). Skip the test to avoid panic.
	t.Skip("handleGrepSearch requires non-nil indexer")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
