// Package llama manages the llama.cpp server lifecycle: download, start, health check,
// and graceful shutdown. It auto-detects GPU variants (CUDA/Vulkan/CPU) on Windows.
package llama

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
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
	"sync"
	"time"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/config"
)

// Manager handles llama-server download, startup, health-check, and termination.
// Uses a cross-process lock file so multiple MCP processes share one server;
// the last process to release the lock stops the server.
type Manager struct {
	Cfg       *config.Config
	cmd       *exec.Cmd
	logFile   *os.File
	Port      int
	Ready     bool
	ModelPath string
	BinPath   string
	lock      *Lock // Cross-process lock for sharing llama-server across MCPs
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

// llamaReleasesURL is the GitHub API endpoint for the latest llama.cpp release.
const llamaReleasesURL = "https://api.github.com/repos/ggml-org/llama.cpp/releases/latest"

var (
	fetchOnce sync.Once
	cachedTag string
)

// release is a minimal GitHub release response for extracting the tag name.
type release struct {
	TagName string `json:"tag_name"`
}

// fetchLatestTag fetches the latest llama.cpp release tag from GitHub.
// The result is cached so the API is only called once per process lifetime.
func fetchLatestTag() string {
	fetchOnce.Do(func() {
		req, err := http.NewRequest("GET", llamaReleasesURL, nil)
		if err != nil {
			slog.Warn("create latest release request, using fallback tag", "error", err)
			cachedTag = "b9291"
			return
		}
		req.Header.Set("Accept", "application/json")

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			slog.Warn("fetch latest release, using fallback tag", "error", err)
			cachedTag = "b9291"
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			slog.Warn("latest release API returned unexpected status, using fallback tag", "status", resp.Status)
			cachedTag = "b9291"
			return
		}

		var rel release
		if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
			slog.Warn("decode latest release, using fallback tag", "error", err)
			cachedTag = "b9291"
			return
		}

		if rel.TagName == "" {
			slog.Warn("latest release has empty tag, using fallback")
			cachedTag = "b9291"
			return
		}

		cachedTag = rel.TagName
		slog.Info("fetched latest llama.cpp release", "tag", cachedTag)
	})
	return cachedTag
}

// downloadLlama fetches the llama-server binary from GitHub releases.
// The latest release tag is fetched dynamically from the GitHub API.
// On Windows CUDA variants, also downloads the matching CUDA runtime DLLs.
// Linux/macOS releases use .tar.gz, Windows uses .zip.
func (m *Manager) downloadLlama(dest string) error {
	tag := fetchLatestTag()
	variant := llamaVariant()
	extractDir := filepath.Dir(dest)
	arch := fmt.Sprintf("llama-%s-bin-%s", tag, variant)

	var primaryURL string
	isWindows := runtime.GOOS == "windows"
	if isWindows {
		primaryURL = fmt.Sprintf("https://github.com/ggml-org/llama.cpp/releases/download/%s/%s.zip", tag, arch)
	} else {
		primaryURL = fmt.Sprintf("https://github.com/ggml-org/llama.cpp/releases/download/%s/%s.tar.gz", tag, arch)
	}

	if err := downloadArchive(primaryURL, extractDir, isWindows); err != nil {
		return fmt.Errorf("download llama: %w", err)
	}

	// On Windows CUDA, download CUDA runtime DLLs
	if strings.HasPrefix(variant, "win-cuda-") {
		cudaTag := strings.TrimPrefix(variant, "win-cuda-") // "12.4-x64"
		cudaVer := strings.TrimSuffix(cudaTag, "-x64")     // "12.4"
		cudartURL := fmt.Sprintf("https://github.com/ggml-org/llama.cpp/releases/download/%s/cudart-llama-bin-win-cuda-%s-x64.zip", tag, cudaVer)
		slog.Info("downloading CUDA runtime DLLs", "url", cudartURL)
		if err := downloadArchive(cudartURL, extractDir, true); err != nil {
			return fmt.Errorf("download CUDA runtime: %w", err)
		}
	}

	// Relocate files if extracted into a versioned subdirectory (e.g. llama-b9291/)
	// tar.gz releases extract into a versioned directory; Windows zip extracts at root.
	versionedDir := filepath.Join(extractDir, fmt.Sprintf("llama-%s", tag))
	if info, err := os.Stat(versionedDir); err == nil && info.IsDir() {
		entries, err := os.ReadDir(versionedDir)
		if err != nil {
			return fmt.Errorf("read versioned dir: %w", err)
		}
		for _, entry := range entries {
			oldPath := filepath.Join(versionedDir, entry.Name())
			newPath := filepath.Join(extractDir, entry.Name())
			if err := os.Rename(oldPath, newPath); err != nil {
				return fmt.Errorf("relocate %s: %w", entry.Name(), err)
			}
		}
		os.RemoveAll(versionedDir)
		slog.Info("relocated files from versioned directory", "dir", versionedDir)
	}

	// Ensure the binary is executable (zip/tar.gz extraction does not preserve permissions on Linux)
	if _, err := os.Stat(dest); err == nil {
		if err := os.Chmod(dest, 0755); err != nil {
			return fmt.Errorf("chmod llama binary: %w", err)
		}
	}

	slog.Info("llama.cpp downloaded and extracted", "dir", extractDir)
	return nil
}

