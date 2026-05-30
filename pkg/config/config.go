// Package config manages the persistent configuration stored at
// ~/.go-mcp/indexing/config.json. It is loaded on startup and provides
// path helpers for all MCP data directories.
package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Config is the root configuration structure for go-indexing-mcp.
type Config struct {
	Llama     LlamaConfig     `json:"llama"`
	Indexing  IndexingConfig  `json:"indexing"`
	Storage   StorageConfig   `json:"storage"`
	Embedding EmbeddingConfig `json:"embedding"`
}

// LlamaConfig controls the llama-server binary path, model, port, GPU variant, GPU layers,
// context size, batch size, pooling mode, and extra arguments.
type LlamaConfig struct {
	BinPath    string   `json:"bin_path"`
	ModelPath  string   `json:"model_path"`
	Port       int      `json:"port"`
	Variant    string   `json:"variant"`
	NGLLayers  int      `json:"ngl_layers"`
	CtxSize    int      `json:"ctx_size"`
	BatchSize  int      `json:"batch_size"`
	UBatchSize int      `json:"ubatch_size"`
	Pooling    string   `json:"pooling"`
	ExtraArgs  []string `json:"extra_args"`
}

// IndexingConfig controls file walking, chunking, git integration, model sleep timeout
// (--sleep-idle-seconds), watch intervals, periodic memory freeing during indexing,
// and automatic updates.
type IndexingConfig struct {
	RootPath           string   `json:"root_path"`
	IgnorePatterns     []string `json:"ignore_patterns"`
	ChunkSize          int      `json:"chunk_size"`
	ChunkOverlap       int      `json:"chunk_overlap"`
	GitEnabled         bool     `json:"git_enabled"`
	IdleTimeoutSecs    int      `json:"idle_timeout_secs"`
	WatchEnabled       bool     `json:"watch_enabled"`
	WatchIntervalSecs  int      `json:"watch_interval_secs"`
	MemoryFreeInterval int      `json:"memory_free_interval"`
	MaxMemoryMB        int      `json:"max_memory_mb"`  // 0 = disabled; llama-server memory threshold in MB
	AutoUpdate         bool     `json:"auto_update"`    // check and download updates on startup
}

// StorageConfig is reserved for future storage settings.
type StorageConfig struct{}

// EmbeddingConfig controls the embedding model name, vector dimensions, batch size,
// and optional prefix prepended to query text for models that use task-specific prompts.
type EmbeddingConfig struct {
	Model       string `json:"model"`
	Dimensions  int    `json:"dimensions"`
	BatchSize   int    `json:"batch_size"`
	QueryPrefix string `json:"query_prefix"`
}

// LlamaProfile holds the optimal llama-server parameters for a given hardware variant.
// Used by VariantProfiles to select defaults when the variant is detected.
type LlamaProfile struct {
	NGLLayers      int
	CtxSize        int
	BatchSize      int
	UBatchSize     int
	Pooling        string
	ExtraArgs      []string
	EmbedBatchSize int
}

// VariantProfiles maps hardware variant names to optimal llama-server parameters.
// Key: "cuda", "vulkan", "avx2", "metal".
//
// -cram 0 disables llama.cpp's host-memory KV cache (PR #16391).
// For embedding models each request is independent, so the cache wastes RAM
// with no benefit. Without this flag, llama-server leaks ~3GB+ during indexing.
// Note: --cram (double dash) was renamed to -cram in b9291.
var VariantProfiles = map[string]LlamaProfile{
	"cuda": {
		NGLLayers:      99,
		CtxSize:        4096,
		BatchSize:      2048,
		UBatchSize:     2048,
		Pooling:        "mean",
		ExtraArgs:      []string{"--no-webui", "-fa", "on", "-cram", "0"},
		EmbedBatchSize: 64,
	},
	"vulkan": {
		NGLLayers:      99,
		CtxSize:        4096,
		BatchSize:      512,
		UBatchSize:     2048,
		Pooling:        "mean",
		ExtraArgs:      []string{"--no-webui", "-fa", "on", "-cram", "0"},
		EmbedBatchSize: 48,
	},
	"avx2": {
		NGLLayers:      0,
		CtxSize:        2048,
		BatchSize:      256,
		UBatchSize:     1024,
		Pooling:        "mean",
		ExtraArgs:      []string{"--no-webui", "--mlock", "-cram", "0"},
		EmbedBatchSize: 16,
	},
	"metal": {
		NGLLayers:      99,
		CtxSize:        4096,
		BatchSize:      1024,
		UBatchSize:     1024,
		Pooling:        "mean",
		ExtraArgs:      []string{"--no-webui", "--no-mmap", "-cram", "0"},
		EmbedBatchSize: 48,
	},
}

