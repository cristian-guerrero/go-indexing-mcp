//go:build cgo

package graph

import (
	"strings"
	"testing"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/parser"
	sitter "github.com/smacker/go-tree-sitter"
)

func newExtractor(t *testing.T) *Extractor {
	t.Helper()
	e := NewExtractor()
	t.Cleanup(e.Close)
	return e
}

func TestExtract_Go_Function(t *testing.T) {
	e := newExtractor(t)
	content := `package main

func foo() {
	println("hello")
}
`
	syms, refs, err := e.Extract(content, "go", "/test/main.go", "main.go", "hash1")
	if err != nil {
		t.Fatal(err)
	}

	var funcs []Symbol
	for _, s := range syms {
		if s.Kind == SymbolFunction {
			funcs = append(funcs, s)
		}
	}
	if len(funcs) != 1 {
		t.Fatalf("expected 1 function, got %d", len(funcs))
	}
	if funcs[0].Name != "foo" {
		t.Fatalf("expected name 'foo', got %q", funcs[0].Name)
	}
	if funcs[0].StartLine != 3 {
		t.Fatalf("expected start line 3, got %d", funcs[0].StartLine)
	}
	if funcs[0].Exported {
		t.Fatal("expected foo to be unexported")
	}
	// Should have call reference to println
	hasPrintln := false
	for _, r := range refs {
		if r.TargetName == "println" {
			hasPrintln = true
			break
		}
	}
	if !hasPrintln {
		t.Fatal("expected call reference to println")
	}
}

func TestExtract_Go_ExportedFunction(t *testing.T) {
	e := newExtractor(t)
	content := `package main

func Hello() {
	println("hello")
}
`
	syms, _, err := e.Extract(content, "go", "/test/main.go", "main.go", "hash2")
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range syms {
		if s.Kind == SymbolFunction && s.Name == "Hello" {
			if !s.Exported {
				t.Fatal("expected Hello to be exported")
			}
			return
		}
	}
	t.Fatal("expected Hello function")
}

func TestExtract_Go_Method(t *testing.T) {
	e := newExtractor(t)
	content := `package main

type Foo struct{}

func (f *Foo) Bar() {}
`
	syms, _, err := e.Extract(content, "go", "/test/main.go", "main.go", "hash3")
	if err != nil {
		t.Fatal(err)
	}

	var methods []Symbol
	for _, s := range syms {
		if s.Kind == SymbolMethod {
			methods = append(methods, s)
		}
	}
	if len(methods) != 1 {
		t.Fatalf("expected 1 method, got %d", len(methods))
	}
	if methods[0].Name != "(*Foo).Bar" {
		t.Fatalf("expected '(*Foo).Bar', got %q", methods[0].Name)
	}
}

func TestExtract_Go_StructInterface(t *testing.T) {
	e := newExtractor(t)
	content := `package main

type Foo struct {
	Name string
}

type Bar interface {
	Do() error
}
`
	syms, _, err := e.Extract(content, "go", "/test/main.go", "main.go", "hash4")
	if err != nil {
		t.Fatal(err)
	}

	hasStruct := false
	hasInterface := false
	for _, s := range syms {
		if s.Name == "Foo" && s.Kind == SymbolStruct {
			hasStruct = true
		}
		if s.Name == "Bar" && s.Kind == SymbolInterface {
			hasInterface = true
		}
	}
	if !hasStruct {
		t.Fatal("expected Foo struct type")
	}
	if !hasInterface {
		t.Fatal("expected Bar interface type")
	}
}

func TestExtract_Go_SingleImport(t *testing.T) {
	e := newExtractor(t)
	content := `package main

import "fmt"
`
	syms, _, err := e.Extract(content, "go", "/test/main.go", "main.go", "hash5")
	if err != nil {
		t.Fatal(err)
	}

	hasFmt := false
	for _, s := range syms {
		if s.Name == "fmt" && s.Kind == SymbolImport {
			hasFmt = true
			break
		}
	}
	if !hasFmt {
		t.Fatal("expected import fmt")
	}
}

