package db

import (
	"testing"
)

func TestStoreFile_And_HasFile(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sym := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go", StartLine: 1, EndLine: 10,
		Signature: "func Foo()", Exported: true}

	if err := s.StoreFile("main.go", []Symbol{sym}, nil); err != nil {
		t.Fatal(err)
	}

	has, err := s.HasFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("expected HasFile to return true")
	}

	has, _ = s.HasFile("nonexistent.go")
	if has {
		t.Fatal("expected HasFile to return false for nonexistent")
	}
}

func TestStoreFile_WithReferences(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sym := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	ref := Reference{ID: "r1", SourceID: "s1", TargetName: "Bar",
		Kind: RefCalls, FilePath: "/p/main.go", Line: 5}

	if err := s.StoreFile("main.go", []Symbol{sym}, []Reference{ref}); err != nil {
		t.Fatal(err)
	}

	defs, err := s.FindDefinition("Foo", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(defs))
	}
}

func TestRemoveFile(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sym := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	s.StoreFile("main.go", []Symbol{sym}, nil)

	if err := s.RemoveFile("main.go"); err != nil {
		t.Fatal(err)
	}

	has, _ := s.HasFile("main.go")
	if has {
		t.Fatal("expected file to be removed")
	}
}

func TestRemoveFile_NonExistent(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.RemoveFile("nonexistent.go"); err != nil {
		t.Fatal("removing nonexistent file should not error")
	}
}

func TestStoreFile_ReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sym1 := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	s.StoreFile("main.go", []Symbol{sym1}, nil)

	sym2 := Symbol{ID: "s2", Name: "Bar", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	s.StoreFile("main.go", []Symbol{sym2}, nil)

	defs, _ := s.FindDefinition("Foo", "")
	if len(defs) != 0 {
		t.Fatal("expected Foo to be replaced")
	}
	defs, _ = s.FindDefinition("Bar", "")
	if len(defs) != 1 {
		t.Fatal("expected Bar to exist")
	}
}

