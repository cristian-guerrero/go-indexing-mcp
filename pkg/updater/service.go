package updater

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/version"
)

// Service manages the update lifecycle: check, download, apply, and verify.
// It stores temporary update files under the given data directory.
type Service struct {
	dataDir       string
	api           *GitHubAPI
	lastCheck     time.Time
	lastResult    *UpdateInfo
	pending       string // version downloaded and ready to apply
	owner, repo   string
}

// New creates a new updater Service that stores updates under dataDir/updates/.
func New(dataDir, owner, repo string) *Service {
	return &Service{
		dataDir: dataDir,
		owner:   owner,
		repo:    repo,
		api:     newGitHubAPI(owner, repo),
	}
}

// CheckForUpdate queries GitHub for the latest release and compares it against
// the current build version. Results are cached for CheckInterval (1 hour).
// Dev builds (version.Version == "dev") always return Available: false.
func (s *Service) CheckForUpdate() *UpdateInfo {
	if version.Version == "dev" {
		s.lastResult = &UpdateInfo{Available: false}
		return s.lastResult
	}

	if time.Since(s.lastCheck) < CheckInterval && s.lastResult != nil {
		return s.lastResult
	}
	s.lastCheck = time.Now()

	release, err := s.api.getLatestRelease()
	if err != nil {
		slog.Debug("update check failed", "error", err)
		s.lastResult = &UpdateInfo{Available: false}
		return s.lastResult
	}

	if !isNewer(version.Version, release.TagName) {
		s.lastResult = &UpdateInfo{Available: false}
		return s.lastResult
	}

	asset := s.api.findAsset(release)
	if asset == nil {
		slog.Debug("no asset found for platform", "goos", runtime.GOOS, "goarch", runtime.GOARCH)
		s.lastResult = &UpdateInfo{Available: false}
		return s.lastResult
	}

	s.lastResult = &UpdateInfo{
		Available: true,
		Version:   release.TagName,
		URL:       asset.DownloadURL,
	}
	return s.lastResult
}

// PendingVersion returns the version tag of a downloaded but not yet applied update.
func (s *Service) PendingVersion() string {
	return s.pending
}

// DownloadUpdate downloads the update binary from the given info URL to the
// updates staging directory. The downloaded file is renamed to a fixed name
// so ApplyUpdate can find it regardless of the CI artifact name.
func (s *Service) DownloadUpdate(info *UpdateInfo) error {
	s.pending = info.Version
	tmpDir := filepath.Join(s.dataDir, "updates")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return fmt.Errorf("create updates dir: %w", err)
	}

	// Download directly from the info URL (already resolved by CheckForUpdate)
	asset := &Asset{
		Name:        "go-indexing-mcp-updated",
		DownloadURL: info.URL,
	}
	if runtime.GOOS == "windows" {
		asset.Name += ".exe"
	}

	downloaded, err := s.api.DownloadAsset(asset, tmpDir)
	if err != nil {
		return fmt.Errorf("download asset: %w", err)
	}

	// Rename to a fixed name so ApplyUpdate can find it
	updatedName := "go-indexing-mcp-updated"
	if runtime.GOOS == "windows" {
		updatedName += ".exe"
	}
	updatedPath := filepath.Join(tmpDir, updatedName)
	if downloaded != updatedPath {
		if err := os.Rename(downloaded, updatedPath); err != nil {
			if err := copyFile(downloaded, updatedPath); err != nil {
				return fmt.Errorf("rename downloaded file: %w", err)
			}
			os.Remove(downloaded)
		}
	}

	// Write version marker for ApplyUpdate
	versionPath := filepath.Join(tmpDir, "go-indexing-mcp-version")
	if err := os.WriteFile(versionPath, []byte(info.Version), 0644); err != nil {
		return fmt.Errorf("write version file: %w", err)
	}

	slog.Info("update downloaded", "version", info.Version, "path", updatedPath)
	return nil
}

// ApplyUpdate replaces the running binary with a previously downloaded update.
// It renames the current executable to .old, writes the new binary in its place,
// and writes a .updated-marker so WasJustUpdated can detect success.
// If no pending update exists, or the pending version is not newer than the
// current version, it cleans up stale files and returns an error (expected on
// normal startup with no pending update).
func (s *Service) ApplyUpdate() error {
	tmpDir := filepath.Join(s.dataDir, "updates")

	if s.pending == "" {
		versionPath := filepath.Join(tmpDir, "go-indexing-mcp-version")
		data, err := os.ReadFile(versionPath)
		if err != nil {
			return fmt.Errorf("no pending update")
		}
		s.pending = strings.TrimSpace(string(data))
	}

	// Guard: if the pending version is not newer, clean up and bail.
	// This prevents re-applying the same update when cleanup failed previously.
	if !isNewer(version.Version, s.pending) {
		os.RemoveAll(tmpDir)
		exe, _ := os.Executable()
		os.Remove(exe + ".old")
		return fmt.Errorf("pending %s is not newer than current %s", s.pending, version.Version)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	updatedName := "go-indexing-mcp-updated"
	if runtime.GOOS == "windows" {
		updatedName += ".exe"
	}
	newBinary := filepath.Join(tmpDir, updatedName)

	if _, err := os.Stat(newBinary); os.IsNotExist(err) {
		os.RemoveAll(tmpDir)
		return fmt.Errorf("updated binary not found at %s", newBinary)
	}

	if runtime.GOOS == "windows" {
		return s.applyUpdateWindows(exe, newBinary, tmpDir)
	}

	// Unix: rename current → .old, copy new → current
	oldBinary := exe + ".old"
	os.Remove(oldBinary)
	if err := os.Rename(exe, oldBinary); err != nil {
		return fmt.Errorf("rename current to .old: %w", err)
	}

	if err := copyFile(newBinary, exe); err != nil {
		os.Rename(oldBinary, exe)
		return fmt.Errorf("copy updated binary: %w", err)
	}

	if err := os.Chmod(exe, 0755); err != nil {
		slog.Warn("chmod updated binary", "error", err)
	}

	os.RemoveAll(tmpDir)

	markerPath := filepath.Join(s.dataDir, ".updated-marker")
	if err := os.WriteFile(markerPath, []byte(s.pending), 0644); err != nil {
		return fmt.Errorf("write updated marker: %w", err)
	}

	slog.Info("update applied successfully", "version", s.pending)
	return nil
}

// WasJustUpdated checks whether the current process was started right after an update
// was applied. It reads the .updated-marker file, removes it, and cleans up the .old
// binary from the previous update cycle. Returns true if this is a post-update startup.
func (s *Service) WasJustUpdated() bool {
	markerPath := filepath.Join(s.dataDir, ".updated-marker")
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return false
	}
	os.Remove(markerPath)

	// Clean up old binary from previous update
	exe, _ := os.Executable()
	oldBinary := exe + ".old"
	os.Remove(oldBinary)

	return string(data) == version.Version
}