// downloadArchive downloads an archive (zip or tar.gz) to a temp file and extracts it.
func downloadArchive(url, extractDir string, isZip bool) error {
	slog.Info("downloading", "url", url)
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: %s", resp.Status)
	}

	tmpFile := filepath.Join(extractDir, "llama-download.tmp")
	f, err := os.Create(tmpFile)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmpFile)
		return err
	}
	f.Close()

	if isZip {
		err = ExtractZip(tmpFile, extractDir)
	} else {
		err = ExtractTarGz(tmpFile, extractDir)
	}
	os.Remove(tmpFile)
	if err != nil {
		return err
	}
	return nil
}

// llamaVariant returns the platform-specific release variant string for llama.cpp.
// On Windows, probes for nvidia-smi (CUDA); non-CUDA systems get the Vulkan build
// (llama.cpp falls back to CPU if Vulkan init fails). On Linux, selects the Vulkan
// build when a GPU is detected; otherwise falls back to the CPU-only build.
func llamaVariant() string {
	osName := runtime.GOOS
	arch := runtime.GOARCH

	switch {
	case osName == "windows" && arch == "arm64":
		return "win-cpu-arm64"
	case osName == "windows" && arch == "amd64":
		switch config.DetectVariant() {
		case "cuda":
			slog.Info("nvidia GPU detected, selecting CUDA variant")
			return "win-cuda-12.4-x64"
		default:
			slog.Info("no CUDA detected, selecting Vulkan variant (falls back to CPU if unavailable)")
			return "win-vulkan-x64"
		}
	case osName == "linux" && arch == "amd64":
		switch config.DetectVariant() {
		case "cuda", "vulkan":
			slog.Info("GPU detected on Linux, selecting Vulkan variant (falls back to CPU if unavailable)")
			return "ubuntu-vulkan-x64"
		default:
			return "ubuntu-x64"
		}
	case osName == "linux" && arch == "arm64":
		return "ubuntu-arm64"
	case osName == "darwin" && arch == "arm64":
		return "macos-arm64"
	case osName == "darwin" && arch == "amd64":
		return "macos-x64"
	default:
		return "win-cpu-x64"
	}
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
		Name: "bge-small-en-v1.5-q4_k_m.gguf",
		URL:  "https://huggingface.co/CompendiumLabs/bge-small-en-v1.5-gguf/resolve/main/bge-small-en-v1.5-q4_k_m.gguf",
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

// applyVariantProfile checks if the configured variant matches the detected system variant.
// If they differ (e.g. hardware changed), applies the optimal profile for the detected variant.
// Also migrates old flag names (e.g. --cram → -cram) if the binary version changed.
func (m *Manager) applyVariantProfile() {
	changed := false
	detected := config.DetectVariant()
	if m.Cfg.Llama.Variant != detected {
		slog.Info("hardware variant changed, applying optimal profile",
			"from", m.Cfg.Llama.Variant, "to", detected)
		m.Cfg.ApplyProfile(detected)
		changed = true
	}

	// Migrate old --cram to -cram (b9291 uses single-dash for this flag)
	for i, arg := range m.Cfg.Llama.ExtraArgs {
		if arg == "--cram" {
			m.Cfg.Llama.ExtraArgs[i] = "-cram"
			changed = true
		}
	}

	if changed {
		if err := config.Save(m.Cfg); err != nil {
			slog.Warn("save config after migration", "error", err)
		}
	}
}

// Start launches llama-server as a subprocess with embedding mode.
// Finds a free port, sets up the process, waits until the health check passes (up to 120s).
// Uses a cross-process lock file (~/.go-mcp/llama-server.lock) so multiple MCP processes
// share the same server — only the last process to release the lock stops it.
func (m *Manager) Start() error {
	m.applyVariantProfile()
	m.lock = NewLock()

	// Phase 1: Check lock file for an existing, healthy server.
	lockData, err := m.lock.Acquire()
	if err != nil {
		slog.Warn("lock acquire failed (will start fresh)", "error", err)
	}

	if lockData != nil {
		if m.isRunning(lockData.Port) {
			m.Port = lockData.Port
			m.Ready = true
			if err := m.lock.AddPID(); err != nil {
				slog.Warn("add pid to lock", "error", err)
			}
			slog.Info("attached to existing llama-server via lock", "port", m.Port, "pids", lockData.PIDs)
			return nil
		}
		slog.Warn("lock file exists but server not responding, will start fresh", "port", lockData.Port)
	}

	// Phase 2: Determine port — reuse stale lock port if available.
	port := m.Cfg.Llama.Port
	if port == 0 {
		if lockData != nil && lockData.Port != 0 {
			port = lockData.Port
		} else {
			port = findFreePort(56000, 57000)
		}
	}

	if findProcessByPort(port) > 0 {
		m.Port = port
		slog.Info("waiting for existing llama-server on port", "port", port)
		if err := m.waitReady(10 * time.Second); err != nil {
			slog.Warn("existing llama-server unresponsive, killing and starting fresh", "port", port, "error", err)
			// Kill the stuck process and clear the lock before starting fresh
			if pid := findProcessByPort(port); pid > 0 {
				if proc, err := os.FindProcess(pid); err == nil {
					_ = proc.Kill()
				}
			}
			if m.lock != nil {
				_ = m.lock.ForceClear()
			}
		} else {
			m.Port = port
			m.Ready = true
			m.lock.Start(port)
			slog.Info("attached to existing llama-server", "port", port)
			return nil
		}
	}

	// Phase 3: Start a brand-new server.
	m.Port = port

	args := []string{
		"--port", strconv.Itoa(port),
		"--model", m.ModelPath,
		"--embedding",
		// "--mlock",
		"--batch-size", strconv.Itoa(m.Cfg.Llama.BatchSize),
		"--ubatch-size", strconv.Itoa(m.Cfg.Llama.UBatchSize),
		"--ctx-size", strconv.Itoa(m.Cfg.Llama.CtxSize),
	}
	if m.Cfg.Llama.NGLLayers > 0 {
		args = append(args, "--n-gpu-layers", strconv.Itoa(m.Cfg.Llama.NGLLayers))
	}
	if m.Cfg.Llama.Pooling != "" {
		args = append(args, "--pooling", m.Cfg.Llama.Pooling)
	}
	if m.Cfg.Indexing.IdleTimeoutSecs > 0 {
		args = append(args, "--sleep-idle-seconds", strconv.Itoa(m.Cfg.Indexing.IdleTimeoutSecs))
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

	slog.Info("starting llama-server", "port", port, "model", m.ModelPath, "log", logPath, "cmd", strings.Join(cmd.Args, " "))
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start llama-server: %w", err)
	}

	if err := m.assignChildToJob(cmd); err != nil {
		slog.Debug("assignChildToJob", "error", err)
	}

	if err := m.waitReady(120 * time.Second); err != nil {
		slog.Warn("llama-server may not be ready yet", "error", err)
	}

	m.lock.Start(port)
	m.Ready = true
	slog.Info("llama-server started", "pid", cmd.Process.Pid)
	return nil
}

// StartedProcess returns true if this Manager instance started the process itself
// (as opposed to reusing an already-running llama-server).
func (m *Manager) StartedProcess() bool {
	return m.cmd != nil && m.cmd.Process != nil
}

// IsRunning checks whether llama-server is responding on the active port.
// Uses m.Port first (set by Start), then falls back to configured or default 56000.
func (m *Manager) IsRunning() bool {
	port := m.Port
	if port == 0 {
		port = m.Cfg.Llama.Port
	}
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
			if resp.StatusCode != 503 {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for llama-server on port %d", m.Port)
}

// KillByPort forcefully stops llama-server on the configured port, ignoring the lock.
// Only intended for the --free CLI flag. Normal shutdown should use Stop().
func (m *Manager) KillByPort() error {
	port := m.Cfg.Llama.Port
	if port == 0 {
		port = 56000
	}

	m.Port = port

	if m.lock == nil {
		m.lock = NewLock()
	}
	if err := m.lock.ForceClear(); err != nil {
		slog.Debug("lock force-clear", "error", err)
	}

	m.stopProcess()

	pid := findProcessByPort(port)
	if pid > 0 {
		slog.Info("force-killing llama-server by port", "port", port, "pid", pid)
		proc, err := os.FindProcess(pid)
		if err == nil {
			if err := proc.Kill(); err != nil {
				return fmt.Errorf("kill process %d: %w", pid, err)
			}
		}
	} else {
		slog.Info("no llama-server found on port", "port", port)
	}

	m.Ready = false
	return nil
}

// Stop releases the lock reference. Only terminates the actual server process
// when this was the last MCP process using it. Other MCPs keep their connection alive.
func (m *Manager) Stop() {
	if m.lock != nil {
		shouldStop, err := m.lock.Release()
		if err != nil {
			slog.Warn("lock release failed", "error", err)
		}
		if !shouldStop {
			slog.Debug("llama-server kept alive for other processes", "port", m.Port)
			return
		}
	}

	m.stopProcess()
}

// stopProcess kills the managed llama-server subprocess and cleans up the log file.
// Calls Wait() in background to release the process handle on Windows so the
// binary file is not locked after shutdown.
func (m *Manager) stopProcess() {
	if m.cmd != nil && m.cmd.Process != nil {
		slog.Info("stopping llama-server", "pid", m.cmd.Process.Pid)
		if err := m.cmd.Process.Kill(); err != nil {
			slog.Warn("kill llama-server", "error", err)
		}
		// Wait() releases the process handle so the binary can be deleted on
		// Windows. Call in background to avoid blocking (process is already dead).
		go m.cmd.Process.Wait()
	}
	if m.logFile != nil {
		m.logFile.Close()
		m.logFile = nil
	}
	m.cmd = nil
	m.Ready = false
}

// Restart stops and re-launches llama-server to free stuck memory (e.g. after a large index).
// Only restarts if this manager started the process AND no other MCP is using the server.
func (m *Manager) Restart() error {
	if !m.StartedProcess() {
		slog.Debug("not restarting llama-server: process was not started by us")
		return nil
	}

	if m.lock != nil {
		pids, err := m.lock.Peek()
		if err == nil && len(pids) > 1 {
			slog.Debug("not restarting: other MCPs are using llama-server", "pids", pids)
			return nil
		}
	}

	slog.Info("restarting llama-server to free memory")
	m.Stop()
	m.cmd = nil
	if err := m.Start(); err != nil {
		return fmt.Errorf("restart llama-server: %w", err)
	}
	return nil
}

// ForceRestart kills llama-server unconditionally (bypassing the StartedProcess and lock checks)
// and starts a fresh instance. Used by the indexer to free memory during large indexing operations
// even when the server was started by another process.
func (m *Manager) ForceRestart() error {
	slog.Info("force-restarting llama-server to free memory")

	// Step 1: Kill by cmd.Process if we started it (safe since stopProcess no longer calls Wait())
	m.stopProcess()

	// Step 2: Kill by port as fallback (for when we attached via lock and m.cmd was nil)
	port := m.Port
	if port == 0 {
		port = m.Cfg.Llama.Port
	}
	if port == 0 {
		port = 56000
	}
	pid := findProcessByPort(port)
	if pid > 0 {
		slog.Debug("killing llama-server on port", "port", port, "pid", pid)
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Kill()
		}
	}

	// Clear lock state so Start() starts fresh
	if m.lock != nil {
		_ = m.lock.ForceClear()
	}
	m.cmd = nil
	m.lock = nil
	m.Ready = false

	// Wait for the port to be released
	time.Sleep(2 * time.Second)

	// Start fresh
	if err := m.Start(); err != nil {
		return fmt.Errorf("force-restart llama-server: %w", err)
	}
	return nil
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
// Falls back to the configured port, then to 56000 if not set.
func (m *Manager) BaseURL() string {
	port := m.Port
	if port == 0 {
		port = m.Cfg.Llama.Port
	}
	if port == 0 {
		port = 56000
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
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

// downloadAndExtractZip downloads a ZIP file to a temp file and extracts it.
// Kept for backward compatibility and Windows CUDA runtime DLL extraction.
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

// ExtractTarGz extracts a tar.gz archive to the given destination directory.
func ExtractTarGz(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		fpath := filepath.Join(dest, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(fpath, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
				return err
			}
			out, err := os.Create(fpath)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
				return err
			}
			// Remove existing file/symlink at target, then create symlink
			os.Remove(fpath)
			if err := os.Symlink(header.Linkname, fpath); err != nil {
				return err
			}
		}
	}
	return nil
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
