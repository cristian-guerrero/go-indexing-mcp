package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestEncodeProjectPath_Windows(t *testing.T) {
	got := EncodeProjectPath(`C:\project\apps\go-indexing-mcp`)
	want := `--C--project-apps-go-indexing-mcp--`
	if got != want {
		t.Errorf("EncodeProjectPath = %q, want %q", got, want)
	}
}

func TestEncodeProjectPath_Unix(t *testing.T) {
	got := EncodeProjectPath(`/home/user/project`)
	want := `---home-user-project--`
	if got != want {
		t.Errorf("EncodeProjectPath = %q, want %q", got, want)
	}
}

func TestEncodeProjectPath_WithDots(t *testing.T) {
	got := EncodeProjectPath(`C:\Users\cristian\.go-mcp-image-generator`)
	want := `--C--Users-cristian-.go-mcp-image-generator--`
	if got != want {
		t.Errorf("EncodeProjectPath = %q, want %q", got, want)
	}
}

func TestStoragePath_EndsWithVectorsGob(t *testing.T) {
	path := StoragePath("/test/project")
	if !strings.HasSuffix(path, "vectors.gob") {
		t.Errorf("StoragePath should end with vectors.gob, got %q", path)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("StoragePath should be absolute, got %q", path)
	}
}

func TestStoragePath_UnderVectorsAndEncoded(t *testing.T) {
	path := StoragePath("/test/project")
	absRoot, _ := filepath.Abs("/test/project")
	encoded := EncodeProjectPath(absRoot)

	if !strings.Contains(path, "vectors") {
		t.Errorf("StoragePath %q should contain 'vectors' directory", path)
	}
	if !strings.Contains(path, encoded) {
		t.Errorf("StoragePath %q should contain encoded path %q", path, encoded)
	}
}

func TestStoragePath_UnderMcpDir(t *testing.T) {
	path := StoragePath("/test/project")
	mcpDir := McpDir()
	if !strings.HasPrefix(path, mcpDir) {
		t.Errorf("StoragePath %q should be under McpDir %q", path, mcpDir)
	}
}
