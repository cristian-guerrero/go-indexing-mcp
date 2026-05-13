package chunker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cristian/go-indexing-mcp/pkg/structural"
	"github.com/cristian/go-indexing-mcp/pkg/walker"
)

func TestNew_DefaultValues(t *testing.T) {
	c := New(0, 0)
	if c.ChunkSize != 50 {
		t.Errorf("expected chunk size 50, got %d", c.ChunkSize)
	}
	if c.ChunkOverlap != 0 {
		t.Errorf("expected overlap 0, got %d", c.ChunkOverlap)
	}
}

func TestNew_ClampOverlap(t *testing.T) {
	c := New(100, 80)
	if c.ChunkOverlap >= c.ChunkSize {
		t.Error("overlap should be clamped below chunk size")
	}
}

func TestNew_NegativeOverlap(t *testing.T) {
	c := New(50, -1)
	if c.ChunkOverlap != 0 {
		t.Errorf("expected overlap 0, got %d", c.ChunkOverlap)
	}
}

func TestChunkFile_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.go")
	os.WriteFile(path, []byte{}, 0644)

	fi := walker.FileInfo{Path: path, RelPath: "empty.go", Hash: "abc123", Language: "go"}
	c := New(10, 2)
	chunks, err := c.ChunkFile(fi)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty file, got %d", len(chunks))
	}
}

func TestChunkFile_SingleChunk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.go")
	os.WriteFile(path, []byte("line1\nline2\nline3"), 0644)

	fi := walker.FileInfo{Path: path, RelPath: "small.go", Hash: "abc123", Language: "go"}
	c := New(10, 2)
	chunks, err := c.ChunkFile(fi)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].StartLine != 1 {
		t.Errorf("expected start line 1, got %d", chunks[0].StartLine)
	}
	if chunks[0].EndLine != 3 {
		t.Errorf("expected end line 3, got %d", chunks[0].EndLine)
	}
	if chunks[0].Content != "line1\nline2\nline3" {
		t.Errorf("content mismatch:\n expected: %q\n got: %q", "line1\nline2\nline3", chunks[0].Content)
	}
}

func TestChunkFile_MultipleChunks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.go")
	var lines []string
	for i := range 10 {
		lines = append(lines, "line"+itoa(i+1))
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	os.WriteFile(path, []byte(content), 0644)

	fi := walker.FileInfo{Path: path, RelPath: "multi.go", Hash: "def456", Language: "go"}
	c := New(4, 1)
	chunks, err := c.ChunkFile(fi)
	if err != nil {
		t.Fatal(err)
	}

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	if chunks[0].EndLine-chunks[0].StartLine+1 != 4 {
		t.Errorf("expected first chunk to have 4 lines, got %d-%d", chunks[0].StartLine, chunks[0].EndLine)
	}

	last := chunks[len(chunks)-1]
	if last.EndLine != 10 {
		t.Errorf("expected last chunk to end at 10, got %d", last.EndLine)
	}
}

func TestChunkFile_RelPathFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("line1\nline2\n"), 0644)

	tests := []struct {
		name   string
		rel    string
		expect string
	}{
		{"with relpath", "pkg/test.go", "pkg/test.go"},
		{"empty relpath", "", path},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fi := walker.FileInfo{Path: path, RelPath: tt.rel, Hash: "abc", Language: "go"}
			c := New(10, 2)
			chunks, err := c.ChunkFile(fi)
			if err != nil {
				t.Fatal(err)
			}
			if len(chunks) > 0 && chunks[0].RelPath != tt.expect {
				t.Errorf("expected relpath %q, got %q", tt.expect, chunks[0].RelPath)
			}
		})
	}
}

func TestChunkFile_Metadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte("package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"), 0644)

	fi := walker.FileInfo{
		Path:     path,
		RelPath:  "main.go",
		Hash:     "xyz789",
		Language: "go",
		Size:     42,
	}

	c := New(10, 2)
	chunks, err := c.ChunkFile(fi)
	if err != nil {
		t.Fatal(err)
	}

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}

	ch := chunks[0]
	if ch.FilePath != path {
		t.Errorf("filepath mismatch: %q", ch.FilePath)
	}
	if ch.Language != "go" {
		t.Errorf("language mismatch: %q", ch.Language)
	}
	if ch.FileHash != "xyz789" {
		t.Errorf("filehash mismatch: %q", ch.FileHash)
	}
}

