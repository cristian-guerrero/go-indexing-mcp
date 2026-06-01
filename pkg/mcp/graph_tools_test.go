package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/graph"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/indexer"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/walker"
	"github.com/mark3labs/mcp-go/mcp"
)

func newMCPServerWithGraph(t *testing.T) (*MCPServer, *graph.GraphQuery, string) {
	t.Helper()

	root := t.TempDir()
	g, err := graph.NewGraphQuery(filepath.Join(root, "graph.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })

	// Create test files on disk (pruneGraph checks these exist)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(root, "util.go"), []byte("package util"), 0644)
	os.WriteFile(filepath.Join(root, "handler.go"), []byte("package handler"), 0644)

	// Store symbols (imports and definitions) in the graph
	imports := []graph.Symbol{
		{ID: "imp1", Name: "fmt", Kind: graph.SymbolImport, FilePath: filepath.Join(root, "main.go"), RelPath: "main.go", StartLine: 1, Signature: "\"fmt\""},
		{ID: "imp2", Name: "os", Kind: graph.SymbolImport, FilePath: filepath.Join(root, "main.go"), RelPath: "main.go", StartLine: 2, Signature: "\"os\""},
		{ID: "imp3", Name: "strings", Kind: graph.SymbolImport, FilePath: filepath.Join(root, "util.go"), RelPath: "util.go", StartLine: 1, Signature: "\"strings\""},
		{ID: "imp4", Name: "github.com/user/project/pkg", Kind: graph.SymbolImport, FilePath: filepath.Join(root, "handler.go"), RelPath: "handler.go", StartLine: 1, Signature: "\"github.com/user/project/pkg\""},
		{ID: "imp5", Name: "fmt", Kind: graph.SymbolImport, FilePath: filepath.Join(root, "handler.go"), RelPath: "handler.go", StartLine: 2, Signature: "\"fmt\""},
	}
	defs := []graph.Symbol{
		{ID: "def1", Name: "main", Kind: graph.SymbolFunction, FilePath: filepath.Join(root, "main.go"), RelPath: "main.go", StartLine: 3, EndLine: 5, Signature: "func main()"},
		{ID: "def2", Name: "hello", Kind: graph.SymbolFunction, FilePath: filepath.Join(root, "util.go"), RelPath: "util.go", StartLine: 3, EndLine: 10, Signature: "func hello()"},
	}

	if err := g.StoreFile("main.go", append(imports[:2], defs[0]), nil); err != nil {
		t.Fatal(err)
	}
	if err := g.StoreFile("util.go", []graph.Symbol{imports[2], defs[1]}, nil); err != nil {
		t.Fatal(err)
	}
	if err := g.StoreFile("handler.go", imports[3:], nil); err != nil {
		t.Fatal(err)
	}

	w := walker.New(root, nil)
	idx := &indexer.Indexer{
		Graph:  g,
		Walker: w,
	}

	s := &MCPServer{
		indexer: idx,
	}

	return s, g, root
}

func TestHandleFindImports_ByModule(t *testing.T) {
	s, _, _ := newMCPServerWithGraph(t)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"pattern": "fmt",
			},
		},
	}

	result, err := s.handleFindImports(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	if len(result.Content) == 0 {
		t.Fatal("expected content")
	}

	text := result.Content[0].(mcp.TextContent).Text
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 fmt imports, got %d", len(parsed))
	}
}

func TestHandleFindImports_ByFullPath(t *testing.T) {
	s, _, _ := newMCPServerWithGraph(t)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"pattern": "github.com/user/project",
			},
		},
	}

	result, err := s.handleFindImports(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}

	text := result.Content[0].(mcp.TextContent).Text
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 import, got %d", len(parsed))
	}
}

func TestHandleFindImports_NoMatch(t *testing.T) {
	s, _, _ := newMCPServerWithGraph(t)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"pattern": "nonexistent",
			},
		},
	}

	result, err := s.handleFindImports(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success with 'no results' message")
	}

	text := result.Content[0].(mcp.TextContent).Text
	if text != "No imports matching 'nonexistent' found." {
		t.Fatalf("unexpected message: %q", text)
	}
}

func TestHandleFindImports_EmptyPattern(t *testing.T) {
	s, _, _ := newMCPServerWithGraph(t)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{},
		},
	}

	result, err := s.handleFindImports(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error for empty pattern")
	}
}

func TestHandleFindImports_NoGraph(t *testing.T) {
	s := &MCPServer{
		indexer: &indexer.Indexer{
			Graph: nil,
		},
	}

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"pattern": "fmt",
			},
		},
	}

	result, err := s.handleFindImports(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success with 'not available' message")
	}

	text := result.Content[0].(mcp.TextContent).Text
	if text != "Knowledge graph is not available. Build with CGO_ENABLED=1 to enable." {
		t.Fatalf("unexpected message: %q", text)
	}
}

func TestHandleSymbolInfo_ByName(t *testing.T) {
	s, _, _ := newMCPServerWithGraph(t)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"name": "hello",
			},
		},
	}

	result, err := s.handleSymbolInfo(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	if len(result.Content) == 0 {
		t.Fatal("expected content")
	}

	text := result.Content[0].(mcp.TextContent).Text
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatal(err)
	}
	defs, ok := parsed["definitions"].([]any)
	if !ok {
		t.Fatal("expected 'definitions' array in response")
	}
	if len(defs) == 0 {
		t.Fatal("expected at least one definition")
	}
}

func TestHandleSymbolInfo_WithPathFilter(t *testing.T) {
	s, _, _ := newMCPServerWithGraph(t)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"name":        "fmt",
				"path_filter": "handler.go",
			},
		},
	}

	result, err := s.handleSymbolInfo(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}

	text := result.Content[0].(mcp.TextContent).Text
	t.Log("symbol info:", text)
}

func TestHandleSymbolInfo_EmptyName(t *testing.T) {
	s, _, _ := newMCPServerWithGraph(t)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{},
		},
	}

	result, err := s.handleSymbolInfo(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error for empty name")
	}
}

func TestHandleSymbolInfo_NoGraph(t *testing.T) {
	s := &MCPServer{
		indexer: &indexer.Indexer{
			Graph: nil,
		},
	}

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"name": "main",
			},
		},
	}

	result, err := s.handleSymbolInfo(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success with 'not available' message")
	}

	text := result.Content[0].(mcp.TextContent).Text
	if text != "Knowledge graph is not available. Build with CGO_ENABLED=1 to enable." {
		t.Fatalf("unexpected message: %q", text)
	}
}

func TestPruneGraph_NilIndexer(t *testing.T) {
	s := &MCPServer{}
	// pruneGraph should not panic with nil indexer
	s.pruneGraph()
}

func TestPruneGraph_NilGraph(t *testing.T) {
	s := &MCPServer{
		indexer: &indexer.Indexer{
			Graph: nil,
		},
	}
	s.pruneGraph()
}

func TestPruneGraph_StaleFiles(t *testing.T) {
	s, g, root := newMCPServerWithGraph(t)

	// Remove main.go from disk (simulate stale file)
	os.Remove(filepath.Join(root, "main.go"))

	// pruneGraph should remove the stale symbols
	s.pruneGraph()

	// After pruning, main.go imports should be gone
	imports := g.FindImports("")
	for _, imp := range imports {
		if imp.RelPath == "main.go" {
			t.Fatalf("expected main.go imports to be pruned, found: %s", imp.Name)
		}
	}
}
