package parser

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGrammarFileName_Windows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific test")
	}
	name := grammarFileName("go")
	if !strings.Contains(name, ".dll") {
		t.Fatalf("expected .dll, got %s", name)
	}
}

func TestGrammarFileName_NonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-windows test")
	}
	name := grammarFileName("go")
	if runtime.GOOS == "darwin" {
		if !strings.Contains(name, ".dylib") {
			t.Fatalf("expected .dylib, got %s", name)
		}
	} else {
		if !strings.Contains(name, ".so") {
			t.Fatalf("expected .so, got %s", name)
		}
	}
}

func TestGrammarFuncName(t *testing.T) {
	tests := []struct {
		lang string
		want string
	}{
		{"go", "tree_sitter_go"},
		{"python", "tree_sitter_python"},
		{"typescript", "tree_sitter_typescript"},
		{"c-sharp", "tree_sitter_c_sharp"},
	}
	for _, tc := range tests {
		got := grammarFuncName(tc.lang)
		if got != tc.want {
			t.Errorf("grammarFuncName(%q) = %q, want %q", tc.lang, got, tc.want)
		}
	}
}

func TestGrammarPath(t *testing.T) {
	// Use a temp dir for testing
	origDir := GrammarDir
	defer func() { GrammarDir = origDir }()

	dir := t.TempDir()
	GrammarDir = func() (string, error) { return dir, nil }

	path, err := GrammarPath("go")
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(dir, grammarFileName("go"))
	if path != expected {
		t.Fatalf("expected %q, got %q", expected, path)
	}
}

func TestGrammarExists_NotFound(t *testing.T) {
	origDir := GrammarDir
	defer func() { GrammarDir = origDir }()

	GrammarDir = func() (string, error) { return t.TempDir(), nil }

	if GrammarExists("nonexistent") {
		t.Fatal("expected false for nonexistent grammar")
	}
}

func TestGrammarExists_Found(t *testing.T) {
	origDir := GrammarDir
	defer func() { GrammarDir = origDir }()

	dir := t.TempDir()
	GrammarDir = func() (string, error) { return dir, nil }

	path := filepath.Join(dir, grammarFileName("go"))
	os.WriteFile(path, []byte("fake grammar"), 0644)

	if !GrammarExists("go") {
		t.Fatal("expected true for existing grammar")
	}
}

func TestGrammarDownloadURL(t *testing.T) {
	cfg := ParserConfig{}
	url := grammarDownloadURL("go", cfg)
	if url == "" {
		t.Fatal("expected non-empty URL")
	}
	if !strings.Contains(url, "github.com") {
		t.Fatal("expected github.com URL")
	}
}

func TestDownloadGrammar_AlreadyExists(t *testing.T) {
	origDir := GrammarDir
	defer func() { GrammarDir = origDir }()

	dir := t.TempDir()
	GrammarDir = func() (string, error) { return dir, nil }

	path := filepath.Join(dir, grammarFileName("go"))
	os.WriteFile(path, []byte("existing grammar"), 0644)

	result, err := DownloadGrammar("go", ParserConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if result != path {
		t.Fatalf("expected %q, got %q", path, result)
	}
}

func TestDownloadGrammar_DownloadSuccess(t *testing.T) {
	origDir := GrammarDir
	defer func() { GrammarDir = origDir }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("grammar binary content"))
	}))
	defer server.Close()

	origURL := grammarDownloadURL
	defer func() { grammarDownloadURL = origURL }()
	grammarDownloadURL = func(language string, cfg ParserConfig) string {
		return server.URL
	}

	dir := t.TempDir()
	GrammarDir = func() (string, error) { return dir, nil }

	path, err := DownloadGrammar("go", ParserConfig{})
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "grammar binary content" {
		t.Fatalf("expected 'grammar binary content', got %s", string(data))
	}
}

func TestDownloadGrammarIfMissing_Existing(t *testing.T) {
	origDir := GrammarDir
	defer func() { GrammarDir = origDir }()

	dir := t.TempDir()
	GrammarDir = func() (string, error) { return dir, nil }

	path := filepath.Join(dir, grammarFileName("go"))
	os.WriteFile(path, []byte("existing"), 0644)

	result, cached, err := DownloadGrammarIfMissing("go", ParserConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !cached {
		t.Fatal("expected cached=true")
	}
	if result != path {
		t.Fatalf("expected %q, got %q", path, result)
	}
}

func TestDownloadGrammarIfMissing_Download(t *testing.T) {
	origDir := GrammarDir
	defer func() { GrammarDir = origDir }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("new grammar"))
	}))
	defer server.Close()

	origURL := grammarDownloadURL
	defer func() { grammarDownloadURL = origURL }()
	grammarDownloadURL = func(language string, cfg ParserConfig) string {
		return server.URL
	}

	dir := t.TempDir()
	GrammarDir = func() (string, error) { return dir, nil }

	result, cached, err := DownloadGrammarIfMissing("go", ParserConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if cached {
		t.Fatal("expected cached=false for new download")
	}
	if result == "" {
		t.Fatal("expected non-empty path")
	}
}

func TestDownloadFileGrammar(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("downloaded"))
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "output")
	if err := downloadFile(server.URL, dest); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(dest)
	if string(data) != "downloaded" {
		t.Fatalf("expected 'downloaded', got %s", string(data))
	}
}

func TestDownloadFileGrammar_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	if err := downloadFile(server.URL, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("expected error for 500")
	}
}
