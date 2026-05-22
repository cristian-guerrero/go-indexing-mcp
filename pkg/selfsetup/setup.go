// Package selfsetup handles the first-run setup experience:
// config creation, llama.cpp download, model download, binary self-copy,
// PATH registration, and run script generation.
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

// SetupUI tracks whether the setup is running interactively (terminal attached).
type SetupUI struct {
	Interactive bool
}

// Run performs the entire first-run setup: config, llama.cpp, model, binary copy,
// PATH update, and run script creation. If not interactive, re-launches in a terminal.
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

	fmt.Println()
	fmt.Println("Setup complete!")
	fmt.Println()
	fmt.Println("To configure an agent to use this MCP server:")
	fmt.Println()
	fmt.Println("  go-indexing-mcp --configure opencode")
	fmt.Println("  go-indexing-mcp --configure kilocode")
	fmt.Println("  go-indexing-mcp --configure pi")
	fmt.Println("  go-indexing-mcp --configure claude")
	fmt.Println()
	fmt.Println("Each command adds the MCP server to the agent's config")
	fmt.Println("and writes a global AGENTS.md with mandatory search instructions.")
	fmt.Println()
	fmt.Println("NOTE: You may need to restart your terminal for PATH changes to take effect.")

	return nil
}

// isInteractive checks if stdout is a character device (terminal).
func isInteractive() bool {
	fileInfo, _ := os.Stdout.Stat()
	return (fileInfo.Mode() & os.ModeCharDevice) != 0
}

// relaunchInTerminal re-executes the binary in a new terminal window
// (cmd.exe on Windows, x-terminal-emulator on Linux, Terminal.app on macOS).
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

// copySelf copies the running binary to ~/.go-mcp/indexing/bin/.
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

// addToPATH appends ~/.go-mcp/indexing/bin/ to the PATH:
// - Windows: persists via SetEnvironmentVariable (user-level).
// - Unix: persists to ~/.bashrc, ~/.zshrc, or ~/.profile (whichever exists).
// Also updates the in-process PATH so the current session can find the binary.
func addToPATH() error {
	binDir := config.McpBinDir()

	// Check if already in PATH
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(entry) == filepath.Clean(binDir) {
			return nil
		}
	}

	updateEnv := func() {
		os.Setenv("PATH", os.Getenv("PATH")+string(filepath.ListSeparator)+binDir)
	}

	if runtime.GOOS == "windows" {
		newPath := os.Getenv("PATH") + ";" + binDir
		if err := setWindowsPATH(newPath); err != nil {
			return err
		}
		updateEnv()
		return nil
	}

	// Unix: persist to shell rc file
	home, err := os.UserHomeDir()
	if err != nil {
		updateEnv()
		return nil
	}

	line := "\n# Added by go-indexing-mcp\nexport PATH=\"$PATH:" + binDir + "\"\n"

	// Try rc files in order of preference
	rcCandidates := []string{".bashrc", ".zshrc", ".profile"}
	for _, rc := range rcCandidates {
		rcPath := filepath.Join(home, rc)
		data, err := os.ReadFile(rcPath)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "go-indexing-mcp") || strings.Contains(string(data), binDir) {
			break
		}
		f, err := os.OpenFile(rcPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			continue
		}
		f.WriteString(line)
		f.Close()
		slog.Info("added to PATH in " + rc)
		break
	}

	updateEnv()
	return nil
}

// setWindowsPATH sets the user-level PATH environment variable persistently via PowerShell.
func setWindowsPATH(newPath string) error {
	cmd := exec.Command("powershell", "-Command",
		fmt.Sprintf(`[Environment]::SetEnvironmentVariable("PATH", "%s", "User")`, strings.ReplaceAll(newPath, `"`, "`\"")))
	return cmd.Run()
}


