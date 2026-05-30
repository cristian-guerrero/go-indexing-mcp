package indexer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/embedder"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/storage"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/walker"
)

func TestNew(t *testing.T) {
	dir := t.TempDir()
	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	em := embedder.New("http://localhost:56000", 768, 8, "")
	st, _ := storage.New(filepath.Join(dir, "test.gob"), 4)
	defer st.Close()

	idx := New(w, ch, em, st, nil, 0, 0)
	if idx == nil {
		t.Fatal("expected non-nil indexer")
	}
	if idx.Walker != w {
		t.Error("Walker mismatch")
	}
	if idx.Chunker != ch {
		t.Error("Chunker mismatch")
	}
	if idx.Embedder != em {
		t.Error("Embedder mismatch")
	}
	if idx.Storage != st {
		t.Error("Storage mismatch")
	}
	if idx.Stats.LastIndexed != "never" {
		t.Errorf("expected LastIndexed='never', got %q", idx.Stats.LastIndexed)
	}
}

func TestNew_NilEmbedder(t *testing.T) {
	dir := t.TempDir()
	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	st, _ := storage.New(filepath.Join(dir, "test.gob"), 4)
	defer st.Close()

	idx := New(w, ch, nil, st, nil, 0, 0)
	if idx.Embedder != nil {
		t.Error("expected nil embedder")
	}
}

func TestHasGlobChars(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"*.go", true},
		{"pkg/**", true},
		{"file?.go", true},
		{"[abc].go", true},
		{"main.go", false},
		{"pkg/util", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.s, func(t *testing.T) {
			got := hasGlobChars(tt.s)
			if got != tt.want {
				t.Errorf("hasGlobChars(%q) = %v, want %v", tt.s, got, tt.want)
			}
		})
	}
}

func TestMatchesPath_ExactPrefix(t *testing.T) {
	tests := []struct {
		rel    string
		filter string
		want   bool
	}{
		{"main.go", "main.go", true},
		{"pkg/main.go", "pkg", true},
		{"pkg/util/helper.go", "pkg", true},
		{"other.go", "pkg", false},
		{"MAIN.GO", "main.go", true},
	}
	for _, tt := range tests {
		t.Run(tt.rel+"_"+tt.filter, func(t *testing.T) {
			got := matchesPath(tt.rel, tt.filter)
			if got != tt.want {
				t.Errorf("matchesPath(%q, %q) = %v, want %v", tt.rel, tt.filter, got, tt.want)
			}
		})
	}
}

func TestMatchesPath_Glob(t *testing.T) {
	tests := []struct {
		rel    string
		filter string
		want   bool
	}{
		{"main.go", "*.go", true},
		{"main.go", "*.py", false},
		{"main_test.go", "*_test.go", true},
		{"pkg/main.go", "pkg/*.go", true},
		{"pkg/sub/main.go", "pkg/*.go", false},
		{"pkg/sub/main.go", "**/*_test.go", false},
		{"pkg/sub/main_test.go", "**/*_test.go", true},
		{"pkg/sub/main_test.go", "*_test.go", true},
	}
	for _, tt := range tests {
		t.Run(tt.rel+"_"+tt.filter, func(t *testing.T) {
			got := matchesPath(tt.rel, tt.filter)
			if got != tt.want {
				t.Errorf("matchesPath(%q, %q) = %v, want %v", tt.rel, tt.filter, got, tt.want)
			}
		})
	}
}

func TestMatchesPath_EmptyFilter(t *testing.T) {
	got := matchesPath("main.go", "")
	if !got {
		t.Error("expected true for empty filter (prefix match on empty string matches everything)")
	}
}

func TestGetStats_EmptyStorage(t *testing.T) {
	dir := t.TempDir()
	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	st, _ := storage.New(filepath.Join(dir, "test.gob"), 4)
	defer st.Close()
	idx := New(w, ch, nil, st, nil, 0, 0)

	stats := idx.GetStats()
	if stats.TotalChunks != 0 {
		t.Errorf("expected 0 chunks, got %d", stats.TotalChunks)
	}
	if stats.TotalFiles != 0 {
		t.Errorf("expected 0 files, got %d", stats.TotalFiles)
	}
}

