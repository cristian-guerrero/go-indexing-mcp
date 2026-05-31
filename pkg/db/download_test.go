package db

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVec0FileName(t *testing.T) {
	name := vec0FileName()
	switch runtime.GOOS {
	case "windows":
		if !strings.HasSuffix(name, ".dll") {
			t.Fatalf("expected .dll, got %s", name)
		}
	case "darwin":
		if !strings.HasSuffix(name, ".dylib") {
			t.Fatalf("expected .dylib, got %s", name)
		}
	default:
		if !strings.HasSuffix(name, ".so") {
			t.Fatalf("expected .so, got %s", name)
		}
	}
}

func TestVec0Platform(t *testing.T) {
	platform := vec0Platform()
	if platform == "" {
		t.Fatal("expected non-empty platform")
	}
}

func TestVec0DownloadURL(t *testing.T) {
	url := vec0DownloadURL()
	if url == "" {
		t.Fatal("expected non-empty URL")
	}
	if !strings.Contains(url, "github.com") {
		t.Fatal("expected github.com URL")
	}
}

func TestExtractTarGz(t *testing.T) {
	dir := t.TempDir()

	// Create a test tar.gz
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	content := []byte("test content")
	hdr := &tar.Header{
		Name: "test.txt",
		Size: int64(len(content)),
		Mode: 0644,
	}
	tw.WriteHeader(hdr)
	tw.Write(content)
	tw.Close()
	gw.Close()

	archivePath := filepath.Join(dir, "test.tar.gz")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	destDir := filepath.Join(dir, "extracted")
	if err := extractTarGz(archivePath, destDir); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(destDir, "test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "test content" {
		t.Fatalf("expected 'test content', got %s", string(data))
	}
}

func TestExtractTarGz_InvalidGzip(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "invalid.tar.gz")
	if err := os.WriteFile(archivePath, []byte("not gzip"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := extractTarGz(archivePath, filepath.Join(dir, "out")); err == nil {
		t.Fatal("expected error for invalid gzip")
	}
}

func TestExtractTarGz_NonExistentFile(t *testing.T) {
	if err := extractTarGz("/nonexistent/file.tar.gz", t.TempDir()); err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestDownloadFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("downloaded content"))
	}))
	defer server.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "output.txt")
	if err := downloadFile(server.URL, dest); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "downloaded content" {
		t.Fatalf("expected 'downloaded content', got %s", string(data))
	}
}

func TestDownloadFile_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	if err := downloadFile(server.URL, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestEnsureVec0Lib_AlreadyExists(t *testing.T) {
	// Override vec0Dir to use temp dir
	origDir := vec0Dir
	defer func() { vec0Dir = origDir }()

	dir := t.TempDir()
	vec0Dir = func() (string, error) { return dir, nil }

	libName := vec0FileName()
	libPath := filepath.Join(dir, libName)
	if err := os.WriteFile(libPath, []byte("fake lib"), 0644); err != nil {
		t.Fatal(err)
	}

	path, err := EnsureVec0Lib()
	if err != nil {
		t.Fatal(err)
	}
	if path != libPath {
		t.Fatalf("expected %q, got %q", libPath, path)
	}
}

func TestEnsureVec0Lib_DownloadAndExtract(t *testing.T) {
	origDir := vec0Dir
	defer func() { vec0Dir = origDir }()

	dir := t.TempDir()
	vec0Dir = func() (string, error) { return dir, nil }

	// Create a fake tar.gz with a .so/.dll/.dylib inside
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	libName := vec0FileName()
	content := []byte("fake extension library")
	hdr := &tar.Header{
		Name: libName,
		Size: int64(len(content)),
		Mode: 0644,
	}
	tw.WriteHeader(hdr)
	tw.Write(content)
	tw.Close()
	gw.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(buf.Bytes())
	}))
	defer server.Close()

	// Override vec0DownloadURL
	origURL := vec0DownloadURL
	defer func() { vec0DownloadURL = origURL }()
	vec0DownloadURL = func() string { return server.URL + "/test.tar.gz" }

	path, err := EnsureVec0Lib()
	if err != nil {
		t.Fatal(err)
	}

	expectedPath := filepath.Join(dir, libName)
	if path != expectedPath {
		t.Fatalf("expected %q, got %q", expectedPath, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fake extension library" {
		t.Fatalf("expected 'fake extension library', got %s", string(data))
	}
}
