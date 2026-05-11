package storage

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/cristian/go-indexing-mcp/pkg/chunker"
	"github.com/cristian/go-indexing-mcp/pkg/walker"
)

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a    []float64
		b    []float64
		want float64
	}{
		{"identical", []float64{1, 0, 0}, []float64{1, 0, 0}, 1.0},
		{"orthogonal", []float64{1, 0}, []float64{0, 1}, 0.0},
		{"opposite", []float64{1, 0}, []float64{-1, 0}, -1.0},
		{"partial", []float64{1, 1}, []float64{1, 0}, 1.0 / math.Sqrt(2)},
		{"zero vector a", []float64{0, 0}, []float64{1, 0}, 0.0},
		{"zero vector b", []float64{1, 0}, []float64{0, 0}, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cosineSimilarity(tt.a, tt.b)
			if math.Abs(got-tt.want) > 1e-10 {
				t.Errorf("cosineSimilarity(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestCosineSimilarity_DifferentLengths(t *testing.T) {
	got := cosineSimilarity([]float64{1, 0}, []float64{1, 0, 0})
	if got != 0 {
		t.Errorf("expected 0 for different lengths, got %v", got)
	}
}

func TestNewAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.gob")
	s, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("storage should not be nil")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func makeChunk(id, relPath string) chunker.Chunk {
	return chunker.Chunk{
		ID:       id,
		FilePath: "/project/" + relPath,
		RelPath:  relPath,
		Language: "go",
		Content:  "content",
		FileHash: "hash",
		StartLine: 1,
		EndLine:   5,
	}
}

func makeEmbeddings(chunks []chunker.Chunk) map[string][]float64 {
	emb := make(map[string][]float64)
	for i, ch := range chunks {
		v := make([]float64, 4)
		v[i%4] = 1.0
		emb[ch.ID] = v
	}
	return emb
}

func TestUpsertAndSearch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.gob")
	s, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		makeChunk("c1", "main.go"),
		makeChunk("c2", "utils.go"),
	}
	emb := makeEmbeddings(chunks)

	if err := s.UpsertChunks(chunks, emb); err != nil {
		t.Fatal(err)
	}

	query := []float64{1, 0, 0, 0}
	results, err := s.Search(query, 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestUpsert_UpdateExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.gob")
	s, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ch := makeChunk("c1", "main.go")
	oldEmb := map[string][]float64{"c1": {1, 0, 0, 0}}

	s.UpsertChunks([]chunker.Chunk{ch}, oldEmb)

	newEmb := map[string][]float64{"c1": {0, 1, 0, 0}}
	if err := s.UpsertChunks([]chunker.Chunk{ch}, newEmb); err != nil {
		t.Fatal(err)
	}

	results, _ := s.Search([]float64{0, 1, 0, 0}, 10)
	if len(results) == 0 || results[0].ID != "c1" {
		t.Error("expected updated chunk to rank first for new vector")
	}
}

func TestDeleteChunksByPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.gob")
	s, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}

	chunks := []chunker.Chunk{
		makeChunk("c1", "main.go"),
		makeChunk("c2", "utils.go"),
	}
	s.UpsertChunks(chunks, makeEmbeddings(chunks))

	if err := s.DeleteChunksByPath("/project/main.go"); err != nil {
		t.Fatal(err)
	}

	results, _ := s.Search([]float64{1, 0, 0, 0}, 10)
	if len(results) != 1 {
		t.Fatalf("expected 1 result after delete, got %d", len(results))
	}
	if results[0].ID != "c2" {
		t.Error("expected remaining chunk to be utils.go")
	}

	s.Close()
}

func TestStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.gob")
	s, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks, files, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if chunks != 0 || files != 0 {
		t.Errorf("expected 0 chunks and 0 files, got %d chunks, %d files", chunks, files)
	}

	s.UpsertChunks(
		[]chunker.Chunk{makeChunk("c1", "a.go"), makeChunk("c2", "b.go")},
		makeEmbeddings([]chunker.Chunk{makeChunk("c1", "a.go"), makeChunk("c2", "b.go")}),
	)

	chunks, files, _ = s.Stats()
	if chunks != 2 || files != 2 {
		t.Errorf("expected 2 chunks and 2 files, got %d chunks, %d files", chunks, files)
	}
}

func TestListFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.gob")
	s, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	files := s.ListFiles()
	if len(files) != 0 {
		t.Errorf("expected empty list, got %d", len(files))
	}

	s.UpsertChunks(
		[]chunker.Chunk{makeChunk("c1", "a.go"), makeChunk("c2", "a.go")},
		makeEmbeddings([]chunker.Chunk{makeChunk("c1", "a.go"), makeChunk("c2", "a.go")}),
	)

	files = s.ListFiles()
	if len(files) != 1 {
		t.Errorf("expected 1 file, got %d", len(files))
	}
}

