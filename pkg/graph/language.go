//go:build onnx

package graph

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// ExtractContext provides context for language-specific AST extraction.
// Carries source, file metadata, and output slices for symbols and references.
type ExtractContext struct {
	Source   []byte
	FilePath string
	RelPath  string
	FileHash string
	Symbols  *[]Symbol
	Refs     *[]Reference
}

// Language defines the interface for language-specific AST extraction.
// Each supported language provides an implementation that configures
// which tree-sitter node types produce symbols and how they are extracted.
type Language interface {
	// Name returns the language identifier (e.g. "go", "python").
	Name() string

	// DefinitionTypes returns node type -> SymbolKind for definition nodes.
	// Nodes matching these types trigger ExtractSymbol.
	DefinitionTypes() map[string]SymbolKind

	// ImportTypes returns node types that represent import statements.
	ImportTypes() map[string]bool

	// CallTypes returns node types that represent function/method calls.
	CallTypes() map[string]bool

	// ExtractSymbol creates a Symbol from a definition node.
	// The node type is guaranteed to be in DefinitionTypes().
	// Return nil to skip this definition.
	ExtractSymbol(ctx *ExtractContext, node *sitter.Node, kind SymbolKind) *Symbol

	// ExtractImports extracts import symbols and their references from an
	// import node. ntype is the node type from ImportTypes().
	ExtractImports(ctx *ExtractContext, node *sitter.Node, ntype string) ([]Symbol, []Reference)

	// WalkExtra is called for every node during AST traversal. Handles
	// language-specific patterns the generic walker doesn't cover, such as
	// Go type_declaration wrapping struct/interface, TS lexical_declaration
	// wrapping variable_declarator, or C/C++ nested function declarators.
	// Return true to skip recursing into children.
	WalkExtra(ctx *ExtractContext, node *sitter.Node) bool

	// IsExported returns true if the symbol name is publicly visible
	// in this language (e.g., uppercase in Go, no underscore prefix in Python).
	IsExported(name string) bool
}

// languages is the registry of supported language extractors.
var languages = make(map[string]Language)

// RegisterLanguage adds a language extractor to the supported set.
// Called from init() in each language_*.go file.
func RegisterLanguage(l Language) {
	languages[l.Name()] = l
}

// GetLanguage returns the Language for the given name, or nil if unsupported.
func GetLanguage(name string) Language {
	return languages[name]
}

// KnownLanguages returns the set of supported language names.
func KnownLanguages() map[string]bool {
	m := make(map[string]bool, len(languages))
	for name := range languages {
		m[name] = true
	}
	return m
}
