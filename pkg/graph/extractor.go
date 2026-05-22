//go:build onnx

package graph

/*
#include <stdlib.h>

#ifdef _WIN32
#include <windows.h>
static void* loadLibrary(const char* path) {
	HMODULE mod = LoadLibraryA(path);
	return (void*)mod;
}
static void* getProcAddress(void* mod, const char* name) {
	return (void*)GetProcAddress((HMODULE)mod, name);
}
#else
#include <dlfcn.h>
static void* loadLibrary(const char* path) {
	return dlopen(path, RTLD_LAZY | RTLD_LOCAL);
}
static void* getProcAddress(void* mod, const char* name) {
	return dlsym(mod, name);
}
#endif

typedef const void* (*tree_sitter_fn)();
static const void* callLanguageFunc(void* mod, const char* funcName) {
	tree_sitter_fn fn = (tree_sitter_fn)getProcAddress(mod, funcName);
	if (!fn) return NULL;
	return fn();
}
*/
import "C"

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/parser"
)

const maxParseSize = 10 * 1024 * 1024 // 10 MB

// extractSymbolNodeTypes maps tree-sitter node types to symbol kinds for definitions.
// Each node type can appear once; comments show which languages use it.
var definitionNodeTypes = map[string]SymbolKind{
	// Go, Zig
	"function_declaration":     SymbolFunction,
	// Go, PHP
	"method_declaration":       SymbolMethod,
	// Go
	"type_declaration":         SymbolType,
	// Go
	"var_declaration":          SymbolVariable,
	// Go, PHP
	"const_declaration":        SymbolConstant,
	// Go
	"package_clause":           SymbolModule,
	// Python, C, C++, PHP
	"function_definition":      SymbolFunction,
	// Python
	"class_definition":         SymbolClass,
	// TS/JS, PHP
	"class_declaration":        SymbolClass,
	// TS/JS, PHP, Zig
	"enum_declaration":         SymbolEnum,
	// TS/JS
	"arrow_function":           SymbolFunction,
	"generator_function":       SymbolFunction,
	"method_definition":        SymbolMethod,
	"lexical_declaration":      SymbolVariable,
	// TS/JS, Zig
	"variable_declaration":     SymbolVariable,
	// TS/JS, PHP
	"interface_declaration":    SymbolInterface,
	"type_alias_declaration":   SymbolType,
	// C, C++
	"struct_specifier":         SymbolStruct,
	"enum_specifier":           SymbolEnum,
	"union_specifier":          SymbolType,
	"type_definition":          SymbolType,
	"preproc_def":              SymbolConstant,
	// C++
	"class_specifier":          SymbolClass,
	"namespace_definition":     SymbolModule,
	// PHP
	"trait_declaration":        SymbolType,
	"property_declaration":     SymbolVariable,
	// Rust
	"function_item":            SymbolFunction,
	"struct_item":              SymbolStruct,
	"enum_item":                SymbolEnum,
	"union_item":               SymbolType,
	"trait_item":               SymbolInterface,
	"type_item":                SymbolType,
	"const_item":               SymbolConstant,
	"static_item":              SymbolVariable,
	"mod_item":                 SymbolModule,
	// Zig
	"struct_declaration":       SymbolStruct,
	"union_declaration":        SymbolType,
	"opaque_declaration":       SymbolType,
	"test_declaration":         SymbolFunction,
}

// importNodeTypes identifies nodes that contain import statements.
var importNodeTypes = map[string]bool{
	"import_declaration":        true, // Go
	"import_statement":          true, // Python, TypeScript
	"import_from_statement":     true, // Python
	"require_call":              true, // JS/TS
}

// callNodeTypes identifies function/method call nodes.
var callNodeTypes = map[string]bool{
	"call_expression":           true, // Go, TS/JS, C/C++
	"call":                      true, // Python
}

// knownExtractorLanguages lists languages for which we have grammar support.
// Must match the languages built by build-grammars.ps1 with valid tree-sitter grammar DLLs.
var knownExtractorLanguages = map[string]bool{
	"go":         true,
	"python":     true,
	"typescript": true,
	"javascript": true,
	"tsx":        true,
	"c":          true,
	"cpp":        true,
	"php":        true,
	"rust":       true,
	"zig":        true,
}

// Extractor extracts symbols and references from source code using tree-sitter AST.
type Extractor struct {
	mu       sync.Mutex
	grammars map[string]*sitter.Language
	parsers  map[string]*sitter.Parser
}

