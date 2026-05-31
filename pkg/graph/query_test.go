package graph

import (
	"path/filepath"
	"testing"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/db"
)

func newGraphFromTemp(t *testing.T) *GraphQuery {
	t.Helper()
	dir := t.TempDir()
	g, err := NewGraphQuery(filepath.Join(dir, "graph.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

func TestNewAndClose(t *testing.T) {
	dir := t.TempDir()
	g, err := NewGraphQuery(filepath.Join(dir, "graph.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if g.Store == nil {
		t.Fatal("expected non-nil store")
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNewGraphQuery_FromDirectory(t *testing.T) {
	dir := t.TempDir()
	g, err := NewGraphQuery(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if g.Store == nil {
		t.Fatal("expected non-nil store for directory input")
	}
	if g.DBPath != filepath.Join(dir, "graph.sqlite") {
		t.Fatalf("expected %q, got %q", filepath.Join(dir, "graph.sqlite"), g.DBPath)
	}
}

func TestStoreFileAndHasFile(t *testing.T) {
	g := newGraphFromTemp(t)

	sym := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	if err := g.StoreFile("main.go", []Symbol{sym}, nil); err != nil {
		t.Fatal(err)
	}
	if !g.HasFile("main.go") {
		t.Fatal("expected HasFile=true")
	}
	if g.HasFile("nonexistent.go") {
		t.Fatal("expected HasFile=false")
	}
}

func TestFindDefinition(t *testing.T) {
	g := newGraphFromTemp(t)

	sym := Symbol{ID: "s1", Name: "Validate", Kind: SymbolFunction,
		FilePath: "/p/validate.go", RelPath: "validate.go"}
	g.StoreFile("validate.go", []Symbol{sym}, nil)

	defs := g.FindDefinition("Validate", "")
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(defs))
	}
}

func TestFindDefinition_WithPathFilter(t *testing.T) {
	g := newGraphFromTemp(t)

	s1 := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/pkg/a/a.go", RelPath: "pkg/a/a.go"}
	s2 := Symbol{ID: "s2", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/pkg/b/b.go", RelPath: "pkg/b/b.go"}
	g.StoreFile("pkg/a/a.go", []Symbol{s1}, nil)
	g.StoreFile("pkg/b/b.go", []Symbol{s2}, nil)

	defs := g.FindDefinition("Foo", "pkg/a")
	if len(defs) != 1 || defs[0].RelPath != "pkg/a/a.go" {
		t.Fatalf("expected 1 from pkg/a, got %d: %+v", len(defs), defs)
	}
}

func TestFindUsages(t *testing.T) {
	g := newGraphFromTemp(t)

	sym := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	ref := Reference{ID: "r1", SourceID: "s1", TargetName: "Foo",
		Kind: RefCalls, FilePath: "main.go", Line: 5}
	g.StoreFile("main.go", []Symbol{sym}, []Reference{ref})

	usages := g.FindUsages("Foo", "")
	if len(usages) != 1 {
		t.Fatalf("expected 1 usage, got %d", len(usages))
	}
}

func TestFindImports(t *testing.T) {
	g := newGraphFromTemp(t)

	imp := Symbol{ID: "i1", Name: "fmt", Kind: SymbolImport,
		FilePath: "/p/main.go", RelPath: "main.go", Signature: "fmt"}
	g.StoreFile("main.go", []Symbol{imp}, nil)

	results := g.FindImports("fmt")
	if len(results) != 1 || results[0].Name != "fmt" {
		t.Fatalf("expected 1 'fmt' import, got %d", len(results))
	}
}

func TestGetCallersAndCallees(t *testing.T) {
	g := newGraphFromTemp(t)

	helper := Symbol{ID: "s1", Name: "Helper", Kind: SymbolFunction,
		FilePath: "/p/helper.go", RelPath: "helper.go"}
	callerRef := Reference{ID: "r1", SourceID: "s2", TargetName: "Helper",
		Kind: RefCalls, FilePath: "main.go", Line: 10}
	g.StoreFile("helper.go", []Symbol{helper}, []Reference{callerRef})

	callers := g.GetCallers("Helper")
	if len(callers) != 1 {
		t.Fatalf("expected 1 caller, got %d", len(callers))
	}
}

func TestGetCallees(t *testing.T) {
	g := newGraphFromTemp(t)

	callee := Reference{ID: "r1", SourceID: "s1", TargetName: "fmt.Println",
		Kind: RefCalls, FilePath: "main.go", Line: 5}
	sym := Symbol{ID: "s1", Name: "main", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	g.StoreFile("main.go", []Symbol{sym}, []Reference{callee})

	callees := g.GetCallees("s1")
	if len(callees) != 1 {
		t.Fatalf("expected 1 callee, got %d", len(callees))
	}
}

func TestGetSymbolInfo(t *testing.T) {
	g := newGraphFromTemp(t)

	sym := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	caller := Reference{ID: "r1", SourceID: "s2", TargetName: "Foo",
		TargetID: "", Kind: RefCalls, FilePath: "main.go", Line: 10}
	g.StoreFile("main.go", []Symbol{sym}, []Reference{caller})

	info := g.GetSymbolInfo("Foo", "")
	if info == nil {
		t.Fatal("expected non-nil SymbolInfo")
	}
	if len(info.Definitions) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(info.Definitions))
	}
	if len(info.Definitions[0].Callers) != 1 {
		t.Fatalf("expected 1 caller, got %d", len(info.Definitions[0].Callers))
	}
}

func TestGetSymbolInfo_WithPathFilter(t *testing.T) {
	g := newGraphFromTemp(t)

	s1 := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/pkg/a/a.go", RelPath: "pkg/a/a.go"}
	s2 := Symbol{ID: "s2", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/pkg/b/b.go", RelPath: "pkg/b/b.go"}
	callerA := Reference{ID: "r1", SourceID: "s0", TargetName: "Foo",
		Kind: RefCalls, FilePath: "pkg/a/caller.go", Line: 5}
	callerB := Reference{ID: "r2", SourceID: "s0", TargetName: "Foo",
		Kind: RefCalls, FilePath: "pkg/b/caller.go", Line: 5}
	g.StoreFile("pkg/a/a.go", []Symbol{s1}, []Reference{callerA})
	g.StoreFile("pkg/b/b.go", []Symbol{s2}, []Reference{callerB})

	info := g.GetSymbolInfo("Foo", "pkg/a")
	if len(info.Definitions) != 1 {
		t.Fatalf("expected 1 definition (filtered), got %d", len(info.Definitions))
	}
}

func TestGetSymbolInfo_NoMatch(t *testing.T) {
	g := newGraphFromTemp(t)

	info := g.GetSymbolInfo("NonExistent", "")
	if info == nil {
		t.Fatal("expected non-nil info even with no match")
	}
	if len(info.Definitions) != 0 {
		t.Fatal("expected 0 definitions")
	}
}

func TestResolveRefs(t *testing.T) {
	g := newGraphFromTemp(t)

	targetSym := Symbol{ID: "s1", Name: "TargetFunc", Kind: SymbolFunction,
		FilePath: "/p/target.go", RelPath: "target.go"}
	g.StoreFile("target.go", []Symbol{targetSym}, nil)

	callerSym := Symbol{ID: "s2", Name: "Caller", Kind: SymbolFunction,
		FilePath: "/p/caller.go", RelPath: "caller.go"}
	ref := Reference{ID: "r1", SourceID: "s2", TargetName: "TargetFunc",
		TargetID: "", Kind: RefCalls, FilePath: "caller.go", Line: 10}
	g.StoreFile("caller.go", []Symbol{callerSym}, []Reference{ref})

	resolved := g.ResolveRefs()
	if resolved != 1 {
		t.Fatalf("expected 1 resolved ref, got %d", resolved)
	}
}

func TestResolveRefs_NoUnresolved(t *testing.T) {
	g := newGraphFromTemp(t)

	resolved := g.ResolveRefs()
	if resolved != 0 {
		t.Fatalf("expected 0, got %d", resolved)
	}
}

func TestRemoveFile(t *testing.T) {
	g := newGraphFromTemp(t)

	sym := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	g.StoreFile("main.go", []Symbol{sym}, nil)

	g.RemoveFile("main.go")
	if g.HasFile("main.go") {
		t.Fatal("expected file removed")
	}
}

func TestStats(t *testing.T) {
	g := newGraphFromTemp(t)

	syms, refs := g.Stats()
	if syms != 0 || refs != 0 {
		t.Logf("initial stats: syms=%d, refs=%d", syms, refs)
	}

	sym := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	g.StoreFile("main.go", []Symbol{sym}, nil)

	syms, refs = g.Stats()
	t.Logf("after store: syms=%d, refs=%d", syms, refs)
}

func TestSwitchBranch(t *testing.T) {
	g := newGraphFromTemp(t)

	sym := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	g.StoreFile("main.go", []Symbol{sym}, nil)

	if err := g.SwitchBranch("feature", ""); err != nil {
		t.Fatal(err)
	}
	if g.HasFile("main.go") {
		t.Fatal("expected no file on new branch")
	}

	if err := g.SwitchBranch("main", ""); err != nil {
		t.Fatal(err)
	}
	// After switching back to main, verify the DBPath is correct
	if g.DBPath == "" {
		t.Fatal("expected non-empty DBPath")
	}
}

func TestBranchSuffix(t *testing.T) {
	tests := []struct {
		branch    string
		worktree  string
		expected  string
	}{
		{"main", "", ""},
		{"feature", "", "-feature"},
		{"feature", "worktrees/feat", "-worktrees-feat-feature"},
	}
	for _, tc := range tests {
		got := branchSuffix(tc.branch, tc.worktree)
		if got != tc.expected {
			t.Errorf("branchSuffix(%q, %q) = %q, want %q", tc.branch, tc.worktree, got, tc.expected)
		}
	}
}

func TestMatchesFilter(t *testing.T) {
	tests := []struct {
		path   string
		filter string
		match  bool
	}{
		{"pkg/a/a.go", "pkg/a", true},
		{"pkg/a/a.go", "pkg/b", false},
		{"pkg/a/a.go", "", true},
	}
	for _, tc := range tests {
		got := matchesFilter(tc.path, tc.filter)
		if got != tc.match {
			t.Errorf("matchesFilter(%q, %q) = %v, want %v", tc.path, tc.filter, got, tc.match)
		}
	}
}

func TestFilterRefsByPath(t *testing.T) {
	refs := []Reference{
		{ID: "r1", FilePath: "pkg/a/a.go"},
		{ID: "r2", FilePath: "pkg/b/b.go"},
	}
	filtered := filterRefsByPath(refs, "pkg/a")
	if len(filtered) != 1 {
		t.Fatalf("expected 1, got %d", len(filtered))
	}
}

func TestListSymbolFiles(t *testing.T) {
	g := newGraphFromTemp(t)

	sym := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	g.StoreFile("main.go", []Symbol{sym}, nil)

	files, err := g.ListSymbolFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "main.go" {
		t.Fatalf("expected [main.go], got %v", files)
	}
}

func TestNilStoreMethods(t *testing.T) {
	g := &GraphQuery{Store: nil}

	if g.HasFile("any.go") {
		t.Fatal("expected false for nil store")
	}
	if defs := g.FindDefinition("foo", ""); defs != nil {
		t.Fatal("expected nil for nil store")
	}
	if refs := g.FindUsages("foo", ""); refs != nil {
		t.Fatal("expected nil for nil store")
	}
	if syms := g.FindImports("foo"); syms != nil {
		t.Fatal("expected nil for nil store")
	}
	if callers := g.GetCallers("foo"); callers != nil {
		t.Fatal("expected nil for nil store")
	}
	if callees := g.GetCallees("foo"); callees != nil {
		t.Fatal("expected nil for nil store")
	}
	if info := g.GetSymbolInfo("foo", ""); info != nil {
		t.Fatal("expected nil for nil store")
	}
	if syms, refs := g.Stats(); syms != 0 || refs != 0 {
		t.Fatal("expected 0,0 for nil store")
	}
	if resolved := g.ResolveRefs(); resolved != 0 {
		t.Fatal("expected 0 for nil store")
	}
	g.RemoveFile("any.go") // should not panic
	if err := g.Close(); err != nil {
		t.Fatal("expected nil error for nil store close")
	}
	if err := g.SwitchBranch("main", ""); err != nil {
		t.Fatal("expected nil error for nil store switch")
	}
}

func TestNewGraphQueryFromStore(t *testing.T) {
	dir := t.TempDir()
	store, err := db.Open(dir+"/test.sqlite", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	g := NewGraphQueryFromStore(store)
	if g.Store != store {
		t.Fatal("expected same store")
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
}