func TestGetStats_WithData(t *testing.T) {
	dir := t.TempDir()
	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	st, _ := storage.New(filepath.Join(dir, "test.gob"), 4)
	defer st.Close()
	idx := New(w, ch, nil, st, nil, 0, 0)

	chunks := []chunker.Chunk{
		{ID: "c1", FilePath: "/a.go", RelPath: "a.go", Language: "go", Content: "package a", FileHash: "h1"},
	}
	st.UpsertChunks(chunks, map[string][]float32{"c1": {1, 0, 0, 0}})

	stats := idx.GetStats()
	if stats.TotalChunks != 1 {
		t.Errorf("expected 1 chunk, got %d", stats.TotalChunks)
	}
}

func TestListFiles_Empty(t *testing.T) {
	dir := t.TempDir()
	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	st, _ := storage.New(filepath.Join(dir, "test.gob"), 4)
	defer st.Close()
	idx := New(w, ch, nil, st, nil, 0, 0)

	files := idx.ListFiles()
	if len(files) != 0 {
		t.Errorf("expected empty list, got %d", len(files))
	}
}

func TestListFiles_NilStorage(t *testing.T) {
	dir := t.TempDir()
	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	idx := New(w, ch, nil, nil, nil, 0, 0)

	files := idx.ListFiles()
	if files != nil {
		t.Errorf("expected nil, got %v", files)
	}
}

func TestPruneStaleEntries(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "pkg")
	os.MkdirAll(sub, 0755)

	goFile := filepath.Join(dir, "main.go")
	os.WriteFile(goFile, []byte("package main\n"), 0644)
	utilFile := filepath.Join(sub, "util.go")
	os.WriteFile(utilFile, []byte("package pkg\n"), 0644)

	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	st, _ := storage.New(filepath.Join(dir, "test.gob"), 4)
	defer st.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		resp := struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}{}
		for range req.Input {
			resp.Data = append(resp.Data, struct {
				Embedding []float64 `json:"embedding"`
			}{[]float64{1, 0, 0, 0}})
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	em := embedder.New(srv.URL, 4, 10, "")
	idx := New(w, ch, em, st, nil, 0, 0)

	if err := idx.IndexAll(); err != nil {
		t.Fatal(err)
	}

	filesBefore := idx.ListFiles()
	if len(filesBefore) == 0 {
		t.Fatal("expected files after index")
	}

	os.Remove(goFile)

	idx.PruneStaleEntries()

	filesAfter := idx.ListFiles()
	for _, f := range filesAfter {
		if strings.HasSuffix(f, "main.go") {
			t.Error("expected main.go to be pruned")
		}
	}
}

func TestIndexPath_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	st, _ := storage.New(filepath.Join(dir, "test.gob"), 4)
	defer st.Close()
	idx := New(w, ch, nil, st, nil, 0, 0)

	err := idx.IndexPath("nonexistent.go")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestIndexPath_Success(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)

	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	st, _ := storage.New(filepath.Join(dir, "test.gob"), 4)
	defer st.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		resp := struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}{}
		for range req.Input {
			resp.Data = append(resp.Data, struct {
				Embedding []float64 `json:"embedding"`
			}{[]float64{1, 0, 0, 0}})
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	em := embedder.New(srv.URL, 4, 10, "")
	idx := New(w, ch, em, st, nil, 0, 0)

	if err := idx.IndexPath("main.go"); err != nil {
		t.Fatal(err)
	}

	stats := idx.GetStats()
	if stats.TotalChunks == 0 {
		t.Error("expected chunks after IndexPath")
	}
}

