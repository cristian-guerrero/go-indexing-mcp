package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func homeEnvVar() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
}

func setTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(homeEnvVar(), dir)
	return dir
}

func TestEncodeProjectPath_Windows(t *testing.T) {
	got := EncodeProjectPath(`C:\project\apps\go-indexing-mcp`)
	want := `--C--project-apps-go-indexing-mcp--`
	if got != want {
		t.Errorf("EncodeProjectPath = %q, want %q", got, want)
	}
}

func TestEncodeProjectPath_Unix(t *testing.T) {
	got := EncodeProjectPath(`/home/user/project`)
	want := `---home-user-project--`
	if got != want {
		t.Errorf("EncodeProjectPath = %q, want %q", got, want)
	}
}

func TestEncodeProjectPath_WithDots(t *testing.T) {
	got := EncodeProjectPath(`C:\Users\cristian\.go-mcp-image-generator`)
	want := `--C--Users-cristian-.go-mcp-image-generator--`
	if got != want {
		t.Errorf("EncodeProjectPath = %q, want %q", got, want)
	}
}

func TestStoragePath_EndsWithVectorsGob(t *testing.T) {
	setTempHome(t)
	path := StoragePath("/test/project")
	if !strings.HasSuffix(path, "vectors.gob") {
		t.Errorf("StoragePath should end with vectors.gob, got %q", path)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("StoragePath should be absolute, got %q", path)
	}
}

func TestStoragePath_UnderVectorsAndEncoded(t *testing.T) {
	setTempHome(t)
	path := StoragePath("/test/project")
	absRoot, _ := filepath.Abs("/test/project")
	encoded := EncodeProjectPath(absRoot)

	if !strings.Contains(path, "vectors") {
		t.Errorf("StoragePath %q should contain 'vectors' directory", path)
	}
	if !strings.Contains(path, encoded) {
		t.Errorf("StoragePath %q should contain encoded path %q", path, encoded)
	}
}