func TestExtract_Go_GroupedImport(t *testing.T) {
	e := newExtractor(t)
	content := `package main

import (
	"fmt"
	"os"
	"strings"
)
`
	syms, _, err := e.Extract(content, "go", "/test/main.go", "main.go", "hash6")
	if err != nil {
		t.Fatal(err)
	}

	expected := []string{"fmt", "os", "strings"}
	for _, exp := range expected {
		found := false
		for _, s := range syms {
			if s.Name == exp && s.Kind == SymbolImport {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected import %q", exp)
		}
	}
}

func TestExtract_Go_VariableAndConstant(t *testing.T) {
	e := newExtractor(t)
	content := `package main

var x = 42

const Y = "hello"
`
	syms, _, err := e.Extract(content, "go", "/test/main.go", "main.go", "hash7")
	if err != nil {
		t.Fatal(err)
	}

	hasVar := false
	hasConst := false
	for _, s := range syms {
		if s.Name == "x" && s.Kind == SymbolVariable {
			hasVar = true
		}
		if s.Name == "Y" && s.Kind == SymbolConstant {
			hasConst = true
		}
	}
	if !hasVar {
		t.Fatal("expected var x")
	}
	if !hasConst {
		t.Fatal("expected const Y")
	}
}

func TestExtract_Go_Package(t *testing.T) {
	e := newExtractor(t)
	content := `package main

func main() {}
`
	syms, _, err := e.Extract(content, "go", "/test/main.go", "main.go", "hash8")
	if err != nil {
		t.Fatal(err)
	}

	// Function and package clause should both be extracted
	if len(syms) == 0 {
		t.Fatal("expected at least one symbol")
	}
	hasFunc := false
	for _, s := range syms {
		if s.Kind == SymbolFunction {
			hasFunc = true
			break
		}
	}
	if !hasFunc {
		t.Fatal("expected main function")
	}
}

func TestExtract_Go_CallReference(t *testing.T) {
	e := newExtractor(t)
	content := `package main

func main() {
	fmt.Println("hello")
	doSomething()
}
`
	_, refs, err := e.Extract(content, "go", "/test/main.go", "main.go", "hash9")
	if err != nil {
		t.Fatal(err)
	}

	calls := map[string]bool{}
	for _, r := range refs {
		if r.Kind == RefCalls {
			calls[r.TargetName] = true
		}
	}
	if !calls["Println"] {
		t.Fatal("expected call reference to Println")
	}
	if !calls["doSomething"] {
		t.Fatal("expected call reference to doSomething")
	}
}

func TestExtract_Go_TypeReferences(t *testing.T) {
	e := newExtractor(t)
	content := `package main

type Foo struct{}

func use() {
	var f Foo
	_ = f
}
`
	_, refs, err := e.Extract(content, "go", "/test/main.go", "main.go", "hash10")
	if err != nil {
		t.Fatal(err)
	}

	hasTypeRef := false
	for _, r := range refs {
		if r.TargetName == "Foo" && r.Kind == RefAccessed {
			hasTypeRef = true
			break
		}
	}
	if !hasTypeRef {
		t.Fatal("expected type reference to Foo")
	}
}

func TestExtract_Go_StructInstantiation(t *testing.T) {
	e := newExtractor(t)
	content := `package main

type Foo struct{}

func newFoo() Foo {
	return Foo{}
}
`
	_, refs, err := e.Extract(content, "go", "/test/main.go", "main.go", "hash11")
	if err != nil {
		t.Fatal(err)
	}

	hasInstantiate := false
	for _, r := range refs {
		if r.TargetName == "Foo" && r.Kind == RefInstantiates {
			hasInstantiate = true
			break
		}
	}
	if !hasInstantiate {
		t.Fatal("expected instantiation reference to Foo{}")
	}
}

func TestExtract_Go_FullFile(t *testing.T) {
	e := newExtractor(t)
	content := `package main

import (
	"fmt"
	"os"
)

type Config struct {
	Port int
}

func NewConfig(port int) *Config {
	return &Config{Port: port}
}

func (c *Config) Run() error {
	fmt.Println("running on port", c.Port)
	return nil
}

func main() {
	cfg := NewConfig(8080)
	if err := cfg.Run(); err != nil {
		os.Exit(1)
	}
}
`
	syms, refs, err := e.Extract(content, "go", "/test/main.go", "main.go", "hash12")
	if err != nil {
		t.Fatal(err)
	}

	// Verify all symbol kinds are present
	kinds := map[SymbolKind]bool{}
	for _, s := range syms {
		kinds[s.Kind] = true
	}
	if !kinds[SymbolImport] {
		t.Fatal("expected import symbols")
	}
	if !kinds[SymbolStruct] {
		t.Fatal("expected struct type symbol")
	}
	if !kinds[SymbolFunction] {
		t.Fatal("expected function symbol")
	}
	if !kinds[SymbolMethod] {
		t.Fatal("expected method symbol")
	}

	// Verify Config type
	var configSym *Symbol
	for _, s := range syms {
		if s.Name == "Config" && s.Kind == SymbolStruct {
			configSym = &s
			break
		}
	}
	if configSym == nil {
		t.Fatal("expected Config struct symbol")
	}
	if !configSym.Exported {
		t.Fatal("expected Config to be exported")
	}

	// Verify method with receiver
	var runMethod *Symbol
	for _, s := range syms {
		if s.Kind == SymbolMethod && strings.Contains(s.Name, "Run") {
			runMethod = &s
			break
		}
	}
	if runMethod == nil {
		t.Fatal("expected Run method")
	}

	// Verify call references
	targets := map[string]bool{}
	for _, r := range refs {
		if r.Kind == RefCalls {
			targets[r.TargetName] = true
		}
	}
	if !targets["NewConfig"] {
		t.Fatal("expected call to NewConfig")
	}
	if !targets["Run"] {
		t.Fatal("expected call to Run")
	}
	if !targets["Exit"] {
		t.Fatal("expected call to os.Exit -> Exit")
	}
}

func TestExtract_Go_LargeFile(t *testing.T) {
	e := newExtractor(t)
	// Generate content just under the max parse size
	body := "package main\n\nfunc main() {\n"
	body += strings.Repeat("\tprintln(\"line\")\n", 1000)
	body += "}\n"

	syms, _, err := e.Extract(body, "go", "/test/main.go", "main.go", "hash13")
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) == 0 {
		t.Fatal("expected at least package symbol")
	}
}

