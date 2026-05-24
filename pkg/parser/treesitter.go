//go:build onnx

package parser

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
	"time"
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// maximumParseSize is the maximum content length to attempt tree-sitter parsing on.
const maximumParseSize = 10 * 1024 * 1024 // 10 MB

// structuralNodeTypes maps tree-sitter node types to our block categories.
var structuralNodeTypes = map[string]string{
	"function_declaration":   "function",
	"method_declaration":     "method",
	"function_definition":    "function",
	"class_declaration":      "class",
	"class_definition":       "class",
	"interface_declaration":  "interface",
	"struct_declaration":     "struct",
	"enum_declaration":       "enum",
	"type_declaration":       "type",
	"type_alias_declaration": "type",
	"trait_declaration":      "trait",
	"impl_declaration":       "impl",
	"module_declaration":     "module",
	"method_definition":      "method",
	"arrow_function":         "function",
	"generator_function":     "function",
	"function_signature":     "function",
	"method_signature":       "method",
	"union_declaration":      "union",
	"opaque_declaration":     "opaque",
	"test_declaration":       "test",
	"comptime_declaration":   "comptime",
}

// knownTreeSitterGrammars lists languages for which we have pre-built grammar DLLs.
// Languages not in this list will fall through to structural parsing without
// attempting an HTTP download (avoiding unnecessary 404 requests).
var knownTreeSitterGrammars = map[string]bool{
	"go":         true,
	"python":     true,
	"javascript": true,
	"typescript": true,
	"tsx":        true,
	"c":          true,
	"cpp":        true,
	"php":        true,
	"rust":       true,
	"zig":        true,
}

// structuralPreferLanguages are languages where the regex-based structural
// splitter works well enough that tree-sitter adds negligible benefit.
// Using tree-sitter for these would add parsing overhead without better blocks.
var structuralPreferLanguages = map[string]bool{
	"json":     true,
	"css":      true,
	"html":     true,
	"yaml":     true,
	"toml":     true,
	"bash":     true,
	"markdown": true,
	"sql":      true,
}

// TreeSitterParser uses tree-sitter AST parsing to extract structural blocks.
// Parsers are cached per language and reused to avoid expensive CGo allocation
// on every Parse() call.
type TreeSitterParser struct {
	mu       sync.Mutex
	grammars map[string]*sitter.Language // language -> loaded grammar
	parsers  map[string]*sitter.Parser   // language -> cached parser (reused)
	cfg      ParserConfig
}

// newTreeSitterParser creates a TreeSitterParser with the given config.
// Downloads all known grammar DLLs synchronously so they are available
// even when all files are already indexed (IsFileIndexed skips Parse()).
func newTreeSitterParser(cfg ParserConfig) (Parser, error) {
	p := &TreeSitterParser{
		grammars: make(map[string]*sitter.Language),
		parsers:  make(map[string]*sitter.Parser),
		cfg:      cfg,
	}

	for lang := range knownTreeSitterGrammars {
		path, cached, err := DownloadGrammarIfMissing(lang, cfg)
		if err != nil {
			slog.Warn("grammar not yet available (will retry on demand)", "language", lang, "error", err)
			continue
		}
		_ = path
		if !cached {
			slog.Info("grammar pre-downloaded", "language", lang)
		}
	}

	return p, nil
}