func TestCommitSHA(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.gob")
	s, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}

	s.SetCommitSHA("abc123")
	if got := s.GetCommitSHA(); got != "abc123" {
		t.Errorf("expected abc123, got %s", got)
	}

	s.Close()

	s2, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	if got := s2.GetCommitSHA(); got != "abc123" {
		t.Errorf("expected persisted abc123, got %s", got)
	}
}

func TestSearchLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.gob")
	s, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var chunks []chunker.Chunk
	for i := range 20 {
		chunks = append(chunks, makeChunk("c"+itoa(i), "f"+itoa(i)+".go"))
	}
	s.UpsertChunks(chunks, makeEmbeddings(chunks))

	results, _ := s.Search([]float64{1, 0, 0, 0}, 5)
	if len(results) != 5 {
		t.Errorf("expected 5 results, got %d", len(results))
	}

	results, _ = s.Search([]float64{1, 0, 0, 0}, 0)
	// test inserts 20 chunks, default limit is 25, so all 20 should be returned
	if len(results) != 20 {
		t.Errorf("expected 20 results (default), got %d", len(results))
	}
}

func TestSearchResultOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.gob")
	s, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		makeChunk("c1", "close.go"),
		makeChunk("c2", "far.go"),
	}
	emb := map[string][]float64{
		"c1": {0.9, 0.1, 0, 0},
		"c2": {0.1, 0.9, 0, 0},
	}
	s.UpsertChunks(chunks, emb)

	query := []float64{0.95, 0.05, 0, 0}
	results, _ := s.Search(query, 10)

	if len(results) < 2 {
		t.Fatal("expected at least 2 results")
	}
	if results[0].ID != "c1" {
		t.Errorf("expected c1 (closer) first, got %s", results[0].ID)
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.gob")

	s1, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}

	chunks := []chunker.Chunk{makeChunk("c1", "main.go")}
	s1.UpsertChunks(chunks, makeEmbeddings(chunks))
	s1.SetCommitSHA("persist-test")
	s1.Close()

	s2, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	results, _ := s2.Search([]float64{1, 0, 0, 0}, 10)
	if len(results) != 1 {
		t.Errorf("expected 1 result after reload, got %d", len(results))
	}
	if s2.GetCommitSHA() != "persist-test" {
		t.Errorf("expected commit SHA to persist, got %s", s2.GetCommitSHA())
	}
}

func TestSwitchBranch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vectors.gob")

	s, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}

	s.UpsertChunks(
		[]chunker.Chunk{makeChunk("c1", "main.go")},
		makeEmbeddings([]chunker.Chunk{makeChunk("c1", "main.go")}),
	)
	s.SetCommitSHA("sha-main")

	if err := s.SwitchBranch("feature"); err != nil {
		t.Fatal(err)
	}

	chunks, files, _ := s.Stats()
	if chunks != 0 || files != 0 {
		t.Errorf("expected empty index on new branch, got %d chunks, %d files", chunks, files)
	}
	if s.GetCommitSHA() != "" {
		t.Errorf("expected empty commit SHA on new branch, got %s", s.GetCommitSHA())
	}

	s.UpsertChunks(
		[]chunker.Chunk{makeChunk("c2", "feature.go")},
		makeEmbeddings([]chunker.Chunk{makeChunk("c2", "feature.go")}),
	)

	if err := s.SwitchBranch("main"); err != nil {
		t.Fatal(err)
	}

	results, _ := s.Search([]float64{1, 0, 0, 0}, 10)
	if len(results) != 1 || results[0].ID != "c1" {
		t.Errorf("expected c1 from main branch, got %v", results)
	}
	if s.GetCommitSHA() != "sha-main" {
		t.Errorf("expected commit SHA sha-main, got %s", s.GetCommitSHA())
	}

	s.Close()

	s2, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	if err := s2.SwitchBranch("feature"); err != nil {
		t.Fatal(err)
	}
	results, _ = s2.Search([]float64{1, 0, 0, 0}, 10)
	if len(results) != 1 || results[0].ID != "c2" {
		t.Errorf("expected c2 from feature branch after reload, got %v", results)
	}
}

func TestSearchResultFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.gob")
	s, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	fi := walker.FileInfo{
		Path:     "/project/src/main.go",
		RelPath:  "src/main.go",
		Hash:     "abcdef",
		Language: "go",
	}

	ch := chunker.Chunk{
		ID:        "chunk1",
		FilePath:  fi.Path,
		RelPath:   fi.RelPath,
		Language:  fi.Language,
		StartLine: 1,
		EndLine:   10,
		Content:   "package main",
		FileHash:  fi.Hash,
	}

	emb := map[string][]float64{"chunk1": {1, 0, 0, 0}}
	s.UpsertChunks([]chunker.Chunk{ch}, emb)

	results, _ := s.Search([]float64{1, 0, 0, 0}, 10)
	if len(results) == 0 {
		t.Fatal("expected results")
	}

	r := results[0]
	if r.ID != "chunk1" || r.FilePath != fi.Path || r.RelPath != fi.RelPath {
		t.Error("search result fields mismatch")
	}
	if r.Score <= 0 {
		t.Error("expected positive score")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
