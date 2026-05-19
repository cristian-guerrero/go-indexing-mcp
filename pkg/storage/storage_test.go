package storage

import (
	"encoding/gob"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/walker"
)

func TestDotProduct32(t *testing.T) {
	tests := []struct {
		name string
		a    []float32
		b    []float32
		want float32
	}{
		{"identical", []float32{1, 0, 0}, []float32{1, 0, 0}, 1.0},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0.0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1.0},
		{"partial", []float32{float32(1.0 / math.Sqrt2), float32(1.0 / math.Sqrt2)}, []float32{1, 0}, float32(1.0 / math.Sqrt2)},
		{"zero vector a", []float32{0, 0}, []float32{1, 0}, 0.0},
		{"zero vector b", []float32{1, 0}, []float32{0, 0}, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dotProduct32(tt.a, tt.b)
			if math.Abs(float64(got-tt.want)) > 1e-6 {
				t.Errorf("dotProduct32(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestDotProduct32_DifferentLengths(t *testing.T) {
	got := dotProduct32([]float32{1, 0}, []float32{1, 0, 0})
	if got != 0 {
		t.Errorf("expected 0 for different lengths, got %v", got)
	}
}

func TestNormalize32(t *testing.T) {
	tests := []struct {
		name  string
		input []float32
		want  float64
	}{
		{"unit vector unchanged", []float32{1, 0, 0}, 1.0},
		{"scaled vector normalized", []float32{3, 4, 0}, 5.0},
		{"zero vector unchanged", []float32{0, 0, 0}, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := make([]float32, len(tt.input))
			copy(in, tt.input)
			normalize32(in)
			var norm float64
			for _, v := range in {
				norm += float64(v) * float64(v)
			}
			norm = math.Sqrt(norm)
			if tt.want == 0 {
				if norm != 0 {
					t.Errorf("expected zero norm, got %v", norm)
				}
			} else if math.Abs(norm-1.0) > 1e-6 {
				t.Errorf("expected unit norm, got %v", norm)
			}
		})
	}
}



func TestNewAndClose(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "vectors.gob"), 4)
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

func makeEmbeddings(chunks []chunker.Chunk) map[string][]float32 {
	emb := make(map[string][]float32)
	for i, ch := range chunks {
		v := make([]float32, 4)
		v[i%4] = 1.0
		emb[ch.ID] = v
	}
	return emb
}

func TestUpsertAndSearch(t *testing.T) {
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
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

	query := []float32{1, 0, 0, 0}
	results, err := s.Search(query, 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestUpsert_UpdateExisting(t *testing.T) {
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ch := makeChunk("c1", "main.go")
	oldEmb := map[string][]float32{"c1": {1, 0, 0, 0}}

	s.UpsertChunks([]chunker.Chunk{ch}, oldEmb)

	newEmb := map[string][]float32{"c1": {0, 1, 0, 0}}
	if err := s.UpsertChunks([]chunker.Chunk{ch}, newEmb); err != nil {
		t.Fatal(err)
	}

	results, _ := s.Search([]float32{0, 1, 0, 0}, 10)
	if len(results) == 0 || results[0].ID != "c1" {
		t.Error("expected updated chunk to rank first for new vector")
	}
}

func TestDeleteChunksByPath(t *testing.T) {
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
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

	results, _ := s.Search([]float32{1, 0, 0, 0}, 10)
	if len(results) != 1 {
		t.Fatalf("expected 1 result after delete, got %d", len(results))
	}
	if results[0].ID != "c2" {
		t.Error("expected remaining chunk to be utils.go")
	}

	s.Close()
}

func TestStats(t *testing.T) {
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
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
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
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
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}

	s.SetCommitSHA("abc123")
	if got := s.GetCommitSHA(); got != "abc123" {
		t.Errorf("expected abc123, got %s", got)
	}

	s.Close()

	s2, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	if got := s2.GetCommitSHA(); got != "abc123" {
		t.Errorf("expected persisted abc123, got %s", got)
	}
}

func TestSearchLimit(t *testing.T) {
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var chunks []chunker.Chunk
	for i := range 20 {
		chunks = append(chunks, makeChunk("c"+itoa(i), "f"+itoa(i)+".go"))
	}
	s.UpsertChunks(chunks, makeEmbeddings(chunks))

	results, _ := s.Search([]float32{1, 0, 0, 0}, 5)
	if len(results) != 5 {
		t.Errorf("expected 5 results, got %d", len(results))
	}

	results, _ = s.Search([]float32{1, 0, 0, 0}, 0)
	// test inserts 20 chunks, default limit is 25, so all 20 should be returned
	if len(results) != 20 {
		t.Errorf("expected 20 results (default), got %d", len(results))
	}
}

func TestSearchResultOrder(t *testing.T) {
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		makeChunk("c1", "close.go"),
		makeChunk("c2", "far.go"),
	}
	emb := map[string][]float32{
		"c1": {0.9, 0.1, 0, 0},
		"c2": {0.1, 0.9, 0, 0},
	}
	s.UpsertChunks(chunks, emb)

	query := []float32{0.95, 0.05, 0, 0}
	results, _ := s.Search(query, 10)

	if len(results) < 2 {
		t.Fatal("expected at least 2 results")
	}
	if results[0].ID != "c1" {
		t.Errorf("expected c1 (closer) first, got %s", results[0].ID)
	}
}

func TestPersistence(t *testing.T) {
	baseDir := t.TempDir()

	s1, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}

	chunks := []chunker.Chunk{makeChunk("c1", "main.go")}
	s1.UpsertChunks(chunks, makeEmbeddings(chunks))
	s1.SetCommitSHA("persist-test")
	s1.Close()

	s2, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	results, _ := s2.Search([]float32{1, 0, 0, 0}, 10)
	if len(results) != 1 {
		t.Errorf("expected 1 result after reload, got %d", len(results))
	}
	if s2.GetCommitSHA() != "persist-test" {
		t.Errorf("expected commit SHA to persist, got %s", s2.GetCommitSHA())
	}
}

