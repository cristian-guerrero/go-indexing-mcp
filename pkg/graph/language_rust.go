//go:build onnx

package graph

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterLanguage(RustLang{})
}

// RustLang implements Language for Rust.
type RustLang struct{}

func (RustLang) Name() string { return "rust" }

func (RustLang) DefinitionTypes() map[string]SymbolKind {
	return map[string]SymbolKind{
		"function_item": SymbolFunction,
		"struct_item":   SymbolStruct,
		"enum_item":     SymbolEnum,
		"union_item":    SymbolType,
		"trait_item":    SymbolInterface,
		"type_item":     SymbolType,
		"const_item":    SymbolConstant,
		"static_item":   SymbolVariable,
		"mod_item":      SymbolModule,
	}
}

func (RustLang) ImportTypes() map[string]bool {
	return nil
}

func (RustLang) CallTypes() map[string]bool {
	return map[string]bool{
		"call_expression": true,
	}
}

func (l RustLang) ExtractSymbol(ctx *ExtractContext, node *sitter.Node, kind SymbolKind) *Symbol {
	return defaultExtractSymbol(ctx, node, kind)
}

func (l RustLang) ExtractImports(ctx *ExtractContext, node *sitter.Node, ntype string) ([]Symbol, []Reference) {
	return nil, nil
}

func (l RustLang) WalkExtra(ctx *ExtractContext, node *sitter.Node) bool {
	return false
}

func (l RustLang) IsExported(name string) bool {
	return true
}
