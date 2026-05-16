// Package config manages the persistent configuration stored at
// ~/.go-mcp/indexing/config.json. It is loaded on startup and provides
// path helpers for all MCP data directories.
package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Config is the root configuration structure for go-indexing-mcp.
type Config struct {
	Llama     LlamaConfig     `json:"llama"`
	Indexing  IndexingConfig  `json:"indexing"`
	Storage   StorageConfig   `json:"storage"`
	Embedding EmbeddingConfig `json:"embedding"`
}

// LlamaConfig controls the llama-server binary path, model, port, GPU layers, context size,
// batch size, pooling mode, and extra arguments.
type LlamaConfig struct {
	BinPath    string   `json:"bin_path"`
	ModelPath  string   `json:"model_path"`
	Port       int      `json:"port"`
	NGLLayers  int      `json:"ngl_layers"`
	CtxSize    int      `json:"ctx_size"`
	BatchSize  int      `json:"batch_size"`
	UBatchSize int      `json:"ubatch_size"`
	Pooling    string   `json:"pooling"`
	ExtraArgs  []string `json:"extra_args"`
}

// IndexingConfig controls file walking, chunking, git integration, idle timeout, and watch intervals.
type IndexingConfig struct {
	RootPath          string   `json:"root_path"`
	IgnorePatterns    []string `json:"ignore_patterns"`
	ChunkSize         int      `json:"chunk_size"`
	ChunkOverlap      int      `json:"chunk_overlap"`
	GitEnabled        bool     `json:"git_enabled"`
	IdleTimeoutSecs   int      `json:"idle_timeout_secs"`
	WatchEnabled      bool     `json:"watch_enabled"`
	WatchIntervalSecs int      `json:"watch_interval_secs"`
}

// StorageConfig is reserved for future storage settings.
type StorageConfig struct{}

// EmbeddingConfig controls the embedding model name, vector dimensions, and batch size.
type EmbeddingConfig struct {
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	BatchSize  int    `json:"batch_size"`
}

// DefaultConfig returns a Config with sensible default values.
// ChunkSize: 50 lines, Overlap: 10, Port: 56000, CtxSize: 4096, BatchSize: 2048,
// UBatchSize: 2048, Dimensions: 768 (jina-embeddings-v2).
func DefaultConfig() *Config {
	return &Config{
		Llama: LlamaConfig{
			BinPath:    "",
			ModelPath:  filepath.Join(ModelsDir(), "jina-embeddings-v2-base-code-Q5_K_M.gguf"),
			Port:       56000,
			NGLLayers:  99,
			CtxSize:    4096,
			BatchSize:  2048,
			UBatchSize: 2048,
			Pooling:    "mean",
			ExtraArgs:  []string{"--no-webui", "--no-mmap"},
		},
		Indexing: IndexingConfig{
			RootPath:          ".",
			IgnorePatterns:    nil,
			ChunkSize:         50,
			ChunkOverlap:      10,
			GitEnabled:        true,
			IdleTimeoutSecs:   300,
			WatchEnabled:      true,
			WatchIntervalSecs: 60,
		},
		Embedding: EmbeddingConfig{
			Model:      "jina-embeddings-v2-base-code-Q5_K_M",
			Dimensions: 768,
			BatchSize:  8,
		},
	}
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

// StoragePath returns the full path to the GOB index file for the given project root.
// Stored under ~/.go-mcp/indexing/vectors/{encoded-project-root}/vectors.gob
func StoragePath(rootPath string) string {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		absRoot = rootPath
	}
	encoded := EncodeProjectPath(absRoot)
	return filepath.Join(McpDir(), "vectors", encoded, "vectors.gob")
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
		cfg.Llama.ExtraArgs = []string{}
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
		cfg.Indexing.IdleTimeoutSecs = 300
	}
	if cfg.Indexing.WatchIntervalSecs == 0 {
		cfg.Indexing.WatchIntervalSecs = 60
		cfg.Indexing.WatchEnabled = true
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
