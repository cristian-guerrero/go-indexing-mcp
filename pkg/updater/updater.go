// Package updater provides automatic update checking, downloading, and self-replacement
// using GitHub Releases. Version comparison uses build-number tags (b<count>) following
// the same scheme used by the CI pipeline (git rev-list --count HEAD).
//
// Architecture:
//   - CheckForUpdate queries the GitHub Releases API for the latest release with a build
//     tag, parses the numeric build number, and compares it against the current version.
//   - DownloadUpdate downloads the platform-appropriate asset to a staging directory.
//   - ApplyUpdate (called at startup) replaces the running binary with a previously
//     downloaded update, keeping the old binary as a .old rollback file.
//   - WasJustUpdated checks for a marker file written after a successful apply, used
//     to log or notify that the update took effect.
//
// Dev builds (version.Version == "dev") skip all update checks.
package updater

import (
	"regexp"
	"time"
)

// GitHub repository defaults for update checks.
const (
	DefaultOwner = "cristian-guerrero"
	DefaultRepo  = "go-indexing-mcp"
)

// CheckInterval limits GitHub API calls to once per hour.
const CheckInterval = 1 * time.Hour

// buildTagRegex matches CI-generated build tags like "b1024".
var buildTagRegex = regexp.MustCompile(`^b(\d+)$`)

// UpdateInfo holds the result of a version check.
type UpdateInfo struct {
	Available bool   `json:"available"`
	Version   string `json:"version"`   // tag name, e.g. "b1024"
	URL       string `json:"url"`       // download URL for the platform asset
}

// Release represents a GitHub release returned by the API.
type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

// Asset represents a downloadable file attached to a GitHub release.
type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
}
