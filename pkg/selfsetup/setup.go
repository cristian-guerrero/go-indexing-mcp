package selfsetup

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/config"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/llama"
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
	fmt.Println("To configure an agent to use this MCP server:")
	fmt.Println()
	fmt.Println("  go-indexing-mcp --configure opencode")
	fmt.Println("  go-indexing-mcp --configure kilocode")
	fmt.Println("  go-indexing-mcp --configure pi")
	fmt.Println()
	fmt.Println("Each command adds the MCP server to the agent's config")
	fmt.Println("and writes a global AGENTS.md with mandatory search instructions.")
	fmt.Println()
	fmt.Println("NOTE: You may need to restart your terminal for PATH changes to take effect.")

	return nil
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
