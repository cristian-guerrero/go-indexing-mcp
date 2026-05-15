// Package llama manages the llama.cpp server lifecycle: download, start, health check,
// and graceful shutdown. It auto-detects GPU variants (CUDA/Vulkan/CPU) on Windows.
package llama

import (
	"archive/zip"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/config"
)

// Manager handles llama-server download, startup, health-check, and termination.
// On Windows, it uses a Job Object (KILL_ON_JOB_CLOSE) to ensure the child process
// is terminated when the parent exits.
type Manager struct {
	Cfg       *config.Config
	cmd       *exec.Cmd
	logFile   *os.File
	Port      int
	Ready     bool
	ModelPath string
	BinPath   string
	jobHandle uintptr // Windows job object (KILL_ON_JOB_CLOSE), 0 on Unix
}

// New creates a Manager from the given config.
func New(cfg *config.Config) *Manager {
	return &Manager{
		Cfg: cfg,
	}
}

// FindOrDownloadLlama locates llama-server in search order:
// 1. Configured bin_path, 2. PATH, 3. ~/.go-mcp/llama-cpp/, 4. Download from GitHub releases.
func (m *Manager) FindOrDownloadLlama() (string, error) {
	binPath := m.Cfg.Llama.BinPath
	if binPath != "" {
		if _, err := os.Stat(binPath); err == nil {
			m.BinPath = binPath
			slog.Info("llama.cpp found at configured path", "path", binPath)
			return binPath, nil
		}
		slog.Warn("configured llama path not found, searching PATH", "path", binPath)
	}

	name := "llama-server"
	if runtime.GOOS == "windows" {
		name = "llama-server.exe"
	}
	found, _ := exec.LookPath(name)
	if found != "" {
		m.BinPath = found
		m.saveBinPath()
		slog.Info("llama.cpp found in PATH", "path", found)
		return found, nil
	}

	llamaDir := config.LlamaCppDir()
	if err := os.MkdirAll(llamaDir, 0755); err != nil {
		return "", fmt.Errorf("create llama-cpp dir: %w", err)
	}
	localPath := filepath.Join(llamaDir, name)
	if _, err := os.Stat(localPath); err == nil {
		m.BinPath = localPath
		m.saveBinPath()
		slog.Info("llama.cpp found in llama-cpp dir", "path", localPath)
		return localPath, nil
	}

	slog.Info("downloading llama.cpp...")
	if err := m.downloadLlama(localPath); err != nil {
		return "", fmt.Errorf("download llama.cpp: %w", err)
	}
	m.BinPath = localPath
	m.saveBinPath()
	return localPath, nil
}

// saveBinPath persists the discovered llama-server path to config.json.
func (m *Manager) saveBinPath() {
	m.Cfg.Llama.BinPath = m.BinPath
	if err := config.Save(m.Cfg); err != nil {
		slog.Warn("save config with bin_path", "error", err)
	}
}

// downloadLlama fetches the llama-server binary from GitHub releases.
// On Windows CUDA variants, also downloads the matching CUDA runtime DLLs.
func (m *Manager) downloadLlama(dest string) error {
	tag := "b4383"
	variant := llamaVariant()
	extractDir := filepath.Dir(dest)

	primaryURL := fmt.Sprintf("https://github.com/ggml-org/llama.cpp/releases/download/%s/llama-%s-bin-%s.zip", tag, tag, variant)
	if err := downloadAndExtractZip(primaryURL, extractDir); err != nil {
		return fmt.Errorf("download llama: %w", err)
	}

	if strings.HasPrefix(variant, "win-cuda-") {
		cudaVer := strings.TrimPrefix(variant, "win-cuda-")
		cudaVer = strings.TrimSuffix(cudaVer, "-x64")
		cudartURL := fmt.Sprintf("https://github.com/ggml-org/llama.cpp/releases/download/%s/cudart-llama-bin-win-%s-x64.zip", tag, cudaVer)
		slog.Info("downloading CUDA runtime DLLs", "url", cudartURL)
		if err := downloadAndExtractZip(cudartURL, extractDir); err != nil {
			return fmt.Errorf("download CUDA runtime: %w", err)
		}
	}

	slog.Info("llama.cpp downloaded and extracted", "dir", extractDir)
	return nil
}

// downloadAndExtractZip downloads a ZIP file to a temp file and extracts it.
func downloadAndExtractZip(url, extractDir string) error {
	slog.Info("downloading", "url", url)
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: %s", resp.Status)
	}

	tmpZip := filepath.Join(extractDir, "llama-download.tmp")
	f, err := os.Create(tmpZip)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmpZip)
		return err
	}
	f.Close()

	if err := ExtractZip(tmpZip, extractDir); err != nil {
		os.Remove(tmpZip)
		return err
	}
	os.Remove(tmpZip)
	return nil
}

