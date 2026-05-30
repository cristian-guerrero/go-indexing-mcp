// Package parser provides a unified Parser interface for extracting structural
// code blocks (functions, classes, methods) from source files. Two implementations
// are available: TreeSitterParser (AST-level, build tag onnx) and
// StructuralParser (regex-based, always available, fallback).
package parser

// Block represents a detected structural code block with line range and name.
type Block struct {
	Type      string // "function", "class", "method", "interface", "struct", "enum"
	Name      string // extracted identifier name (empty for regex fallback)
	StartLine int    // 1-indexed
	EndLine   int    // 1-indexed
}

// Parser extracts structural blocks from source code content by language.
type Parser interface {
	// Parse returns structural blocks found in the given source content.
	Parse(content, language string) ([]Block, error)

	// SupportedLanguages returns the list of languages this parser can handle.
	SupportedLanguages() []string

	// Close frees any CGo resources held by the parser (tree-sitter objects).
	// Safe to call multiple times; no-op after first call.
	Close()
}
