package parser

import (
	"strings"
	"testing"
)

func TestGrammarExists(t *testing.T) {
	if !GrammarExists("go") {
		t.Skip("Go grammar DLL not found, skipping integration test")
	}
}

func TestTreeSitterParser_Parse_Go(t *testing.T) {
	if !GrammarExists("go") {
		t.Skip("Go grammar DLL not found")
	}

	p, err := newTreeSitterParser(ParserConfig{})
	if err != nil {
		t.Skipf("tree-sitter parser not available: %v", err)
	}

	source := `package main

import "fmt"

func main() {
	fmt.Println("hello")
}

func add(a, b int) int {
	return a + b
}
`

	blocks, err := p.Parse(source, "go")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(blocks) == 0 {
		t.Fatal("expected at least 1 block")
	}

	t.Logf("found %d blocks:", len(blocks))
	for _, b := range blocks {
		t.Logf("  %s %s (lines %d-%d)", b.Type, b.Name, b.StartLine, b.EndLine)
	}

	var foundMain, foundAdd bool
	for _, b := range blocks {
		if b.Name == "main" && b.Type == "function" {
			foundMain = true
		}
		if b.Name == "add" && b.Type == "function" {
			foundAdd = true
		}
	}
	if !foundMain {
		t.Error("expected 'main' function block")
	}
	if !foundAdd {
		t.Error("expected 'add' function block")
	}
}

func TestTreeSitterParser_Parse_Python(t *testing.T) {
	if !GrammarExists("python") {
		t.Skip("Python grammar DLL not found")
	}

	p, err := newTreeSitterParser(ParserConfig{})
	if err != nil {
		t.Skipf("tree-sitter parser not available: %v", err)
	}

	source := `def hello():
    print("hello")

class MyClass:
    def method(self):
        pass
`

	blocks, err := p.Parse(source, "python")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(blocks) == 0 {
		t.Fatal("expected at least 1 block")
	}

	t.Logf("found %d blocks:", len(blocks))
	for _, b := range blocks {
		t.Logf("  %s %s (lines %d-%d)", b.Type, b.Name, b.StartLine, b.EndLine)
	}
}

func TestTreeSitterParser_SupportedLanguages(t *testing.T) {
	if !GrammarExists("go") {
		t.Skip("Go grammar DLL not found, skipping integration test")
	}

	p, err := newTreeSitterParser(ParserConfig{})
	if err != nil {
		t.Skipf("tree-sitter parser not available: %v", err)
	}

	langs := p.SupportedLanguages()
	t.Logf("supported: %v", langs)
	if len(langs) == 0 {
		t.Error("expected at least 1 supported language")
	}
}

func TestDownloadFile(t *testing.T) {
	t.Run("grammarFileName", func(t *testing.T) {
		if name := grammarFileName("go"); !strings.Contains(name, "go") {
			t.Errorf("unexpected name: %s", name)
		}
	})

	t.Run("grammarFuncName", func(t *testing.T) {
		if name := grammarFuncName("go"); name != "tree_sitter_go" {
			t.Errorf("unexpected func name: %s", name)
		}
		if name := grammarFuncName("tsx"); name != "tree_sitter_tsx" {
			t.Errorf("unexpected func name: %s", name)
		}
	})
}