// NewExtractor creates an Extractor and pre-downloads grammars for known languages.
func NewExtractor() *Extractor {
	e := &Extractor{
		grammars: make(map[string]*sitter.Language),
		parsers:  make(map[string]*sitter.Parser),
	}

	// Pre-download grammars
	for lang := range knownExtractorLanguages {
		path, cached, err := parser.DownloadGrammarIfMissing(lang, parser.ParserConfig{})
		if err != nil {
			slog.Warn("graph: grammar not yet available", "language", lang, "error", err)
			continue
		}
		_ = path
		if !cached {
			slog.Info("graph: grammar pre-downloaded", "language", lang)
		}
	}

	return e
}

// Extract parses source code and returns all symbols and references found.
// The fileHash is used to build deterministic symbol IDs.
func (e *Extractor) Extract(content, language, filePath, relPath, fileHash string) ([]Symbol, []Reference, error) {
	if !knownExtractorLanguages[language] {
		return nil, nil, nil
	}
	if len(content) > maxParseSize {
		return nil, nil, fmt.Errorf("content too large: %d bytes", len(content))
	}

	lang, stParser, err := e.getParser(language)
	if err != nil {
		return nil, nil, err
	}

	tree := stParser.Parse(nil, []byte(content))
	if tree == nil {
		return nil, nil, fmt.Errorf("parse %s: tree is nil", language)
	}
	defer tree.Close()

	root := tree.RootNode()
	if root == nil {
		return nil, nil, nil
	}

	source := []byte(content)
	var symbols []Symbol
	var refs []Reference

	walkAST(root, source, language, filePath, relPath, fileHash, &symbols, &refs, lang)

	return symbols, refs, nil
}

// getParser returns a cached tree-sitter language and parser for the given language.
func (e *Extractor) getParser(language string) (*sitter.Language, *sitter.Parser, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if lang, ok := e.grammars[language]; ok {
		return lang, e.parsers[language], nil
	}

	path, err := parser.DownloadGrammar(language, parser.ParserConfig{})
	if err != nil {
		return nil, nil, fmt.Errorf("download grammar %s: %w", language, err)
	}

	lang, err := loadGrammarFromDLL(path, language)
	if err != nil {
		return nil, nil, fmt.Errorf("load grammar %s: %w", language, err)
	}

	stParser := sitter.NewParser()
	stParser.SetLanguage(lang)

	e.grammars[language] = lang
	e.parsers[language] = stParser

	slog.Info("graph: grammar loaded", "language", language, "path", path)
	return lang, stParser, nil
}

// walkAST recursively walks the tree-sitter AST extracting symbols and references.
func walkAST(node *sitter.Node, source []byte, language, filePath, relPath, fileHash string,
	symbols *[]Symbol, refs *[]Reference, lang *sitter.Language) {

	if node == nil {
		return
	}

	ntype := node.Type()
	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1

	// Extract definitions
	if kind, ok := definitionNodeTypes[ntype]; ok {
		sym := extractDefinition(node, source, kind, language, filePath, relPath, fileHash, startLine, endLine)
		if sym != nil {
			*symbols = append(*symbols, *sym)
		}
	}

	// Handle Go type_declaration specially: check child for struct/interface
	if ntype == "type_declaration" && language == "go" {
		if typeSym := extractGoTypeSpec(node, source, filePath, relPath, fileHash, startLine); typeSym != nil {
			*symbols = append(*symbols, *typeSym)
		}
	}

	// Handle lexical/variable declarations in TS/JS (const/let/var).
	// These wrap variable_declarator children that hold the actual name.
	if (ntype == "lexical_declaration" || ntype == "variable_declaration") &&
		(language == "typescript" || language == "javascript" || language == "tsx") {
		if vars := extractLexicalDeclarations(node, source, language, filePath, relPath, fileHash, startLine); vars != nil {
			*symbols = append(*symbols, vars...)
		}
	}

	// Handle Go var_declaration/const_declaration: wrap var_spec/const_spec children.
	if (ntype == "var_declaration" || ntype == "const_declaration") && language == "go" {
		specType := "var_spec"
		kind := SymbolVariable
		if ntype == "const_declaration" {
			specType = "const_spec"
			kind = SymbolConstant
		}
		if specs := extractGoSpecDeclarations(node, source, specType, kind, filePath, relPath, fileHash); specs != nil {
			*symbols = append(*symbols, specs...)
		}
	}

	// Handle C/C++ function_definition: name is nested in declarator → function_declarator.
	if ntype == "function_definition" && (language == "c" || language == "cpp") {
		if sym := extractCFunctionDefinition(node, source, language, filePath, relPath, fileHash, startLine, endLine); sym != nil {
			*symbols = append(*symbols, *sym)
		}
	}

	// Extract imports
	if importNodeTypes[ntype] {
		imports := extractImports(node, source, ntype, language, filePath, relPath, fileHash, startLine)
		*symbols = append(*symbols, imports...)
	}

	// Extract calls
	if callNodeTypes[ntype] {
		calls := extractCalls(node, source, ntype, language, filePath, relPath, fileHash, startLine)
		*refs = append(*refs, calls...)
	}

	// Extract type references: any type_identifier that is not the name in a type_spec
	// (i.e., not a type definition) is a usage of that type.
	if ntype == "type_identifier" {
		parent := node.Parent()
		if parent != nil && parent.Type() == "type_spec" && node == parent.ChildByFieldName("name") {
			// This is the name in a type definition (e.g. "type Indexer struct"),
			// skip it — the definition symbol is already created above.
		} else {
			typeName := node.Content(source)
			if typeName != "" && len(typeName) < 200 {
				col := int(node.StartPoint().Column)
				id := symbolID(fileHash, relPath, startLine, fmt.Sprintf("type:%s:c%d", typeName, col))
				*refs = append(*refs, Reference{
					ID:         refID(id, typeName, RefAccessed, startLine),
					SourceID:   id,
					TargetName: typeName,
					Kind:       RefAccessed,
					FilePath:   filePath,
					Line:       startLine,
					Confidence: 1.0,
				})
			}
		}
	}

	// Recurse into children
	for i := 0; i < int(node.ChildCount()); i++ {
		walkAST(node.Child(i), source, language, filePath, relPath, fileHash, symbols, refs, lang)
	}
}

