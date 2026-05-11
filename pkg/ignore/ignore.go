package ignore

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	gitignorelib "github.com/sabhiram/go-gitignore"
)

var DefaultPatterns = []string{
	".git",
	".github",
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

type Matcher struct {
	gitIgnore  *gitignorelib.GitIgnore
	patterns   []string
	root       string
}

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
