//go:build cgo

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

	for lang := range KnownLanguages() {
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
	langImpl := GetLanguage(language)
	if langImpl == nil {
		return nil, nil, nil
	}

	if len(content) > maxParseSize {
		return nil, nil, fmt.Errorf("content too large: %d bytes", len(content))
	}

	_, stParser, err := e.getParser(language)
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

	var symbols []Symbol
	var refs []Reference
	ctx := &ExtractContext{
		Source:   []byte(content),
		FilePath: filePath,
		RelPath:  relPath,
		FileHash: fileHash,
		Symbols:  &symbols,
		Refs:     &refs,
	}

	walkAST(root, ctx, langImpl)

	return symbols, refs, nil
}

// Close frees all cached tree-sitter parser objects.
// Must be called when the Extractor is no longer needed to prevent CGo memory leaks.
func (e *Extractor) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for lang, parser := range e.parsers {
		parser.Close()
		delete(e.parsers, lang)
	}
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
func walkAST(node *sitter.Node, ctx *ExtractContext, lang Language) {
	if node == nil {
		return
	}

	ntype := node.Type()
	startLine := int(node.StartPoint().Row) + 1

	// Extract definitions
	if kind, ok := lang.DefinitionTypes()[ntype]; ok {
		sym := lang.ExtractSymbol(ctx, node, kind)
		if sym != nil {
			sym.Exported = lang.IsExported(sym.Name)
			*ctx.Symbols = append(*ctx.Symbols, *sym)
		}
	}

	// Extract imports
	if lang.ImportTypes()[ntype] {
		imps, refs := lang.ExtractImports(ctx, node, ntype)
		*ctx.Symbols = append(*ctx.Symbols, imps...)
		*ctx.Refs = append(*ctx.Refs, refs...)
	}

	// Extract calls
	if lang.CallTypes()[ntype] {
		calls := extractCalls(node, ctx.Source, ntype, ctx.FilePath, ctx.RelPath, ctx.FileHash, startLine)
		*ctx.Refs = append(*ctx.Refs, calls...)
	}

	// Extract type references: any type_identifier that is not the name in a
	// type definition is a usage of that type.
	if ntype == "type_identifier" {
		parent := node.Parent()
		if parent != nil && parent.Type() == "type_spec" && node == parent.ChildByFieldName("name") {
			// Skip — this is the name in a type definition (e.g. "type Indexer struct")
		} else {
			typeName := node.Content(ctx.Source)
			if typeName != "" && len(typeName) < 200 {
				col := int(node.StartPoint().Column)
				*ctx.Refs = append(*ctx.Refs, makeTypeRef(
					ctx.FileHash, ctx.RelPath, startLine, col, typeName, ctx.FilePath,
				))
			}
		}
	}

	// Language-specific extra processing
	if lang.WalkExtra(ctx, node) {
		return
	}

	// Recurse into children
	for i := 0; i < int(node.ChildCount()); i++ {
		walkAST(node.Child(i), ctx, lang)
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
