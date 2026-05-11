package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNew(t *testing.T) {
	m := New(t.TempDir(), nil)
	if m == nil {
		t.Fatal("matcher should not be nil")
	}
}

func TestShouldIgnore_DefaultPatterns(t *testing.T) {
	m := New(t.TempDir(), nil)

	tests := []struct {
		path   string
		ignore bool
	}{
		{"main.go", false},
		{"pkg/util.go", false},
		{"node_modules/package/index.js", true},
		{".git/HEAD", true},
		{"vendor/pkg/file.go", true},
		{"file.exe", true},
		{"image.png", true},
		{"archive.zip", true},
		{"document.pdf", true},
		{"file.min.js", true},
		{"__pycache__/module.pyc", true},
		{".DS_Store", true},
		{"go.sum", true},
		{"package-lock.json", true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := m.ShouldIgnore(tt.path)
			if got != tt.ignore {
				t.Errorf("ShouldIgnore(%q) = %v, want %v", tt.path, got, tt.ignore)
			}
		})
	}
}

func TestShouldIgnore_ExtraPatterns(t *testing.T) {
	extra := []string{"*.custom"}
	m := New(t.TempDir(), extra)

	if !m.ShouldIgnore("file.custom") {
		t.Error("expected file.custom to be ignored")
	}
	if m.ShouldIgnore("file.txt") {
		t.Error("expected file.txt not to be ignored")
	}
}

func TestShouldIgnore_GitIgnore(t *testing.T) {
	dir := t.TempDir()
	gitignore := filepath.Join(dir, ".gitignore")
	os.WriteFile(gitignore, []byte("*.secret\nbuild/\n"), 0644)

	m := New(dir, nil)

	if !m.ShouldIgnore("credentials.secret") {
		t.Error("expected .secret files to be ignored from .gitignore")
	}
	if !m.ShouldIgnore("build/output.bin") {
		t.Error("expected build/ to be ignored from .gitignore")
	}
	if m.ShouldIgnore("main.go") {
		t.Error("expected main.go not to be ignored")
	}
}

func TestShouldIgnore_EmptyPath(t *testing.T) {
	m := New(t.TempDir(), nil)
	if m.ShouldIgnore("") {
		t.Error("empty path should not be ignored")
	}
}

func TestShouldIgnore_NestedPaths(t *testing.T) {
	m := New(t.TempDir(), nil)

	tests := []struct {
		path   string
		ignore bool
	}{
		{"src/.DS_Store", true},
		{"src/node_modules/lib/index.js", true},
		{"src/__pycache__/main.pyc", true},
		{".venv/bin/python", true},
		{"src/main.go", false},
		{"README.md", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := m.ShouldIgnore(tt.path)
			if got != tt.ignore {
				t.Errorf("ShouldIgnore(%q) = %v, want %v", tt.path, got, tt.ignore)
			}
		})
	}
}

func TestGitIgnoreNotFound(t *testing.T) {
	m := New(t.TempDir(), nil)
	if m.ShouldIgnore("test.go") {
		t.Error("expected test.go not to be ignored")
	}
}
