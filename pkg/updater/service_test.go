package updater

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/version"
)

func TestNew(t *testing.T) {
	s := New(t.TempDir(), "owner", "repo")
	if s == nil {
		t.Fatal("expected non-nil service")
	}
}

func TestCheckForUpdate_DevBuild(t *testing.T) {
	defer func(v string) { version.Version = v }(version.Version)
	version.Version = "dev"

	s := New(t.TempDir(), "owner", "repo")
	info := s.CheckForUpdate()
	if info == nil {
		t.Fatal("expected non-nil info")
	}
	if info.Available {
		t.Fatal("expected Available=false for dev build")
	}
}

func TestCheckForUpdate_WithUpdate(t *testing.T) {
	defer func(v string) { version.Version = v }(version.Version)
	version.Version = "b100"

	assetName := newGitHubAPI("", "").platformAssetName()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases") {
			w.Write([]byte(`[{"tag_name":"b200","assets":[{"name":"` + assetName + `","browser_download_url":"` + srv.URL + `/download"}]}]`))
			return
		}
		w.Write([]byte("binary"))
	}))
	defer srv.Close()

	s := &Service{
		dataDir: t.TempDir(),
		owner:   "test",
		repo:    "repo",
		api:     newGitHubAPIWithClient("test", "repo", srv.Client(), srv.URL),
	}

	info := s.CheckForUpdate()
	if info == nil {
		t.Fatal("expected non-nil info")
	}
	if !info.Available {
		t.Fatal("expected Available=true for b100 -> b200")
	}
	if info.Version != "b200" {
		t.Fatalf("expected version 'b200', got %s", info.Version)
	}
}

func TestCheckForUpdate_NoNewerVersion(t *testing.T) {
	defer func(v string) { version.Version = v }(version.Version)
	version.Version = "b200"

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"tag_name":"b100","assets":[{"name":"test","browser_download_url":"` + srv.URL + `/download"}]}]`))
	}))
	defer srv.Close()

	s := &Service{
		dataDir: t.TempDir(),
		api:     newGitHubAPIWithClient("test", "repo", srv.Client(), srv.URL),
	}

	info := s.CheckForUpdate()
	if info == nil {
		t.Fatal("expected non-nil info")
	}
	if info.Available {
		t.Fatal("expected Available=false for same/older version")
	}
}

func TestCheckForUpdate_CacheHit(t *testing.T) {
	defer func(v string) { version.Version = v }(version.Version)
	version.Version = "b100"

	callCount := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		assetName := newGitHubAPI("", "").platformAssetName()
		w.Write([]byte(`[{"tag_name":"b200","assets":[{"name":"` + assetName + `","browser_download_url":"` + srv.URL + `/download"}]}]`))
	}))
	defer srv.Close()

	s := &Service{
		dataDir: t.TempDir(),
		api:     newGitHubAPIWithClient("test", "repo", srv.Client(), srv.URL),
	}

	s.CheckForUpdate()
	s.CheckForUpdate()

	if callCount != 1 {
		t.Fatalf("expected 1 API call (cached), got %d", callCount)
	}
}

func TestPendingVersion(t *testing.T) {
	s := New(t.TempDir(), "owner", "repo")
	if v := s.PendingVersion(); v != "" {
		t.Fatalf("expected empty pending, got %s", v)
	}
}

func TestDownloadUpdate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("update binary"))
	}))
	defer server.Close()

	s := New(t.TempDir(), "owner", "repo")
	info := &UpdateInfo{
		Available: true,
		Version:   "b200",
		URL:       server.URL,
	}

	if err := s.DownloadUpdate(info); err != nil {
		t.Fatal(err)
	}

	if v := s.PendingVersion(); v != "b200" {
		t.Fatalf("expected pending 'b200', got %s", v)
	}
}

func TestApplyUpdate_NoPendingVersion(t *testing.T) {
	s := New(t.TempDir(), "owner", "repo")
	err := s.ApplyUpdate()
	if err == nil {
		t.Fatal("expected error when no pending update")
	}
}

func TestApplyUpdate_PendingNotNewer(t *testing.T) {
	defer func(v string) { version.Version = v }(version.Version)
	version.Version = "b200"

	s := New(t.TempDir(), "owner", "repo")
	s.pending = "b100"

	err := s.ApplyUpdate()
	if err == nil {
		t.Fatal("expected error when pending is not newer")
	}
}

func TestWasJustUpdated_NoMarker(t *testing.T) {
	s := New(t.TempDir(), "owner", "repo")
	if s.WasJustUpdated() {
		t.Fatal("expected false with no marker file")
	}
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")

	if err := os.WriteFile(src, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world" {
		t.Fatalf("expected 'hello world', got %s", string(data))
	}
}

func TestCopyFile_NonExistentSource(t *testing.T) {
	if err := copyFile("/nonexistent/file.txt", t.TempDir()+"/out"); err == nil {
		t.Fatal("expected error for nonexistent source")
	}
}
