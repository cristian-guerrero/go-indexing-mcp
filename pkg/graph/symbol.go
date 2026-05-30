// Package graph implements a knowledge graph over source code symbols and their
// relationships. Symbols (functions, classes, imports) are extracted from source
// files via AST parsing, and references (calls, imports, extends) connect them
// into a queryable graph for cross-file code intelligence.
package graph

import "github.com/cristian-guerrero/go-indexing-mcp/pkg/db"

// SymbolKind represents the type of a code symbol.
type SymbolKind = db.SymbolKind

const (
	SymbolFunction  SymbolKind = db.SymbolFunction  // func Foo, def foo
	SymbolMethod               = db.SymbolMethod    // func (r) Method
	SymbolClass                = db.SymbolClass     // class Foo
	SymbolStruct               = db.SymbolStruct    // type Foo struct
	SymbolInterface            = db.SymbolInterface // interface Foo
	SymbolEnum                 = db.SymbolEnum      // enum Foo
	SymbolType                 = db.SymbolType      // type Foo string
	SymbolVariable             = db.SymbolVariable  // var/let/const assignment
	SymbolConstant             = db.SymbolConstant  // const Foo
	SymbolImport               = db.SymbolImport    // import statement
	SymbolModule               = db.SymbolModule    // package/module declaration
)

// RefKind represents the type of relationship between two symbols.
type RefKind = db.RefKind

const (
	RefCalls       RefKind = db.RefCalls       // function/method call
	RefImports              = db.RefImports     // import statement
	RefExtends              = db.RefExtends     // class extends another class
	RefImplements           = db.RefImplements  // implements interface
	RefInstantiates         = db.RefInstantiates // new instance creation
	RefAssigned             = db.RefAssigned    // variable assignment
	RefAccessed             = db.RefAccessed    // member/property access
	RefContains             = db.RefContains    // file contains symbol
	RefDecorates            = db.RefDecorates   // decorator/annotation
)

// Symbol represents a code symbol (function, class, import, etc.) extracted
// from source code via AST parsing.
type Symbol = db.Symbol

// Reference represents a relationship between two symbols, such as a function
// call or import statement.
type Reference = db.Reference

// SymbolInfo is a complete profile of a code symbol.
type SymbolInfo = db.SymbolInfo
