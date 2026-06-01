package db

import (
	"testing"
)

func TestSymbolKind_String(t *testing.T) {
	tests := []struct {
		kind SymbolKind
		want string
	}{
		{SymbolFunction, "function"},
		{SymbolMethod, "method"},
		{SymbolClass, "class"},
		{SymbolStruct, "struct"},
		{SymbolInterface, "interface"},
		{SymbolEnum, "enum"},
		{SymbolType, "type"},
		{SymbolVariable, "variable"},
		{SymbolConstant, "constant"},
		{SymbolImport, "import"},
		{SymbolModule, "module"},
	}
	for _, tc := range tests {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("SymbolKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestSymbolKind_String_Unknown(t *testing.T) {
	if got := SymbolKind(99).String(); got != "unknown" {
		t.Fatalf("expected 'unknown', got %q", got)
	}
}

func TestRefKind_String(t *testing.T) {
	tests := []struct {
		kind RefKind
		want string
	}{
		{RefCalls, "calls"},
		{RefImports, "imports"},
		{RefExtends, "extends"},
		{RefImplements, "implements"},
		{RefInstantiates, "instantiates"},
		{RefAssigned, "assigned"},
		{RefAccessed, "accessed"},
		{RefContains, "contains"},
		{RefDecorates, "decorates"},
	}
	for _, tc := range tests {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("RefKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestRefKind_String_Unknown(t *testing.T) {
	if got := RefKind(99).String(); got != "unknown" {
		t.Fatalf("expected 'unknown', got %q", got)
	}
}
