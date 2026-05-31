package graph

import (
	"testing"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/db"
)

func TestFormatSymbolInfo_NilInput(t *testing.T) {
	if result := FormatSymbolInfo(nil); result != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestFormatSymbolInfo_EmptyInput(t *testing.T) {
	info := &SymbolInfo{}
	result := FormatSymbolInfo(info)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Definitions) != 0 {
		t.Fatal("expected empty definitions")
	}
}

func TestFormatSymbolInfo_WithDefinition(t *testing.T) {
	info := &SymbolInfo{
		Definitions: []SymbolDef{
			{
				Symbol: db.Symbol{
					Name:      "Foo",
					Kind:      SymbolFunction,
					FilePath:  "/p/main.go",
					StartLine: 10,
					EndLine:   20,
					Signature: "func Foo()",
				},
			},
		},
	}
	result := FormatSymbolInfo(info)
	if len(result.Definitions) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(result.Definitions))
	}
	def := result.Definitions[0]
	if def.Name != "Foo" {
		t.Fatalf("expected 'Foo', got %s", def.Name)
	}
	if def.Kind != "function" {
		t.Fatalf("expected 'function', got %s", def.Kind)
	}
	if def.FilePath != "/p/main.go" {
		t.Fatalf("expected '/p/main.go', got %s", def.FilePath)
	}
}

func TestFormatSymbolInfo_WithCallers(t *testing.T) {
	info := &SymbolInfo{
		Definitions: []SymbolDef{
			{
				Symbol: db.Symbol{Name: "Foo", Kind: SymbolFunction, FilePath: "/p/main.go"},
				Callers: []Reference{
					{TargetName: "main", FilePath: "/p/main.go", Line: 5},
				},
			},
		},
	}
	result := FormatSymbolInfo(info)
	if len(result.Definitions[0].Callers) != 1 {
		t.Fatalf("expected 1 caller, got %d", len(result.Definitions[0].Callers))
	}
}

func TestFormatSymbolInfo_WithCallees(t *testing.T) {
	info := &SymbolInfo{
		Definitions: []SymbolDef{
			{
				Symbol: db.Symbol{Name: "Foo", Kind: SymbolFunction, FilePath: "/p/main.go"},
				Callees: []Reference{
					{TargetName: "fmt.Println", FilePath: "/p/main.go", Line: 15},
				},
			},
		},
	}
	result := FormatSymbolInfo(info)
	if len(result.Definitions[0].Callees) != 1 {
		t.Fatalf("expected 1 callee, got %d", len(result.Definitions[0].Callees))
	}
}

func TestFormatSymbolInfo_WithUsages(t *testing.T) {
	info := &SymbolInfo{
		Usages: []Reference{
			{TargetName: "Foo", Kind: RefCalls, FilePath: "/p/other.go", Line: 3},
		},
	}
	result := FormatSymbolInfo(info)
	if len(result.Usages) != 1 {
		t.Fatalf("expected 1 usage, got %d", len(result.Usages))
	}
	if result.Usages[0].Kind != "calls" {
		t.Fatalf("expected 'calls', got %s", result.Usages[0].Kind)
	}
}

func TestFormatSymbolInfo_SuppressesDuplicateUsages(t *testing.T) {
	info := &SymbolInfo{
		Definitions: []SymbolDef{
			{
				Symbol: db.Symbol{Name: "Foo", Kind: SymbolFunction, FilePath: "/p/main.go"},
				Callers: []Reference{
					{TargetName: "main", FilePath: "/p/main.go", Line: 5},
				},
			},
		},
		Usages: []Reference{
			{TargetName: "main", FilePath: "/p/main.go", Line: 5},
		},
	}
	result := FormatSymbolInfo(info)
	if result.Usages != nil {
		t.Fatal("expected usages to be suppressed (duplicate of caller set)")
	}
}

func TestFormatSymbolInfo_DoesNotSuppressDifferentUsages(t *testing.T) {
	info := &SymbolInfo{
		Definitions: []SymbolDef{
			{
				Symbol: db.Symbol{Name: "Foo", Kind: SymbolFunction, FilePath: "/p/main.go"},
				Callers: []Reference{
					{TargetName: "main", FilePath: "/p/main.go", Line: 5},
				},
			},
		},
		Usages: []Reference{
			{TargetName: "Foo", FilePath: "/p/other.go", Line: 10},
		},
	}
	result := FormatSymbolInfo(info)
	if result.Usages == nil {
		t.Fatal("expected usages not to be suppressed")
	}
}

func TestRefSetsEqual_EqualSets(t *testing.T) {
	a := []FormatRefResult{
		{TargetName: "main", FilePath: "/p/main.go", Line: 1},
		{TargetName: "foo", FilePath: "/p/foo.go", Line: 2},
	}
	b := []FormatRefResult{
		{TargetName: "foo", FilePath: "/p/foo.go", Line: 2},
		{TargetName: "main", FilePath: "/p/main.go", Line: 1},
	}
	if !refSetsEqual(a, b) {
		t.Fatal("expected equal sets")
	}
}

func TestRefSetsEqual_DifferentLengths(t *testing.T) {
	a := []FormatRefResult{{TargetName: "main", FilePath: "/p/main.go", Line: 1}}
	b := []FormatRefResult{
		{TargetName: "main", FilePath: "/p/main.go", Line: 1},
		{TargetName: "foo", FilePath: "/p/foo.go", Line: 2},
	}
	if refSetsEqual(a, b) {
		t.Fatal("expected different lengths to not be equal")
	}
}

func TestRefSetsEqual_DifferentContent(t *testing.T) {
	a := []FormatRefResult{{TargetName: "main", FilePath: "/p/main.go", Line: 1}}
	b := []FormatRefResult{{TargetName: "other", FilePath: "/p/main.go", Line: 1}}
	if refSetsEqual(a, b) {
		t.Fatal("expected different content to not be equal")
	}
}

func TestRefSetsEqual_Empty(t *testing.T) {
	if !refSetsEqual(nil, nil) {
		t.Fatal("expected empty sets to be equal")
	}
	if !refSetsEqual([]FormatRefResult{}, []FormatRefResult{}) {
		t.Fatal("expected empty sets to be equal")
	}
}
