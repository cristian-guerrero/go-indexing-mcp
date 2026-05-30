// Package graph implements a knowledge graph over source code symbols and their
// relationships. Symbols (functions, classes, imports) are extracted from source
// files via AST parsing, and references (calls, imports, extends) connect them
// into a queryable graph for cross-file code intelligence.
package graph

// SymbolKind represents the type of a code symbol.
type SymbolKind int

const (
	SymbolFunction  SymbolKind = iota // func Foo, def foo
	SymbolMethod                      // func (r) Method
	SymbolClass                       // class Foo
	SymbolStruct                      // type Foo struct
	SymbolInterface                   // interface Foo
	SymbolEnum                        // enum Foo
	SymbolType                        // type Foo string
	SymbolVariable                    // var/let/const assignment
	SymbolConstant                    // const Foo
	SymbolImport                      // import statement
	SymbolModule                      // package/module declaration
)

func (k SymbolKind) String() string {
	switch k {
	case SymbolFunction:
		return "function"
	case SymbolMethod:
		return "method"
	case SymbolClass:
		return "class"
	case SymbolStruct:
		return "struct"
	case SymbolInterface:
		return "interface"
	case SymbolEnum:
		return "enum"
	case SymbolType:
		return "type"
	case SymbolVariable:
		return "variable"
	case SymbolConstant:
		return "constant"
	case SymbolImport:
		return "import"
	case SymbolModule:
		return "module"
	default:
		return "unknown"
	}
}

// RefKind represents the type of relationship between two symbols.
type RefKind int

const (
	RefCalls       RefKind = iota // function/method call
	RefImports                    // import statement
	RefExtends                    // class extends another class
	RefImplements                 // implements interface
	RefInstantiates               // new instance creation
	RefAssigned                   // variable assignment
	RefAccessed                   // member/property access
	RefContains                   // file contains symbol
	RefDecorates                  // decorator/annotation
)

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

// Symbol represents a code symbol (function, class, import, etc.) extracted
// from source code via AST parsing.
type Symbol struct {
	ID        string     // unique identifier: hash:relpath:line:name
	Name      string     // symbol identifier (e.g., "main", "validate")
	Kind      SymbolKind // function, class, struct, etc.
	FilePath  string     // absolute file path
	RelPath   string     // relative file path from project root
	StartLine int        // 1-based start line
	EndLine   int        // 1-based end line
	Signature string     // full signature text (e.g., "func validate(input string) error")
	Exported  bool       // true if symbol is exported/public
}

// Reference represents a relationship between two symbols, such as a function
// call or import statement.
type Reference struct {
	ID         string  // unique identifier: hash of source+target+kind+line
	SourceID   string  // symbol ID of the reference source
	TargetName string  // name of the target being referenced
	TargetID   string  // resolved symbol ID (empty if unresolved)
	Kind       RefKind // calls, imports, extends, etc.
	FilePath   string  // file where the reference occurs
	Line       int     // 1-based line number
	Confidence float64 // 1.0 = exact match, <1.0 = fuzzy/cross-file
}

