//go:build cgo

package graph

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterLanguage(PHPLang{})
}

// PHPLang implements Language for PHP.
type PHPLang struct{}

func (PHPLang) Name() string { return "php" }

func (PHPLang) DefinitionTypes() map[string]SymbolKind {
	return map[string]SymbolKind{
		"function_definition":   SymbolFunction,
		"method_declaration":    SymbolMethod,
		"class_declaration":     SymbolClass,
		"enum_declaration":      SymbolEnum,
		"interface_declaration": SymbolInterface,
		"trait_declaration":     SymbolType,
		"property_declaration":  SymbolVariable,
		"const_declaration":     SymbolConstant,
	}
}

func (PHPLang) ImportTypes() map[string]bool {
	return map[string]bool{
		"namespace_use_declaration": true,
	}
}

func (PHPLang) CallTypes() map[string]bool {
	return map[string]bool{
		"call_expression": true,
	}
}

func (l PHPLang) ExtractSymbol(ctx *ExtractContext, node *sitter.Node, kind SymbolKind) *Symbol {
	return defaultExtractSymbol(ctx, node, kind)
}

func (l PHPLang) ExtractImports(ctx *ExtractContext, node *sitter.Node, ntype string) ([]Symbol, []Reference) {
	content := node.Content(ctx.Source)
	endLine := int(node.EndPoint().Row) + 1
	startLine := int(node.StartPoint().Row) + 1
	var symbols []Symbol

	// use App\Models\User or use App\Models\User as Admin
	rest := strings.TrimPrefix(content, "use ")
	rest = strings.TrimSpace(rest)
	rest = strings.TrimPrefix(rest, "\\")
	rest = strings.TrimPrefix(rest, "function ")
	rest = strings.TrimPrefix(rest, "const ")
	if rest == "" {
		return nil, nil
	}

	path := rest
	if idx := strings.Index(strings.ToLower(rest), " as "); idx > 0 {
		path = strings.TrimSpace(rest[:idx])
	}
	if path != "" {
		id := symbolID(ctx.FileHash, ctx.RelPath, startLine, path)
		symbols = append(symbols, Symbol{
			ID: id, Name: path, Kind: SymbolImport,
			FilePath: ctx.FilePath, RelPath: ctx.RelPath,
			StartLine: startLine, EndLine: endLine,
			Signature: content, Exported: false,
		})
	}

	refs := extractPHPImportRefs(content, ctx.FilePath, ctx.RelPath, ctx.FileHash, startLine)
	return symbols, refs
}

func (l PHPLang) WalkExtra(ctx *ExtractContext, node *sitter.Node) bool {
	return false
}

func (l PHPLang) IsExported(name string) bool {
	return true
}

// extractPHPImportRefs extracts import references from PHP `use` statements.
func extractPHPImportRefs(content, filePath, relPath, fileHash string, startLine int) []Reference {
	rest := strings.TrimPrefix(content, "use ")
	rest = strings.TrimSpace(rest)
	rest = strings.TrimPrefix(rest, "\\")
	rest = strings.TrimPrefix(rest, "function ")
	rest = strings.TrimPrefix(rest, "const ")
	if rest == "" {
		return nil
	}

	// Handle grouped use: use App\Models\{User, Post}
	if idx := strings.Index(rest, "{"); idx > 0 {
		closeIdx := strings.Index(rest, "}")
		if closeIdx <= idx {
			return nil
		}
		inner := rest[idx+1 : closeIdx]
		var refs []Reference
		for _, item := range strings.Split(inner, ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				name := item
				if asIdx := strings.Index(strings.ToLower(item), " as "); asIdx > 0 {
					name = strings.TrimSpace(item[:asIdx])
				}
				refs = append(refs, makeImportRef(fileHash, relPath, startLine, name, filePath))
			}
		}
		return refs
	}

	// Single use
	name := rest
	if asIdx := strings.Index(strings.ToLower(rest), " as "); asIdx > 0 {
		name = strings.TrimSpace(rest[:asIdx])
	}
	if idx := strings.LastIndex(name, "\\"); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		return nil
	}
	return []Reference{makeImportRef(fileHash, relPath, startLine, name, filePath)}
}
