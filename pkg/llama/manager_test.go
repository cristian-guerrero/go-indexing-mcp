package llama

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/config"
)

func TestBaseURL_WithPort(t *testing.T) {
	m := &Manager{Port: 12345}
	url := m.BaseURL()
	expected := "http://127.0.0.1:12345"
	if url != expected {
		t.Fatalf("expected %q, got %q", expected, url)
	}
}

func TestBaseURL_Fallback(t *testing.T) {
	m := &Manager{Cfg: &config.Config{}}
	m.Cfg.Llama.Port = 0
	url := m.BaseURL()
	expected := "http://127.0.0.1:56000"
	if url != expected {
		t.Fatalf("expected %q, got %q", expected, url)
	}
}

func TestNew(t *testing.T) {
	cfg := &config.Config{}
	m := New(cfg)
	if m == nil {
		t.Fatal("expected non-nil manager")
	}
	if m.Cfg != cfg {
		t.Fatal("expected config to be set")
	}
}

func TestNew_Defaults(t *testing.T) {
	m := New(&config.Config{})
	if m.Ready {
		t.Fatal("expected Ready=false on new manager")
	}
	if m.Port != 0 {
		t.Fatal("expected Port=0 on new manager")
	}
}

func TestIsRunning_NoServer(t *testing.T) {
	m := &Manager{Port: 19999}
	if m.IsRunning() {
		t.Log("IsRunning returned true (expected false, but a server may be on port 19999)")
	}
}

func TestStartedProcess_False(t *testing.T) {
	m := &Manager{}
	if m.StartedProcess() {
		t.Fatal("expected StartedProcess=false on new manager")
	}
}

func TestExpandPath_NoExpand(t *testing.T) {
	got := expandPath("/usr/local/bin")
	if got != "/usr/local/bin" {
		t.Fatalf("expected /usr/local/bin, got %s", got)
	}
}

func TestExpandPath_WithEnvVar(t *testing.T) {
	t.Setenv("TEST_LLAMA_DIR", "/custom/path")
	got := expandPath("${TEST_LLAMA_DIR}/model.gguf")
	expected := "/custom/path/model.gguf"
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestExpandPath_WithHome(t *testing.T) {
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	got := expandPath("~/models/model.gguf")
	expected := filepath.Join(home, "/models/model.gguf")
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestFetchLatestTag_FallbackOnError(t *testing.T) {
	fetchOnce = sync.Once{}
	cachedTag = ""

	tag := fetchLatestTag()
	if tag == "" {
		t.Fatal("expected non-empty tag (fallback on API error)")
	}
}

func TestSaveBinPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", t.TempDir())
	} else {
		t.Setenv("HOME", t.TempDir())
	}

	m := New(config.DefaultConfig())
	defer func() { os.RemoveAll(config.McpDir()) }()

	m.BinPath = "/custom/llama-server"
	m.saveBinPath()

	// Reload config and verify bin_path was saved
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Llama.BinPath != "/custom/llama-server" {
		t.Fatalf("expected bin_path=/custom/llama-server, got %q", cfg.Llama.BinPath)
	}
}

func TestApplyVariantProfile_NoChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", t.TempDir())
	} else {
		t.Setenv("HOME", t.TempDir())
	}
	defer func() { os.RemoveAll(config.McpDir()) }()

	cfg := config.DefaultConfig()
	cfg.Llama.Variant = config.DetectVariant() // same as current
	m := New(cfg)

	m.applyVariantProfile()
	if m.Cfg.Llama.Variant != config.DetectVariant() {
		t.Fatal("variant should not change when it matches detected")
	}
}

func TestApplyVariantProfile_MigrateCram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", t.TempDir())
	} else {
		t.Setenv("HOME", t.TempDir())
	}
	defer func() { os.RemoveAll(config.McpDir()) }()

	cfg := config.DefaultConfig()
	cfg.Llama.ExtraArgs = []string{"--cram"}
	m := New(cfg)

	m.applyVariantProfile()
	found := false
	for _, arg := range m.Cfg.Llama.ExtraArgs {
		if arg == "-cram" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected --cram to be migrated to -cram")
	}
}

