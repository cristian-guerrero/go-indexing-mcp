package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// GitHubAPI wraps calls to the GitHub Releases API for a given owner/repo.
type GitHubAPI struct {
	owner   string
	repo    string
	client  *http.Client
	baseURL string
}

// newGitHubAPI creates a new GitHubAPI for the given repository.
func newGitHubAPI(owner, repo string) *GitHubAPI {
	return &GitHubAPI{
		owner:   owner,
		repo:    repo,
		client:  &http.Client{},
		baseURL: "https://api.github.com",
	}
}

// newGitHubAPIWithClient creates a GitHubAPI with a custom HTTP client and base URL.
// Used for testing with httptest.Server.
func newGitHubAPIWithClient(owner, repo string, client *http.Client, baseURL string) *GitHubAPI {
	return &GitHubAPI{
		owner:   owner,
		repo:    repo,
		client:  client,
		baseURL: baseURL,
	}
}

// getLatestRelease fetches all releases, filters to build tags (b<N>), and returns
// the one with the highest build number. Returns an error if no build releases exist.
func (g *GitHubAPI) getLatestRelease() (*Release, error) {
	releases, err := g.listReleases()
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}

	var buildReleases []Release
	for _, r := range releases {
		if buildTagRegex.MatchString(r.TagName) {
			buildReleases = append(buildReleases, r)
		}
	}

	if len(buildReleases) == 0 {
		return nil, fmt.Errorf("no build releases found")
	}

	sort.Slice(buildReleases, func(i, j int) bool {
		return parseBuildNumber(buildReleases[i].TagName) > parseBuildNumber(buildReleases[j].TagName)
	})

	return &buildReleases[0], nil
}

// listReleases fetches up to 30 releases from the GitHub API.
func (g *GitHubAPI) listReleases() ([]Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=30", g.baseURL, g.owner, g.repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var releases []Release
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, fmt.Errorf("parse releases: %w", err)
	}
	return releases, nil
}

// findAsset finds the asset matching the current platform (OS + arch).
// CI produces: go-indexing-mcp-{goos}-{goarch}[.exe]
func (g *GitHubAPI) findAsset(release *Release) *Asset {
	suffix := g.platformAssetName()
	for i := range release.Assets {
		if release.Assets[i].Name == suffix {
			return &release.Assets[i]
		}
	}
	// Fall back to suffix match
	for i := range release.Assets {
		if strings.Contains(release.Assets[i].Name, suffix) {
			return &release.Assets[i]
		}
	}
	return nil
}

// platformAssetName returns the expected CI asset name for the current platform.
func (g *GitHubAPI) platformAssetName() string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	return fmt.Sprintf("go-indexing-mcp-%s-%s%s", runtime.GOOS, runtime.GOARCH, ext)
}

// DownloadAsset downloads the asset from its browser_download_url to destDir.
// Returns the full path to the downloaded file.
func (g *GitHubAPI) DownloadAsset(asset *Asset, destDir string) (string, error) {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("create download dir: %w", err)
	}

	destPath := filepath.Join(destDir, asset.Name)
	out, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("create file: %w", err)
	}
	defer out.Close()

	req, err := http.NewRequest("GET", asset.DownloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/octet-stream")

	resp, err := g.client.Do(req)
	if err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		os.Remove(destPath)
		return "", fmt.Errorf("download returned %d", resp.StatusCode)
	}

	if _, err := io.Copy(out, resp.Body); err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("download data: %w", err)
	}

	return destPath, nil
}