func TestExtract_EmptyContent(t *testing.T) {
	e := newExtractor(t)
	syms, refs, err := e.Extract("", "go", "/test/main.go", "main.go", "hash14")
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) != 0 {
		t.Fatalf("expected 0 symbols for empty content, got %d", len(syms))
	}
	if len(refs) != 0 {
		t.Fatalf("expected 0 refs for empty content, got %d", len(refs))
	}
}

func TestExtract_UnsupportedLanguage(t *testing.T) {
	e := newExtractor(t)
	syms, refs, err := e.Extract("content", "unsupported", "/test/f.x", "f.x", "hash15")
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) != 0 {
		t.Fatalf("expected 0 symbols for unsupported language, got %d", len(syms))
	}
	if len(refs) != 0 {
		t.Fatalf("expected 0 refs for unsupported language, got %d", len(refs))
	}
}

func TestExtract_FileHashInSymbolID(t *testing.T) {
	e := newExtractor(t)
	content := "package main\nfunc foo() {}\n"

	syms1, _, err := e.Extract(content, "go", "/test/a.go", "a.go", "hashA")
	if err != nil {
		t.Fatal(err)
	}
	syms2, _, err := e.Extract(content, "go", "/test/b.go", "b.go", "hashB")
	if err != nil {
		t.Fatal(err)
	}

	if syms1[0].ID == syms2[0].ID {
		t.Fatal("expected different IDs for different file hashes")
	}
}

func TestNewExtractor_LoadsGrammars(t *testing.T) {
	e := newExtractor(t)
	if e == nil {
		t.Fatal("expected non-nil extractor")
	}
	if len(e.grammars) > 0 {
		t.Logf("pre-loaded %d grammars", len(e.grammars))
	}
}

func TestLoadGrammarFromDLL_Go(t *testing.T) {
	path, err := parser.DownloadGrammar("go", parser.ParserConfig{})
	if err != nil {
		t.Skipf("Go grammar not available: %v", err)
	}

	_, err = loadGrammarFromDLL(path, "go")
	if err != nil {
		t.Fatal(err)
	}
}

func TestLoadGrammarFromDLL_InvalidPath(t *testing.T) {
	_, err := loadGrammarFromDLL("/nonexistent/grammar.dll", "go")
	if err == nil {
		t.Fatal("expected error for nonexistent grammar")
	}
}

func TestExtract_Go_MultipleFiles(t *testing.T) {
	e := newExtractor(t)

	files := []struct {
		name    string
		content string
		hash    string
	}{
		{"main.go", "package main\nfunc main() { hello() }\n", "h1"},
		{"util.go", "package main\nfunc hello() {}\n", "h2"},
	}

	type result struct {
		syms []Symbol
		refs []Reference
	}
	results := make([]result, len(files))

	for i, f := range files {
		syms, refs, err := e.Extract(f.content, "go", "/test/"+f.name, f.name, f.hash)
		if err != nil {
			t.Fatal(err)
		}
		results[i] = result{syms, refs}
	}

	// main.go should have a call to hello
	hasHelloCall := false
	for _, r := range results[0].refs {
		if r.TargetName == "hello" {
			hasHelloCall = true
			break
		}
	}
	if !hasHelloCall {
		t.Fatal("expected call to hello from main.go")
	}

	// util.go should have hello function definition
	hasHelloDef := false
	for _, s := range results[1].syms {
		if s.Name == "hello" && s.Kind == SymbolFunction {
			hasHelloDef = true
			break
		}
	}
	if !hasHelloDef {
		t.Fatal("expected hello function in util.go")
	}
}

