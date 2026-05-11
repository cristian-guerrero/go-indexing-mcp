package walker

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cristian/go-indexing-mcp/pkg/ignore"
)

type FileInfo struct {
	Path     string
	RelPath  string
	Hash     string
	Language string
	Size     int64
}

type Walker struct {
	Root      string
	IgnoreMatcher *ignore.Matcher
}

func New(root string, extraIgnores []string) *Walker {
	abs, _ := filepath.Abs(root)
	return &Walker{
		Root:          abs,
		IgnoreMatcher: ignore.New(abs, extraIgnores),
	}
}

func (w *Walker) Walk() ([]FileInfo, error) {
	var files []FileInfo
	err := filepath.Walk(w.Root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if fi.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(w.Root, path)
		if w.IgnoreMatcher.ShouldIgnore(rel) {
			return nil
		}
		lang := detectLanguage(rel)
		if lang == "" {
			return nil
		}
		hash, err := fileHash(path)
		if err != nil {
			return nil
		}
		files = append(files, FileInfo{
			Path:     path,
			RelPath:  rel,
			Hash:     hash,
			Language: lang,
			Size:     fi.Size(),
		})
		return nil
	})
	return files, err
}

func (w *Walker) GetHeadSHA() string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = w.Root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (w *Walker) GetBranch() string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = w.Root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		return ""
	}
	return branch
}

func (w *Walker) GetChangedFiles(sinceSHA string) ([]FileInfo, error) {
	args := []string{"diff", "--name-only"}
	if sinceSHA != "" {
		args = append(args, sinceSHA)
	} else {
		args = append(args, "HEAD")
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = w.Root
	out, err := cmd.Output()
	if err != nil {
		return w.Walk()
	}

	var files []FileInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		if w.IgnoreMatcher.ShouldIgnore(line) {
			continue
		}
		lang := detectLanguage(line)
		if lang == "" {
			continue
		}
		fullPath := filepath.Join(w.Root, line)
		fi, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		hash, _ := fileHash(fullPath)
		files = append(files, FileInfo{
			Path:     fullPath,
			RelPath:  line,
			Hash:     hash,
			Language: lang,
			Size:     fi.Size(),
		})
	}
	return files, nil
}

func detectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".jsx", ".ts", ".tsx":
		return "javascript"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp":
		return "cpp"
	case ".cs":
		return "csharp"
	case ".rb":
		return "ruby"
	case ".php":
		return "php"
	case ".swift":
		return "swift"
	case ".kt", ".kts":
		return "kotlin"
	case ".scala":
		return "scala"
	case ".sql":
		return "sql"
	case ".sh", ".bash":
		return "bash"
	case ".ps1":
		return "powershell"
	case ".md":
		return "markdown"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".toml":
		return "toml"
	case ".html":
		return "html"
	case ".css", ".scss":
		return "css"
	default:
		return ""
	}
}

func fileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:8]), nil
}
