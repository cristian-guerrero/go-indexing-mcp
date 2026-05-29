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

func TestStorageDir_EndsWithEncodedProject(t *testing.T) {
	setTempHome(t)
	dir := StorageDir("/test/project")
	absRoot, _ := filepath.Abs("/test/project")
	encoded := EncodeProjectPath(absRoot)
	if !strings.HasSuffix(dir, encoded) {
		t.Errorf("StorageDir %q should end with encoded path %q", dir, encoded)
	}
	if !strings.Contains(filepath.Base(dir), encoded) {
		t.Errorf("StorageDir base %q should be encoded path %q", filepath.Base(dir), encoded)
	}
}

func TestStorageDir_UnderMcpDir(t *testing.T) {
	setTempHome(t)
	dir := StorageDir("/test/project")
	mcpDir := McpDir()
	if !strings.HasPrefix(dir, mcpDir) {
		t.Errorf("StorageDir %q should be under McpDir %q", dir, mcpDir)
	}
}

func TestEncodeFilePath_Windows(t *testing.T) {
	got := EncodeFilePath(`src\main.go`)
	want := `src-main.go.gob`
	if got != want {
		t.Errorf("EncodeFilePath = %q, want %q", got, want)
	}
}

func TestEncodeFilePath_Unix(t *testing.T) {
	got := EncodeFilePath(`src/main.go`)
	want := `src-main.go.gob`
	if got != want {
		t.Errorf("EncodeFilePath = %q, want %q", got, want)
	}
}

func TestEncodeFilePath_WithColon(t *testing.T) {
	got := EncodeFilePath(`C:src/main.go`)
	want := `C-src-main.go.gob`
	if got != want {
		t.Errorf("EncodeFilePath = %q, want %q", got, want)
	}
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
		if cfg.Llama.Variant == "" {
			t.Error("Llama.Variant should not be empty")
		}
		if cfg.Llama.Pooling != "mean" {
			t.Errorf("Llama.Pooling = %q, want \"mean\"", cfg.Llama.Pooling)
		}
		if cfg.Llama.ExtraArgs == nil {
			t.Error("Llama.ExtraArgs should not be nil")
		}
		if cfg.Llama.ModelPath == "" {
			t.Error("Llama.ModelPath should not be empty")
		}
		// Verify parameters match the detected variant profile
		profile, ok := VariantProfiles[cfg.Llama.Variant]
		if !ok {
			t.Fatalf("unknown variant %q", cfg.Llama.Variant)
		}
		if cfg.Llama.NGLLayers != profile.NGLLayers {
			t.Errorf("Llama.NGLLayers = %d, want %d for variant %q", cfg.Llama.NGLLayers, profile.NGLLayers, cfg.Llama.Variant)
		}
		if cfg.Llama.CtxSize != profile.CtxSize {
			t.Errorf("Llama.CtxSize = %d, want %d for variant %q", cfg.Llama.CtxSize, profile.CtxSize, cfg.Llama.Variant)
		}
		if cfg.Llama.BatchSize != profile.BatchSize {
			t.Errorf("Llama.BatchSize = %d, want %d for variant %q", cfg.Llama.BatchSize, profile.BatchSize, cfg.Llama.Variant)
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
		if cfg.Indexing.MemoryFreeInterval != 10000 {
			t.Errorf("Indexing.MemoryFreeInterval = %d, want 10000 (effectively disabled, --cram 0 handles memory)", cfg.Indexing.MemoryFreeInterval)
		}
	})
	t.Run("Embedding defaults", func(t *testing.T) {
		expectedModel := "jina-embeddings-v2-base-code-Q5_K_M"
		expectedDims := 768
		if cfg.Llama.Variant == "avx2" {
			expectedModel = "bge-small-en-v1.5-q4_k_m"
			expectedDims = 384
		}
		if cfg.Embedding.Model != expectedModel {
			t.Errorf("Embedding.Model = %q, want %q for variant %q", cfg.Embedding.Model, expectedModel, cfg.Llama.Variant)
		}
		if cfg.Embedding.Dimensions != expectedDims {
			t.Errorf("Embedding.Dimensions = %d, want %d for variant %q", cfg.Embedding.Dimensions, expectedDims, cfg.Llama.Variant)
		}
		profile, ok := VariantProfiles[cfg.Llama.Variant]
		if !ok {
			t.Fatalf("unknown variant %q", cfg.Llama.Variant)
		}
		if cfg.Embedding.BatchSize != profile.EmbedBatchSize {
			t.Errorf("Embedding.BatchSize = %d, want %d for variant %q", cfg.Embedding.BatchSize, profile.EmbedBatchSize, cfg.Llama.Variant)
		}
	})
}

