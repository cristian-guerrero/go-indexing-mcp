//go:build onnx

package graph

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterLanguage(PythonLang{})
}

// PythonLang implements Language for Python.
type PythonLang struct{}

func (PythonLang) Name() string { return "python" }

func (PythonLang) DefinitionTypes() map[string]SymbolKind {
	return map[string]SymbolKind{
		"function_definition": SymbolFunction,
		"class_definition":    SymbolClass,
	}
}

func (PythonLang) ImportTypes() map[string]bool {
	return map[string]bool{
		"import_statement":      true,
		"import_from_statement": true,
	}
}

func (PythonLang) CallTypes() map[string]bool {
	return map[string]bool{
		"call": true,
	}
}

func (l PythonLang) ExtractSymbol(ctx *ExtractContext, node *sitter.Node, kind SymbolKind) *Symbol {
	return defaultExtractSymbol(ctx, node, kind)
}

func (l PythonLang) ExtractImports(ctx *ExtractContext, node *sitter.Node, ntype string) ([]Symbol, []Reference) {
	content := node.Content(ctx.Source)
	endLine := int(node.EndPoint().Row) + 1
	startLine := int(node.StartPoint().Row) + 1
	var symbols []Symbol
	var refs []Reference

	switch ntype {
	case "import_statement":
		// import foo, bar
		rest := strings.TrimPrefix(content, "import ")
		for _, name := range strings.Split(rest, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			id := symbolID(ctx.FileHash, ctx.RelPath, startLine, name)
			symbols = append(symbols, Symbol{
				ID: id, Name: name, Kind: SymbolImport,
				FilePath: ctx.FilePath, RelPath: ctx.RelPath,
				StartLine: startLine, EndLine: endLine,
				Signature: content, Exported: false,
			})
		}
		refs = extractPythonImportRefs(content, ctx.FilePath, ctx.RelPath, ctx.FileHash, startLine)

	case "import_from_statement":
		// from module import name
		path := extractPythonFromImport(content)
		if path != "" {
			id := symbolID(ctx.FileHash, ctx.RelPath, startLine, path)
			symbols = append(symbols, Symbol{
				ID: id, Name: path, Kind: SymbolImport,
				FilePath: ctx.FilePath, RelPath: ctx.RelPath,
				StartLine: startLine, EndLine: endLine,
				Signature: content, Exported: false,
			})
		}
		refs = extractPythonFromImportRefs(content, ctx.FilePath, ctx.RelPath, ctx.FileHash, startLine)
	}

	return symbols, refs
}

func (l PythonLang) WalkExtra(ctx *ExtractContext, node *sitter.Node) bool {
	return false
}

func (l PythonLang) IsExported(name string) bool {
	return !strings.HasPrefix(name, "_")
}

// extractPythonFromImport extracts the module path from a "from X import Y" statement.
func extractPythonFromImport(content string) string {
	rest := strings.TrimPrefix(content, "from ")
	if idx := strings.Index(rest, " import "); idx > 0 {
		return rest[:idx]
	}
	return ""
}

// extractPythonFromImportRefs extracts imported names from Python "from module import X" statements.
func extractPythonFromImportRefs(content, filePath, relPath, fileHash string, startLine int) []Reference {
	rest := strings.TrimPrefix(content, "from ")
	if idx := strings.Index(rest, " import "); idx > 0 {
		namesPart := rest[idx+len(" import "):]
		var refs []Reference
		for _, name := range strings.Split(namesPart, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				refs = append(refs, makeImportRef(fileHash, relPath, startLine, name, filePath))
			}
		}
		return refs
	}
	return nil
}

// extractPythonImportRefs extracts imported names from Python "import X" statements.
func extractPythonImportRefs(content, filePath, relPath, fileHash string, startLine int) []Reference {
	rest := strings.TrimPrefix(content, "import ")
	var refs []Reference
	for _, name := range strings.Split(rest, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			if idx := strings.Index(name, "."); idx > 0 {
				name = name[:idx]
			}
			refs = append(refs, makeImportRef(fileHash, relPath, startLine, name, filePath))
		}
	}
	return refs
}