func TestStoragePath_UnderMcpDir(t *testing.T) {
	setTempHome(t)
	path := StoragePath("/test/project")
	mcpDir := McpDir()
	if !strings.HasPrefix(path, mcpDir) {
		t.Errorf("StoragePath %q should be under McpDir %q", path, mcpDir)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg == nil {
		t.Fatal("DefaultConfig returned nil")
	}
	t.Run("Llama defaults", func(t *testing.T) {
		if cfg.Llama.Port != 56000 {
			t.Errorf("Llama.Port = %d, want 56000", cfg.Llama.Port)
		}
		if cfg.Llama.ExtraArgs == nil {
			t.Error("Llama.ExtraArgs should not be nil")
		}
		if cfg.Llama.ModelPath == "" {
			t.Error("Llama.ModelPath should not be empty")
		}
	})
	t.Run("Indexing defaults", func(t *testing.T) {
		if cfg.Indexing.RootPath != "." {
			t.Errorf("Indexing.RootPath = %q, want '.'", cfg.Indexing.RootPath)
		}
		if cfg.Indexing.ChunkSize != 50 {
			t.Errorf("Indexing.ChunkSize = %d, want 50", cfg.Indexing.ChunkSize)
		}
		if cfg.Indexing.ChunkOverlap != 10 {
			t.Errorf("Indexing.ChunkOverlap = %d, want 10", cfg.Indexing.ChunkOverlap)
		}
		if !cfg.Indexing.GitEnabled {
			t.Error("Indexing.GitEnabled should be true")
		}
		if cfg.Indexing.IdleTimeoutSecs != 300 {
			t.Errorf("Indexing.IdleTimeoutSecs = %d, want 300", cfg.Indexing.IdleTimeoutSecs)
		}
	})
	t.Run("Embedding defaults", func(t *testing.T) {
		if cfg.Embedding.Model != "jina-embeddings-v2-base-code-Q5_K_M" {
			t.Errorf("Embedding.Model = %q, want jina-embeddings-v2-base-code-Q5_K_M", cfg.Embedding.Model)
		}
		if cfg.Embedding.Dimensions != 768 {
			t.Errorf("Embedding.Dimensions = %d, want 768", cfg.Embedding.Dimensions)
		}
		if cfg.Embedding.BatchSize != 8 {
			t.Errorf("Embedding.BatchSize = %d, want 8", cfg.Embedding.BatchSize)
		}
	})
}

func TestMcpDir(t *testing.T) {
	home := setTempHome(t)
	got := McpDir()
	want := filepath.Join(home, ".go-mcp", "indexing")
	if got != want {
		t.Errorf("McpDir() = %q, want %q", got, want)
	}
}

func TestModelsDir(t *testing.T) {
	home := setTempHome(t)
	got := ModelsDir()
	want := filepath.Join(home, ".go-mcp", "models", "embeddings")
	if got != want {
		t.Errorf("ModelsDir() = %q, want %q", got, want)
	}
}

func TestMcpBinDir(t *testing.T) {
	setTempHome(t)
	got := McpBinDir()
	if !strings.HasSuffix(got, "bin") {
		t.Errorf("McpBinDir() = %q, should end with 'bin'", got)
	}
}

func TestLlamaCppDir(t *testing.T) {
	home := setTempHome(t)
	got := LlamaCppDir()
	want := filepath.Join(home, ".go-mcp", "llama-cpp")
	if got != want {
		t.Errorf("LlamaCppDir() = %q, want %q", got, want)
	}
}

func TestConfigPath(t *testing.T) {
	setTempHome(t)
	got := ConfigPath()
	if !strings.HasSuffix(got, "config.json") {
		t.Errorf("ConfigPath() = %q, should end with config.json", got)
	}
}

func TestSaveAndLoad_RoundTrip(t *testing.T) {
	setTempHome(t)

	cfg := DefaultConfig()
	cfg.Llama.Port = 9999
	cfg.Indexing.ChunkSize = 100

	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Llama.Port != 9999 {
		t.Errorf("Llama.Port = %d, want 9999", loaded.Llama.Port)
	}
	if loaded.Indexing.ChunkSize != 100 {
		t.Errorf("Indexing.ChunkSize = %d, want 100", loaded.Indexing.ChunkSize)
	}
}

func TestLoad_CreatesDefaultOnMissing(t *testing.T) {
	setTempHome(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Llama.Port != 56000 {
		t.Errorf("expected default port 56000, got %d", cfg.Llama.Port)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	setTempHome(t)

	os.MkdirAll(McpDir(), 0755)
	os.WriteFile(ConfigPath(), []byte("{invalid json}"), 0644)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestFillMissing_AllEmpty(t *testing.T) {
	cfg := &Config{}
	fillMissing(cfg)

	if cfg.Llama.Port != 56000 {
		t.Errorf("Llama.Port = %d, want 56000", cfg.Llama.Port)
	}
	if cfg.Indexing.ChunkSize != 50 {
		t.Errorf("Indexing.ChunkSize = %d, want 50", cfg.Indexing.ChunkSize)
	}
	if cfg.Indexing.ChunkOverlap != 10 {
		t.Errorf("Indexing.ChunkOverlap = %d, want 10", cfg.Indexing.ChunkOverlap)
	}
	if cfg.Embedding.Dimensions != 768 {
		t.Errorf("Embedding.Dimensions = %d, want 768", cfg.Embedding.Dimensions)
	}
	if cfg.Llama.ExtraArgs == nil {
		t.Error("ExtraArgs should not be nil after fillMissing")
	}
}

func TestFillMissing_PreservesExistingValues(t *testing.T) {
	cfg := &Config{
		Llama: LlamaConfig{
			Port: 1234,
		},
		Indexing: IndexingConfig{
			ChunkSize: 99,
		},
	}
	fillMissing(cfg)

	if cfg.Llama.Port != 1234 {
		t.Errorf("Llama.Port should stay 1234, got %d", cfg.Llama.Port)
	}
	if cfg.Indexing.ChunkSize != 99 {
		t.Errorf("Indexing.ChunkSize should stay 99, got %d", cfg.Indexing.ChunkSize)
	}
}

func TestFillMissing_ChunkOverlapComputed(t *testing.T) {
	cfg := &Config{
		Indexing: IndexingConfig{
			ChunkSize:    100,
			ChunkOverlap: 0,
		},
	}
	fillMissing(cfg)
	// ChunkOverlap should be ChunkSize / 5 = 20
	if cfg.Indexing.ChunkOverlap != 20 {
		t.Errorf("ChunkOverlap = %d, want 20 (ChunkSize/5)", cfg.Indexing.ChunkOverlap)
	}
}

func TestFillMissing_WatchIntervalDefaults(t *testing.T) {
	cfg := &Config{
		Indexing: IndexingConfig{
			WatchIntervalSecs: 0,
		},
	}
	fillMissing(cfg)
	if cfg.Indexing.WatchIntervalSecs != 60 {
		t.Errorf("WatchIntervalSecs = %d, want 60", cfg.Indexing.WatchIntervalSecs)
	}
	if !cfg.Indexing.WatchEnabled {
		t.Error("WatchEnabled should be true after fillMissing")
	}
}

func TestFillMissing_DefaultBinPath(t *testing.T) {
	cfg := &Config{}
	fillMissing(cfg)
	if cfg.Llama.BinPath != "" {
		t.Errorf("default BinPath should be empty, got %q", cfg.Llama.BinPath)
	}
}