func TestDefaultConfigForVariant_Cuda(t *testing.T) {
	cfg := DefaultConfigForVariant("cuda")
	if cfg.Llama.NGLLayers != 99 {
		t.Errorf("cuda NGLLayers = %d, want 99", cfg.Llama.NGLLayers)
	}
	if cfg.Llama.BatchSize != 2048 {
		t.Errorf("cuda BatchSize = %d, want 2048", cfg.Llama.BatchSize)
	}
	if cfg.Llama.UBatchSize != 2048 {
		t.Errorf("cuda UBatchSize = %d, want 2048", cfg.Llama.UBatchSize)
	}
	if cfg.Llama.CtxSize != 4096 {
		t.Errorf("cuda CtxSize = %d, want 4096", cfg.Llama.CtxSize)
	}
	if cfg.Llama.Variant != "cuda" {
		t.Errorf("cuda variant = %q, want \"cuda\"", cfg.Llama.Variant)
	}
	if cfg.Embedding.BatchSize != 64 {
		t.Errorf("cuda Embedding.BatchSize = %d, want 64", cfg.Embedding.BatchSize)
	}
}

func TestDefaultConfigForVariant_Vulkan(t *testing.T) {
	cfg := DefaultConfigForVariant("vulkan")
	if cfg.Llama.NGLLayers != 99 {
		t.Errorf("vulkan NGLLayers = %d, want 99", cfg.Llama.NGLLayers)
	}
	if cfg.Llama.BatchSize != 512 {
		t.Errorf("vulkan BatchSize = %d, want 512", cfg.Llama.BatchSize)
	}
	if cfg.Llama.UBatchSize != 2048 {
		t.Errorf("vulkan UBatchSize = %d, want 2048", cfg.Llama.UBatchSize)
	}
	if cfg.Llama.CtxSize != 4096 {
		t.Errorf("vulkan CtxSize = %d, want 4096", cfg.Llama.CtxSize)
	}
	if cfg.Embedding.BatchSize != 48 {
		t.Errorf("vulkan Embedding.BatchSize = %d, want 48", cfg.Embedding.BatchSize)
	}
}

func TestDefaultConfigForVariant_Avx2(t *testing.T) {
	cfg := DefaultConfigForVariant("avx2")
	if cfg.Llama.NGLLayers != 0 {
		t.Errorf("avx2 NGLLayers = %d, want 0", cfg.Llama.NGLLayers)
	}
	if cfg.Llama.BatchSize != 256 {
		t.Errorf("avx2 BatchSize = %d, want 256", cfg.Llama.BatchSize)
	}
	if cfg.Llama.UBatchSize != 1024 {
		t.Errorf("avx2 UBatchSize = %d, want 1024", cfg.Llama.UBatchSize)
	}
	if cfg.Llama.CtxSize != 2048 {
		t.Errorf("avx2 CtxSize = %d, want 2048", cfg.Llama.CtxSize)
	}
	if cfg.Embedding.BatchSize != 16 {
		t.Errorf("avx2 Embedding.BatchSize = %d, want 16", cfg.Embedding.BatchSize)
	}
}

func TestDefaultConfigForVariant_Metal(t *testing.T) {
	cfg := DefaultConfigForVariant("metal")
	if cfg.Llama.NGLLayers != 99 {
		t.Errorf("metal NGLLayers = %d, want 99", cfg.Llama.NGLLayers)
	}
	if cfg.Llama.BatchSize != 1024 {
		t.Errorf("metal BatchSize = %d, want 1024", cfg.Llama.BatchSize)
	}
	if cfg.Llama.UBatchSize != 1024 {
		t.Errorf("metal UBatchSize = %d, want 1024", cfg.Llama.UBatchSize)
	}
	if cfg.Llama.CtxSize != 4096 {
		t.Errorf("metal CtxSize = %d, want 4096", cfg.Llama.CtxSize)
	}
	if cfg.Embedding.BatchSize != 48 {
		t.Errorf("metal Embedding.BatchSize = %d, want 48", cfg.Embedding.BatchSize)
	}
}

