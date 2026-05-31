package updater

import (
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestPlatformAssetName(t *testing.T) {
	api := newGitHubAPI("owner", "repo")
	name := api.platformAssetName()
	if !strings.HasPrefix(name, "go-indexing-mcp-") {
		t.Fatalf("expected go-indexing-mcp- prefix, got %s", name)
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
		t.Fatalf("expected .exe suffix on windows, got %s", name)
	}
	if runtime.GOOS != "windows" && strings.HasSuffix(name, ".exe") {
		t.Fatalf("expected no .exe suffix on non-windows, got %s", name)
	}
}

func TestFindAsset_ExactMatch(t *testing.T) {
	api := newGitHubAPI("owner", "repo")
	expectedName := api.platformAssetName()

	release := &Release{
		TagName: "b100",
		Assets: []Asset{
			{Name: "other-file.txt", DownloadURL: "https://example.com/other"},
			{Name: expectedName, DownloadURL: "https://example.com/asset"},
		},
	}

	asset := api.findAsset(release)
	if asset == nil {
		t.Fatal("expected to find asset")
	}
	if asset.Name != expectedName {
		t.Fatalf("expected %s, got %s", expectedName, asset.Name)
	}
}

func TestFindAsset_FallbackMatch(t *testing.T) {
	api := newGitHubAPI("owner", "repo")
	suffix := api.platformAssetName()

	release := &Release{
		TagName: "b100",
		Assets: []Asset{
			{Name: "extra-prefix-" + suffix, DownloadURL: "https://example.com/asset"},
		},
	}

	asset := api.findAsset(release)
	if asset == nil {
		t.Fatal("expected to find asset via fallback")
	}
}

func TestFindAsset_NoMatch(t *testing.T) {
	api := newGitHubAPI("owner", "repo")
	release := &Release{
		TagName: "b100",
		Assets: []Asset{
			{Name: "unrelated-file.txt", DownloadURL: "https://example.com/other"},
		},
	}

	asset := api.findAsset(release)
	if asset != nil {
		t.Fatal("expected nil for no matching asset")
	}
}

func TestListReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/releases") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`[
			{"tag_name": "b100", "assets": []},
			{"tag_name": "b200", "assets": []}
		]`))
	}))
	defer server.Close()

	api := newGitHubAPIWithClient("test", "repo", server.Client(), server.URL)

	releases, err := api.listReleases()
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(releases))
	}
	if releases[0].TagName != "b100" {
		t.Fatalf("expected first tag 'b100', got %s", releases[0].TagName)
	}
}

func TestListReleases_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	api := newGitHubAPIWithClient("test", "repo", server.Client(), server.URL)

	_, err := api.listReleases()
	if err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestGetLatestRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"tag_name": "v1.0", "assets": []},
			{"tag_name": "b200", "assets": []},
			{"tag_name": "b100", "assets": []}
		]`))
	}))
	defer server.Close()

	api := newGitHubAPIWithClient("test", "repo", server.Client(), server.URL)

	release, err := api.getLatestRelease()
	if err != nil {
		t.Fatal(err)
	}
	if release.TagName != "b200" {
		t.Fatalf("expected latest tag 'b200', got %s", release.TagName)
	}
}

func TestGetLatestRelease_NoBuildReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"tag_name": "v1.0", "assets": []}
		]`))
	}))
	defer server.Close()

	api := newGitHubAPIWithClient("test", "repo", server.Client(), server.URL)

	_, err := api.getLatestRelease()
	if err == nil {
		t.Fatal("expected error when no build releases")
	}
}

func TestDownloadAsset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("binary content"))
	}))
	defer server.Close()

	api := newGitHubAPIWithClient("test", "repo", server.Client(), server.URL)

	dir := t.TempDir()
	asset := &Asset{
		Name:        "test-binary.exe",
		DownloadURL: server.URL,
	}

	path, err := api.DownloadAsset(asset, dir)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "binary content" {
		t.Fatalf("expected 'binary content', got %s", string(data))
	}
}

func TestDownloadAsset_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	api := newGitHubAPIWithClient("test", "repo", server.Client(), server.URL)

	_, err := api.DownloadAsset(&Asset{DownloadURL: server.URL}, t.TempDir())
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}
