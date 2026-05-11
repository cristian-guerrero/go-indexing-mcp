package treeparse

import (
	"testing"
)

func TestNew(t *testing.T) {
	p := New("/usr/bin/tree-sitter")
	if p.BinPath != "/usr/bin/tree-sitter" {
		t.Errorf("expected /usr/bin/tree-sitter, got %s", p.BinPath)
	}
}

func TestParseBlocks_UnsupportedLanguage(t *testing.T) {
	p := New("tree-sitter")
	blocks, err := p.ParseBlocks("test.sql", "sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Errorf("expected 0 blocks for sql, got %d", len(blocks))
	}
}

func TestParseTreeJSON_Direct(t *testing.T) {
	data := []byte(`{
		"type": "program",
		"start_position": {"row": 0, "column": 0},
		"end_position": {"row": 10, "column": 0},
		"children": [
			{
				"type": "function_declaration",
				"start_position": {"row": 0, "column": 0},
				"end_position": {"row": 5, "column": 1},
				"children": []
			}
		]
	}`)

	root, err := parseTreeJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if root.Type != "program" {
		t.Errorf("expected program, got %s", root.Type)
	}
}

func TestParseTreeJSON_WithTreeField(t *testing.T) {
	data := []byte(`{
		"tree": {
			"type": "program",
			"start_position": {"row": 0, "column": 0},
			"end_position": {"row": 10, "column": 0},
			"children": [
				{
					"type": "function_declaration",
					"start_position": {"row": 0, "column": 0},
					"end_position": {"row": 5, "column": 1}
				}
			]
		}
	}`)

	root, err := parseTreeJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if root.Type != "program" {
		t.Errorf("expected program, got %s", root.Type)
	}
}

func TestParseTreeJSON_ArrayFormat(t *testing.T) {
	data := []byte(`[
		{
			"file_path": "test.go",
			"tree": {
				"type": "program",
				"start_position": {"row": 0, "column": 0},
				"end_position": {"row": 10, "column": 0},
				"children": []
			}
		}
	]`)

	root, err := parseTreeJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if root.Type != "program" {
		t.Errorf("expected program, got %s", root.Type)
	}
}

func TestCollectBlocks_GoFunctions(t *testing.T) {
	types := structuralTypes["go"]
	root := jsonNode{
		Type: "program",
		Children: []jsonNode{
			{
				Type:          "function_declaration",
				StartPosition: jsonPosition{Row: 4, Column: 0},
				EndPosition:   jsonPosition{Row: 20, Column: 0},
			},
			{
				Type:          "function_declaration",
				StartPosition: jsonPosition{Row: 22, Column: 0},
				EndPosition:   jsonPosition{Row: 30, Column: 0},
			},
		},
	}

	var blocks []Block
	collectBlocks(root, types, &blocks)

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].StartLine != 5 {
		t.Errorf("expected start line 5, got %d", blocks[0].StartLine)
	}
	if blocks[0].EndLine != 20 {
		t.Errorf("expected end line 20, got %d", blocks[0].EndLine)
	}
	if blocks[0].NodeType != "function_declaration" {
		t.Errorf("expected function_declaration, got %s", blocks[0].NodeType)
	}
}

func TestCollectBlocks_SkipsNonStructural(t *testing.T) {
	types := structuralTypes["go"]
	root := jsonNode{
		Type: "program",
		Children: []jsonNode{
			{
				Type:          "import_declaration",
				StartPosition: jsonPosition{Row: 1, Column: 0},
				EndPosition:   jsonPosition{Row: 3, Column: 0},
			},
			{
				Type:          "function_declaration",
				StartPosition: jsonPosition{Row: 5, Column: 0},
				EndPosition:   jsonPosition{Row: 10, Column: 0},
			},
		},
	}

	var blocks []Block
	collectBlocks(root, types, &blocks)

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
}

func TestCollectBlocks_DoesNotRecurseIntoStructural(t *testing.T) {
	types := structuralTypes["java"]
	root := jsonNode{
		Type: "program",
		Children: []jsonNode{
			{
				Type:          "class_declaration",
				StartPosition: jsonPosition{Row: 0, Column: 0},
				EndPosition:   jsonPosition{Row: 10, Column: 0},
				Children: []jsonNode{
					{
						Type:          "method_declaration",
						StartPosition: jsonPosition{Row: 2, Column: 0},
						EndPosition:   jsonPosition{Row: 5, Column: 0},
					},
				},
			},
		},
	}

	var blocks []Block
	collectBlocks(root, types, &blocks)

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (class only), got %d", len(blocks))
	}
	if blocks[0].NodeType != "class_declaration" {
		t.Errorf("expected class_declaration, got %s", blocks[0].NodeType)
	}
}