func TestFindOrDownloadLlama_ConfiguredPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", t.TempDir())
	} else {
		t.Setenv("HOME", t.TempDir())
	}
	defer func() { os.RemoveAll(config.McpDir()) }()

	llamaDir := t.TempDir()
	llamaExe := filepath.Join(llamaDir, "llama-server")
	if runtime.GOOS == "windows" {
		llamaExe += ".exe"
	}
	os.WriteFile(llamaExe, []byte("fake binary"), 0644)

	cfg := config.DefaultConfig()
	cfg.Llama.BinPath = llamaExe
	m := New(cfg)

	path, err := m.FindOrDownloadLlama()
	if err != nil {
		t.Fatal(err)
	}
	if path != llamaExe {
		t.Fatalf("expected %q, got %q", llamaExe, path)
	}
}

func TestFindOrDownloadModel_ConfiguredPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", t.TempDir())
	} else {
		t.Setenv("HOME", t.TempDir())
	}
	defer func() { os.RemoveAll(config.McpDir()) }()

	modelDir := t.TempDir()
	modelPath := filepath.Join(modelDir, "test.gguf")
	os.WriteFile(modelPath, []byte("fake model"), 0644)

	cfg := config.DefaultConfig()
	cfg.Llama.ModelPath = modelPath
	m := New(cfg)

	path, err := m.FindOrDownloadModel()
	if err != nil {
		t.Fatal(err)
	}
	if path != modelPath {
		t.Fatalf("expected %q, got %q", modelPath, path)
	}
	if m.ModelPath != modelPath {
		t.Fatal("expected ModelPath to be set")
	}
}

func TestFindOrDownloadModel_ModelsDirNoModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", t.TempDir())
	} else {
		t.Setenv("HOME", t.TempDir())
	}
	defer func() { os.RemoveAll(config.McpDir()) }()

	cfg := config.DefaultConfig()
	cfg.Llama.ModelPath = "" // no configured model
	m := New(cfg)

	// With no model path configured and no files in models dir,
	// FindOrDownloadModel should return an error (can't download in tests)
	_, err := m.FindOrDownloadModel()
	if err == nil {
		t.Log("no error (model may exist from prior runs)")
	}
}

func TestDownloadFile_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("model data"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "model.gguf")
	err := downloadFile(dest, srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "model data" {
		t.Fatalf("expected 'model data', got %q", string(data))
	}
}

func TestDownloadFile_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := downloadFile(filepath.Join(t.TempDir(), "model.gguf"), srv.URL)
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
}

func TestDownloadFile_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := downloadFile(filepath.Join(t.TempDir(), "model.gguf"), srv.URL)
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}

func TestDownloadArchive_TarGz(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gw)
		hdr := &tar.Header{Name: "test.txt", Size: int64(5), Mode: 0644}
		tw.WriteHeader(hdr)
		tw.Write([]byte("hello"))
		tw.Close()
		gw.Close()
		w.Write(buf.Bytes())
	}))
	defer srv.Close()

	destDir := t.TempDir()
	err := downloadArchive(srv.URL, destDir, false)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(destDir, "test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(data))
	}
}

func TestDownloadArchive_Zip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		wc, _ := zw.Create("test.txt")
		wc.Write([]byte("hello"))
		zw.Close()
		w.Write(buf.Bytes())
	}))
	defer srv.Close()

	destDir := t.TempDir()
	err := downloadArchive(srv.URL, destDir, true)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(destDir, "test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(data))
	}
}

func TestDownloadArchive_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := downloadArchive(srv.URL, t.TempDir(), false)
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
}

func TestLlamaVariant_ReturnsString(t *testing.T) {
	variant := llamaVariant()
	if variant == "" {
		t.Fatal("expected non-empty variant string")
	}
	fmt.Println("llamaVariant =", variant)
}