func TestIndexAll(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "util.go"), []byte("package util\nfunc Help() {}\n"), 0644)

	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	st, _ := storage.New(filepath.Join(dir, "test.gob"), 4)
	defer st.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		resp := struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}{}
		for range req.Input {
			resp.Data = append(resp.Data, struct {
				Embedding []float64 `json:"embedding"`
			}{[]float64{1, 0, 0, 0}})
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	em := embedder.New(srv.URL, 4, 10, "")
	idx := New(w, ch, em, st, nil, 0, 0)

	if err := idx.IndexAll(); err != nil {
		t.Fatal(err)
	}

	stats := idx.GetStats()
	if stats.TotalChunks == 0 {
		t.Error("expected chunks after IndexAll")
	}
	if stats.TotalFiles != 2 {
		t.Errorf("expected 2 files, got %d", stats.TotalFiles)
	}
}

func TestIndexAll_AlreadyRunning(t *testing.T) {
	dir := t.TempDir()
	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	idx := New(w, ch, nil, nil, nil, 0, 0)

	idx.Running = true
	err := idx.IndexAll()
	if err == nil {
		t.Fatal("expected error when already running")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("expected 'already in progress', got %s", err)
	}
}

func TestSearch_HybridMode(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)

	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	st, _ := storage.New(filepath.Join(dir, "test.gob"), 4)
	defer st.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			var req struct {
				Input []string `json:"input"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			resp := struct {
				Data []struct {
					Embedding []float64 `json:"embedding"`
				} `json:"data"`
			}{}
			for range req.Input {
				resp.Data = append(resp.Data, struct {
					Embedding []float64 `json:"embedding"`
				}{[]float64{1, 0, 0, 0}})
			}
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer srv.Close()

	em := embedder.New(srv.URL, 4, 10, "")
	idx := New(w, ch, em, st, nil, 0, 0)
	idx.IndexAll()

	results, err := idx.Search("main", "", 10, "hybrid")
	if err != nil {
		t.Fatal(err)
	}
	_ = results
}

func TestSearch_GrepMode(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)

	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	st, _ := storage.New(filepath.Join(dir, "test.gob"), 4)
	defer st.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}{Data: []struct {
			Embedding []float64 `json:"embedding"`
		}{{[]float64{1, 0, 0, 0}}}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	em := embedder.New(srv.URL, 4, 10, "")
	idx := New(w, ch, em, st, nil, 0, 0)
	idx.IndexAll()

	results, err := idx.Search("main", "", 10, "grep")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected grep results for 'main'")
	}
}

func TestSearchGrep(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)

	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	st, _ := storage.New(filepath.Join(dir, "test.gob"), 4)
	defer st.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}{Data: []struct {
			Embedding []float64 `json:"embedding"`
		}{{[]float64{1, 0, 0, 0}}}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	em := embedder.New(srv.URL, 4, 10, "")
	idx := New(w, ch, em, st, nil, 0, 0)
	idx.IndexAll()

	results, err := idx.SearchGrep(storage.GrepOptions{Query: "package", Limit: 10}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected grep results for 'package'")
	}
}

func TestSearchGrep_PathFilter(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)

	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	st, _ := storage.New(filepath.Join(dir, "test.gob"), 4)
	defer st.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}{Data: []struct {
			Embedding []float64 `json:"embedding"`
		}{{[]float64{1, 0, 0, 0}}}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	em := embedder.New(srv.URL, 4, 10, "")
	idx := New(w, ch, em, st, nil, 0, 0)
	idx.IndexAll()

	results, err := idx.SearchGrep(storage.GrepOptions{Query: "package", Limit: 10}, "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Error("expected 0 results with no-match path filter")
	}
}

func TestFilterByPath(t *testing.T) {
	dir := t.TempDir()
	w := walker.New(dir, nil)
	ch := chunker.New(50, 10)
	st, _ := storage.New(filepath.Join(dir, "test.gob"), 4)
	defer st.Close()
	idx := New(w, ch, nil, st, nil, 0, 0)

	results := []storage.SearchResult{
		{RelPath: "main.go"},
		{RelPath: "pkg/util.go"},
		{RelPath: "pkg/sub/helper.go"},
	}

	filtered := idx.filterByPath(results, "pkg")
	if len(filtered) != 2 {
		t.Errorf("expected 2 filtered results, got %d", len(filtered))
	}

	empty := idx.filterByPath(results, "")
	if len(empty) != 3 {
		t.Errorf("expected all results with empty filter, got %d", len(empty))
	}
}

