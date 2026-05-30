package db

// SearchResult is a public search result with score for ranking.
type SearchResult struct {
	ID        string  `json:"id"`
	FilePath  string  `json:"file_path"`
	RelPath   string  `json:"rel_path"`
	Language  string  `json:"language"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Content   string  `json:"content"`
	Score     float64 `json:"score"`
}

// GrepMatch represents a single matching line within a chunk.
type GrepMatch struct {
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// GrepResult is a search result from grep mode with per-line matches.
type GrepResult struct {
	ID        string      `json:"id"`
	FilePath  string      `json:"file_path"`
	RelPath   string      `json:"rel_path"`
	Language  string      `json:"language"`
	StartLine int         `json:"start_line"`
	EndLine   int         `json:"end_line"`
	Content   string      `json:"content"`
	Score     float64     `json:"score"`
	Matches   []GrepMatch `json:"matches,omitempty"`
}

// GrepOptions configures a grep search.
type GrepOptions struct {
	Query         string
	Limit         int
	CaseSensitive bool
	WholeWord     bool
	Language      string
}

const (
	// StorageFormatVersion is the current on-disk format version.
	StorageFormatVersion = 2

	// GraphFormatVersion is the current graph format version.
	GraphFormatVersion = 2
)

// SymbolKind represents the kind of a code symbol.
type SymbolKind int

const (
	SymbolFunction   SymbolKind = iota // 0
	SymbolMethod                       // 1
	SymbolClass                        // 2
	SymbolStruct                       // 3
	SymbolInterface                    // 4
	SymbolEnum                         // 5
	SymbolType                         // 6
	SymbolVariable                     // 7
	SymbolConstant                     // 8
	SymbolImport                       // 9
	SymbolModule                       // 10
)

var SymbolKindNames = map[SymbolKind]string{
	SymbolFunction:   "function",
	SymbolMethod:     "method",
	SymbolClass:      "class",
	SymbolStruct:     "struct",
	SymbolInterface:  "interface",
	SymbolEnum:       "enum",
	SymbolType:       "type",
	SymbolVariable:   "variable",
	SymbolConstant:   "constant",
	SymbolImport:     "import",
	SymbolModule:     "module",
}

// RefKind represents the kind of a code reference.
type RefKind int

const (
	RefCalls        RefKind = iota // 0
	RefImports                     // 1
	RefExtends                     // 2
	RefImplements                  // 3
	RefInstantiates                // 4
	RefAssigned                    // 5
	RefAccessed                    // 6
	RefContains                    // 7
	RefDecorates                   // 8
)

// Symbol is a code symbol extracted from source.
type Symbol struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Kind      SymbolKind `json:"kind"`
	FilePath  string     `json:"file_path"`
	RelPath   string     `json:"rel_path"`
	StartLine int        `json:"start_line"`
	EndLine   int        `json:"end_line"`
	Signature string     `json:"signature"`
	Exported  bool       `json:"exported"`
}

// Reference is a cross-reference between symbols.
type Reference struct {
	ID         string  `json:"id"`
	SourceID   string  `json:"source_id"`
	TargetName string  `json:"target_name"`
	TargetID   string  `json:"target_id,omitempty"`
	Kind       RefKind `json:"kind"`
	FilePath   string  `json:"file_path"`
	Line       int     `json:"line"`
	Confidence float64 `json:"confidence"`
}

// SymbolInfo is a complete profile of a code symbol.
type SymbolInfo struct {
	Definitions []Symbol    `json:"definitions"`
	Usages      []Reference `json:"usages"`
	Callers     []Reference `json:"callers"`
	Callees     []Reference `json:"callees"`
}

func (k SymbolKind) String() string {
	if name, ok := SymbolKindNames[k]; ok {
		return name
	}
	return "unknown"
}

func (k RefKind) String() string {
	switch k {
	case RefCalls:
		return "calls"
	case RefImports:
		return "imports"
	case RefExtends:
		return "extends"
	case RefImplements:
		return "implements"
	case RefInstantiates:
		return "instantiates"
	case RefAssigned:
		return "assigned"
	case RefAccessed:
		return "accessed"
	case RefContains:
		return "contains"
	case RefDecorates:
		return "decorates"
	default:
		return "unknown"
	}
}