func TestCollectBlocks_PreservesChildOrder(t *testing.T) {
	types := structuralTypes["go"]
	root := jsonNode{
		Type: "program",
		Children: []jsonNode{
			{
				Type:          "function_declaration",
				StartPosition: jsonPosition{Row: 20, Column: 0},
				EndPosition:   jsonPosition{Row: 30, Column: 0},
			},
			{
				Type:          "function_declaration",
				StartPosition: jsonPosition{Row: 5, Column: 0},
				EndPosition:   jsonPosition{Row: 15, Column: 0},
			},
		},
	}

	var blocks []Block
	collectBlocks(root, types, &blocks)

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].StartLine != 21 || blocks[1].StartLine != 6 {
		t.Errorf("expected original child order, got %d then %d", blocks[0].StartLine, blocks[1].StartLine)
	}
}

func TestStructuralTypes_PopularLanguages(t *testing.T) {
	popular := []string{"go", "python", "javascript", "rust", "java", "c", "cpp", "csharp", "ruby", "php"}
	for _, lang := range popular {
		if _, ok := structuralTypes[lang]; !ok {
			t.Errorf("missing structural types for %s", lang)
		}
	}
}

func TestExtractBlocks_SortsByStartLine(t *testing.T) {
	root := jsonNode{
		Type: "program",
		Children: []jsonNode{
			{
				Type:          "function_declaration",
				StartPosition: jsonPosition{Row: 20, Column: 0},
				EndPosition:   jsonPosition{Row: 30, Column: 0},
			},
			{
				Type:          "function_declaration",
				StartPosition: jsonPosition{Row: 5, Column: 0},
				EndPosition:   jsonPosition{Row: 15, Column: 0},
			},
		},
	}

	blocks := extractBlocks(root, structuralTypes["go"])
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].StartLine != 6 || blocks[1].StartLine != 21 {
		t.Errorf("expected sorted 6 before 21, got %d before %d", blocks[0].StartLine, blocks[1].StartLine)
	}
}

func TestParseBatchOutput_ArrayFormat(t *testing.T) {
	data := []byte(`[
		{"file_path": "a.go", "tree": {"type": "program", "start_position": {"row": 0, "col": 0}, "end_position": {"row": 10, "col": 0}, "children": [
			{"type": "function_declaration", "start_position": {"row": 2, "col": 0}, "end_position": {"row": 8, "col": 0}}
		]}},
		{"file_path": "b.go", "tree": {"type": "program", "start_position": {"row": 0, "col": 0}, "end_position": {"row": 5, "col": 0}, "children": []}}
	]`)

	files := []ParseFile{
		{Path: "a.go", Language: "go"},
		{Path: "b.go", Language: "go"},
	}

	results, err := parseBatchOutput(data, files)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	aBlocks, ok := results["a.go"]
	if !ok {
		t.Fatal("expected a.go in results")
	}
	if len(aBlocks) != 1 {
		t.Fatalf("expected 1 block for a.go, got %d", len(aBlocks))
	}
	if aBlocks[0].StartLine != 3 {
		t.Errorf("expected start line 3, got %d", aBlocks[0].StartLine)
	}
}

func TestParseBatchOutput_JSONLinesFormat(t *testing.T) {
	data := []byte(`{"file_path": "a.go", "tree": {"type": "program", "start_position": {"row": 0, "col": 0}, "end_position": {"row": 10, "col": 0}, "children": [
		{"type": "function_declaration", "start_position": {"row": 2, "col": 0}, "end_position": {"row": 8, "col": 0}}
	]}}
{"file_path": "b.go", "tree": {"type": "program", "start_position": {"row": 0, "col": 0}, "end_position": {"row": 5, "col": 0}, "children": []}}
`)

	files := []ParseFile{
		{Path: "a.go", Language: "go"},
		{Path: "b.go", Language: "go"},
	}

	results, err := parseBatchOutput(data, files)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestParseBatchOutput_SkipsUnsupportedLanguage(t *testing.T) {
	data := []byte(`[
		{"file_path": "a.sql", "tree": {"type": "program", "start_position": {"row": 0, "col": 0}, "end_position": {"row": 5, "col": 0}, "children": []}}
	]`)

	files := []ParseFile{
		{Path: "a.sql", Language: "sql"},
	}

	results, err := parseBatchOutput(data, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for unsupported language, got %d", len(results))
	}
}

func TestParseBlocksBatch_FiltersUnsupported(t *testing.T) {
	p := New("tree-sitter")
	files := []ParseFile{
		{Path: "a.sql", Language: "sql"},
		{Path: "b.txt", Language: ""},
	}

	results, err := p.ParseBlocksBatch(files)
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Errorf("expected nil result for unsupported languages, got %v", results)
	}
}