func TestDefaultConfigForVariant_Fallback(t *testing.T) {
	cfg := DefaultConfigForVariant("unknown-variant")
	if cfg.Llama.Variant != "cuda" {
		t.Errorf("unknown variant fallback = %q, want \"cuda\"", cfg.Llama.Variant)
	}
}

func TestApplyProfile(t *testing.T) {
	cfg := DefaultConfigForVariant("cuda")
	cfg.ApplyProfile("avx2")
	if cfg.Llama.Variant != "avx2" {
		t.Errorf("ApplyProfile variant = %q, want \"avx2\"", cfg.Llama.Variant)
	}
	if cfg.Llama.NGLLayers != 0 {
		t.Errorf("ApplyProfile NGLLayers = %d, want 0", cfg.Llama.NGLLayers)
	}
	if cfg.Llama.BatchSize != 256 {
		t.Errorf("ApplyProfile BatchSize = %d, want 256", cfg.Llama.BatchSize)
	}
	if cfg.Llama.CtxSize != 2048 {
		t.Errorf("ApplyProfile CtxSize = %d, want 2048", cfg.Llama.CtxSize)
	}
	if cfg.Embedding.BatchSize != 16 {
		t.Errorf("ApplyProfile Embedding.BatchSize = %d, want 16", cfg.Embedding.BatchSize)
	}
}

func TestApplyProfile_UnknownVariant(t *testing.T) {
	cfg := DefaultConfigForVariant("cuda")
	cfg.ApplyProfile("nonexistent")
	if cfg.Llama.Variant != "cuda" {
		t.Errorf("unknown variant should not change variant, got %q", cfg.Llama.Variant)
	}
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
	if cfg.Llama.Variant == "" {
		t.Error("Llama.Variant should not be empty after fillMissing")
	}
	if cfg.Llama.Pooling != "mean" {
		t.Errorf("Llama.Pooling = %q, want \"mean\"", cfg.Llama.Pooling)
	}
	if cfg.Indexing.ChunkSize != 50 {
		t.Errorf("Indexing.ChunkSize = %d, want 50", cfg.Indexing.ChunkSize)
	}
	if cfg.Indexing.ChunkOverlap != 10 {
		t.Errorf("Indexing.ChunkOverlap = %d, want 10", cfg.Indexing.ChunkOverlap)
	}
	expectedDims := 768
	if cfg.Llama.Variant == "avx2" {
		expectedDims = 384
	}
	if cfg.Embedding.Dimensions != expectedDims {
		t.Errorf("Embedding.Dimensions = %d, want %d for variant %q", cfg.Embedding.Dimensions, expectedDims, cfg.Llama.Variant)
	}
	if cfg.Embedding.Model == "" {
		t.Error("Embedding.Model should not be empty after fillMissing")
	}
	if cfg.Llama.ExtraArgs == nil {
		t.Error("ExtraArgs should not be nil after fillMissing")
	}
	// CtxSize, BatchSize, UBatchSize, NGLLayers, Embedding.BatchSize are
	// synced from the variant-specific profile on every config load.
	profile, ok := VariantProfiles[cfg.Llama.Variant]
	if !ok {
		t.Fatalf("unknown variant %q after fillMissing", cfg.Llama.Variant)
	}
	if cfg.Llama.CtxSize != profile.CtxSize {
		t.Errorf("Llama.CtxSize = %d, want %d for variant %q", cfg.Llama.CtxSize, profile.CtxSize, cfg.Llama.Variant)
	}
	if cfg.Llama.BatchSize != profile.BatchSize {
		t.Errorf("Llama.BatchSize = %d, want %d for variant %q", cfg.Llama.BatchSize, profile.BatchSize, cfg.Llama.Variant)
	}
	if cfg.Llama.UBatchSize != profile.UBatchSize {
		t.Errorf("Llama.UBatchSize = %d, want %d for variant %q", cfg.Llama.UBatchSize, profile.UBatchSize, cfg.Llama.Variant)
	}
	if cfg.Embedding.BatchSize != profile.EmbedBatchSize {
		t.Errorf("Embedding.BatchSize = %d, want %d for variant %q", cfg.Embedding.BatchSize, profile.EmbedBatchSize, cfg.Llama.Variant)
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
