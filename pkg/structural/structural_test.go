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
	if blocks[0].HasDecorators {
		t.Error("expected no decorators for plain Go function")
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

func TestParseBlocks_ZigFunctions(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.zig")
	content := `const std = @import("std");

pub fn main() void {
    std.debug.print("hello\n", .{});
}

fn add(a: i32, b: i32) i32 {
    return a + b;
}

pub const Struct = struct {
    x: i32,
    y: i32,
};
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "zig")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
	if blocks[0].StartLine != 3 {
		t.Errorf("expected first block start at 3 (pub fn main), got %d", blocks[0].StartLine)
	}
	if blocks[1].StartLine != 7 {
		t.Errorf("expected second block start at 7 (fn add), got %d", blocks[1].StartLine)
	}
	if blocks[2].StartLine != 11 {
		t.Errorf("expected third block start at 11 (struct), got %d", blocks[2].StartLine)
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
		{"const x = 1;", "javascript", false},
	}

	for _, tt := range tests {
		got := IsLineStructuralStart(tt.line, tt.lang)
		if got != tt.expected {
			t.Errorf("IsLineStructuralStart(%q, %q) = %v, want %v", tt.line, tt.lang, got, tt.expected)
		}
	}
}

func TestParseBlocks_TypeScriptWithDecorators(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "controller.ts")
	content := `import { Controller, Get } from '@nestjs/common';

@Controller('cats')
export class CatsController {
    @Get()
    @HttpCode(201)
    findAll(): Cat[] {
        return [];
    }

    @Post()
    create(@Body() dto: CreateCatDto): string {
        return 'created';
    }
}
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "javascript")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (class is top-level, methods are nested), got %d", len(blocks))
	}

	if blocks[0].StartLine != 3 {
		t.Errorf("expected class block start at line 3 (@Controller line), got %d", blocks[0].StartLine)
	}

	if !blocks[0].HasDecorators {
		t.Error("expected class block to have decorators")
	}
}

func TestParseBlocks_PythonWithDecorators(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.py")
	content := `from flask import Flask

app = Flask(__name__)

@app.route('/api/cats', methods=['GET'])
def get_cats():
    return []

@app.route('/api/cats', methods=['POST'])
@app.login_required
def create_cat():
    return 'ok'
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "python")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	if blocks[0].StartLine != 5 {
		t.Errorf("expected get_cats block start at line 5 (@app.route line), got %d", blocks[0].StartLine)
	}

	if blocks[1].StartLine != 9 {
		t.Errorf("expected create_cat block start at line 9 (@app.route line), got %d", blocks[1].StartLine)
	}
}

func TestParseBlocks_JavaWithAnnotations(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "CatController.java")
	content := `import org.springframework.web.bind.annotation.*;

@RestController
@RequestMapping("/api/cats")
public class CatController {

    @GetMapping
    public List<Cat> getCats() {
        return new ArrayList<>();
    }

    @PostMapping
    @ResponseStatus(HttpStatus.CREATED)
    public Cat createCat(@RequestBody Cat cat) {
        return cat;
    }
}
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "java")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (class is top-level, methods are nested), got %d", len(blocks))
	}

	if blocks[0].StartLine != 3 {
		t.Errorf("expected class block start at line 3 (@RestController line), got %d", blocks[0].StartLine)
	}
}

func TestParseBlocks_CSharpWithAttributes(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "CatsController.cs")
	content := `using Microsoft.AspNetCore.Mvc;

[ApiController]
[Route("api/[controller]")]
public class CatsController : ControllerBase
{
    [HttpGet]
    public IActionResult GetCats()
    {
        return Ok(new List<Cat>());
    }

    [HttpPost]
    public IActionResult CreateCat([FromBody] Cat cat)
    {
        return Ok(cat);
    }
}
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "csharp")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (class is top-level, methods are nested), got %d", len(blocks))
	}

	if blocks[0].StartLine != 3 {
		t.Errorf("expected class block start at line 3 ([ApiController] line), got %d", blocks[0].StartLine)
	}
}

func TestParseBlocks_RustWithAttributes(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.rs")
	content := `use axum::prelude::*;

#[tokio::main]
async fn main() {
    let app = Router::new().route("/", get(handler));
    axum::Server::bind(&"0.0.0.0:3000".parse().unwrap())
        .serve(app.into_make_service())
        .await
        .unwrap();
}