// DetectVariant probes the system and returns the hardware variant name:
// "cuda", "vulkan", "avx2", "metal", or "cpu".
// GPU detection uses nvidia-smi (CUDA) and platform-specific methods:
//   - Windows: WMI Win32_VideoController
//   - Linux: /sys/class/drm
// Falls back to "avx2" (CPU) when no GPU is found.
// Note: llama.cpp Vulkan builds fall back to CPU at runtime, so the download
// variant selection (llamaVariant) always prefers the Vulkan build over AVX2.
func DetectVariant() string {
	switch runtime.GOOS {
	case "darwin":
		return "metal"
	case "windows", "linux":
		if nvidiaSMIExists() {
			return "cuda"
		}
		if hasGPU() {
			return "vulkan"
		}
		return "avx2"
	default:
		return "cpu"
	}
}

func nvidiaSMIExists() bool {
	_, err := exec.LookPath("nvidia-smi")
	return err == nil
}

// DefaultConfig returns a Config with sensible default values detected from the system.
// The llama-server parameters are selected based on the detected hardware variant.
// ChunkSize: 50 lines, Overlap: 10, Port: 56000.
// Model: jina-embeddings-v2-base-code (768d) for GPU, bge-small-en-v1.5 (384d) for CPU.
func DefaultConfig() *Config {
	return DefaultConfigForVariant(DetectVariant())
}

// DefaultConfigForVariant returns a Config with defaults optimized for the given variant.
// Supported variants: "cuda", "vulkan", "avx2", "metal". Falls back to "cuda" for unknown variants.
// For CPU-only ("avx2") uses the smaller bge-small-en-v1.5 (384d) model.
// For GPU variants uses jina-embeddings-v2-base-code (768d).
func DefaultConfigForVariant(variant string) *Config {
	profile, ok := VariantProfiles[variant]
	if !ok {
		variant = "cuda"
		profile = VariantProfiles["cuda"]
	}

	modelName := "jina-embeddings-v2-base-code-Q5_K_M"
	modelFile := modelName + ".gguf"
	dimensions := 768
	if variant == "avx2" {
		modelName = "bge-small-en-v1.5-q4_k_m"
		modelFile = modelName + ".gguf"
		dimensions = 384
	}

	return &Config{
		Llama: LlamaConfig{
			BinPath:    "",
			ModelPath:  filepath.Join(ModelsDir(), modelFile),
			Port:       56000,
			Variant:    variant,
			NGLLayers:  profile.NGLLayers,
			CtxSize:    profile.CtxSize,
			BatchSize:  profile.BatchSize,
			UBatchSize: profile.UBatchSize,
			Pooling:    profile.Pooling,
			ExtraArgs:  profile.ExtraArgs,
		},
		Indexing: IndexingConfig{
			RootPath:           ".",
			IgnorePatterns:     nil,
			ChunkSize:          50,
			ChunkOverlap:       10,
			GitEnabled:         true,
			IdleTimeoutSecs:    300,
			WatchEnabled:       true,
			WatchIntervalSecs:  60,
			MemoryFreeInterval: 10000, // effectively disabled; -cram 0 handles memory
			MaxMemoryMB:        0,
			AutoUpdate:         true,
		},
		Embedding: EmbeddingConfig{
			Model:       modelName,
			Dimensions:  dimensions,
			BatchSize:   profile.EmbedBatchSize,
			QueryPrefix: "",
		},
	}
}

