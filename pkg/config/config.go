package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Llama    LlamaConfig    `yaml:"llama"`
	Indexing IndexingConfig `yaml:"indexing"`
	Storage  StorageConfig  `yaml:"storage"`
	Embedding EmbeddingConfig `yaml:"embedding"`
}

type LlamaConfig struct {
	BinPath   string   `yaml:"bin_path"`
	ModelPath string   `yaml:"model_path"`
	Port      int      `yaml:"port"`
	ExtraArgs []string `yaml:"extra_args"`
}

type IndexingConfig struct {
	RootPath       string   `yaml:"root_path"`
	IgnorePatterns []string `yaml:"ignore_patterns"`
	ChunkSize      int      `yaml:"chunk_size"`
	ChunkOverlap   int      `yaml:"chunk_overlap"`
	GitEnabled     bool     `yaml:"git_enabled"`
}

type StorageConfig struct {
	Path string `yaml:"path"`
}

type EmbeddingConfig struct {
	Model      string `yaml:"model"`
	Dimensions int    `yaml:"dimensions"`
	BatchSize  int    `yaml:"batch_size"`
}

func DefaultConfig() *Config {
	mcpDir := McpDir()
	return &Config{
		Llama: LlamaConfig{
			BinPath:   "",
			ModelPath: filepath.Join(mcpDir, "models", "nomic-embed-text-v1.5.Q4_K_M.gguf"),
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
			Path: filepath.Join(mcpDir, "vectors.gob"),
		},
		Embedding: EmbeddingConfig{
			Model:      "jina-embeddings-v2-base-code",
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
	return filepath.Join(McpDir(), "config.yaml")
}

func Load() (*Config, error) {
	path := ConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			if err := Save(cfg); err != nil {
				return nil, fmt.Errorf("save default config: %w", err)
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

func Save(cfg *Config) error {
	dir := McpDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(ConfigPath(), data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}


