//go:build onnx

package graph

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterLanguage(CLang{})
	RegisterLanguage(CppLang{})
}

// cDefinitionTypes are the shared C/C++ definition node types.
var cDefinitionTypes = map[string]SymbolKind{
	"function_definition": SymbolFunction,
	"struct_specifier":    SymbolStruct,
	"enum_specifier":      SymbolEnum,
	"union_specifier":     SymbolType,
	"type_definition":     SymbolType,
	"preproc_def":         SymbolConstant,
}

// CLang implements Language for C.
type CLang struct{}

func (CLang) Name() string { return "c" }

func (CLang) DefinitionTypes() map[string]SymbolKind { return cDefinitionTypes }

func (CLang) ImportTypes() map[string]bool { return nil }

func (CLang) CallTypes() map[string]bool {
	return map[string]bool{"call_expression": true}
}

func (l CLang) ExtractSymbol(ctx *ExtractContext, node *sitter.Node, kind SymbolKind) *Symbol {
	return extractCSymbol(ctx, node, kind)
}

func (l CLang) ExtractImports(ctx *ExtractContext, node *sitter.Node, ntype string) ([]Symbol, []Reference) {
	return nil, nil
}

func (l CLang) WalkExtra(ctx *ExtractContext, node *sitter.Node) bool {
	return false
}

func (l CLang) IsExported(name string) bool {
	return true
}

// CppLang implements Language for C++.
type CppLang struct{}

func (CppLang) Name() string { return "cpp" }

func (CppLang) DefinitionTypes() map[string]SymbolKind {
	m := make(map[string]SymbolKind, len(cDefinitionTypes)+2)
	for k, v := range cDefinitionTypes {
		m[k] = v
	}
	m["class_specifier"]      = SymbolClass
	m["namespace_definition"] = SymbolModule
	return m
}

func (CppLang) ImportTypes() map[string]bool { return nil }

func (CppLang) CallTypes() map[string]bool {
	return map[string]bool{"call_expression": true}
}

func (l CppLang) ExtractSymbol(ctx *ExtractContext, node *sitter.Node, kind SymbolKind) *Symbol {
	sym := extractCSymbol(ctx, node, kind)
	if sym == nil {
		return nil
	}
	// Check if function is inside a class/struct → upgrade to method
	if kind == SymbolFunction {
		if className := extractCPPEnclosingClass(node, ctx.Source); className != "" {
			sym.Kind = SymbolMethod
			sym.Name = "(" + className + ")." + sym.Name
		}
	}
	return sym
}

func (l CppLang) ExtractImports(ctx *ExtractContext, node *sitter.Node, ntype string) ([]Symbol, []Reference) {
	return nil, nil
}

func (l CppLang) WalkExtra(ctx *ExtractContext, node *sitter.Node) bool {
	return false
}

func (l CppLang) IsExported(name string) bool {
	return true
}

// extractCSymbol extracts a symbol from C/C++ function_definition nodes where
// the name is nested inside declarator -> function_declarator -> declarator.
func extractCSymbol(ctx *ExtractContext, node *sitter.Node, kind SymbolKind) *Symbol {
	if kind != SymbolFunction {
		// struct, enum, typedef use standard name extraction
		return defaultExtractSymbol(ctx, node, kind)
	}

	// C/C++: function name is nested in declarator
	name := extractCFunctionName(node, ctx.Source)
	if name == "" {
		return nil
	}

	sig := extractSignatureLine(node, ctx.Source)
	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1
	id := symbolID(ctx.FileHash, ctx.RelPath, startLine, name)
	return &Symbol{
		ID:        id,
		Name:      name,
		Kind:      kind,
		FilePath:  ctx.FilePath,
		RelPath:   ctx.RelPath,
		StartLine: startLine,
		EndLine:   endLine,
		Signature: sig,
	}
}

// extractCFunctionName finds the function name in a C/C++ function_definition node.
// The name is nested inside declarator -> function_declarator -> declarator -> identifier.
func extractCFunctionName(node *sitter.Node, source []byte) string {
	decl := node.ChildByFieldName("declarator")
	if decl == nil {
		return ""
	}

	if decl.Type() == "function_declarator" {
		return extractNodeName(decl, source)
	}

	for i := 0; i < int(decl.ChildCount()); i++ {
		child := decl.Child(i)
		if child == nil || child.Type() != "function_declarator" {
			continue
		}
		inner := child.ChildByFieldName("declarator")
		if inner != nil {
			if name := extractNodeName(inner, source); name != "" {
				return name
			}
		}
		return extractNodeName(child, source)
	}

	return ""
}

// extractCPPEnclosingClass walks up the AST to find an enclosing class_specifier
// or struct_specifier, returning the class/struct name, or "" if not inside one.
func extractCPPEnclosingClass(node *sitter.Node, source []byte) string {
	for p := node.Parent(); p != nil; p = p.Parent() {
		ptype := p.Type()
		if ptype == "class_specifier" || ptype == "struct_specifier" {
			return extractNodeName(p, source)
		}
	}
	return ""
}