// ApplyProfile overwrites the llama configuration with the profile values for the given variant.
// Used when the system variant changes (e.g. GPU removed) to keep config in sync.
func (c *Config) ApplyProfile(variant string) {
	profile, ok := VariantProfiles[variant]
	if !ok {
		return
	}
	c.Llama.Variant = variant
	c.Llama.NGLLayers = profile.NGLLayers
	c.Llama.CtxSize = profile.CtxSize
	c.Llama.BatchSize = profile.BatchSize
	c.Llama.UBatchSize = profile.UBatchSize
	c.Llama.Pooling = profile.Pooling
	c.Llama.ExtraArgs = profile.ExtraArgs
	c.Embedding.BatchSize = profile.EmbedBatchSize
}

// McpDir returns ~/.go-mcp/indexing/, the root directory for all MCP data.
func McpDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".go-mcp", "indexing")
}

// ModelsDir returns ~/.go-mcp/models/embeddings/, where GGUF embedding models are stored.
func ModelsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".go-mcp", "models", "embeddings")
}

// EncodeProjectPath encodes an absolute path into a filesystem-safe folder name.
// Windows: C:\project\apps\go-indexing-mcp → --C--project-apps-go-indexing-mcp--
// Unix:    /home/user/project              → ---home-user-project--
func EncodeProjectPath(absPath string) string {
	s := strings.ReplaceAll(absPath, ":", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	s = strings.ReplaceAll(s, "/", "-")
	return "--" + s + "--"
}

// StorageDir returns the base directory for per-file index storage.
// Stored under ~/.go-mcp/indexing/vectors/{encoded-project-root}/
func StorageDir(rootPath string) string {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		absRoot = rootPath
	}
	encoded := EncodeProjectPath(absRoot)
	return filepath.Join(McpDir(), "vectors", encoded)
}

// StoragePath returns the full path to the SQLite index file for the given project root.
func StoragePath(rootPath string) string {
	return StorageDbPath(rootPath)
}

// StorageDbPath returns the full path to the SQLite index file for the given project root.
// Uses a single .sqlite file per project (branch isolation done via filename suffix).
func StorageDbPath(rootPath string) string {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		absRoot = rootPath
	}
	encoded := EncodeProjectPath(absRoot)
	return filepath.Join(McpDir(), "vectors", encoded, "index.sqlite")
}

// EncodeFilePath encodes a relative file path into a filesystem-safe filename.
// "src/main.go" → "src-main.go.gob"
func EncodeFilePath(relPath string) string {
	s := strings.ReplaceAll(relPath, "\\", "-")
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, ":", "-")
	return s + ".gob"
}

// McpBinDir returns ~/.go-mcp/indexing/bin/, where the self-copied binary lives.
func McpBinDir() string {
	return filepath.Join(McpDir(), "bin")
}

// LlamaCppDir returns ~/.go-mcp/llama-cpp/, where llama-server binaries are downloaded.
func LlamaCppDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".go-mcp", "llama-cpp")
}

// ConfigPath returns the full path to the config.json file.
func ConfigPath() string {
	return filepath.Join(McpDir(), "config.json")
}

// Load reads config from ConfigPath(), creating a default one if it doesn't exist.
// Missing fields are filled from DefaultConfig() to ensure forward compatibility.
func Load() (*Config, error) {
	path := ConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Info("config not found, creating default")
			cfg := DefaultConfig()
			if err := Save(cfg); err != nil {
				return nil, fmt.Errorf("save default config: %w", err)
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	fillMissing(cfg)
	if err := Save(cfg); err != nil {
		slog.Warn("save config after merge", "error", err)
	}
	return cfg, nil
}

// cleanExtraArgs validates and deduplicates ExtraArgs for the given variant.
// If the variant has a known profile, returns the profile's args (authoritative).
// Otherwise returns args with consecutive duplicate values deduplicated.
func cleanExtraArgs(args []string, variant string, def *Config) []string {
	if profile, ok := VariantProfiles[variant]; ok {
		return profile.ExtraArgs
	}
	if len(args) < 3 {
		return args
	}
	// Deduplicate consecutive duplicate values (e.g. "-cram", "0", "0" → "-cram", "0")
	result := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if i > 0 && i < len(args)-1 && args[i] == args[i+1] && !strings.HasPrefix(args[i], "-") {
			continue
		}
		result = append(result, args[i])
	}
	return result
}

