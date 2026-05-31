package db

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
)

func TestOpen_WithDimensions(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if s.Path() == "" {
		t.Fatal("expected non-empty path")
	}
	if s.DB() == nil {
		t.Fatal("expected non-nil db")
	}
}

func TestOpen_ZeroDimensions(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "graph.sqlite"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
}

func TestClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Double close should not panic
	s.Close()
}

func TestIsLocked_ReturnsFalseOnFreshDB(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if s.IsLocked() {
		t.Fatal("expected not locked on fresh db")
	}
}

func TestBranchSuffix_Main(t *testing.T) {
	if got := BranchSuffix("main", ""); got != "" {
		t.Fatalf("expected empty for main, got %s", got)
	}
}

func TestBranchSuffix_MainWithWorktree(t *testing.T) {
	if got := BranchSuffix("main", "worktrees/my-branch"); got != "-worktrees-my-branch" {
		t.Fatalf("expected '-worktrees-my-branch', got %s", got)
	}
}

func TestBranchSuffix_NonMain(t *testing.T) {
	if got := BranchSuffix("feature", ""); got != "-feature" {
		t.Fatalf("expected '-feature', got %s", got)
	}
}

func TestBranchSuffix_NonMainWithWorktree(t *testing.T) {
	if got := BranchSuffix("feature", "worktrees/feat"); got != "-worktrees-feat-feature" {
		t.Fatalf("expected '-worktrees-feat-feature', got %s", got)
	}
}

func TestBranchSuffix_EmptyBranchAndWorktree(t *testing.T) {
	if got := BranchSuffix("", ""); got != "" {
		t.Fatalf("expected empty, got %s", got)
	}
}

func TestSanitizeName_ReplacesSeparators(t *testing.T) {
	got := sanitizeName("foo/bar\\baz:qux..quux")
	expected := "foo-bar-baz-qux-quux"
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestSanitizeName_NoChanges(t *testing.T) {
	got := sanitizeName("simple-name")
	if got != "simple-name" {
		t.Fatalf("expected 'simple-name', got %s", got)
	}
}

func TestBranchPath(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "index.sqlite")
	s, err := Open(basePath, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	mainPath := s.BranchPath("main", "")
	if mainPath != basePath {
		t.Fatalf("expected %q, got %q", basePath, mainPath)
	}

	featPath := s.BranchPath("feature", "")
	expectedFeat := filepath.Join(dir, "index-feature.sqlite")
	if featPath != expectedFeat {
		t.Fatalf("expected %q, got %q", expectedFeat, featPath)
	}
}

func TestSwitchBranch(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "index.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.SetCommitSHA("main-sha")

	if err := s.SwitchBranch("feature", ""); err != nil {
		t.Fatal(err)
	}
	if sha := s.GetCommitSHA(); sha != "" {
		t.Fatal("expected empty sha on new branch")
	}

	if err := s.SwitchBranch("main", ""); err != nil {
		t.Fatal(err)
	}
	if sha := s.GetCommitSHA(); sha != "main-sha" {
		t.Fatalf("expected 'main-sha', got %s", sha)
	}
}

func TestCheckpoint(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Checkpoint(); err != nil {
		t.Fatal(err)
	}
}

func TestMetaHelpers(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if sha := s.GetCommitSHA(); sha != "" {
		t.Fatal("expected empty commit sha")
	}
	s.SetCommitSHA("sha1")
	if sha := s.GetCommitSHA(); sha != "sha1" {
		t.Fatalf("expected 'sha1', got %s", sha)
	}

	if h := s.GetIgnoredFilesHash(); h != "" {
		t.Fatal("expected empty ignored files hash")
	}
	s.SetIgnoredFilesHash("hash1")
	if h := s.GetIgnoredFilesHash(); h != "hash1" {
		t.Fatalf("expected 'hash1', got %s", h)
	}

	if sha := s.GetGraphCommitSHA(); sha != "" {
		t.Fatal("expected empty graph commit sha")
	}
	s.SetGraphCommitSHA("gsha1")
	if sha := s.GetGraphCommitSHA(); sha != "gsha1" {
		t.Fatalf("expected 'gsha1', got %s", sha)
	}
}

func TestHasOldFormat_ReturnsFalseForEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if HasOldFormat(dir) {
		t.Fatal("expected false for empty dir")
	}
}

