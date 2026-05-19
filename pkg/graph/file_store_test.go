package graph

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()

	db, err := OpenGraph(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Store symbols and refs for two files
	storeFile(t, db, "pkg/indexer/indexer.go", []Symbol{
		{ID: "h1:pkg/indexer/indexer.go:26:Indexer", Name: "Indexer", Kind: SymbolStruct,
			FilePath: "C:/project/apps/go-indexing-mcp/pkg/indexer/indexer.go",
			RelPath: "pkg/indexer/indexer.go", StartLine: 26, EndLine: 39,
			Signature: "type Indexer struct {", Exported: true},
		{ID: "h1:pkg/indexer/indexer.go:54:New", Name: "New", Kind: SymbolFunction,
			FilePath: "C:/project/apps/go-indexing-mcp/pkg/indexer/indexer.go",
			RelPath: "pkg/indexer/indexer.go", StartLine: 54, EndLine: 67,
			Signature: "func New(...) *Indexer {", Exported: true},
	}, []Reference{
		{ID: "r1", SourceID: "h1:pkg/indexer/indexer.go:54:New", TargetName: "New", Kind: RefCalls,
			FilePath: "pkg/indexer/indexer.go", Line: 55, Confidence: 1.0},
	})

	storeFile(t, db, "internal/cli/handlers.go", []Symbol{
		{ID: "h2:internal/cli/handlers.go:112:RunGenerate", Name: "RunGenerate", Kind: SymbolFunction,
			FilePath: "C:/project/apps/go-indexing-mcp/internal/cli/handlers.go",
			RelPath: "internal/cli/handlers.go", StartLine: 112, EndLine: 209,
			Signature: "func RunGenerate(...) int {", Exported: true},
	}, []Reference{
		{ID: "r2", SourceID: "h2:internal/cli/handlers.go:112:RunGenerate", TargetName: "New", Kind: RefCalls,
			FilePath: "internal/cli/handlers.go", Line: 112, Confidence: 1.0},
		{ID: "r3", SourceID: "h2:internal/cli/handlers.go:112:RunGenerate", TargetName: "Indexer", Kind: RefCalls,
			FilePath: "internal/cli/handlers.go", Line: 112, Confidence: 1.0},
	})

	db.Close()

	// Now reload and verify
	db2, err := OpenGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	kg := NewGraph()
	if err := db2.LoadAll(kg); err != nil {
		t.Fatal("LoadAll:", err)
	}

	symCount, refCount := kg.Stats()
	if symCount != 3 {
		t.Errorf("expected 3 symbols, got %d", symCount)
	}
	if refCount != 3 {
		t.Errorf("expected 3 refs, got %d", refCount)
	}

	// Verify Indexer definition exists
	defs := kg.FindByName("Indexer")
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition for Indexer, got %d", len(defs))
	}
	if defs[0].Name != "Indexer" {
		t.Errorf("expected name Indexer, got %s", defs[0].Name)
	}
	if defs[0].Kind != SymbolStruct {
		t.Errorf("expected kind SymbolStruct, got %v", defs[0].Kind)
	}

	// Verify usages of "New" exist — two refs target New (r1 from indexer.go, r2 from handlers.go)
	usages := kg.FindUsages("New", "")
	if len(usages) != 2 {
		t.Fatalf("expected 2 usages of 'New', got %d", len(usages))
	}

	// Verify usages of "Indexer" exist (type reference from handlers.go)
	indexerUsages := kg.FindUsages("Indexer", "")
	if len(indexerUsages) != 1 {
		t.Fatalf("expected 1 usage of 'Indexer', got %d — this confirms the storage round-trip is correct, but the extractor doesn't create type references", len(indexerUsages))
	}

	// Verify usages by file
	fileSymbols := kg.GetFileSymbols("pkg/indexer/indexer.go")
	if len(fileSymbols) != 2 {
		t.Errorf("expected 2 symbols in indexer.go, got %d", len(fileSymbols))
	}

	// RemoveFile test
	if err := db2.RemoveFile("pkg/indexer/indexer.go"); err != nil {
		t.Fatal("RemoveFile:", err)
	}

	// Re-create cache to verify removal
	kg2 := NewGraph()
	if err := db2.LoadAll(kg2); err != nil {
		t.Fatal("LoadAll after remove:", err)
	}
	symCount2, refCount2 := kg2.Stats()
	if symCount2 != 1 {
		t.Errorf("expected 1 symbol after removal, got %d", symCount2)
	}
	if refCount2 != 2 {
		t.Errorf("expected 2 refs after removal, got %d", refCount2)
	}
}