// fillMissing merges default values into any zero/empty config fields.
// This ensures forward compatibility when new fields are added.
func fillMissing(cfg *Config) {
	def := DefaultConfig()

	if cfg.Llama.BinPath == "" {
		cfg.Llama.BinPath = def.Llama.BinPath
	} else {
		if _, err := os.Stat(cfg.Llama.BinPath); os.IsNotExist(err) {
			slog.Warn("llama bin_path not found, will search PATH", "path", cfg.Llama.BinPath)
		}
	}

	if cfg.Llama.ModelPath == "" {
		cfg.Llama.ModelPath = def.Llama.ModelPath
	} else {
		expanded := os.ExpandEnv(cfg.Llama.ModelPath)
		if _, err := os.Stat(expanded); os.IsNotExist(err) {
			slog.Warn("model_path not found, will search defaults or download", "path", cfg.Llama.ModelPath)
		}
	}

	if cfg.Llama.Port == 0 {
		cfg.Llama.Port = def.Llama.Port
	}

	if cfg.Llama.Variant == "" {
		cfg.Llama.Variant = def.Llama.Variant
	}
	if cfg.Llama.Pooling == "" {
		cfg.Llama.Pooling = def.Llama.Pooling
	}
	if cfg.Llama.CtxSize == 0 {
		cfg.Llama.CtxSize = def.Llama.CtxSize
	}
	if cfg.Llama.BatchSize == 0 {
		cfg.Llama.BatchSize = def.Llama.BatchSize
	}
	if cfg.Llama.UBatchSize == 0 {
		cfg.Llama.UBatchSize = def.Llama.UBatchSize
	}
	if cfg.Llama.ExtraArgs == nil {
		cfg.Llama.ExtraArgs = def.Llama.ExtraArgs
	} else {
		cfg.Llama.ExtraArgs = cleanExtraArgs(cfg.Llama.ExtraArgs, cfg.Llama.Variant, def)
	}

	// Sync profile values for known variants on every load. This ensures auto-updates
	// propagate tuning changes (UBatchSize, BatchSize, etc.) to existing configs.
	if profile, ok := VariantProfiles[cfg.Llama.Variant]; ok {
		cfg.Llama.NGLLayers = profile.NGLLayers
		cfg.Llama.CtxSize = profile.CtxSize
		cfg.Llama.BatchSize = profile.BatchSize
		cfg.Llama.UBatchSize = profile.UBatchSize
		cfg.Embedding.BatchSize = profile.EmbedBatchSize
	}

	if cfg.Indexing.RootPath == "" {
		cfg.Indexing.RootPath = def.Indexing.RootPath
	}
	if cfg.Indexing.ChunkSize == 0 {
		cfg.Indexing.ChunkSize = def.Indexing.ChunkSize
	}
	if cfg.Indexing.ChunkOverlap == 0 && cfg.Indexing.ChunkSize != 0 {
		cfg.Indexing.ChunkOverlap = cfg.Indexing.ChunkSize / 5
	}
	if cfg.Indexing.IdleTimeoutSecs == 0 {
		cfg.Indexing.IdleTimeoutSecs = def.Indexing.IdleTimeoutSecs
	}
	if cfg.Indexing.WatchIntervalSecs == 0 {
		cfg.Indexing.WatchIntervalSecs = 60
		cfg.Indexing.WatchEnabled = true
	}
	if cfg.Indexing.MemoryFreeInterval == 0 {
		cfg.Indexing.MemoryFreeInterval = 100
	}

	if cfg.Embedding.Dimensions == 0 {
		cfg.Embedding.Dimensions = def.Embedding.Dimensions
	}
	if cfg.Embedding.BatchSize == 0 {
		cfg.Embedding.BatchSize = def.Embedding.BatchSize
	}
	if cfg.Embedding.Model == "" {
		cfg.Embedding.Model = def.Embedding.Model
	}

}

// Save writes the config to ConfigPath() as indented JSON, creating the directory if needed.
func Save(cfg *Config) error {
	dir := McpDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(ConfigPath(), data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
