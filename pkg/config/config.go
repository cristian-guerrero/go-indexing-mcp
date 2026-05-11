package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

type Config struct {
	Llama    LlamaConfig    `json:"llama"`
	Indexing IndexingConfig `json:"indexing"`
	Storage  StorageConfig  `json:"storage"`
	Embedding EmbeddingConfig `json:"embedding"`
}

type LlamaConfig struct {
	BinPath   string   `json:"bin_path"`
	ModelPath string   `json:"model_path"`
	Port      int      `json:"port"`
	ExtraArgs []string `json:"extra_args"`
}

type IndexingConfig struct {
	RootPath       string   `json:"root_path"`
	IgnorePatterns []string `json:"ignore_patterns"`
	ChunkSize      int      `json:"chunk_size"`
	ChunkOverlap   int      `json:"chunk_overlap"`
	GitEnabled     bool     `json:"git_enabled"`
}

type StorageConfig struct {
	Path string `json:"path"`
}

type EmbeddingConfig struct {
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	BatchSize  int    `json:"batch_size"`
}

func DefaultConfig() *Config {
	mcpDir := McpDir()
	return &Config{
		Llama: LlamaConfig{
			BinPath:   "",
			ModelPath: filepath.Join(mcpDir, "models", "jina-embeddings-v2-base-code-Q5_K_M.gguf"),
			Port:      0,
			ExtraArgs: []string{},
		},
		Indexing: IndexingConfig{
			RootPath:       ".",
			IgnorePatterns: nil,
			ChunkSize:      50,
			ChunkOverlap:   10,
			GitEnabled:     true,
		},
		Storage: StorageConfig{
			Path: filepath.Join(".go-mcp", "vectors.gob"),
		},
		Embedding: EmbeddingConfig{
			Model:      "jina-embeddings-v2-base-code-Q5_K_M",
			Dimensions: 768,
			BatchSize:  8,
		},
	}
}

func McpDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".go-mcp", "indexing")
}

func McpBinDir() string {
	return filepath.Join(McpDir(), "bin")
}

func ConfigPath() string {
	return filepath.Join(McpDir(), "config.json")
}

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

	if cfg.Storage.Path == "" {
		cfg.Storage.Path = def.Storage.Path
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


