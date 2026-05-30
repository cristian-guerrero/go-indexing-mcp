//go:build onnx

package graph

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterLanguage(ZigLang{})
}

// ZigLang implements Language for Zig.
type ZigLang struct{}

func (ZigLang) Name() string { return "zig" }

func (ZigLang) DefinitionTypes() map[string]SymbolKind {
	return map[string]SymbolKind{
		"function_declaration": SymbolFunction,
		"enum_declaration":     SymbolEnum,
		"struct_declaration":   SymbolStruct,
		"union_declaration":    SymbolType,
		"opaque_declaration":   SymbolType,
		"test_declaration":     SymbolFunction,
	}
}

func (ZigLang) ImportTypes() map[string]bool {
	return nil
}

func (ZigLang) CallTypes() map[string]bool {
	return map[string]bool{
		"call_expression": true,
	}
}

func (l ZigLang) ExtractSymbol(ctx *ExtractContext, node *sitter.Node, kind SymbolKind) *Symbol {
	return defaultExtractSymbol(ctx, node, kind)
}

func (l ZigLang) ExtractImports(ctx *ExtractContext, node *sitter.Node, ntype string) ([]Symbol, []Reference) {
	return nil, nil
}

// WalkExtra handles Zig @import builtin function references.
// For `const x = @import("module")`, creates a RefImports reference
// for the variable name x so --symbol-info finds it as a usage.
func (l ZigLang) WalkExtra(ctx *ExtractContext, node *sitter.Node) bool {
	if node.Type() == "builtin_function" {
		refs := extractZigImportRef(node, ctx.Source, ctx.FilePath, ctx.RelPath, ctx.FileHash, int(node.StartPoint().Row)+1)
		*ctx.Refs = append(*ctx.Refs, refs...)
	}
	return false
}

func (l ZigLang) IsExported(name string) bool {
	return true
}

// extractZigImportRef extracts import references from Zig @import builtin calls.
func extractZigImportRef(node *sitter.Node, source []byte, filePath, relPath, fileHash string, startLine int) []Reference {
	builtinID := node.Child(0)
	if builtinID == nil {
		return nil
	}
	if builtinID.Content(source) != "@import" && builtinID.Content(source) != "@cImport" {
		return nil
	}

	name := findZigImportName(node, source)
	if name == "" {
		return nil
	}

	return []Reference{makeImportRef(fileHash, relPath, startLine, name, filePath)}
}

// findZigImportName walks up from a @import node to find the enclosing variable name.
func findZigImportName(node *sitter.Node, source []byte) string {
	for n := node.Parent(); n != nil; n = n.Parent() {
		if n.Type() == "variable_declaration" {
			for i := 0; i < int(n.ChildCount()); i++ {
				child := n.Child(i)
				if child != nil && child.Type() == "_variable_declaration_header" {
					for j := 0; j < int(child.ChildCount()); j++ {
						gc := child.Child(j)
						if gc != nil && gc.Type() == "identifier" {
							return gc.Content(source)
						}
					}
				}
			}
		}
	}
	return ""
}
