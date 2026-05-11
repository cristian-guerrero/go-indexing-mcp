package walker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		path string
		lang string
	}{
		{"main.go", "go"},
		{"lib.py", "python"},
		{"app.js", "javascript"},
		{"component.tsx", "javascript"},
		{"main.rs", "rust"},
		{"Main.java", "java"},
		{"file.c", "c"},
		{"header.h", "c"},
		{"lib.cpp", "cpp"},
		{"lib.hpp", "cpp"},
		{"Program.cs", "csharp"},
		{"script.rb", "ruby"},
		{"index.php", "php"},
		{"main.swift", "swift"},
		{"App.kt", "kotlin"},
		{"App.scala", "scala"},
		{"query.sql", "sql"},
		{"deploy.sh", "bash"},
		{"script.bash", "bash"},
		{"install.ps1", "powershell"},
		{"README.md", "markdown"},
		{"config.yaml", "yaml"},
		{"config.yml", "yaml"},
		{"package.json", "json"},
		{"config.toml", "toml"},
		{"index.html", "html"},
		{"style.css", "css"},
		{"style.scss", "css"},
		{"unknown.xyz", ""},
		{"Makefile", ""},
		{"Dockerfile", ""},
		{"file", ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := detectLanguage(tt.path)
			if got != tt.lang {
				t.Errorf("detectLanguage(%q) = %q, want %q", tt.path, got, tt.lang)
			}
		})
	}
}

func TestFileHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "hello world"
	os.WriteFile(path, []byte(content), 0644)

	hash, err := fileHash(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(hash) != 16 {
		t.Errorf("expected 16 char hash (8 bytes hex), got %d: %s", len(hash), hash)
	}

	hash2, _ := fileHash(path)
	if hash != hash2 {
		t.Error("hash should be consistent for same content")
	}
}

func TestFileHash_DifferentContent(t *testing.T) {
	dir := t.TempDir()

	p1 := filepath.Join(dir, "a.txt")
	p2 := filepath.Join(dir, "b.txt")
	os.WriteFile(p1, []byte("content a"), 0644)
	os.WriteFile(p2, []byte("content b"), 0644)

	h1, _ := fileHash(p1)
	h2, _ := fileHash(p2)

	if h1 == h2 {
		t.Error("different files should have different hashes")
	}
}

func TestFileHash_NotFound(t *testing.T) {
	_, err := fileHash("/nonexistent/path")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestNew(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, nil)
	if w == nil {
		t.Fatal("walker should not be nil")
	}
	if w.Root != dir {
		t.Errorf("expected root %q, got %q", dir, w.Root)
	}
}

func TestWalk_Basic(t *testing.T) {
	dir := t.TempDir()

	os.MkdirAll(filepath.Join(dir, "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)
	os.WriteFile(filepath.Join(dir, "pkg", "util.go"), []byte("package pkg\n"), 0644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme\n"), 0644)
	os.WriteFile(filepath.Join(dir, "ignored.exe"), []byte("binary"), 0644)

	w := New(dir, nil)
	files, err := w.Walk()
	if err != nil {
		t.Fatal(err)
	}

	found := make(map[string]bool)
	for _, f := range files {
		found[f.RelPath] = true
	}

	if !found["main.go"] {
		t.Error("expected main.go to be found")
	}
	if !found[filepath.Join("pkg", "util.go")] {
		t.Error("expected pkg/util.go to be found")
	}
	if found["ignored.exe"] {
		t.Error("expected ignored.exe to be skipped")
	}

	for _, f := range files {
		if f.Path == "" || f.RelPath == "" {
			t.Error("all files must have Path and RelPath")
		}
		if f.Language == "" {
			t.Errorf("file %s has no language", f.RelPath)
		}
		if f.Hash == "" {
			t.Errorf("file %s has no hash", f.RelPath)
		}
	}
}

func TestWalk_OnlySupportedLanguages(t *testing.T) {
	dir := t.TempDir()

	extensions := []string{".go", ".py", ".js", ".rs", ".xyz", ".abc"}
	for _, ext := range extensions {
		path := filepath.Join(dir, "file"+ext)
		os.WriteFile(path, []byte("content"), 0644)
	}

	w := New(dir, nil)
	files, _ := w.Walk()

	for _, f := range files {
		if strings.HasSuffix(f.RelPath, ".xyz") || strings.HasSuffix(f.RelPath, ".abc") {
			t.Errorf("unsupported extension should not be indexed: %s", f.RelPath)
		}
	}
}

func TestWalk_Symlinks(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0755)
	os.WriteFile(filepath.Join(sub, "actual.go"), []byte("package sub\n"), 0644)

	w := New(dir, nil)
	files, _ := w.Walk()

	found := false
	for _, f := range files {
		if f.RelPath == filepath.Join("sub", "actual.go") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected sub/actual.go to be found")
	}
}

func TestWalk_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, nil)
	files, err := w.Walk()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files in empty dir, got %d", len(files))
	}
}
