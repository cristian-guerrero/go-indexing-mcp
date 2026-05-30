//go:build onnx

package graph

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterLanguage(GoLang{})
}

// GoLang implements Language for Go.
type GoLang struct{}

func (GoLang) Name() string { return "go" }

func (GoLang) DefinitionTypes() map[string]SymbolKind {
	return map[string]SymbolKind{
		"function_declaration": SymbolFunction,
		"method_declaration":   SymbolMethod,
		"type_declaration":     SymbolType,
		"var_declaration":      SymbolVariable,
		"const_declaration":    SymbolConstant,
		"package_clause":       SymbolModule,
	}
}

func (GoLang) ImportTypes() map[string]bool {
	return map[string]bool{
		"import_declaration": true,
	}
}

func (GoLang) CallTypes() map[string]bool {
	return map[string]bool{
		"call_expression": true,
	}
}

func (l GoLang) ExtractSymbol(ctx *ExtractContext, node *sitter.Node, kind SymbolKind) *Symbol {
	sym := defaultExtractSymbol(ctx, node, kind)
	if sym == nil {
		return nil
	}
	// For Go methods, prepend receiver type to the name
	if kind == SymbolMethod {
		if receiver := extractGoReceiver(node, ctx.Source); receiver != "" {
			sym.Name = "(" + receiver + ")." + sym.Name
		}
	}
	// Set exported after name is finalized
	sym.Exported = l.IsExported(sym.Name)
	return sym
}

func (l GoLang) ExtractImports(ctx *ExtractContext, node *sitter.Node, ntype string) ([]Symbol, []Reference) {
	content := node.Content(ctx.Source)
	startLine := int(node.StartPoint().Row) + 1
	var symbols []Symbol
	importLine := startLine

	// Single import: import "fmt"
	if strings.HasPrefix(content, `import "`) {
		path := extractGoImportPath(content)
		if path != "" {
			endLine := int(node.EndPoint().Row) + 1
			id := symbolID(ctx.FileHash, ctx.RelPath, startLine, path)
			symbols = append(symbols, Symbol{
				ID: id, Name: path, Kind: SymbolImport,
				FilePath: ctx.FilePath, RelPath: ctx.RelPath,
				StartLine: startLine, EndLine: endLine,
				Signature: content, Exported: false,
			})
		}
	}

	// Grouped import: import ( "fmt" "os" )
	if strings.Contains(content, "(\n") || strings.Contains(content, "(\r") {
		lines := strings.Split(content, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			path := extractGoImportPath(line)
			if path != "" {
				id := symbolID(ctx.FileHash, ctx.RelPath, importLine, path)
				symbols = append(symbols, Symbol{
					ID: id, Name: path, Kind: SymbolImport,
					FilePath: ctx.FilePath, RelPath: ctx.RelPath,
					StartLine: importLine, EndLine: importLine,
					Signature: line, Exported: false,
				})
			}
			importLine++
		}
	}

	return symbols, nil
}

func (l GoLang) WalkExtra(ctx *ExtractContext, node *sitter.Node) bool {
	ntype := node.Type()

	switch {
	case ntype == "type_declaration":
		if typeSym := extractGoTypeSpec(node, ctx.Source, ctx.FilePath, ctx.RelPath, ctx.FileHash); typeSym != nil {
			*ctx.Symbols = append(*ctx.Symbols, *typeSym)
		}

	case ntype == "var_declaration":
		if specs := extractGoSpecDeclarations(node, ctx.Source, "var_spec", SymbolVariable, ctx.FilePath, ctx.RelPath, ctx.FileHash); specs != nil {
			*ctx.Symbols = append(*ctx.Symbols, specs...)
		}

	case ntype == "const_declaration":
		if specs := extractGoSpecDeclarations(node, ctx.Source, "const_spec", SymbolConstant, ctx.FilePath, ctx.RelPath, ctx.FileHash); specs != nil {
			*ctx.Symbols = append(*ctx.Symbols, specs...)
		}
	}

	return false
}

func (GoLang) IsExported(name string) bool {
	if name == "" {
		return false
	}
	// Handle methods: "(receiver).Method" -> check "Method"
	if idx := strings.LastIndex(name, "."); idx > 0 {
		name = name[idx+1:]
	}
	return name[0] >= 'A' && name[0] <= 'Z'
}

// extractGoTypeSpec handles Go type declarations that define structs, interfaces, etc.
func extractGoTypeSpec(node *sitter.Node, source []byte, filePath, relPath, fileHash string) *Symbol {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		ct := child.Type()
		if ct != "type_spec" {
			continue
		}
		name := extractNodeName(child, source)
		if name == "" {
			continue
		}

		var kind SymbolKind
		for j := 0; j < int(child.ChildCount()); j++ {
			grandchild := child.Child(j)
			if grandchild == nil {
				continue
			}
			switch grandchild.Type() {
			case "struct_type":
				kind = SymbolStruct
			case "interface_type":
				kind = SymbolInterface
			}
		}
		if kind == SymbolType {
			continue
		}

		sig := extractSignatureLine(child, source)
		exported := name[0] >= 'A' && name[0] <= 'Z'
		startLine := int(child.StartPoint().Row) + 1
		endLine := int(child.EndPoint().Row) + 1

		id := symbolID(fileHash, relPath, startLine, name)
		return &Symbol{
			ID:        id,
			Name:      name,
			Kind:      kind,
			FilePath:  filePath,
			RelPath:   relPath,
			StartLine: startLine,
			EndLine:   endLine,
			Signature: sig,
			Exported:  exported,
		}
	}
	return nil
}

// extractGoSpecDeclarations extracts symbols from Go var/const declarations.
func extractGoSpecDeclarations(node *sitter.Node, source []byte, specType string, kind SymbolKind, filePath, relPath, fileHash string) []Symbol {
	var symbols []Symbol
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil || child.Type() != specType {
			continue
		}
		name := extractNodeName(child, source)
		if name == "" {
			continue
		}

		startLine := int(child.StartPoint().Row) + 1
		endLine := int(child.EndPoint().Row) + 1
		sig := extractSignatureLine(child, source)
		exported := name[0] >= 'A' && name[0] <= 'Z'

		id := symbolID(fileHash, relPath, startLine, name)
		symbols = append(symbols, Symbol{
			ID: id, Name: name, Kind: kind,
			FilePath: filePath, RelPath: relPath,
			StartLine: startLine, EndLine: endLine,
			Signature: sig, Exported: exported,
		})
	}
	return symbols
}

// extractGoReceiver extracts the receiver type from a Go method declaration.
func extractGoReceiver(node *sitter.Node, source []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil || child.Type() != "receiver" {
			continue
		}
		return child.Content(source)
	}
	return ""
}