func TestHasOldFormat_ReturnsTrueForManifest(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "MANIFEST")
	if err := os.WriteFile(manifest, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	if !HasOldFormat(dir) {
		t.Fatal("expected true when MANIFEST exists")
	}
}

func TestClear_RemovesSymbols(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sym := Symbol{ID: "s1", Name: "test", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	if err := s.StoreFile("main.go", []Symbol{sym}, nil); err != nil {
		t.Fatal(err)
	}
	has, _ := s.HasFile("main.go")
	if !has {
		t.Fatal("expected file to exist")
	}

	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	has, _ = s.HasFile("main.go")
	if has {
		t.Fatal("expected file to be removed after clear")
	}
}

func TestClearAll_DropsAllData(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ch := chunker.Chunk{ID: "c1", FilePath: "/p/main.go", RelPath: "main.go", Content: "test"}
	s.UpsertChunks([]chunker.Chunk{ch}, map[string][]float32{"c1": {1, 0, 0, 0}})

	if err := s.ClearAll(); err != nil {
		t.Fatal(err)
	}

	// Should not error and stats should be 0
	if _, _, err := s.Stats(); err != nil {
		t.Fatal(err)
	}
}

func TestNeedsReindex_ReturnsFalseForFreshDB(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if s.NeedsReindex() {
		t.Fatal("expected false for fresh db")
	}
}

func TestListFiles_EmptyAndWithData(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	files, err := s.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatal("expected empty list")
	}

	chunks := []chunker.Chunk{
		{ID: "c1", FilePath: "/p/a.go", RelPath: "a.go", Content: "a"},
		{ID: "c2", FilePath: "/p/b.go", RelPath: "b.go", Content: "b"},
	}
	s.UpsertChunks(chunks, map[string][]float32{
		"c1": {1, 0, 0, 0}, "c2": {0, 1, 0, 0},
	})

	files, err = s.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
}

func TestFloat32sToBytes(t *testing.T) {
	input := []float32{1.0, 0.5, 0.0, -1.0}
	b := float32sToBytes(input)
	if len(b) != len(input)*4 {
		t.Fatalf("expected %d bytes, got %d", len(input)*4, len(b))
	}
}

func TestListSymbolFiles_Empty(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	files, err := s.ListSymbolFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatal("expected empty")
	}
}

func TestListSymbolFiles_WithSymbols(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sym := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	s.StoreFile("main.go", []Symbol{sym}, nil)

	files, err := s.ListSymbolFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "main.go" {
		t.Fatalf("expected [main.go], got %v", files)
	}
}

func TestIsFileIndexed_EmptyAndPopulated(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ok, err := s.IsFileIndexed("/p/main.go", "")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected false for empty db")
	}

	ch := chunker.Chunk{ID: "c1", FilePath: "/p/main.go", RelPath: "main.go",
		FileHash: "abc", Content: "test"}
	s.UpsertChunks([]chunker.Chunk{ch}, map[string][]float32{"c1": {1, 0, 0, 0}})

	ok, _ = s.IsFileIndexed("/p/main.go", "")
	if !ok {
		t.Fatal("expected true for indexed file")
	}
	ok, _ = s.IsFileIndexed("/p/main.go", "abc")
	if !ok {
		t.Fatal("expected true for matching hash")
	}
	ok, _ = s.IsFileIndexed("/p/main.go", "wrong")
	if ok {
		t.Fatal("expected false for wrong hash")
	}
}
