// Package ignore implements file filtering using gitignore patterns plus
// a comprehensive set of default ignore patterns for common build artifacts,
// dependencies, and binary files. Supports nested gitignore files at the root level.
package ignore

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	gitignorelib "github.com/sabhiram/go-gitignore"
)

// DefaultPatterns is the built-in ignore list covering common build artifacts,
// dependency directories, binary/library files, images, lock files, and IDE configs.
var DefaultPatterns = []string{
	".git",
	"node_modules",
	"__pycache__",
	".venv",
	"venv",
	".env",
	".next",
	".nuxt",
	"build",
	"dist",
	"target",
	"vendor",
	".idea",
	".vscode",
	"*.exe",
	"*.dll",
	"*.so",
	"*.dylib",
	"*.bin",
	"*.class",
	"*.o",
	"*.obj",
	"*.lib",
	"*.wasm",
	"*.pyc",
	"*.pyo",
	"*.png",
	"*.jpg",
	"*.jpeg",
	"*.gif",
	"*.ico",
	"*.svg",
	"*.woff",
	"*.ttf",
	"*.eot",
	"*.zip",
	"*.tar.gz",
	"*.7z",
	"*.rar",
	"*.pdf",
	"*.min.js",
	"*.min.css",
	"*.lock",
	"package-lock.json",
	"yarn.lock",
	"pnpm-lock.yaml",
	"go.sum",
	"Cargo.lock",
	"Pipfile.lock",
	"poetry.lock",
	".gitkeep",
	".go-mcp",
	".DS_Store",
	"Thumbs.db",
	"*.log",
	".tox",
	".mypy_cache",
	".pytest_cache",
	".gradle",
	".terraform",
}

// Matcher evaluates whether a file path should be ignored based on
// default patterns, user-provided patterns, and .gitignore rules at the project root.
type Matcher struct {
	gitIgnore  *gitignorelib.GitIgnore
	patterns   []string
	root       string
}

// New creates a Matcher with default + extra patterns and loads .gitignore at root.
func New(root string, extraPatterns []string) *Matcher {
	allPatterns := append([]string{}, DefaultPatterns...)
	allPatterns = append(allPatterns, extraPatterns...)

	gi := loadGitIgnore(root)

	return &Matcher{
		gitIgnore: gi,
		patterns:  allPatterns,
		root:      root,
	}
}

// loadGitIgnore reads root/.gitignore and compiles it into a GitIgnore matcher.
func loadGitIgnore(root string) *gitignorelib.GitIgnore {
	path := filepath.Join(root, ".gitignore")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil
	}
	gi := gitignorelib.CompileIgnoreLines(lines...)
	return gi
}

// ShouldIgnore checks a relative path against all patterns — exact match,
// filepath.Match glob, directory prefix, and .gitignore rules. Returns true if ignored.
func (m *Matcher) ShouldIgnore(relPath string) bool {
	if relPath == "" {
		return false
	}
	rel := filepath.ToSlash(relPath)
	for _, p := range m.patterns {
		matched, _ := filepath.Match(p, rel)
		if matched {
			return true
		}
		if strings.HasPrefix(rel, p+"/") || rel == p {
			return true
		}
		if !strings.ContainsAny(p, "/*?[") {
			base := filepath.Base(rel)
			if base == p {
				return true
			}
			if strings.Contains(rel, "/"+p+"/") || strings.HasSuffix(rel, "/"+p) {
				return true
			}
		}
	}
	if m.gitIgnore != nil && m.gitIgnore.MatchesPath(rel) {
		return true
	}
	return false
}