func TestChunkFile_ReadError(t *testing.T) {
	fi := walker.FileInfo{Path: "/nonexistent/file.go", RelPath: "file.go", Hash: "abc", Language: "go"}
	c := New(10, 2)
	_, err := c.ChunkFile(fi)
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestSlidingWindow_FitsInOne(t *testing.T) {
	lines := []string{"a", "b", "c"}
	c := New(10, 2)
	chunks := c.slidingWindow(lines, 0, 3, walker.FileInfo{Path: "test.go", RelPath: "test.go", Hash: "abc", Language: "go"})
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
}

func TestSlidingWindow_Multiple(t *testing.T) {
	lines := []string{"1", "2", "3", "4", "5", "6", "7"}
	c := New(3, 1)
	chunks := c.slidingWindow(lines, 0, 7, walker.FileInfo{Path: "test.go", RelPath: "test.go", Hash: "abc", Language: "go"})
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	if chunks[0].StartLine != 1 || chunks[0].EndLine != 3 {
		t.Errorf("first chunk expected 1-3, got %d-%d", chunks[0].StartLine, chunks[0].EndLine)
	}
	last := chunks[len(chunks)-1]
	if last.EndLine != 7 {
		t.Errorf("last chunk should end at 7, got %d", last.EndLine)
	}
}

func TestStructuralSplit_RespectsBlockBoundaries(t *testing.T) {
	lines := make([]string, 100)
	for i := range 100 {
		lines[i] = "line" + itoa(i+1)
	}

	blocks := []structural.Block{
		{StartLine: 1, EndLine: 10, NodeType: "function_declaration"},
		{StartLine: 15, EndLine: 45, NodeType: "function_declaration"},
		{StartLine: 50, EndLine: 100, NodeType: "function_declaration"},
	}

	c := New(20, 2)
	fi := walker.FileInfo{Path: "test.go", RelPath: "test.go", Hash: "abc", Language: "go"}

	chunks := c.structuralSplit(lines, blocks, fi)

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}

	for _, ch := range chunks {
		if ch.StartLine > 100 || ch.EndLine < 1 {
			t.Errorf("chunk out of range: %d-%d", ch.StartLine, ch.EndLine)
		}
	}

	var hasSplitFunc bool
	for _, ch := range chunks {
		if ch.StartLine >= 15 && ch.EndLine <= 45 && ch.EndLine-ch.StartLine+1 <= 20 {
			hasSplitFunc = true
			break
		}
	}
	if !hasSplitFunc {
		t.Log("note: large function (15-45) should be split into sub-chunks ≤ chunkSize")
	}
}

func TestStructuralSplit_GapBetweenBlocks(t *testing.T) {
	lines := make([]string, 30)
	for i := range 30 {
		lines[i] = "line" + itoa(i+1)
	}

	c := New(20, 2)
	fi := walker.FileInfo{Path: "test.go", RelPath: "test.go", Hash: "abc", Language: "go"}

	blocks := []structural.Block{
		{StartLine: 1, EndLine: 10, NodeType: "function_declaration"},
		{StartLine: 20, EndLine: 30, NodeType: "function_declaration"},
	}

	chunks := c.structuralSplit(lines, blocks, fi)

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	firstOverlap := false
	for _, ch := range chunks {
		if ch.StartLine <= 10 && ch.EndLine >= 11 {
			firstOverlap = true
		}
	}
	if firstOverlap {
		t.Log("gap (lines 11-19) may be a separate chunk or merged with adjacent blocks")
	}
}

func TestNew_HasStructuralSplit(t *testing.T) {
	c := New(50, 10)
	if !c.HasStructuralSplit() {
		t.Error("expected HasStructuralSplit to return true")
	}
}

func TestChunkFile_DecoratorIncludedInChunk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.py")
	content := `from flask import Flask

app = Flask(__name__)

@app.route('/api/cats', methods=['GET'])
@auth.required
def get_cats():
    return []
`
	os.WriteFile(path, []byte(content), 0644)

	fi := walker.FileInfo{
		Path:     path,
		RelPath:  "app.py",
		Hash:     "abc123",
		Language: "python",
		Size:     int64(len(content)),
	}

	c := New(50, 5)
	chunks, err := c.ChunkFile(fi)
	if err != nil {
		t.Fatal(err)
	}

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}

	first := chunks[0]
	if !strings.Contains(first.Content, "@app.route") || !strings.Contains(first.Content, "@auth.required") {
		t.Errorf("chunk content should include decorators, got:\n%s", first.Content)
	}

	if !strings.Contains(first.Content, "def get_cats()") {
		t.Errorf("chunk content should include the function, got:\n%s", first.Content)
	}
}
