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

	"github.com/cristian/go-indexing-mcp/pkg/config"
)

type Manager struct {
	Cfg       *config.Config
	cmd       *exec.Cmd
	logFile   *os.File
	Port      int
	Ready     bool
	ModelPath string
	BinPath   string
}

func New(cfg *config.Config) *Manager {
	return &Manager{
		Cfg: cfg,
	}
}

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

func (m *Manager) saveBinPath() {
	m.Cfg.Llama.BinPath = m.BinPath
	if err := config.Save(m.Cfg); err != nil {
		slog.Warn("save config with bin_path", "error", err)
	}
}

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

	modelsDir := filepath.Join(config.McpDir(), "models")
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

func expandPath(p string) string {
	expanded := os.ExpandEnv(p)
	if strings.HasPrefix(expanded, "~") {
		home, _ := os.UserHomeDir()
		expanded = filepath.Join(home, expanded[1:])
	}
	return expanded
}

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

	cmd := exec.Command(m.BinPath, args...)

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

	if err := m.waitReady(120 * time.Second); err != nil {
		slog.Warn("llama-server may not be ready yet", "error", err)
	}

	m.Ready = true
	slog.Info("llama-server started", "pid", cmd.Process.Pid)
	return nil
}

func (m *Manager) StartedProcess() bool {
	return m.cmd != nil && m.cmd.Process != nil
}

func (m *Manager) IsRunning() bool {
	port := m.Cfg.Llama.Port
	if port == 0 {
		port = 56000
	}
	return m.isRunning(port)
}

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
	m.Ready = false
}

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

func (m *Manager) BaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", m.Port)
}

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