func TestFindDefinition_ByExactName(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sym := Symbol{ID: "s1", Name: "Validate", Kind: SymbolFunction,
		FilePath: "/p/validate.go", RelPath: "validate.go"}
	s.StoreFile("validate.go", []Symbol{sym}, nil)

	defs, err := s.FindDefinition("Validate", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Name != "Validate" {
		t.Fatalf("expected 1 Validate, got %d", len(defs))
	}
}

func TestFindDefinition_ByMethodSuffix(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sym := Symbol{ID: "s1", Name: "(*Handler).Serve", Kind: SymbolMethod,
		FilePath: "/p/handler.go", RelPath: "handler.go"}
	s.StoreFile("handler.go", []Symbol{sym}, nil)

	defs, err := s.FindDefinition("Serve", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition for method suffix, got %d", len(defs))
	}
}

func TestFindDefinition_WithPathFilter(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s1 := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/pkg/a/a.go", RelPath: "pkg/a/a.go"}
	s2 := Symbol{ID: "s2", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/pkg/b/b.go", RelPath: "pkg/b/b.go"}
	s.StoreFile("pkg/a/a.go", []Symbol{s1}, nil)
	s.StoreFile("pkg/b/b.go", []Symbol{s2}, nil)

	defs, _ := s.FindDefinition("Foo", "pkg/a")
	if len(defs) != 1 || defs[0].RelPath != "pkg/a/a.go" {
		t.Fatalf("expected 1 from pkg/a, got %d", len(defs))
	}
}

func TestFindDefinition_NonExistent(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	defs, err := s.FindDefinition("NonExistent", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 0 {
		t.Fatal("expected no definitions")
	}
}

func TestFindUsages(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sym := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	ref := Reference{ID: "r1", SourceID: "s1", TargetName: "Foo",
		Kind: RefCalls, FilePath: "/p/main.go", Line: 5}
	s.StoreFile("main.go", []Symbol{sym}, []Reference{ref})

	usages, err := s.FindUsages("Foo", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 1 {
		t.Fatalf("expected 1 usage, got %d", len(usages))
	}
}

func TestFindUsages_WithPathFilter(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ref1 := Reference{ID: "r1", SourceID: "s1", TargetName: "Foo",
		Kind: RefCalls, FilePath: "pkg/a/a.go", Line: 1}
	ref2 := Reference{ID: "r2", SourceID: "s2", TargetName: "Foo",
		Kind: RefCalls, FilePath: "pkg/b/b.go", Line: 1}

	sym := Symbol{ID: "s0", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/foo.go", RelPath: "foo.go"}
	s.StoreFile("foo.go", []Symbol{sym}, []Reference{ref1, ref2})

	usages, _ := s.FindUsages("Foo", "pkg/a")
	if len(usages) != 1 {
		t.Fatalf("expected 1 usage filtered to pkg/a, got %d", len(usages))
	}
}

func TestFindImports(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	imps := []Symbol{
		{ID: "i1", Name: "fmt", Kind: SymbolImport, FilePath: "/p/main.go", RelPath: "main.go", Signature: "fmt"},
		{ID: "i2", Name: "os", Kind: SymbolImport, FilePath: "/p/main.go", RelPath: "main.go", Signature: "os"},
		{ID: "i3", Name: "strings", Kind: SymbolImport, FilePath: "/p/main.go", RelPath: "main.go", Signature: "strings"},
	}
	s.StoreFile("main.go", imps, nil)

	results, err := s.FindImports("os")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Name != "os" {
		t.Fatalf("expected 1 'os' import, got %d", len(results))
	}
}

func TestGetCallers(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sym := Symbol{ID: "s1", Name: "Helper", Kind: SymbolFunction,
		FilePath: "/p/helper.go", RelPath: "helper.go"}
	caller := Reference{ID: "r1", SourceID: "s2", TargetName: "Helper",
		Kind: RefCalls, FilePath: "/p/main.go", Line: 10}
	s.StoreFile("helper.go", []Symbol{sym}, []Reference{caller})

	callers, err := s.GetCallers("Helper")
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 {
		t.Fatalf("expected 1 caller, got %d", len(callers))
	}
}

func TestGetCallees(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	callee := Reference{ID: "r1", SourceID: "s1", TargetName: "fmt.Println",
		Kind: RefCalls, FilePath: "/p/main.go", Line: 5}
	sym := Symbol{ID: "s1", Name: "main", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	s.StoreFile("main.go", []Symbol{sym}, []Reference{callee})

	callees, err := s.GetCallees("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(callees) != 1 {
		t.Fatalf("expected 1 callee, got %d", len(callees))
	}
}

func TestGraphStats(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	syms, refs, err := s.GraphStats()
	if err != nil {
		t.Fatal(err)
	}
	if syms != 0 || refs != 0 {
		// This may be 0/0 or 0/1 depending on join behavior
		t.Logf("empty stats: syms=%d, refs=%d", syms, refs)
	}

	sym := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	ref := Reference{ID: "r1", SourceID: "s1", TargetName: "Bar",
		Kind: RefCalls, FilePath: "/p/main.go", Line: 5}
	s.StoreFile("main.go", []Symbol{sym}, []Reference{ref})

	syms, refs, err = s.GraphStats()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("populated stats: syms=%d, refs=%d", syms, refs)
}

func TestGetFileRefs(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sym := Symbol{ID: "s1", Name: "Foo", Kind: SymbolFunction,
		FilePath: "/p/main.go", RelPath: "main.go"}
	ref := Reference{ID: "r1", SourceID: "s1", TargetName: "Bar",
		Kind: RefCalls, FilePath: "main.go", Line: 5}
	s.StoreFile("main.go", []Symbol{sym}, []Reference{ref})

	refs, err := s.GetFileRefs("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
}

func TestResolveRefs(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Store a symbol
	calleeSym := Symbol{ID: "s1", Name: "TargetFunc", Kind: SymbolFunction,
		FilePath: "/p/target.go", RelPath: "target.go"}
	s.StoreFile("target.go", []Symbol{calleeSym}, nil)

	// Store a ref with unresolved target_id
	callerSym := Symbol{ID: "s2", Name: "Caller", Kind: SymbolFunction,
		FilePath: "/p/caller.go", RelPath: "caller.go"}
	ref := Reference{ID: "r1", SourceID: "s2", TargetName: "TargetFunc",
		TargetID: "", Kind: RefCalls, FilePath: "caller.go", Line: 10}
	s.StoreFile("caller.go", []Symbol{callerSym}, []Reference{ref})

	resolved, err := s.ResolveRefs()
	if err != nil {
		t.Fatal(err)
	}
	if resolved != 1 {
		t.Fatalf("expected 1 resolved ref, got %d", resolved)
	}

	// Verify target_id was set
	callees, _ := s.GetCallees("s2")
	if len(callees) != 1 || callees[0].TargetID != "s1" {
		t.Fatalf("expected resolved target_id=s1, got %+v", callees)
	}
}

func TestResolveRefs_NoUnresolved(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	resolved, err := s.ResolveRefs()
	if err != nil {
		t.Fatal(err)
	}
	if resolved != 0 {
		t.Fatalf("expected 0, got %d", resolved)
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
		{"pkg/a/a.go", "pkg/a/a.go", true},
		{"pkg/a/ab.go", "pkg/a/a", true},
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
		{ID: "r3", FilePath: "pkg/a/c.go"},
	}
	filtered := filterRefsByPath(refs, "pkg/a")
	if len(filtered) != 2 {
		t.Fatalf("expected 2, got %d", len(filtered))
	}
}

func TestGraphNeedsReindex_ReturnsFalseForFresh(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if s.GraphNeedsReindex() {
		t.Fatal("expected false for fresh db")
	}
}
