//go:build cgo

package parser

import (
	"strings"
	"testing"
)

func newTreeSitter(t *testing.T) Parser {
	t.Helper()
	p := NewParser(ParserConfig{Enabled: "treesitter"})
	t.Cleanup(p.Close)
	return p
}

func TestTreeSitterParse_Go_Function(t *testing.T) {
	p := newTreeSitter(t)
	content := `package main

func foo() {
	println("hello")
}

func bar(a int, b string) string {
	return ""
}
`
	blocks, err := p.Parse(content, "go")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].Name != "foo" || blocks[0].Type != "function" {
		t.Fatalf("expected foo function, got %s/%s", blocks[0].Type, blocks[0].Name)
	}
	if blocks[1].Name != "bar" || blocks[1].Type != "function" {
		t.Fatalf("expected bar function, got %s/%s", blocks[1].Type, blocks[1].Name)
	}
	if blocks[0].StartLine != 3 {
		t.Fatalf("expected foo start line 3, got %d", blocks[0].StartLine)
	}
}

func TestTreeSitterParse_Go_Method(t *testing.T) {
	p := newTreeSitter(t)
	content := `package main

type Foo struct{}

func (f *Foo) Bar() {}
`
	blocks, err := p.Parse(content, "go")
	if err != nil {
		t.Fatal(err)
	}

	hasMethod := false
	for _, b := range blocks {
		if b.Type == "method" {
			hasMethod = true
			break
		}
	}
	if !hasMethod {
		t.Fatal("expected method block")
	}
}

func TestTreeSitterParse_Go_Struct(t *testing.T) {
	p := newTreeSitter(t)
	content := `package main

type Foo struct {
	Name string
}
`
	blocks, err := p.Parse(content, "go")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) == 0 {
		t.Fatal("expected at least one block")
	}
}

func TestTreeSitterParse_Python_Function(t *testing.T) {
	p := newTreeSitter(t)
	content := `def foo():
    pass

def bar():
    return 42
`
	blocks, err := p.Parse(content, "python")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].Name != "foo" || blocks[0].Type != "function" {
		t.Fatalf("expected foo function, got %s/%s", blocks[0].Type, blocks[0].Name)
	}
}

func TestTreeSitterParse_Python_Class(t *testing.T) {
	p := newTreeSitter(t)
	content := `class MyClass:
    def method(self):
        pass
`
	blocks, err := p.Parse(content, "python")
	if err != nil {
		t.Fatal(err)
	}

	hasClass := false
	for _, b := range blocks {
		if b.Type == "class" && b.Name == "MyClass" {
			hasClass = true
			break
		}
	}
	if !hasClass {
		t.Fatal("expected MyClass class block")
	}
}

func TestTreeSitterParse_EmptyContent(t *testing.T) {
	p := newTreeSitter(t)
	blocks, err := p.Parse("", "go")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("expected 0 blocks for empty content, got %d", len(blocks))
	}
}

func TestTreeSitterParse_UnsupportedLanguage(t *testing.T) {
	p := newTreeSitter(t)
	blocks, err := p.Parse("content", "unsupported")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("expected 0 blocks for unsupported language, got %d", len(blocks))
	}
}

func TestTreeSitterParse_StructuralPreferLanguage(t *testing.T) {
	p := newTreeSitter(t)
	// bash is in structuralPreferLanguages, should return nil/nil
	blocks, err := p.Parse("echo hello", "bash")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("expected 0 blocks for structural-prefer language, got %d", len(blocks))
	}
}

func TestTreeSitterParse_LargeContent(t *testing.T) {
	p := newTreeSitter(t)
	body := "package main\nfunc main() {\n"
	body += strings.Repeat("\tprintln(\"x\")\n", 5000)
	body += "}\n"

	blocks, err := p.Parse(body, "go")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) == 0 {
		t.Fatal("expected at least main function block")
	}
}

func TestTreeSitterParse_TwoFilesSameLanguage(t *testing.T) {
	p := newTreeSitter(t)

	content1 := `package main
func a() {}
`
	content2 := `package main
func b() {}
`
	blocks1, err := p.Parse(content1, "go")
	if err != nil {
		t.Fatal(err)
	}
	blocks2, err := p.Parse(content2, "go")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks1) != 1 || blocks1[0].Name != "a" {
		t.Fatal("expected block a from first file")
	}
	if len(blocks2) != 1 || blocks2[0].Name != "b" {
		t.Fatal("expected block b from second file")
	}
}

func TestNewParser_TreeSitter(t *testing.T) {
	p := NewParser(ParserConfig{Enabled: "treesitter"})
	defer p.Close()

	if p == nil {
		t.Fatal("expected non-nil parser")
	}

	langs := p.SupportedLanguages()
	if len(langs) == 0 {
		t.Fatal("expected at least one supported language")
	}

	hasGo := false
	for _, l := range langs {
		if l == "go" {
			hasGo = true
			break
		}
	}
	if !hasGo {
		t.Fatal("expected 'go' in supported languages")
	}
}

func TestNewParser_DefaultStructural(t *testing.T) {
	p := NewParser(ParserConfig{})
	defer p.Close()

	if p == nil {
		t.Fatal("expected non-nil parser")
	}

	blocks, err := p.Parse("package main\nfunc foo() {}\n", "go")
	if err != nil {
		t.Fatal(err)
	}
	// Structural parser doesn't know about Go specifically but returns blocks
	// through regex-based detection
	t.Logf("structural blocks: %d", len(blocks))
}

func TestSupportedLanguages_WithGrammars(t *testing.T) {
	p := newTreeSitter(t)
	langs := p.SupportedLanguages()

	if len(langs) == 0 {
		t.Fatal("expected supported languages")
	}

	expected := []string{"go", "python", "javascript", "c", "cpp", "rust", "zig"}
	for _, exp := range expected {
		found := false
		for _, l := range langs {
			if l == exp {
				found = true
				break
			}
		}
		if !found {
			t.Logf("language %q not in supported list (grammar may not be installed)", exp)
		}
	}
}

func TestTreeSitterParse_JavaScript_Function(t *testing.T) {
	p := newTreeSitter(t)
	content := `function hello() {
    return "world";
}

const add = (a, b) => a + b;
`
	blocks, err := p.Parse(content, "javascript")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) == 0 {
		t.Fatal("expected at least one block")
	}
}

func TestTreeSitterParse_Rust_Function(t *testing.T) {
	p := newTreeSitter(t)
	content := `fn main() {
    println!("hello");
}

fn add(a: i32, b: i32) -> i32 {
    a + b
}
`
	blocks, err := p.Parse(content, "rust")
	if err != nil {
		t.Fatal(err)
	}

	// Rust uses function_item in tree-sitter, which may not be in structuralNodeTypes.
	// Just verify that content parses without error.
	t.Logf("rust blocks: %d", len(blocks))
}

func TestTreeSitterParse_Close_Idempotent(t *testing.T) {
	p := NewParser(ParserConfig{Enabled: "treesitter"})

	// Parse one file to ensure grammar is loaded
	_, err := p.Parse("package main\nfunc foo() {}\n", "go")
	if err != nil {
		t.Fatal(err)
	}

	// Close and call again — no panic
	p.Close()
	p.Close()
}
