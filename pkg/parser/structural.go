package parser

import (
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/structural"
)

// StructuralParser wraps pkg/structural.Splitter as a Parser.
// Uses regex-based block detection for 17 languages. Always available.
type StructuralParser struct {
	splitter *structural.Splitter
}

// NewStructuralParser creates a StructuralParser using the regex-based splitter.
func NewStructuralParser() *StructuralParser {
	return &StructuralParser{
		splitter: structural.New(),
	}
}

// Parse delegates to structural.Splitter.
func (p *StructuralParser) Parse(content, language string) ([]Block, error) {
	return nil, nil
}

// SupportedLanguages returns all languages the regex-based structural parser handles.
func (p *StructuralParser) SupportedLanguages() []string {
	return structural.SupportedLanguages()
}

// Close is a no-op for StructuralParser (no CGo resources).
func (p *StructuralParser) Close() {}
