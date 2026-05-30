// Package db provides auto-download of the sqlite-vec loadable extension.
package db

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

const (
	vec0Version = "0.1.9"
	vec0Repo   = "asg017/sqlite-vec"
)

// vec0Dir returns ~/.go-mcp/indexing/lib/, creating it if needed.
func vec0Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, ".go-mcp", "indexing", "lib")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create vec0 dir: %w", err)
	}
	return dir, nil
}

// vec0FileName returns the platform-specific extension filename.
func vec0FileName() string {
	switch runtime.GOOS {
	case "windows":
		return "vec0.dll"
	case "darwin":
		return "vec0.dylib"
	default:
		return "vec0.so"
	}
}

// vec0Platform returns the platform identifier used in GitHub release filenames.
func vec0Platform() string {
	switch runtime.GOOS {
	case "windows":
		return "windows-x86_64"
	case "darwin":
		switch runtime.GOARCH {
		case "arm64":
			return "macos-aarch64"
		default:
			return "macos-x86_64"
		}
	default:
		switch runtime.GOARCH {
		case "arm64", "aarch64":
			return "linux-aarch64"
		default:
			return "linux-x86_64"
		}
	}
}

// vec0DownloadURL builds the download URL for the vec0 extension archive.
func vec0DownloadURL() string {
	platform := vec0Platform()
	return fmt.Sprintf("https://github.com/%s/releases/download/v%s/sqlite-vec-%s-loadable-%s.tar.gz",
		vec0Repo, vec0Version, vec0Version, platform)
}

// EnsureVec0Lib checks that the sqlite-vec extension exists at the expected path,
// downloading and extracting it from GitHub Releases if missing.
// Returns the full path to the extension library.
func EnsureVec0Lib() (string, error) {
	dir, err := vec0Dir()
	if err != nil {
		return "", err
	}

	libPath := filepath.Join(dir, vec0FileName())
	if _, err := os.Stat(libPath); err == nil {
		return libPath, nil
	}

	url := vec0DownloadURL()
	slog.Info("downloading sqlite-vec extension", "version", vec0Version, "url", url)

	tmpDir := filepath.Join(dir, ".download")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", fmt.Errorf("create download dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpArchive := filepath.Join(tmpDir, "vec0.tar.gz")
	if err := downloadFile(url, tmpArchive); err != nil {
		return "", fmt.Errorf("download vec0: %w", err)
	}

	if err := extractTarGz(tmpArchive, tmpDir); err != nil {
		return "", fmt.Errorf("extract vec0: %w", err)
	}

	// The archive contains the .dll/.so/.dylib directly; find it
	found := ""
	filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext == ".dll" || ext == ".so" || ext == ".dylib" {
			found = path
		}
		return nil
	})
	if found == "" {
		return "", fmt.Errorf("no extension library found in archive")
	}
	if err := os.Rename(found, libPath); err != nil {
		return "", fmt.Errorf("rename: %w", err)
	}

	slog.Info("sqlite-vec extension ready", "path", libPath)
	return libPath, nil
}

// extractTarGz extracts a .tar.gz archive to the given directory.
func extractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, header.Name)
		switch header.Typeflag {
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
	return nil
}

// downloadFile downloads a URL to a file.
func downloadFile(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return nil
}


