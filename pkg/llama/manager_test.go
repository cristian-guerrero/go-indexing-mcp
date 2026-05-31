package llama

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestBaseURL_WithPort(t *testing.T) {
	m := &Manager{Port: 12345}
	url := m.BaseURL()
	expected := "http://127.0.0.1:12345"
	if url != expected {
		t.Fatalf("expected %q, got %q", expected, url)
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
