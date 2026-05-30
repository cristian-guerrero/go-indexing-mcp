//go:build onnx

package graph

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterLanguage(TypeScriptLang{})
	RegisterLanguage(JavaScriptLang{})
	RegisterLanguage(TSXLang{})
}

// tsDefinitionTypes are the shared TypeScript/JavaScript/TSX definition node types.
var tsDefinitionTypes = map[string]SymbolKind{
	"function_declaration":   SymbolFunction,
	"class_declaration":      SymbolClass,
	"enum_declaration":       SymbolEnum,
	"arrow_function":         SymbolFunction,
	"generator_function":     SymbolFunction,
	"method_definition":      SymbolMethod,
	"lexical_declaration":    SymbolVariable,
	"variable_declaration":   SymbolVariable,
	"interface_declaration":  SymbolInterface,
	"type_alias_declaration": SymbolType,
}

// TypeScriptLang implements Language for TypeScript.
type TypeScriptLang struct{}

func (TypeScriptLang) Name() string { return "typescript" }

func (TypeScriptLang) DefinitionTypes() map[string]SymbolKind { return tsDefinitionTypes }

func (TypeScriptLang) ImportTypes() map[string]bool {
	return map[string]bool{"import_statement": true}
}

func (TypeScriptLang) CallTypes() map[string]bool {
	return map[string]bool{"call_expression": true}
}

func (l TypeScriptLang) ExtractSymbol(ctx *ExtractContext, node *sitter.Node, kind SymbolKind) *Symbol {
	return defaultExtractSymbol(ctx, node, kind)
}

func (l TypeScriptLang) ExtractImports(ctx *ExtractContext, node *sitter.Node, ntype string) ([]Symbol, []Reference) {
	return extractTSImports(ctx, node, ntype)
}

func (l TypeScriptLang) WalkExtra(ctx *ExtractContext, node *sitter.Node) bool {
	return walkTSExtra(ctx, node)
}

func (l TypeScriptLang) IsExported(name string) bool {
	return strings.HasPrefix(name, "export ") || !strings.HasPrefix(name, "_")
}

// JavaScriptLang implements Language for JavaScript.
type JavaScriptLang struct{}

func (JavaScriptLang) Name() string { return "javascript" }

func (JavaScriptLang) DefinitionTypes() map[string]SymbolKind { return tsDefinitionTypes }

func (JavaScriptLang) ImportTypes() map[string]bool {
	return map[string]bool{"import_statement": true}
}

func (JavaScriptLang) CallTypes() map[string]bool {
	return map[string]bool{"call_expression": true}
}

func (l JavaScriptLang) ExtractSymbol(ctx *ExtractContext, node *sitter.Node, kind SymbolKind) *Symbol {
	return defaultExtractSymbol(ctx, node, kind)
}

func (l JavaScriptLang) ExtractImports(ctx *ExtractContext, node *sitter.Node, ntype string) ([]Symbol, []Reference) {
	return extractTSImports(ctx, node, ntype)
}

func (l JavaScriptLang) WalkExtra(ctx *ExtractContext, node *sitter.Node) bool {
	return walkTSExtra(ctx, node)
}

func (l JavaScriptLang) IsExported(name string) bool {
	return strings.HasPrefix(name, "export ") || !strings.HasPrefix(name, "_")
}

// TSXLang implements Language for TSX (TypeScript JSX).
type TSXLang struct{}

func (TSXLang) Name() string { return "tsx" }

func (TSXLang) DefinitionTypes() map[string]SymbolKind { return tsDefinitionTypes }

func (TSXLang) ImportTypes() map[string]bool {
	return map[string]bool{"import_statement": true}
}

func (TSXLang) CallTypes() map[string]bool {
	return map[string]bool{"call_expression": true}
}

func (l TSXLang) ExtractSymbol(ctx *ExtractContext, node *sitter.Node, kind SymbolKind) *Symbol {
	return defaultExtractSymbol(ctx, node, kind)
}

func (l TSXLang) ExtractImports(ctx *ExtractContext, node *sitter.Node, ntype string) ([]Symbol, []Reference) {
	return extractTSImports(ctx, node, ntype)
}

func (l TSXLang) WalkExtra(ctx *ExtractContext, node *sitter.Node) bool {
	return walkTSExtra(ctx, node)
}

func (l TSXLang) IsExported(name string) bool {
	return strings.HasPrefix(name, "export ") || !strings.HasPrefix(name, "_")
}

