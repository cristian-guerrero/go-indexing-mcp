package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
)

func TestNewAndClose(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "index.sqlite"), 4)
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
	}
}

func TestUpsertAndSearch(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		makeChunk("c1", "main.go"),
	}
	embeddings := map[string][]float32{
		"c1": {0.5, 0.5, 0.5, 0.5},
	}

	if err := s.UpsertChunks(chunks, embeddings); err != nil {
		t.Fatal(err)
	}

	results, err := s.Search([]float32{0.5, 0.5, 0.5, 0.5}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
}

func TestUpsert_UpdateExisting(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{makeChunk("c1", "main.go")}
	emb1 := map[string][]float32{"c1": {1, 0, 0, 0}}
	if err := s.UpsertChunks(chunks, emb1); err != nil {
		t.Fatal(err)
	}

	emb2 := map[string][]float32{"c1": {0, 1, 0, 0}}
	if err := s.UpsertChunks(chunks, emb2); err != nil {
		t.Fatal(err)
	}

	results, err := s.Search([]float32{0, 1, 0, 0}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected result after update")
	}
}

func TestDeleteChunksByPath(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		makeChunk("c1", "main.go"),
		makeChunk("c2", "main.go"),
	}
	emb := map[string][]float32{"c1": {1, 0, 0, 0}, "c2": {0, 1, 0, 0}}
	if err := s.UpsertChunks(chunks, emb); err != nil {
		t.Fatal(err)
	}

	chunks2, _, _ := s.Stats()
	if chunks2 == 0 {
		t.Fatal("expected chunks")
	}

	if err := s.DeleteChunksByPath("/project/main.go"); err != nil {
		t.Fatal(err)
	}

	chunks3, _, _ := s.Stats()
	if chunks3 != 0 {
		t.Fatal("expected 0 chunks after delete, got", chunks3)
	}
}

func TestStats(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ch, fi, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if ch != 0 || fi != 0 {
		t.Fatal("expected empty stats")
	}

	chunks := []chunker.Chunk{
		makeChunk("c1", "a.go"),
		makeChunk("c2", "b.go"),
	}
	emb := map[string][]float32{"c1": {1, 0, 0, 0}, "c2": {0, 1, 0, 0}}
	s.UpsertChunks(chunks, emb)

	ch, fi, _ = s.Stats()
	if ch != 2 || fi != 2 {
		t.Fatalf("expected 2 chunks / 2 files, got %d / %d", ch, fi)
	}
}

func TestListFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		makeChunk("c1", "src/main.go"),
		makeChunk("c2", "src/util.go"),
	}
	emb := map[string][]float32{"c1": {1, 0, 0, 0}, "c2": {0, 1, 0, 0}}
	s.UpsertChunks(chunks, emb)

	files := s.ListFiles()
	if len(files) != 2 {
		t.Fatal("expected 2 files")
	}
}

func TestCommitSHA(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if sha := s.GetCommitSHA(); sha != "" {
		t.Fatal("expected empty sha")
	}

	s.SetCommitSHA("abc123")
	if sha := s.GetCommitSHA(); sha != "abc123" {
		t.Fatalf("expected abc123, got %s", sha)
	}

	// Re-open and verify persistence
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := New(filepath.Join(dir, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if sha := s2.GetCommitSHA(); sha != "abc123" {
		t.Fatalf("expected abc123 after reopen, got %s", sha)
	}
}

func TestSearchLimit(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "index.sqlite"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i := 0; i < 10; i++ {
		id := string(rune('a' + i))
		chunks := []chunker.Chunk{makeChunk(id, id+".go")}
		emb := map[string][]float32{id: {1, float32(i) / 10}}
		s.UpsertChunks(chunks, emb)
	}

	results, err := s.Search([]float32{1, 1}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) > 3 {
		t.Fatalf("expected at most 3 results, got %d", len(results))
	}
}

func TestSearchResultOrder(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "index.sqlite"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		makeChunk("c1", "close.go"),
		makeChunk("c2", "far.go"),
	}
	emb := map[string][]float32{
		"c1": {0.99, 0.01},
		"c2": {0.01, 0.99},
	}
	s.UpsertChunks(chunks, emb)

	results, err := s.Search([]float32{1, 0}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 2 {
		t.Fatal("expected at least 2 results")
	}
	if results[0].ID != "c1" {
		t.Fatalf("expected c1 first, got %s", results[0].ID)
	}
}

func TestNeedsReindex(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if s.NeedsReindex() {
		t.Fatal("fresh db should not need reindex")
	}
}

func TestClearAll(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{makeChunk("c1", "main.go")}
	emb := map[string][]float32{"c1": {1, 0, 0, 0}}
	s.UpsertChunks(chunks, emb)

	if err := s.ClearAll(); err != nil {
		t.Fatal(err)
	}

	ch, _, _ := s.Stats()
	if ch != 0 {
		t.Fatal("expected 0 chunks after clear")
	}
}

func TestSwitchBranch(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{makeChunk("c1", "main.go")}
	emb := map[string][]float32{"c1": {1, 0, 0, 0}}
	s.UpsertChunks(chunks, emb)
	s.SetCommitSHA("main-sha")

	if err := s.SwitchBranch("feature", ""); err != nil {
		t.Fatal(err)
	}

	if sha := s.GetCommitSHA(); sha != "" {
		t.Fatal("expected empty sha on new branch")
	}

	// Switch back to main
	if err := s.SwitchBranch("main", ""); err != nil {
		t.Fatal(err)
	}

	if sha := s.GetCommitSHA(); sha != "main-sha" {
		t.Fatalf("expected main-sha, got %s", sha)
	}
}

func TestSearchGrep_BasicLiteral(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ch := chunker.Chunk{
		ID: "c1", FilePath: "/p/main.go", RelPath: "main.go",
		Language: "go", Content: "func main() {\n\tfmt.Println(\"hello\")\n}",
	}
	chunks := []chunker.Chunk{ch}
	s.UpsertChunks(chunks, map[string][]float32{"c1": {1, 0, 0, 0}})

	results, err := s.SearchGrep(GrepOptions{Query: "main", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected grep results")
	}
}

func TestIsFileIndexed(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ok := s.IsFileIndexed("/project/main.go", "")
	if ok {
		t.Fatal("should not be indexed")
	}

	chunks := []chunker.Chunk{{
		ID: "c1", FilePath: "/project/main.go", RelPath: "main.go",
		FileHash: "abc123", Content: "content",
	}}
	s.UpsertChunks(chunks, map[string][]float32{"c1": {1, 0, 0, 0}})

	ok = s.IsFileIndexed("/project/main.go", "abc123")
	if !ok {
		t.Fatal("should be indexed")
	}

	ok = s.IsFileIndexed("/project/main.go", "wrong")
	if ok {
		t.Fatal("should not match different hash")
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.sqlite")

	s, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}

	chunks := []chunker.Chunk{makeChunk("c1", "persist.go")}
	emb := map[string][]float32{"c1": {0.5, 0.5, 0.5, 0.5}}
	s.UpsertChunks(chunks, emb)
	s.SetCommitSHA("persist-sha")
	s.Close()

	s2, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	if sha := s2.GetCommitSHA(); sha != "persist-sha" {
		t.Fatalf("expected persist-sha, got %s", sha)
	}

	results, _ := s2.Search([]float32{0.5, 0.5, 0.5, 0.5}, 10)
	if len(results) == 0 {
		t.Fatal("expected persisted results")
	}
}

// Ensure os is used
var _ = os.DevNull