// llamaVariant returns the platform-specific release variant string for llama.cpp.
// On Windows, probes for nvidia-smi (CUDA) or vulkaninfo (Vulkan), falling back to AVX2.
func llamaVariant() string {
	osName := runtime.GOOS
	arch := runtime.GOARCH

	switch {
	case osName == "windows" && arch == "amd64":
		variant := detectWindowsVariant()
		return fmt.Sprintf("win-%s-x64", variant)
	case osName == "windows" && arch == "arm64":
		return "win-llvm-arm64"
	case osName == "linux" && arch == "amd64":
		return "ubuntu-x64.zip"
	case osName == "linux" && arch == "arm64":
		return "ubuntu-arm64.zip"
	case osName == "darwin" && arch == "arm64":
		return "macos-arm64.zip"
	case osName == "darwin" && arch == "amd64":
		return "macos-x64.zip"
	default:
		return "win-avx2-x64.zip"
	}
}

// detectWindowsVariant checks for NVIDIA GPU (nvidia-smi) or Vulkan support,
// returning the appropriate llama.cpp variant: cuda-cu12.4, vulkan, or avx2.
func detectWindowsVariant() string {
	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		slog.Info("nvidia GPU detected, selecting CUDA variant")
		return "cuda-cu12.4"
	}
	if _, err := exec.LookPath("vulkaninfo"); err == nil {
		slog.Info("Vulkan detected, selecting Vulkan variant")
		return "vulkan"
	}
	slog.Info("no GPU detected, selecting CPU AVX2 variant")
	return "avx2"
}

// modelFallbacks is an ordered list of embedding model URLs to try during download.
// Falls through each model if download fails, so a working model is always found.
var modelFallbacks = []struct {
	Name string
	URL  string
}{
	{
		Name: "jina-embeddings-v2-base-code-Q5_K_M.gguf",
		URL:  "https://huggingface.co/second-state/jina-embeddings-v2-base-code-GGUF/resolve/main/jina-embeddings-v2-base-code-Q5_K_M.gguf",
	},
	{
		Name: "nomic-embed-text-v1.5.Q4_K_M.gguf",
		URL:  "https://huggingface.co/nomic-ai/nomic-embed-text-v1.5-GGUF/resolve/main/nomic-embed-text-v1.5.Q4_K_M.gguf",
	},
	{
		Name: "bge-small-en-v1.5.Q4_K_M.gguf",
		URL:  "https://huggingface.co/ChristianAzinn/bge-small-en-v1.5-Q4_K_M-GGUF/resolve/main/bge-small-en-v1.5-q4_k_m.gguf",
	},
}

// FindOrDownloadModel searches for a GGUF embedding model in:
// 1. Configured model_path, 2. ~/.go-mcp/models/embeddings/, 3. Download from HuggingFace.
func (m *Manager) FindOrDownloadModel() (string, error) {
	modelPath := m.Cfg.Llama.ModelPath
	if modelPath != "" {
		expanded := expandPath(modelPath)
		if _, err := os.Stat(expanded); err == nil {
			m.ModelPath = expanded
			slog.Info("model found", "path", expanded)
			return expanded, nil
		}
	}

	modelsDir := config.ModelsDir()
	if err := os.MkdirAll(modelsDir, 0755); err != nil {
		return "", fmt.Errorf("create models dir: %w", err)
	}

	for _, fb := range modelFallbacks {
		localPath := filepath.Join(modelsDir, fb.Name)
		if _, err := os.Stat(localPath); err == nil {
			m.ModelPath = localPath
			slog.Info("model found", "path", localPath)
			return localPath, nil
		}
	}

	for _, fb := range modelFallbacks {
		localPath := filepath.Join(modelsDir, fb.Name)
		slog.Info("downloading embedding model", "name", fb.Name)
		if err := downloadFile(localPath, fb.URL); err != nil {
			slog.Warn("download failed, trying next model", "name", fb.Name, "error", err)
			continue
		}
		m.ModelPath = localPath
		return localPath, nil
	}

	return "", fmt.Errorf("all model downloads failed")
}

// expandPath expands environment variables and ~ to the user's home directory.
func expandPath(p string) string {
	expanded := os.ExpandEnv(p)
	if strings.HasPrefix(expanded, "~") {
		home, _ := os.UserHomeDir()
		expanded = filepath.Join(home, expanded[1:])
	}
	return expanded
}

// downloadFile downloads a URL to a temp file, then atomically renames it to dest.
func downloadFile(dest, url string) error {
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	resp, err := http.Get(url)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("HTTP %s", resp.Status)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()

	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	return nil
}