// extractTSImports handles TypeScript/JavaScript/TSX import statements.
func extractTSImports(ctx *ExtractContext, node *sitter.Node, ntype string) ([]Symbol, []Reference) {
	content := node.Content(ctx.Source)
	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1
	var symbols []Symbol

	if ntype == "import_statement" && strings.Contains(content, "from ") {
		parts := strings.Split(content, "from ")
		if len(parts) >= 2 {
			path := strings.Trim(parts[len(parts)-1], " \"';")
			if path != "" {
				id := symbolID(ctx.FileHash, ctx.RelPath, startLine, path)
				symbols = append(symbols, Symbol{
					ID: id, Name: path, Kind: SymbolImport,
					FilePath: ctx.FilePath, RelPath: ctx.RelPath,
					StartLine: startLine, EndLine: endLine,
					Signature: content, Exported: false,
				})
			}
		}
	}

	refs := extractJSImportRefs(content, ctx.FilePath, ctx.RelPath, ctx.FileHash, startLine)
	return symbols, refs
}

// extractJSImportRefs extracts import references from TS/JS import statements.
func extractJSImportRefs(content, filePath, relPath, fileHash string, startLine int) []Reference {
	importPart := content
	if idx := strings.Index(content, " from "); idx > 0 {
		importPart = content[len("import "):idx]
	} else {
		importPart = strings.TrimPrefix(content, "import ")
	}
	importPart = strings.TrimSpace(importPart)
	if importPart == "" {
		return nil
	}

	if strings.HasPrefix(importPart, "'") || strings.HasPrefix(importPart, "\"") {
		return nil
	}

	if strings.HasPrefix(importPart, "* as ") {
		name := strings.TrimSpace(strings.TrimPrefix(importPart, "* as "))
		if name != "" {
			return []Reference{makeImportRef(fileHash, relPath, startLine, name, filePath)}
		}
		return nil
	}

	var refs []Reference

	if strings.HasPrefix(importPart, "{") {
		inner := strings.TrimSpace(importPart[1 : len(importPart)-1])
		for _, item := range strings.Split(inner, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			name := item
			if idx := strings.Index(strings.ToLower(item), " as "); idx > 0 {
				name = strings.TrimSpace(item[:idx])
			}
			if name != "" {
				refs = append(refs, makeImportRef(fileHash, relPath, startLine, name, filePath))
			}
		}
		return refs
	}

	name := strings.TrimSpace(importPart)
	if name == "" {
		return nil
	}
	return []Reference{makeImportRef(fileHash, relPath, startLine, name, filePath)}
}

// walkTSExtra handles TypeScript/JavaScript/TSX-specific AST patterns:
// lexical_declaration/variable_declaration wrappers containing arrow functions
// and class expressions, plus JSX element references.
func walkTSExtra(ctx *ExtractContext, node *sitter.Node) bool {
	ntype := node.Type()

	// Extract inner symbols from const/let/var declarations
	if (ntype == "lexical_declaration" || ntype == "variable_declaration") {
		if vars := extractLexicalDeclarations(node, ctx.Source, ctx.FilePath, ctx.RelPath, ctx.FileHash, int(node.StartPoint().Row)+1); vars != nil {
			*ctx.Symbols = append(*ctx.Symbols, vars...)
		}
	}

	// Extract JSX component references
	if ntype == "jsx_self_closing_element" || ntype == "jsx_element" {
		if ref := extractJSXRef(node, ctx.Source, ctx.FilePath, ctx.RelPath, ctx.FileHash, int(node.StartPoint().Row)+1); ref != nil {
			*ctx.Refs = append(*ctx.Refs, ref...)
		}
	}

	return false
}

// extractLexicalDeclarations extracts inner symbols from const/let/var declarations.
// Walks variable_declarator children to find arrow functions and class expressions.
func extractLexicalDeclarations(node *sitter.Node, source []byte, filePath, relPath, fileHash string, startLine int) []Symbol {
	var symbols []Symbol
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil || child.Type() != "variable_declarator" {
			continue
		}
		name := extractNodeName(child, source)
		if name == "" {
			continue
		}

		kind := SymbolVariable
		for j := 0; j < int(child.ChildCount()); j++ {
			val := child.Child(j)
			if val == nil {
				continue
			}
			switch val.Type() {
			case "arrow_function", "function":
				kind = SymbolFunction
			case "class":
				kind = SymbolClass
			}
		}

		endLine := int(child.EndPoint().Row) + 1
		sig := extractSignatureLine(child, source)
		exported := !strings.HasPrefix(name, "_")

		id := symbolID(fileHash, relPath, startLine, name)
		symbols = append(symbols, Symbol{
			ID:        id,
			Name:      name,
			Kind:      kind,
			FilePath:  filePath,
			RelPath:   relPath,
			StartLine: startLine,
			EndLine:   endLine,
			Signature: sig,
			Exported:  exported,
		})
	}
	return symbols
}
