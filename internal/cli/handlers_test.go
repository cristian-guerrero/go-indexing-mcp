package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripJSONComments_NoComments(t *testing.T) {
	input := `{"key": "value"}`
	got := stripJSONComments(input)
	if got != input {
		t.Fatalf("expected %q, got %q", input, got)
	}
}

func TestStripJSONComments_LineComment(t *testing.T) {
	input := "{\n// this is a comment\n\"key\": \"value\"\n}"
	expected := "{\n\n\"key\": \"value\"\n}"
	got := stripJSONComments(input)
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestStripJSONComments_BlockComment(t *testing.T) {
	input := "{\n/* block\ncomment */\n\"key\": \"value\"\n}"
	expected := "{\n\n\"key\": \"value\"\n}"
	got := stripJSONComments(input)
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestStripJSONComments_StringLiteralBoundary(t *testing.T) {
	input := `{"url": "http://example.com/foo"}`
	got := stripJSONComments(input)
	if got != input {
		t.Fatalf("expected no change, got %q", got)
	}
}

func TestStripJSONComments_StringWithEscapedQuote(t *testing.T) {
	input := `{"msg": "hello \"world\""}`
	got := stripJSONComments(input)
	if got != input {
		t.Fatalf("expected no change, got %q", got)
	}
}

func TestToForwardPath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"C:\\project\\file.go", "C:/project/file.go"},
		{"unix/path/file.go", "unix/path/file.go"},
		{"mixed\\slash/path", "mixed/slash/path"},
		{"", ""},
	}
	for _, tc := range tests {
		got := toForwardPath(tc.input)
		if got != tc.expected {
			t.Errorf("toForwardPath(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestMergeAgentsSection_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")

	changed, err := mergeAgentsSection(path, "custom content")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true for new file")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "custom content") {
		t.Fatal("expected custom content in file")
	}
	if !strings.Contains(content, sectionStart) {
		t.Fatal("expected section start marker")
	}
	if !strings.Contains(content, sectionEnd) {
		t.Fatal("expected section end marker")
	}
}

func TestMergeAgentsSection_ReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")

	os.WriteFile(path, []byte("prefix\n"+sectionStart+"\nold content\n"+sectionEnd+"\nsuffix\n"), 0644)

	changed, err := mergeAgentsSection(path, "new content")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true for replaced section")
	}

	data, _ := os.ReadFile(path)
	content := string(data)
	if strings.Contains(content, "old content") {
		t.Fatal("expected old content to be replaced")
	}
	if !strings.Contains(content, "new content") {
		t.Fatal("expected new content in file")
	}
	if !strings.Contains(content, "prefix") {
		t.Fatal("expected prefix to be preserved")
	}
	if !strings.Contains(content, "suffix") {
		t.Fatal("expected suffix to be preserved")
	}
}

func TestMergeAgentsSection_NoChangeWhenSame(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	section := "same content"

	changed, err := mergeAgentsSection(path, section)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true for first write")
	}

	changed, err = mergeAgentsSection(path, section)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected changed=false for identical content")
	}
}

func TestMergeAgentsSection_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")

	os.WriteFile(path, []byte("existing content\n"), 0644)

	changed, err := mergeAgentsSection(path, "new section")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}

	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, "existing content") {
		t.Fatal("expected existing content preserved")
	}
	if !strings.Contains(content, "new section") {
		t.Fatal("expected new section appended")
	}
}
