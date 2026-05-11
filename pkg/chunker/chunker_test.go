package chunker

import (
	"os"
	"path/filepath"
	"testing"

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