// Start launches llama-server as a subprocess with embedding mode.
// Finds a free port, sets up the process, waits until the health check passes (up to 120s).
// On Windows, assigns the child to a Job Object for guaranteed cleanup on exit.
func (m *Manager) Start() error {
	port := m.Cfg.Llama.Port
	if port == 0 {
		port = findFreePort(56000, 57000)
	}
	m.Port = port

	if m.isRunning(port) {
		slog.Info("llama-server already running on port", "port", port)
		m.Ready = true
		return nil
	}

	args := []string{
		"--port", strconv.Itoa(port),
		"--model", m.ModelPath,
		"--embedding",
		"--no-webui",
		"--mlock",
		"--batch-size", "2048",
		"--ubatch-size", "2048",
		"--ctx-size", "4096",
	}
	args = append(args, m.Cfg.Llama.ExtraArgs...)

	m.setupJob()

	cmd := exec.Command(m.BinPath, args...)
	setChildDeath(cmd)

	logDir := config.McpDir()
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "llama-server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open llama log: %w", err)
	}

	cmd.Stdout = logFile
	cmd.Stderr = logFile
	m.cmd = cmd
	m.logFile = logFile

	slog.Info("starting llama-server", "port", port, "model", m.ModelPath, "log", logPath)
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start llama-server: %w", err)
	}

	if err := m.assignChildToJob(cmd); err != nil {
		slog.Warn("failed to assign llama-server to job object, child may survive parent death", "error", err)
	}

	if err := m.waitReady(120 * time.Second); err != nil {
		slog.Warn("llama-server may not be ready yet", "error", err)
	}

	m.Ready = true
	slog.Info("llama-server started", "pid", cmd.Process.Pid)
	return nil
}

// StartedProcess returns true if this Manager instance started the process itself
// (as opposed to reusing an already-running llama-server).
func (m *Manager) StartedProcess() bool {
	return m.cmd != nil && m.cmd.Process != nil
}

// IsRunning checks whether llama-server is responding on the configured port.
func (m *Manager) IsRunning() bool {
	port := m.Cfg.Llama.Port
	if port == 0 {
		port = 56000
	}
	return m.isRunning(port)
}

// isRunning checks if llama-server is responding on the given port.
// Returns true if the server responds with HTTP 200 or 400 to an embeddings request.
func (m *Manager) isRunning(port int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/embeddings", port)
	body := `{"input":["test"],"model":"test"}`
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200 || resp.StatusCode == 400
}

// waitReady polls the /v1/embeddings endpoint until the server responds
// successfully or the timeout expires. Polls every 500ms.
func (m *Manager) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 1 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/embeddings", m.Port)
	body := `{"input":["test"]}`
	for time.Now().Before(deadline) {
		req, err := http.NewRequest("POST", url, strings.NewReader(body))
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for llama-server on port %d", m.Port)
}

// KillByPort stops llama-server on the configured port.
// Tries the managed process first, then falls back to finding the process by port via netstat.
func (m *Manager) KillByPort() error {
	port := m.Cfg.Llama.Port
	if port == 0 {
		port = 56000
	}

	if !m.isRunning(port) {
		slog.Info("no llama-server found on port", "port", port)
		return nil
	}

	m.Port = port
	m.Stop()

	if m.isRunning(port) {
		pid := findProcessByPort(port)
		if pid > 0 {
			proc, err := os.FindProcess(pid)
			if err == nil {
				proc.Kill()
			}
		}
	}
	return nil
}

// Stop terminates the managed llama-server process and cleans up the log file.
func (m *Manager) Stop() {
	if m.cmd != nil && m.cmd.Process != nil {
		slog.Info("stopping llama-server", "pid", m.cmd.Process.Pid)
		m.cmd.Process.Kill()
		m.cmd.Wait()
	}
	if m.logFile != nil {
		m.logFile.Close()
		m.logFile = nil
	}
	m.cleanupJob()
	m.Ready = false
}

// findProcessByPort uses `netstat -ano` on Windows to find the PID listening on port.
// Returns 0 if no process found or on non-Windows platforms.
func findProcessByPort(port int) int {
	if runtime.GOOS != "windows" {
		return 0
	}
	cmd := exec.Command("netstat", "-ano")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	target := fmt.Sprintf(":%d", port)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, target) && strings.Contains(line, "LISTENING") {
			parts := strings.Fields(line)
			if len(parts) >= 5 {
				pid, _ := strconv.Atoi(parts[len(parts)-1])
				return pid
			}
		}
	}
	return 0
}

// ForceDownloadLlama downloads llama-server unconditionally (skips PATH check).
// Used by the --download-llama flag.
func (m *Manager) ForceDownloadLlama() (string, error) {
	llamaDir := config.LlamaCppDir()
	if err := os.MkdirAll(llamaDir, 0755); err != nil {
		return "", fmt.Errorf("create llama-cpp dir: %w", err)
	}

	name := "llama-server"
	if runtime.GOOS == "windows" {
		name = "llama-server.exe"
	}
	localPath := filepath.Join(llamaDir, name)

	if err := m.downloadLlama(localPath); err != nil {
		return "", fmt.Errorf("download llama: %w", err)
	}

	m.BinPath = localPath
	m.saveBinPath()
	return localPath, nil
}

// BaseURL returns the full base URL for the llama.cpp API (http://127.0.0.1:{port}).
func (m *Manager) BaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", m.Port)
}

// findFreePort finds the first available TCP port in [min, max] range.
// Used to pick a port if the configured port is busy.
func findFreePort(min, max int) int {
	for port := min; port <= max; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			ln.Close()
			return port
		}
	}
	return min
}

// ExtractZip extracts a ZIP archive to the given destination directory.
// Creates directories as needed. Preserves the original file structure.
func ExtractZip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, 0755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(fpath)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
