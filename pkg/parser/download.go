package parser

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// GrammarDir returns ~/.go-mcp/tree-sitter/grammars/, creating it if needed.
// Declared as a variable for testability (can be replaced in tests).
var GrammarDir = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, ".go-mcp", "tree-sitter", "grammars")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create grammar dir: %w", err)
	}
	return dir, nil
}

// grammarFileName returns the platform-specific filename for a grammar library.
func grammarFileName(language string) string {
	switch runtime.GOOS {
	case "windows":
		return "tree-sitter-" + language + ".dll"
	case "darwin":
		return "libtree-sitter-" + language + ".dylib"
	default:
		return "libtree-sitter-" + language + ".so"
	}
}

// GrammarPath returns the expected path for a grammar library file.
func GrammarPath(language string) (string, error) {
	dir, err := GrammarDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, grammarFileName(language)), nil
}

// GrammarExists checks if a grammar library file exists on disk.
func GrammarExists(language string) bool {
	path, err := GrammarPath(language)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// grammarDownloadURL builds the download URL for a grammar shared library.
// Declared as a variable for testability (can be replaced in tests).
var grammarDownloadURL = func(language string, cfg ParserConfig) string {
	baseURL := cfg.GrammarURL
	if baseURL == "" {
		tag := "grammars-v1"
		if runtime.GOOS == "linux" {
			tag = "grammars-linux-v1"
		}
		baseURL = fmt.Sprintf("https://github.com/cristian-guerrero/go-indexing-mcp/releases/download/%s", tag)
	}
	return fmt.Sprintf("%s/%s", baseURL, grammarFileName(language))
}

// grammarFuncName returns the expected C export name for a language grammar.
func grammarFuncName(language string) string {
	return "tree_sitter_" + strings.ReplaceAll(language, "-", "_")
}

// DownloadGrammar downloads a grammar shared library for the given language
// if it doesn't already exist. Returns the path to the grammar file.
func DownloadGrammar(language string, cfg ParserConfig) (string, error) {
	path, err := GrammarPath(language)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	url := grammarDownloadURL(language, cfg)
	slog.Info("downloading grammar", "language", language, "url", url)

	tmpPath := path + ".tmp"
	if err := downloadFile(url, tmpPath); err != nil {
		return "", fmt.Errorf("download grammar %s: %w", language, err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("rename grammar %s: %w", language, err)
	}

	slog.Info("grammar downloaded", "language", language, "path", path)
	return path, nil
}

// DownloadGrammarIfMissing downloads a grammar only if it doesn't exist.
func DownloadGrammarIfMissing(language string, cfg ParserConfig) (path string, cached bool, err error) {
	path, err = GrammarPath(language)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(path); err == nil {
		return path, true, nil
	}
	path, err = DownloadGrammar(language, cfg)
	return path, false, err
}

// downloadFile is a simple HTTP download helper that writes a URL's content to a file.
func downloadFile(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return nil
}