func TestExtract_Python_Function(t *testing.T) {
	e := newExtractor(t)
	content := `def foo():
    pass

def hello():
    return "hi"
`
	syms, _, err := e.Extract(content, "python", "/test/main.py", "main.py", "hash20")
	if err != nil {
		t.Fatal(err)
	}

	hasFoo := false
	hasHello := false
	for _, s := range syms {
		if s.Name == "foo" && s.Kind == SymbolFunction {
			hasFoo = true
		}
		if s.Name == "hello" && s.Kind == SymbolFunction {
			hasHello = true
		}
	}
	if !hasFoo {
		t.Fatal("expected foo function")
	}
	if !hasHello {
		t.Fatal("expected hello function")
	}
}

func TestExtract_Python_Class(t *testing.T) {
	e := newExtractor(t)
	content := `class MyClass:
    def method(self):
        pass
`
	syms, _, err := e.Extract(content, "python", "/test/main.py", "main.py", "hash21")
	if err != nil {
		t.Fatal(err)
	}

	hasClass := false
	for _, s := range syms {
		if s.Name == "MyClass" && s.Kind == SymbolClass {
			hasClass = true
			break
		}
	}
	if !hasClass {
		t.Fatal("expected MyClass class")
	}
}

func TestExtract_Python_Import(t *testing.T) {
	e := newExtractor(t)
	content := `import os
import sys, json
`
	syms, _, err := e.Extract(content, "python", "/test/main.py", "main.py", "hash22")
	if err != nil {
		t.Fatal(err)
	}

	expected := []string{"os", "sys", "json"}
	for _, exp := range expected {
		found := false
		for _, s := range syms {
			if s.Name == exp && s.Kind == SymbolImport {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected import %q", exp)
		}
	}
}

func TestExtract_Python_FromImport(t *testing.T) {
	e := newExtractor(t)
	content := `from typing import List, Optional
`
	syms, _, err := e.Extract(content, "python", "/test/main.py", "main.py", "hash23")
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, s := range syms {
		if s.Name == "typing" && s.Kind == SymbolImport {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected import 'typing' from from-import")
	}
}

func TestExtract_Python_CallReference(t *testing.T) {
	e := newExtractor(t)
	content := `def foo():
    print("hello")
    bar()
`
	_, refs, err := e.Extract(content, "python", "/test/main.py", "main.py", "hash24")
	if err != nil {
		t.Fatal(err)
	}

	calls := map[string]bool{}
	for _, r := range refs {
		if r.Kind == RefCalls {
			calls[r.TargetName] = true
		}
	}
	if !calls["print"] {
		t.Fatal("expected call to print")
	}
	if !calls["bar"] {
		t.Fatal("expected call to bar")
	}
}

func TestExtract_Python_Exported(t *testing.T) {
	e := newExtractor(t)
	content := `def public_func():
    pass

def _private_func():
    pass
`
	syms, _, err := e.Extract(content, "python", "/test/main.py", "main.py", "hash25")
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range syms {
		if s.Name == "public_func" && !s.Exported {
			t.Fatal("expected public_func to be exported")
		}
		if s.Name == "_private_func" && s.Exported {
			t.Fatal("expected _private_func to be unexported")
		}
	}
}

func TestExtract_SitterNode(t *testing.T) {
	// Test that a tree-sitter node can be created and queried
	content := "package main\nfunc foo() {}\n"
	parser := sitter.NewParser()
	defer parser.Close()

	parser.SetLanguage(loadGrammar(t))

	tree := parser.Parse(nil, []byte(content))
	if tree == nil {
		t.Fatal("expected non-nil tree")
	}
	defer tree.Close()

	root := tree.RootNode()
	if root == nil {
		t.Fatal("expected non-nil root")
	}
	if root.Type() != "source_file" {
		t.Fatalf("expected 'source_file', got %q", root.Type())
	}

	// Check children count (package + function)
	if int(root.ChildCount()) < 2 {
		t.Fatalf("expected at least 2 children, got %d", root.ChildCount())
	}
}

func loadGrammar(t *testing.T) *sitter.Language {
	t.Helper()
	path, err := parser.DownloadGrammar("go", parser.ParserConfig{})
	if err != nil {
		t.Skipf("Go grammar not available: %v", err)
	}
	lang, err := loadGrammarFromDLL(path, "go")
	if err != nil {
		t.Skipf("skipping: %v", err)
	}
	return lang
}