// extractDefinition creates a Symbol from a definition node.
func extractDefinition(node *sitter.Node, source []byte, kind SymbolKind, language, filePath, relPath, fileHash string, startLine, endLine int) *Symbol {
	name := extractNodeName(node, source)
	if name == "" {
		return nil
	}

	// For methods in Go, include receiver in the name for disambiguation
	if kind == SymbolMethod && language == "go" {
		if receiver := extractGoReceiver(node, source); receiver != "" {
			name = "(" + receiver + ")." + name
		}
	}

	sig := extractSignatureLine(node, source)
	exported := isExported(name, language)

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

// extractGoTypeSpec handles Go type declarations that define structs, interfaces, etc.
func extractGoTypeSpec(node *sitter.Node, source []byte, filePath, relPath, fileHash string, startLine int) *Symbol {
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
		exported := isExported(name, "go")
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
// Walks var_spec/const_spec children to find each declared name in grouped
// declarations (e.g. `var (x = 1; y = 2)`).
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
		exported := isExported(name, "go")

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

// extractCFunctionDefinition extracts a function name from a C/C++ function_definition node.
// The name is nested inside declarator → function_declarator → declarator → identifier.
func extractCFunctionDefinition(node *sitter.Node, source []byte, language, filePath, relPath, fileHash string, startLine, endLine int) *Symbol {
	decl := node.ChildByFieldName("declarator")
	if decl == nil {
		return nil
	}

	var name string

	if decl.Type() == "function_declarator" {
		// Directly a function_declarator (simple case)
		name = extractNodeName(decl, source)
	} else {
		// Search for function_declarator inside declarator
		for i := 0; i < int(decl.ChildCount()); i++ {
			child := decl.Child(i)
			if child == nil || child.Type() != "function_declarator" {
				continue
			}
			inner := child.ChildByFieldName("declarator")
			if inner != nil {
				name = extractNodeName(inner, source)
			}
			if name == "" {
				name = extractNodeName(child, source)
			}
			break
		}
	}

	if name == "" {
		return nil
	}

	sig := extractSignatureLine(node, source)
	exported := isExported(name, language)

	id := symbolID(fileHash, relPath, startLine, name)
	return &Symbol{
		ID: id, Name: name, Kind: SymbolFunction,
		FilePath: filePath, RelPath: relPath,
		StartLine: startLine, EndLine: endLine,
		Signature: sig, Exported: exported,
	}
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

// extractLexicalDeclarations extracts symbols from const/let/var declarations in
// TypeScript/JavaScript. Walks variable_declarator children to find the declared name.
func extractLexicalDeclarations(node *sitter.Node, source []byte, language, filePath, relPath, fileHash string, startLine int) []Symbol {
	var symbols []Symbol
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil || child.Type() != "variable_declarator" {
			continue
		}
		name := extractNodeName(child, source)
		if name == "" {
			continue
		}

		kind := SymbolVariable
		for j := 0; j < int(child.ChildCount()); j++ {
			val := child.Child(j)
			if val == nil {
				continue
			}
			switch val.Type() {
			case "arrow_function", "function":
				kind = SymbolFunction
			case "class":
				kind = SymbolClass
			}
		}

		endLine := int(child.EndPoint().Row) + 1
		sig := extractSignatureLine(child, source)
		exported := isExported(name, language)

		id := symbolID(fileHash, relPath, startLine, name)
		symbols = append(symbols, Symbol{
			ID:        id,
			Name:      name,
			Kind:      kind,
			FilePath:  filePath,
			RelPath:   relPath,
			StartLine: startLine,
			EndLine:   endLine,
			Signature: sig,
			Exported:  exported,
		})
	}
	return symbols
}

// extractImports extracts import symbols from import nodes.
func extractImports(node *sitter.Node, source []byte, ntype, language, filePath, relPath, fileHash string, startLine int) []Symbol {
	var symbols []Symbol
	content := node.Content(source)
	endLine := int(node.EndPoint().Row) + 1

	switch {
	case language == "go" && ntype == "import_declaration":
		// Single import: import "fmt"
		if strings.HasPrefix(content, `import "`) {
			path := extractGoImportPath(content)
			if path != "" {
				id := symbolID(fileHash, relPath, startLine, path)
				symbols = append(symbols, Symbol{
					ID: id, Name: path, Kind: SymbolImport,
					FilePath: filePath, RelPath: relPath,
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
					// Find actual line number for each import
					impLine := startLine + strings.Index(content, line)/len(content) // approximate
					id := symbolID(fileHash, relPath, impLine, path)
					symbols = append(symbols, Symbol{
						ID: id, Name: path, Kind: SymbolImport,
						FilePath: filePath, RelPath: relPath,
						StartLine: impLine, EndLine: impLine,
						Signature: line, Exported: false,
					})
				}
			}
		}

	case language == "python" && ntype == "import_statement":
		// import foo, bar
		rest := strings.TrimPrefix(content, "import ")
		for _, name := range strings.Split(rest, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				id := symbolID(fileHash, relPath, startLine, name)
				symbols = append(symbols, Symbol{
					ID: id, Name: name, Kind: SymbolImport,
					FilePath: filePath, RelPath: relPath,
					StartLine: startLine, EndLine: endLine,
					Signature: content, Exported: false,
				})
			}
		}

	case language == "python" && ntype == "import_from_statement":
		// from module import name
		path := extractPythonFromImport(content)
		if path != "" {
			id := symbolID(fileHash, relPath, startLine, path)
			symbols = append(symbols, Symbol{
				ID: id, Name: path, Kind: SymbolImport,
				FilePath: filePath, RelPath: relPath,
				StartLine: startLine, EndLine: endLine,
				Signature: content, Exported: false,
			})
		}

	case (language == "typescript" || language == "javascript") && ntype == "import_statement":
		// import { X } from "module"
		if strings.Contains(content, "from ") {
			parts := strings.Split(content, "from ")
			if len(parts) >= 2 {
				path := strings.Trim(parts[len(parts)-1], " \"';")
				if path != "" {
					id := symbolID(fileHash, relPath, startLine, path)
					symbols = append(symbols, Symbol{
						ID: id, Name: path, Kind: SymbolImport,
						FilePath: filePath, RelPath: relPath,
						StartLine: startLine, EndLine: endLine,
						Signature: content, Exported: false,
					})
				}
			}
		}
	}

	return symbols
}

// extractGoImportPath extracts the quoted path from a Go import line.
func extractGoImportPath(line string) string {
	line = strings.TrimSpace(line)
	// Remove alias: alias "path" → "path"
	if idx := strings.LastIndex(line, `"`); idx > 0 {
		line = line[strings.Index(line, `"`):]
	}
	if strings.HasPrefix(line, `"`) && strings.HasSuffix(line, `"`) {
		return strings.Trim(line, `"`)
	}
	return ""
}

// extractPythonFromImport extracts the module path from a "from X import Y" statement.
func extractPythonFromImport(content string) string {
	// from module.submod import name
	rest := strings.TrimPrefix(content, "from ")
	if idx := strings.Index(rest, " import "); idx > 0 {
		return rest[:idx]
	}
	return ""
}

// extractCalls extracts call references from call expression nodes.
func extractCalls(node *sitter.Node, source []byte, ntype, language, filePath, relPath, fileHash string, startLine int) []Reference {
	name := extractCallName(node, source, language)
	if name == "" {
		return nil
	}

	id := symbolID(fileHash, relPath, startLine, "call:"+name)
	return []Reference{{
		ID:         refID(id, name, RefCalls, startLine),
		SourceID:   id,
		TargetName: name,
		Kind:       RefCalls,
		FilePath:   filePath,
		Line:       startLine,
		Confidence: 1.0,
	}}
}

// extractCallName extracts the function/method name from a call expression.
func extractCallName(node *sitter.Node, source []byte, language string) string {
	if int(node.ChildCount()) == 0 {
		return ""
	}

	firstChild := node.Child(0)
	if firstChild == nil {
		return ""
	}

	ftype := firstChild.Type()

	switch {
	case ftype == "identifier":
		// Direct function call: foo()
		return firstChild.Content(source)

	case ftype == "selector_expression" || ftype == "attribute" || ftype == "field_expression":
		// Method call: obj.method() (Go/TS: selector_expression, C#: attribute, C++: field_expression)
		// Last child is the method name
		nc := int(firstChild.ChildCount())
		if nc > 0 {
			last := firstChild.Child(nc - 1)
			if last != nil {
				return last.Content(source)
			}
		}
		return firstChild.Content(source)

	case strings.HasPrefix(ftype, "member"):
		// a.b.c() style calls
		nc := int(firstChild.ChildCount())
		if nc > 0 {
			last := firstChild.Child(nc - 1)
			if last != nil {
				return last.Content(source)
			}
		}
		return firstChild.Content(source)
	}

	return ""
}

// extractNodeName gets the name/identifier from a tree-sitter node.
func extractNodeName(node *sitter.Node, source []byte) string {
	nameNode := node.ChildByFieldName("name")
	if nameNode != nil {
		return nameNode.Content(source)
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		childType := child.Type()
		if childType == "identifier" || childType == "type_identifier" || strings.HasPrefix(childType, "ident") {
			return child.Content(source)
		}
	}

	return ""
}

// extractSignatureLine returns the first line of a definition as its signature.
func extractSignatureLine(node *sitter.Node, source []byte) string {
	start := node.StartPoint().Row
	end := node.EndPoint().Row

	var lines []string
	for row := start; row <= end && int(row)-int(start) < 5; row++ {
		line := extractLine(source, int(row))
		lines = append(lines, line)
		if strings.Contains(line, ")") || strings.Contains(line, "{") || strings.Contains(line, ":") {
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// extractLine extracts a single line from the source bytes.
func extractLine(source []byte, row int) string {
	start := 0
	currentRow := 0
	for i := 0; i < len(source); i++ {
		if currentRow == row {
			start = i
			for i < len(source) && source[i] != '\n' && source[i] != '\r' {
				i++
			}
			return string(source[start:i])
		}
		if source[i] == '\n' {
			currentRow++
		}
	}
	return ""
}

// isExported returns true if a name is exported/public in the given language.
func isExported(name, language string) bool {
	if name == "" {
		return false
	}
	switch language {
	case "go":
		return name[0] >= 'A' && name[0] <= 'Z'
	case "python":
		return !strings.HasPrefix(name, "_")
	case "typescript", "javascript":
		return strings.HasPrefix(name, "export ") || !strings.HasPrefix(name, "_")
	default:
		return true
	}
}

// loadGrammarFromDLL loads a tree-sitter grammar from a shared library file.
func loadGrammarFromDLL(path, language string) (*sitter.Language, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))

	mod := C.loadLibrary(cPath)
	if mod == nil {
		return nil, fmt.Errorf("load library %s: check that the grammar DLL exists and is compatible", path)
	}

	funcName := "tree_sitter_" + strings.ReplaceAll(language, "-", "_")
	cFuncName := C.CString(funcName)
	defer C.free(unsafe.Pointer(cFuncName))

	langPtr := C.callLanguageFunc(mod, cFuncName)
	if langPtr == nil {
		return nil, fmt.Errorf("symbol %s not found in %s", funcName, filepath.Base(path))
	}

	return sitter.NewLanguage(unsafe.Pointer(langPtr)), nil
}
