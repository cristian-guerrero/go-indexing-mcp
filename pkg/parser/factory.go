package parser

import (
	"log/slog"
)

// ParserConfig controls parser selection and grammar download behavior.
type ParserConfig struct {
	// Enabled selects the parser implementation:
	// "treesitter" requires CGO_ENABLED=1; empty or "structural" uses regex fallback.
	Enabled string `json:"enabled,omitempty"`

	// GrammarURL is the base URL for downloading grammar shared libraries.
	// If empty, uses the default GitHub releases URL.
	GrammarURL string `json:"grammar_url,omitempty"`
}

// NewParser creates a Parser based on config and available build tags.
// Returns a StructuralParser (regex fallback) when tree-sitter is not available
// or when the config explicitly selects "structural".
func NewParser(cfg ParserConfig) Parser {
	switch cfg.Enabled {
	case "treesitter":
		ts, err := newTreeSitterParser(cfg)
		if err != nil {
			slog.Warn("tree-sitter parser not available, falling back to structural", "error", err)
			return NewStructuralParser()
		}
		return ts
	default:
		return NewStructuralParser()
	}
}