func storeFile(t *testing.T, db *GraphDB, relPath string, symbols []Symbol, refs []Reference) {
	t.Helper()
	if err := db.StoreFile(relPath, symbols, refs); err != nil {
		t.Fatal("StoreFile:", err)
	}
}

func TestFileStoreOnDiskFormat(t *testing.T) {
	dir := t.TempDir()

	db, err := OpenGraph(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Store a file with a subdirectory path
	err = db.StoreFile("pkg/config/config.go", []Symbol{
		{ID: "h:pkg/config/config.go:10:Config", Name: "Config", Kind: SymbolStruct,
			RelPath: "pkg/config/config.go"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Verify the file exists on disk at the expected location
	expectedPath := filepath.Join(dir, "pkg", "config", "config.go.json")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Fatalf("expected file at %s, but it doesn't exist", expectedPath)
	}

	// Verify subdirectories were created
	subDir := filepath.Join(dir, "pkg", "config")
	if fi, err := os.Stat(subDir); err != nil {
		t.Fatalf("expected subdirectory %s: %v", subDir, err)
	} else if !fi.IsDir() {
		t.Fatalf("expected %s to be a directory", subDir)
	}

	// Load back and verify
	db2, err := OpenGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	kg := NewGraph()
	if err := db2.LoadAll(kg); err != nil {
		t.Fatal("LoadAll:", err)
	}

	defs := kg.FindByName("Config")
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition for Config, got %d", len(defs))
	}
}

func TestFileStoreClear(t *testing.T) {
	dir := t.TempDir()

	db, err := OpenGraph(dir)
	if err != nil {
		t.Fatal(err)
	}

	storeFile(t, db, "a.go", []Symbol{
		{ID: "h:a.go:1:A", Name: "A", Kind: SymbolFunction, RelPath: "a.go"},
	}, nil)
	storeFile(t, db, "b.go", []Symbol{
		{ID: "h:b.go:1:B", Name: "B", Kind: SymbolFunction, RelPath: "b.go"},
	}, nil)

	stats, err := db.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats != 2 {
		t.Fatalf("expected 2 files, got %d", stats)
	}

	if err := db.Clear(); err != nil {
		t.Fatal("Clear:", err)
	}

	stats, err = db.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats != 0 {
		t.Fatalf("expected 0 files after clear, got %d", stats)
	}
}

func TestFileStoreRemoveFile(t *testing.T) {
	dir := t.TempDir()

	db, err := OpenGraph(dir)
	if err != nil {
		t.Fatal(err)
	}

	storeFile(t, db, "a.go", []Symbol{
		{ID: "h:a.go:1:A", Name: "A", Kind: SymbolFunction, RelPath: "a.go"},
	}, nil)
	storeFile(t, db, "b.go", []Symbol{
		{ID: "h:b.go:1:B", Name: "B", Kind: SymbolFunction, RelPath: "b.go"},
	}, nil)

	if err := db.RemoveFile("a.go"); err != nil {
		t.Fatal(err)
	}

	kg := NewGraph()
	if err := db.LoadAll(kg); err != nil {
		t.Fatal(err)
	}

	syms, _ := kg.Stats()
	if syms != 1 {
		t.Fatalf("expected 1 symbol after removal, got %d", syms)
	}
}

func TestFileStoreStats(t *testing.T) {
	dir := t.TempDir()

	db, err := OpenGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Empty dir
	stats, err := db.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats != 0 {
		t.Errorf("expected 0, got %d", stats)
	}

	// Add files
	storeFile(t, db, "a.go", []Symbol{{ID: "h:a.go:1:A", Name: "A", Kind: SymbolFunction, RelPath: "a.go"}}, nil)
	storeFile(t, db, "b.go", []Symbol{{ID: "h:b.go:1:B", Name: "B", Kind: SymbolFunction, RelPath: "b.go"}}, nil)

	stats, err = db.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats != 2 {
		t.Errorf("expected 2, got %d", stats)
	}
}
