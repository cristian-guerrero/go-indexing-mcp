package selfsetup

import (
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cristian/go-indexing-mcp/pkg/config"
	"github.com/cristian/go-indexing-mcp/pkg/llama"
)

type SetupUI struct {
	Interactive bool
}

func Run() error {
	ui := &SetupUI{
		Interactive: isInteractive(),
	}

	if !ui.Interactive {
		relaunchInTerminal()
		return nil
	}

	slog.Info("=== go-indexing-mcp setup ===")

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	fmt.Println("✓ Config loaded")

	mgr := llama.New(cfg)

	llamaPath, err := mgr.FindOrDownloadLlama()
	if err != nil {
		return fmt.Errorf("llama setup: %w", err)
	}
	fmt.Printf("✓ llama.cpp: %s\n", llamaPath)

	modelPath, err := mgr.FindOrDownloadModel()
	if err != nil {
		fmt.Printf("⚠ Model download: %s\n", err)
		fmt.Println("  You can download a GGUF embedding model manually and set model_path in config.json")
	} else {
		fmt.Printf("✓ Model: %s\n", modelPath)
	}

	if err := copySelf(); err != nil {
		fmt.Printf("⚠ Copy binary: %s\n", err)
	} else {
		fmt.Println("✓ Binary copied to MCP bin dir")
	}

	treeSitterPath, err := FindOrDownloadTreeSitter(cfg)
	if err != nil {
		fmt.Printf("⚠ Tree-sitter CLI: %s\n", err)
	} else {
		cfg.Indexing.TreeSitterBinPath = treeSitterPath
		if err := config.Save(cfg); err != nil {
			fmt.Printf("⚠ Save config: %s\n", err)
		} else {
			fmt.Printf("✓ Tree-sitter CLI: %s\n", treeSitterPath)
		}
	}

	if err := addToPATH(); err != nil {
		fmt.Printf("⚠ Add to PATH: %s\n", err)
	} else {
		fmt.Println("✓ MCP bin dir added to PATH")
	}

	if err := createRunScript(); err != nil {
		fmt.Printf("⚠ Create run script: %s\n", err)
	} else {
		fmt.Println("✓ Run script created")
	}

	fmt.Println()
	fmt.Println("Setup complete!")
	fmt.Println()
	fmt.Println("To use the MCP server, run:")
	fmt.Println("  " + runScriptPath())
	fmt.Println()
	fmt.Println("Or add to your MCP client config:")
	fmt.Println(`  {
    "command": "` + runScriptPath() + `",
    "args": ["--mcp"]
  }`)
	fmt.Println()
	fmt.Println("NOTE: You may need to restart your terminal for PATH changes to take effect.")

	return nil
}

func FindOrDownloadTreeSitter(cfg *config.Config) (string, error) {
	if cfg.Indexing.TreeSitterBinPath != "" {
		if _, err := os.Stat(cfg.Indexing.TreeSitterBinPath); err == nil {
			return cfg.Indexing.TreeSitterBinPath, nil
		}
		slog.Warn("configured tree-sitter path not found, searching PATH", "path", cfg.Indexing.TreeSitterBinPath)
	}

	name := "tree-sitter"
	if runtime.GOOS == "windows" {
		name = "tree-sitter.exe"
	}
	found, _ := exec.LookPath(name)
	if found != "" {
		slog.Info("tree-sitter found in PATH", "path", found)
		return found, nil
	}

	binDir := config.McpBinDir()
	localPath := filepath.Join(binDir, name)
	if _, err := os.Stat(localPath); err == nil {
		slog.Info("tree-sitter found in MCP bin dir", "path", localPath)
		return localPath, nil
	}

	slog.Info("downloading tree-sitter CLI...")
	if err := downloadTreeSitter(localPath); err != nil {
		return "", fmt.Errorf("download tree-sitter: %w", err)
	}
	slog.Info("tree-sitter downloaded", "path", localPath)
	return localPath, nil
}

func downloadTreeSitter(dest string) error {
	url := treeSitterDownloadURL()
	slog.Info("downloading from", "url", url)

	binDir := filepath.Dir(dest)
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: %s", resp.Status)
	}

	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create tmp file: %w", err)
	}

	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gr.Close()

	if _, err := io.Copy(f, gr); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write file: %w", err)
	}
	f.Close()

	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	if err := os.Chmod(dest, 0755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	return nil
}

func treeSitterDownloadURL() string {
	tag := "v0.26.8"
	goos := runtime.GOOS

	arch := runtime.GOARCH
	var archName string
	switch arch {
	case "amd64":
		archName = "x64"
	case "arm64":
		archName = "arm64"
	default:
		archName = "x64"
	}

	var osName string
	switch goos {
	case "darwin":
		osName = "macos"
	default:
		osName = goos
	}

	return fmt.Sprintf("https://github.com/tree-sitter/tree-sitter/releases/download/%s/tree-sitter-%s-%s.gz", tag, osName, archName)
}

func isInteractive() bool {
	fileInfo, _ := os.Stdout.Stat()
	return (fileInfo.Mode() & os.ModeCharDevice) != 0
}

func relaunchInTerminal() {
	self, _ := os.Executable()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/K", self)
	case "linux":
		cmd = exec.Command("x-terminal-emulator", "-e", self)
		if cmd.Err != nil {
			cmd = exec.Command("xterm", "-e", self)
		}
	case "darwin":
		cmd = exec.Command("open", "-a", "Terminal", self)
	default:
		cmd = exec.Command(self)
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
}

func copySelf() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}

	binDir := config.McpBinDir()
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return err
	}

	name := filepath.Base(self)
	dest := filepath.Join(binDir, name)

	same, _ := filepath.EvalSymlinks(self)
	if same == dest {
		return nil
	}

	data, err := os.ReadFile(self)
	if err != nil {
		return err
	}

	if err := os.WriteFile(dest, data, 0755); err != nil {
		return err
	}

	return nil
}

func addToPATH() error {
	binDir := config.McpBinDir()
	pathEnv := os.Getenv("PATH")
	sep := ";"
	if runtime.GOOS != "windows" {
		sep = ":"
	}

	for _, entry := range filepath.SplitList(pathEnv) {
		if filepath.Clean(entry) == filepath.Clean(binDir) {
			return nil
		}
	}

	newPath := pathEnv + sep + binDir

	if runtime.GOOS == "windows" {
		if err := setWindowsPATH(newPath); err != nil {
			return err
		}
	}

	os.Setenv("PATH", newPath)
	return nil
}

func setWindowsPATH(newPath string) error {
	cmd := exec.Command("powershell", "-Command",
		fmt.Sprintf(`[Environment]::SetEnvironmentVariable("PATH", "%s", "User")`, strings.ReplaceAll(newPath, `"`, "`\"")))
	return cmd.Run()
}

func createRunScript() error {
	binName := "go-indexing-mcp"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	if runtime.GOOS == "windows" {
		script := fmt.Sprintf(`@echo off
"%%~dp0bin\%s" --mcp %%*
`, binName)
		scriptPath := filepath.Join(config.McpDir(), "run.bat")
		return os.WriteFile(scriptPath, []byte(script), 0755)
	}

	script := fmt.Sprintf(`#!/bin/bash
exec "$(dirname "$0")/bin/%s" --mcp "$@"
`, binName)
	scriptPath := filepath.Join(config.McpDir(), "run.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		return err
	}
	return nil
}

func runScriptPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(config.McpDir(), "run.bat")
	}
	return filepath.Join(config.McpDir(), "run.sh")
}
