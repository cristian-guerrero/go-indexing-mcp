package structural

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestParseBlocks_UnsupportedLanguage(t *testing.T) {
	s := New()
	blocks, err := s.ParseBlocks("test.sql", "sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Errorf("expected 0 blocks for sql, got %d", len(blocks))
	}
}

func TestParseBlocks_GoFunctions(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	content := `package main

func foo() {
	println("hello")
}

func bar() {
	println("world")
}
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "go")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].StartLine != 3 {
		t.Errorf("expected first block start at 3, got %d", blocks[0].StartLine)
	}
	if blocks[0].EndLine != 5 {
		t.Errorf("expected first block end at 5, got %d", blocks[0].EndLine)
	}
}

func TestParseBlocks_GoStruct(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	content := `package main

type Foo struct {
	Name string
	Age  int
}

func bar() {}
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "go")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].StartLine != 3 {
		t.Errorf("expected first block at 3, got %d", blocks[0].StartLine)
	}
}

func TestParseBlocks_PythonFunctions(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.py")
	content := `def foo():
    pass

def bar():
    x = 1
    return x

class MyClass:
    def method(self):
        pass
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "python")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
}

func TestParseBlocks_JavaScript(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.js")
	content := `function foo() {
    console.log("hello");
}

class Bar {
    constructor() {
        this.x = 1;
    }
}
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "javascript")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
}

func TestParseBlocks_EmptyFile(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.go")
	os.WriteFile(path, []byte{}, 0644)

	blocks, err := s.ParseBlocks(path, "go")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Errorf("expected 0 blocks, got %d", len(blocks))
	}
}

func TestParseBlocks_NoStructurals(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	content := "package main\n\nconst x = 1\n\nvar y = 2\n"
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "go")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Errorf("expected 0 blocks, got %d", len(blocks))
	}
}

func TestParseBlocksBatch(t *testing.T) {
	s := New()
	dir := t.TempDir()

	path1 := filepath.Join(dir, "a.go")
	os.WriteFile(path1, []byte("package a\nfunc foo() {}\n"), 0644)

	path2 := filepath.Join(dir, "b.go")
	os.WriteFile(path2, []byte("package b\nfunc bar() {}\n"), 0644)

	files := []ParseFile{
		{Path: path1, Language: "go"},
		{Path: path2, Language: "go"},
	}

	results, err := s.ParseBlocksBatch(files)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestParseBlocksBatch_SkipsUnsupported(t *testing.T) {
	s := New()
	files := []ParseFile{
		{Path: "a.sql", Language: "sql"},
	}
	results, err := s.ParseBlocksBatch(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestFindBraceEnd_Nested(t *testing.T) {
	lines := []string{
		"func foo() {",
		"    if true {",
		"        for i := 0; i < 10; i++ {",
		"            println(i)",
		"        }",
		"    }",
		"}",
		"",
		"func bar() {}",
	}

	end := findBraceEnd(lines, nil, 0)
	if end != 6 {
		t.Errorf("expected end at line 6, got %d", end)
	}
}

func TestFindBraceEnd_Strings(t *testing.T) {
	lines := []string{
		`func foo() {`,
		`    s := "hello { world }"`,
		`    fmt.Println(s)`,
		`}`,
	}

	end := findBraceEnd(lines, nil, 0)
	if end != 3 {
		t.Errorf("expected end at line 3, got %d", end)
	}
}

func TestFindBraceEnd_Comments(t *testing.T) {
	lines := []string{
		"func foo() {",
		"    // this is a { comment",
		"    x := 1",
		"}",
	}

	end := findBraceEnd(lines, nil, 0)
	if end != 3 {
		t.Errorf("expected end at line 3, got %d", end)
	}
}

func TestFindIndentEnd_Python(t *testing.T) {
	lines := []string{
		"def foo():",
		"    x = 1",
		"    if x:",
		"        print('nested')",
		"    return x",
		"",
		"def bar():",
		"    pass",
	}

	end := findIndentEnd(lines, nil, 0)
	if end != 5 {
		t.Errorf("expected end at line 5 (blank line before next def), got %d", end)
	}
}

func TestSupportedLanguages(t *testing.T) {
	langs := SupportedLanguages()
	popular := map[string]bool{"go": true, "python": true, "javascript": true, "rust": true, "java": true, "c": true, "cpp": true, "json": true, "yaml": true, "toml": true, "markdown": true}
	for _, l := range langs {
		delete(popular, l)
	}
	if len(popular) > 0 {
		t.Errorf("missing languages: %v", popular)
	}
}

func TestParseBlocks_JSON(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{
	"server": {
		"port": 8080,
		"host": "localhost"
	},
	"database": {
		"host": "db.local",
		"port": 5432
	}
}
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "json")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].StartLine < 1 || blocks[0].EndLine < blocks[0].StartLine {
		t.Errorf("invalid first block: %d-%d", blocks[0].StartLine, blocks[0].EndLine)
	}
}

func TestParseBlocks_YAML(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `server:
  port: 8080
  host: localhost

database:
  host: db.local
  port: 5432
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "yaml")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
}

func TestParseBlocks_TOML(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `[server]
port = 8080
host = "localhost"

[database]
host = "db.local"
port = 5432
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "toml")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
}

func TestParseBlocks_Markdown(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "readme.md")
	content := `# Title
Content here

## Section 1
Section content

## Section 2
More content
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "markdown")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
}

func TestFindSectionEnd(t *testing.T) {
	patterns := []*regexp.Regexp{reTOMLSect}
	lines := []string{
		"[server]",
		"port = 8080",
		"",
		"[database]",
		"host = \"db.local\"",
		"",
		"[cache]",
		"ttl = 300",
	}

	end := findSectionEnd(lines, patterns, 0)
	if end != 2 {
		t.Errorf("expected section end at line 2, got %d", end)
	}

	end = findSectionEnd(lines, patterns, 3)
	if end != 5 {
		t.Errorf("expected section end at line 5, got %d", end)
	}

	end = findSectionEnd(lines, patterns, 6)
	if end != 7 {
		t.Errorf("expected section end at line 7 (EOF), got %d", end)
	}
}

func TestIsLineStructuralStart(t *testing.T) {
	tests := []struct {
		line     string
		lang     string
		expected bool
	}{
		{"func foo() {", "go", true},
		{"    func foo() {", "go", true},
		{"type Foo struct {", "go", true},
		{"package main", "go", false},
		{"import \"fmt\"", "go", false},
		{"def foo():", "python", true},
		{"class Foo:", "python", true},
		{"x = 1", "python", false},
		{"function foo() {", "javascript", true},
		{"const x = 1;", "javascript", false}, // const x = () => would match
	}

	for _, tt := range tests {
		got := IsLineStructuralStart(tt.line, tt.lang)
		if got != tt.expected {
			t.Errorf("IsLineStructuralStart(%q, %q) = %v, want %v", tt.line, tt.lang, got, tt.expected)
		}
	}
}