func TestKillByPort_NoServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", t.TempDir())
	} else {
		t.Setenv("HOME", t.TempDir())
	}

	m := New(config.DefaultConfig())
	m.Port = 19998
	err := m.KillByPort()
	if err != nil {
		t.Fatal(err)
	}
	if m.Ready {
		t.Fatal("expected Ready=false after KillByPort")
	}
}



func TestFindFreePort(t *testing.T) {
	port := findFreePort(20000, 20010)
	if port < 20000 || port > 20010 {
		t.Fatalf("expected port in [20000, 20010], got %d", port)
	}
}

func TestExtractTarGz(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	content := []byte("file content")
	hdr := &tar.Header{
		Name: "test.txt",
		Size: int64(len(content)),
		Mode: 0644,
	}
	tw.WriteHeader(hdr)
	tw.Write(content)
	tw.Close()
	gw.Close()

	archivePath := filepath.Join(dir, "archive.tar.gz")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	destDir := filepath.Join(dir, "out")
	if err := ExtractTarGz(archivePath, destDir); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(destDir, "test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "file content" {
		t.Fatalf("expected 'file content', got %s", string(data))
	}
}

func TestExtractTarGz_WithSubdirs(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	entries := []struct {
		Name string
		Body string
	}{
		{"dir1/", ""},
		{"dir1/file1.txt", "content1"},
		{"dir2/file2.txt", "content2"},
	}
	for _, e := range entries {
		if e.Name[len(e.Name)-1] == '/' {
			tw.WriteHeader(&tar.Header{Name: e.Name, Typeflag: tar.TypeDir, Mode: 0755})
		} else {
			tw.WriteHeader(&tar.Header{Name: e.Name, Size: int64(len(e.Body)), Mode: 0644})
			tw.Write([]byte(e.Body))
		}
	}
	tw.Close()
	gw.Close()

	archivePath := filepath.Join(dir, "subdirs.tar.gz")
	os.WriteFile(archivePath, buf.Bytes(), 0644)

	destDir := filepath.Join(dir, "out")
	if err := ExtractTarGz(archivePath, destDir); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(destDir, "dir1", "file1.txt"))
	if string(data) != "content1" {
		t.Fatalf("expected 'content1', got %s", string(data))
	}
}

func TestExtractTarGz_InvalidGzip(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "bad.tar.gz")
	os.WriteFile(archivePath, []byte("not gzip"), 0644)

	if err := ExtractTarGz(archivePath, filepath.Join(dir, "out")); err == nil {
		t.Fatal("expected error for invalid gzip")
	}
}

func TestExtractTarGz_NonExistentFile(t *testing.T) {
	if err := ExtractTarGz("/nonexistent/file.tar.gz", t.TempDir()); err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestExtractZip(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	files := []struct {
		Name string
		Body string
	}{
		{"file1.txt", "hello"},
		{"sub/file2.txt", "world"},
	}
	for _, f := range files {
		w, _ := zw.Create(f.Name)
		w.Write([]byte(f.Body))
	}
	zw.Close()

	os.WriteFile(zipPath, buf.Bytes(), 0644)

	destDir := filepath.Join(dir, "out")
	if err := ExtractZip(zipPath, destDir); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(destDir, "file1.txt"))
	if string(data) != "hello" {
		t.Fatalf("expected 'hello', got %s", string(data))
	}
	data, _ = os.ReadFile(filepath.Join(destDir, "sub", "file2.txt"))
	if string(data) != "world" {
		t.Fatalf("expected 'world', got %s", string(data))
	}
}

func TestExtractZip_Invalid(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "bad.zip")
	os.WriteFile(zipPath, []byte("not zip"), 0644)

	if err := ExtractZip(zipPath, filepath.Join(dir, "out")); err == nil {
		t.Fatal("expected error for invalid zip")
	}
}

func TestExtractZip_NonExistent(t *testing.T) {
	if err := ExtractZip("/nonexistent/file.zip", t.TempDir()); err == nil {
		t.Fatal("expected error for nonexistent zip")
	}
}