// Parse parses source content with tree-sitter and returns structural blocks.
// Grammars and parsers are cached per language for reuse across files.
func (p *TreeSitterParser) Parse(content, language string) ([]Block, error) {
	if len(content) > maximumParseSize {
		return nil, fmt.Errorf("content too large for tree-sitter: %d bytes", len(content))
	}

	if !knownTreeSitterGrammars[language] {
		return nil, nil
	}

	if structuralPreferLanguages[language] {
		return nil, nil
	}

	p.mu.Lock()

	lang, ok := p.grammars[language]
	if !ok {
		tDL := time.Now()
		path, err := DownloadGrammar(language, p.cfg)
		if err != nil {
			p.mu.Unlock()
			return nil, fmt.Errorf("download grammar %s: %w", language, err)
		}
		dlMs := time.Since(tDL).Milliseconds()

		tLoad := time.Now()
		lang, err = loadGrammarFromDLL(path, language)
		if err != nil {
			p.mu.Unlock()
			return nil, fmt.Errorf("load grammar %s: %w", language, err)
		}
		loadMs := time.Since(tLoad).Milliseconds()

		p.grammars[language] = lang
		slog.Info("tree-sitter grammar loaded",
			"language", language, "path", path,
			"download_ms", dlMs, "dll_load_ms", loadMs,
		)
	}

	stParser, ok := p.parsers[language]
	if !ok {
		stParser = sitter.NewParser()
		stParser.SetLanguage(lang)
		p.parsers[language] = stParser
	}
	p.mu.Unlock()

	tParse := time.Now()
	tree := stParser.Parse(nil, []byte(content))
	tParseDone := time.Now()
	parseMs := tParseDone.Sub(tParse).Milliseconds()
	if parseMs > 100 {
		slog.Warn("tree-sitter parse", "language", language, "content_bytes", len(content), "parse_ms", parseMs)
	}
	if tree == nil {
		return nil, fmt.Errorf("parse %s: tree is nil", language)
	}
	defer tree.Close()

	root := tree.RootNode()
	if root == nil {
		return nil, nil
	}

	blocks := extractStructuralBlocks(root, []byte(content))
	if len(blocks) == 0 {
		return nil, nil
	}

	sortBlocks(blocks)
	return blocks, nil
}

// SupportedLanguages returns all languages for which we have grammar support.
func (p *TreeSitterParser) SupportedLanguages() []string {
	var available []string
	for l := range knownTreeSitterGrammars {
		if GrammarExists(l) {
			available = append(available, l)
		}
	}

	if len(available) > 0 {
		return available
	}

	var all []string
	for l := range knownTreeSitterGrammars {
		all = append(all, l)
	}
	return all
}

// loadGrammarFromDLL loads a tree-sitter grammar from a shared library file.
func loadGrammarFromDLL(path, language string) (*sitter.Language, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))

	mod := C.loadLibrary(cPath)
	if mod == nil {
		return nil, fmt.Errorf("load library %s: check that the grammar DLL exists and is compatible", path)
	}

	funcName := grammarFuncName(language)
	cFuncName := C.CString(funcName)
	defer C.free(unsafe.Pointer(cFuncName))

	langPtr := C.callLanguageFunc(mod, cFuncName)
	if langPtr == nil {
		return nil, fmt.Errorf("symbol %s not found in %s", funcName, filepath.Base(path))
	}

	return sitter.NewLanguage(unsafe.Pointer(langPtr)), nil
}

// extractStructuralBlocks walks the tree and collects structural blocks.
func extractStructuralBlocks(node *sitter.Node, source []byte) []Block {
	var blocks []Block

	var walk func(*sitter.Node, int)
	walk = func(n *sitter.Node, depth int) {
		if n == nil {
			return
		}

		ntype := n.Type()
		blockType, ok := structuralNodeTypes[ntype]

		if ok {
			name := extractNodeName(n, source)

			blocks = append(blocks, Block{
				Type:      blockType,
				Name:      name,
				StartLine: int(n.StartPoint().Row) + 1,
				EndLine:   int(n.EndPoint().Row) + 1,
			})
		}

		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i), depth+1)
		}
	}

	walk(node, 0)
	return blocks
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

// sortBlocks sorts blocks by StartLine, then EndLine (ascending).
func sortBlocks(blocks []Block) {
	for i := 0; i < len(blocks); i++ {
		for j := i + 1; j < len(blocks); j++ {
			if blocks[i].StartLine > blocks[j].StartLine ||
				(blocks[i].StartLine == blocks[j].StartLine && blocks[i].EndLine > blocks[j].EndLine) {
				blocks[i], blocks[j] = blocks[j], blocks[i]
			}
		}
	}
}

// Close frees all cached tree-sitter parsers to release CGo-allocated memory.
func (p *TreeSitterParser) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for lang, parser := range p.parsers {
		parser.Close()
		delete(p.parsers, lang)
	}
}