#[derive(Debug, Clone)]
struct Cat {
    name: String,
}
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "rust")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (async fn + struct), got %d", len(blocks))
	}

	if blocks[0].StartLine != 3 {
		t.Errorf("expected async fn block start at line 3 (#[tokio::main] line), got %d", blocks[0].StartLine)
	}

	if blocks[0].EndLine != 10 {
		t.Errorf("expected async fn block end at line 10, got %d", blocks[0].EndLine)
	}

	if blocks[1].StartLine != 12 {
		t.Errorf("expected Cat struct block start at line 12 (#[derive] line), got %d", blocks[1].StartLine)
	}

	if !blocks[0].HasDecorators {
		t.Error("expected async fn block to have decorators")
	}

	if !blocks[1].HasDecorators {
		t.Error("expected struct block to have decorators")
	}
}

func TestParseBlocks_KotlinWithAnnotations(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "CatController.kt")
	content := `import org.springframework.web.bind.annotation.*

@RestController
@RequestMapping("/api/cats")
class CatController {

    @GetMapping
    fun getCats(): List<Cat> = listOf()

    @PostMapping
    fun createCat(): Cat = Cat()
}
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "kotlin")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (class is top-level, methods are nested), got %d", len(blocks))
	}

	if blocks[0].StartLine != 3 {
		t.Errorf("expected class block start at line 3 (@RestController line), got %d", blocks[0].StartLine)
	}
}

func TestParseBlocks_PHPWithAttributes(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "CatController.php")
	content := `<?php

use Symfony\Component\Routing\Annotation\Route;

#[Route('/api/cats')]
class CatController
{
    #[Route('', methods: ['GET'])]
    public function getCats(): array
    {
        return [];
    }

    #[Route('', methods: ['POST'])]
    public function createCat(): array
    {
        return [];
    }
}
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "php")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (class is top-level, methods are nested), got %d", len(blocks))
	}

	if blocks[0].StartLine != 5 {
		t.Errorf("expected class block start at line 5 (#[Route] line), got %d", blocks[0].StartLine)
	}
}

func TestParseBlocks_DecoratorNoFalsePositiveOnRegularCode(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.py")
	content := `if condition:
    pass

@app.route('/api')
def handler():
    return 'ok'
`
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "python")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}

	if blocks[0].StartLine != 4 {
		t.Errorf("expected handler block start at line 4 (@app.route line), got %d, decorator should not include unrelated indented code", blocks[0].StartLine)
	}

	if !blocks[0].HasDecorators {
		t.Error("expected handler block to have decorators")
	}
}

func TestParseBlocks_DecoratorSkipsBlankLines(t *testing.T) {
	s := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ts")
	content := "@Get()\n\nfunction findAll() {}\n"
	os.WriteFile(path, []byte(content), 0644)

	blocks, err := s.ParseBlocks(path, "javascript")
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}

	if blocks[0].StartLine != 1 {
		t.Errorf("expected block start at line 1 (@Get() line), got %d", blocks[0].StartLine)
	}
}

func TestCollectDecorators_Single(t *testing.T) {
	lines := []string{
		"@Get()",
		"function findAll() {",
		"}",
	}
	patterns := []*regexp.Regexp{regexp.MustCompile(`^\s*@`)}

	start := collectDecorators(lines, 1, patterns)
	if start != 0 {
		t.Errorf("expected start=0 (includes @Get line), got %d", start)
	}
}

func TestCollectDecorators_Multiple(t *testing.T) {
	lines := []string{
		"@Get()",
		"@HttpCode(201)",
		"function findAll() {",
		"}",
	}
	patterns := []*regexp.Regexp{regexp.MustCompile(`^\s*@`)}

	start := collectDecorators(lines, 2, patterns)
	if start != 0 {
		t.Errorf("expected start=0 (includes both decorators), got %d", start)
	}
}

func TestCollectDecorators_NoFalsePositive(t *testing.T) {
	lines := []string{
		"if condition:",
		"    pass",
		"",
		"@app.route('/api')",
		"def handler():",
		"    pass",
	}
	patterns := []*regexp.Regexp{regexp.MustCompile(`^\s*@`)}

	start := collectDecorators(lines, 4, patterns)
	if start != 3 {
		t.Errorf("expected start=3 (@app.route line only), got %d, should not include unrelated 'if condition:    pass' lines", start)
	}
}

func TestCollectDecorators_NestedDecoratorsNotConsumed(t *testing.T) {
	lines := []string{
		"@Controller('cats')",
		"export class CatsController {",
		"    @Get()",
		"    @HttpCode(201)",
		"    findAll(): Cat[] {",
		"    }",
		"}",
	}
	patterns := []*regexp.Regexp{regexp.MustCompile(`^\s*@`)}

	start := collectDecorators(lines, 1, patterns)
	if start != 0 {
		t.Errorf("expected start=0 (@Controller line only), got %d, should not include @Get/@HttpCode which are inside the class", start)
	}
}