func TestSwitchBranch(t *testing.T) {
	baseDir := t.TempDir()

	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}

	s.UpsertChunks(
		[]chunker.Chunk{makeChunk("c1", "main.go")},
		makeEmbeddings([]chunker.Chunk{makeChunk("c1", "main.go")}),
	)
	s.SetCommitSHA("sha-main")

	if err := s.SwitchBranch("feature", ""); err != nil {
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

	if err := s.SwitchBranch("main", ""); err != nil {
		t.Fatal(err)
	}

	results, _ := s.Search([]float32{1, 0, 0, 0}, 10)
	if len(results) != 1 || results[0].ID != "c1" {
		t.Errorf("expected c1 from main branch, got %v", results)
	}
	if s.GetCommitSHA() != "sha-main" {
		t.Errorf("expected commit SHA sha-main, got %s", s.GetCommitSHA())
	}

	s.Close()

	s2, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	if err := s2.SwitchBranch("feature", ""); err != nil {
		t.Fatal(err)
	}
	results, _ = s2.Search([]float32{1, 0, 0, 0}, 10)
	if len(results) != 1 || results[0].ID != "c2" {
		t.Errorf("expected c2 from feature branch after reload, got %v", results)
	}
}

func TestSearchResultFields(t *testing.T) {
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
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

	emb := map[string][]float32{"chunk1": {1, 0, 0, 0}}
	s.UpsertChunks([]chunker.Chunk{ch}, emb)

	results, _ := s.Search([]float32{1, 0, 0, 0}, 10)
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

func TestSearchGrep_BasicLiteral(t *testing.T) {
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		{ID: "c1", FilePath: "/p/main.go", RelPath: "main.go", Language: "go", StartLine: 1, EndLine: 5, Content: "func validate() error {\n\treturn nil\n}", FileHash: "h1"},
		{ID: "c2", FilePath: "/p/util.go", RelPath: "util.go", Language: "go", StartLine: 1, EndLine: 3, Content: "func helper() {\n\tvalidate()\n}", FileHash: "h2"},
	}
	s.UpsertChunks(chunks, makeEmbeddings(chunks))

	results, err := s.SearchGrep(GrepOptions{Query: "validate", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Matches == nil {
		t.Error("expected matches to be populated")
	}
}

func TestSearchGrep_LanguageFilter(t *testing.T) {
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		{ID: "c1", FilePath: "/p/main.go", RelPath: "main.go", Language: "go", StartLine: 1, EndLine: 3, Content: "func main() {}", FileHash: "h1"},
		{ID: "c2", FilePath: "/p/app.py", RelPath: "app.py", Language: "python", StartLine: 1, EndLine: 3, Content: "def main(): pass", FileHash: "h2"},
	}
	s.UpsertChunks(chunks, makeEmbeddings(chunks))

	results, err := s.SearchGrep(GrepOptions{Query: "main", Limit: 10, Language: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (go only), got %d", len(results))
	}
	if results[0].Language != "go" {
		t.Errorf("expected go, got %s", results[0].Language)
	}
}

func TestSearchGrep_CaseSensitive(t *testing.T) {
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		{ID: "c1", FilePath: "/p/a.go", RelPath: "a.go", Language: "go", StartLine: 1, EndLine: 3, Content: "func Error() {}", FileHash: "h1"},
		{ID: "c2", FilePath: "/p/b.go", RelPath: "b.go", Language: "go", StartLine: 1, EndLine: 3, Content: "func error() {}", FileHash: "h2"},
	}
	s.UpsertChunks(chunks, makeEmbeddings(chunks))

	results, err := s.SearchGrep(GrepOptions{Query: "Error", Limit: 10, CaseSensitive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (case-sensitive), got %d", len(results))
	}
	if results[0].RelPath != "a.go" {
		t.Errorf("expected a.go, got %s", results[0].RelPath)
	}
}

func TestSearchGrep_WholeWord(t *testing.T) {
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		{ID: "c1", FilePath: "/p/a.go", RelPath: "a.go", Language: "go", StartLine: 1, EndLine: 3, Content: "func get() {}", FileHash: "h1"},
		{ID: "c2", FilePath: "/p/b.go", RelPath: "b.go", Language: "go", StartLine: 1, EndLine: 3, Content: "func getter() {}", FileHash: "h2"},
	}
	s.UpsertChunks(chunks, makeEmbeddings(chunks))

	results, err := s.SearchGrep(GrepOptions{Query: "get", Limit: 10, WholeWord: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (whole word), got %d", len(results))
	}
	if results[0].RelPath != "a.go" {
		t.Errorf("expected a.go, got %s", results[0].RelPath)
	}
}

func TestSearchGrep_DefinitionBoost(t *testing.T) {
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		{ID: "c1", FilePath: "/p/a.go", RelPath: "a.go", Language: "go", StartLine: 1, EndLine: 3, Content: "// validate checks input\nfmt.Println(\"validate\")", FileHash: "h1"},
		{ID: "c2", FilePath: "/p/b.go", RelPath: "b.go", Language: "go", StartLine: 1, EndLine: 3, Content: "func validate() error {", FileHash: "h2"},
	}
	s.UpsertChunks(chunks, makeEmbeddings(chunks))

	results, err := s.SearchGrep(GrepOptions{Query: "validate", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].RelPath != "b.go" {
		t.Errorf("expected b.go (definition) first, got %s", results[0].RelPath)
	}
}

func TestSearchGrep_LineMatches(t *testing.T) {
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		{ID: "c1", FilePath: "/p/a.go", RelPath: "a.go", Language: "go", StartLine: 10, EndLine: 15, Content: "line one\nline two\nline three\nvalidate here\nline five", FileHash: "h1"},
	}
	s.UpsertChunks(chunks, makeEmbeddings(chunks))

	results, err := s.SearchGrep(GrepOptions{Query: "validate", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if len(results[0].Matches) != 1 {
		t.Fatalf("expected 1 match line, got %d", len(results[0].Matches))
	}
	if results[0].Matches[0].Line != 13 {
		t.Errorf("expected match on line 13, got %d", results[0].Matches[0].Line)
	}
}

func TestSearchGrep_Regex(t *testing.T) {
	baseDir := t.TempDir()
	s, err := New(filepath.Join(baseDir, "vectors.gob"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		{ID: "c1", FilePath: "/p/a.go", RelPath: "a.go", Language: "go", StartLine: 1, EndLine: 5, Content: "type FileDownloader struct {\n\turl string\n}", FileHash: "h1"},
		{ID: "c2", FilePath: "/p/b.go", RelPath: "b.go", Language: "go", StartLine: 1, EndLine: 5, Content: "type Reader struct {\n\tbuf []byte\n}", FileHash: "h2"},
	}
	s.UpsertChunks(chunks, makeEmbeddings(chunks))

	results, err := s.SearchGrep(GrepOptions{Query: "type.*Down", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].RelPath != "a.go" {
		t.Errorf("expected a.go, got %s", results[0].RelPath)
	}
}

// TestBruteForceIndexExact verifies that bruteForceIndex returns correct results.
func TestBruteForceIndexExact(t *testing.T) {
	bf := NewBruteForceIndex()
	records := []ChunkRecord{
		{ID: "c1", Vector: []float32{1, 0, 0, 0}},
		{ID: "c2", Vector: []float32{0, 1, 0, 0}},
		{ID: "c3", Vector: []float32{0, 0, 1, 0}},
		{ID: "c4", Vector: []float32{0, 0, 0, 1}},
	}
	if err := bf.Build(records); err != nil {
		t.Fatal(err)
	}

	results, err := bf.Query([]float32{1, 0, 0, 0}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}
	if results[0].ID != "c1" {
		t.Errorf("expected c1 first, got %s", results[0].ID)
	}
}

// TestCoverIndexConsistency verifies cover tree results match brute force for small data.
func TestCoverIndexConsistency(t *testing.T) {
	records := make([]ChunkRecord, 50)
	rng := newRand(42)
	for i := range records {
		vec := make([]float32, 8)
		for j := range vec {
			vec[j] = rng.Float32()*2 - 1
		}
		normalize32(vec)
		records[i] = ChunkRecord{ID: itoa(i), Vector: vec}
	}

	bf := NewBruteForceIndex()
	bf.Build(records)

	cv := NewCoverIndex(1.3, CosineDistance)
	cv.Build(records)

	query := make([]float32, 8)
	for i := range query {
		query[i] = rng.Float32()*2 - 1
	}

	bfResults, _ := bf.Query(query, 10)
	cvResults, _ := cv.Query(query, 10)

	if len(bfResults) != len(cvResults) {
		t.Fatalf("expected same count: brute=%d cover=%d", len(bfResults), len(cvResults))
	}

	topK := 5
	for i := 0; i < topK && i < len(bfResults); i++ {
		if bfResults[i].ID != cvResults[i].ID {
			t.Errorf("rank %d mismatch: brute=%s cover=%s", i, bfResults[i].ID, cvResults[i].ID)
		}
	}
}

// TestAutoSelectIndexThreshold verifies auto-selection of index backend based on size.
func TestAutoSelectIndexThreshold(t *testing.T) {
	s := &Storage{
		records:   make([]ChunkRecord, 100),
		indexKind: IndexKindAuto,
	}
	// 100 records → should select brute-force
	if kind := s.resolveIndexKind(); kind != IndexKindBruteForce {
		t.Errorf("expected brute-force for 100 records, got %s", kind)
	}

	s.records = make([]ChunkRecord, 5000)
	for i := range s.records {
		s.records[i].Vector = make([]float32, 128)
	}
	// 5000 records with 128 dims → density = 39 → should select cover
	if kind := s.resolveIndexKind(); kind != IndexKindCover {
		t.Errorf("expected cover for 5000 records/128 dims, got %s", kind)
	}

	// With explicit IndexKindBruteForce, always brute-force
	s.indexKind = IndexKindBruteForce
	if kind := s.resolveIndexKind(); kind != IndexKindBruteForce {
		t.Errorf("expected brute-force when explicitly set, got %s", kind)
	}
}

// TestConcurrentBuild verifies that only one build happens with concurrent calls.
func TestConcurrentBuild(t *testing.T) {
	cache := NewIndexCacheEntry()
	var buildCount int32
	var mu sync.Mutex

	builder := func() (VectorIndex, error) {
		mu.Lock()
		buildCount++
		mu.Unlock()
		return NewBruteForceIndex(), nil
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := cache.GetOrBuild(builder)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if buildCount != 1 {
		t.Errorf("expected 1 build, got %d", buildCount)
	}
}

// TestBuildAfterInvalidate verifies that Invalidate triggers a rebuild.
func TestBuildAfterInvalidate(t *testing.T) {
	cache := NewIndexCacheEntry()

	idx1, _ := cache.GetOrBuild(func() (VectorIndex, error) {
		return NewBruteForceIndex(), nil
	})
	if idx1 == nil {
		t.Fatal("expected non-nil index")
	}

	cache.Invalidate()

	idx2, _ := cache.GetOrBuild(func() (VectorIndex, error) {
		return NewBruteForceIndex(), nil
	})
	if idx2 == nil {
		t.Fatal("expected non-nil index after rebuild")
	}
	if idx1 == idx2 {
		t.Error("expected a new index after invalidate")
	}
}

// TestCacheNoDeadlock verifies that Invalidate + GetOrBuild don't deadlock.
func TestCacheNoDeadlock(t *testing.T) {
	cache := NewIndexCacheEntry()

	// Build once
	cache.GetOrBuild(func() (VectorIndex, error) {
		return NewBruteForceIndex(), nil
	})

	// Concurrent invalidation and build
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cache.Invalidate()
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			cache.GetOrBuild(func() (VectorIndex, error) {
				return NewBruteForceIndex(), nil
			})
		}()
	}
	wg.Wait()
}

// TestStorageVersion tests format version saving and loading.
func TestStorageVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vectors.gob")

	// Create fresh storage, version should be saved as StorageFormatVersion
	st, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}

	if st.NeedsReindex() {
		t.Error("fresh storage should not need reindex")
	}

	// Save a record so the gob is written
	err = st.UpsertChunks([]chunker.Chunk{
		{ID: "c1", FilePath: "test.go", RelPath: "test.go", Language: "go", Content: "package test", StartLine: 1, EndLine: 1, FileHash: "hash1"},
	}, map[string][]float32{"c1": {1, 0, 0, 0}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Reload — version should match
	st2, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	if st2.NeedsReindex() {
		t.Error("reloaded storage with matching version should not need reindex")
	}
}

// TestStorageVersionMismatch tests that a version mismatch triggers NeedsReindex.
func TestStorageVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vectors.gob")

	// Create and save with current version
	st, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	err = st.UpsertChunks([]chunker.Chunk{
		{ID: "c1", FilePath: "test.go", RelPath: "test.go", Language: "go", Content: "package test", StartLine: 1, EndLine: 1, FileHash: "hash1"},
	}, map[string][]float32{"c1": {1, 0, 0, 0}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Manually corrupt the version to simulate a future format change
	data := StorageData{
		Version:   StorageFormatVersion + 1,
		Records:   []ChunkRecord{{ID: "c1", FilePath: "test.go", RelPath: "test.go", Language: "go", Content: "package test", StartLine: 1, EndLine: 1, FileHash: "hash1", Vector: []float32{1, 0, 0, 0}}},
		CommitSHA: "",
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := gob.NewEncoder(f)
	if err := enc.Encode(data); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	// Reload — version mismatch should trigger NeedsReindex
	st2, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	if !st2.NeedsReindex() {
		t.Error("expected NeedsReindex for version mismatch")
	}

	// ClearAll should resolve the mismatch
	if err := st2.ClearAll(); err != nil {
		t.Fatal(err)
	}
	if st2.NeedsReindex() {
		t.Error("after ClearAll, NeedsReindex should be false")
	}
}

// TestStorageVersionLegacy tests that old format (version 0) does not trigger reindex.
func TestStorageVersionLegacy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vectors.gob")

	// Write a gob without the Version field (pre-versioning format)
	legacyData := struct {
		Records   []ChunkRecord
		CommitSHA string
	}{
		Records: []ChunkRecord{
			{ID: "c1", FilePath: "test.go", RelPath: "test.go", Language: "go", Content: "package test", StartLine: 1, EndLine: 1, FileHash: "hash1", Vector: []float32{1, 0, 0, 0}},
		},
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := gob.NewEncoder(f)
	if err := enc.Encode(legacyData); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	// Load legacy format — should not need reindex
	st, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if st.NeedsReindex() {
		t.Error("legacy format (version 0) should not need reindex")
	}

	// Records should be loaded
	chunks, files, err := st.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if chunks != 1 {
		t.Errorf("expected 1 chunk from legacy format, got %d", chunks)
	}
	if files != 1 {
		t.Errorf("expected 1 file from legacy format, got %d", files)
	}
}

// newRand is a seeded random source for deterministic tests.
func newRand(seed int64) *randWrapper {
	return &randWrapper{seed: seed}
}

// randWrapper provides deterministic float32 values.
type randWrapper struct {
	seed  int64
	state int64
}

func (r *randWrapper) Float32() float32 {
	if r.state == 0 {
		r.state = r.seed
	}
	r.state = r.state*1103515245 + 12345
	// LCG low bits
	return float32(r.state&0x7fffffff) / float32(1<<31)
}
