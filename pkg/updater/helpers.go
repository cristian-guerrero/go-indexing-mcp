package updater

import (
	"fmt"
	"io"
	"os"
	"strconv"
)

// parseBuildNumber extracts the numeric build number from a tag like "b1024".
// Returns 0 if the tag doesn't match the expected format.
func parseBuildNumber(tag string) int {
	matches := buildTagRegex.FindStringSubmatch(tag)
	if matches == nil {
		return 0
	}
	n, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0
	}
	return n
}

// isNewer returns true if latestTag represents a newer build than currentTag.
// Both must match the b<number> format. Returns false if either is unparseable.
func isNewer(currentTag, latestTag string) bool {
	current := parseBuildNumber(currentTag)
	latest := parseBuildNumber(latestTag)
	if current == 0 || latest == 0 {
		return false
	}
	return latest > current
}

// copyFile copies a file from src to dst. The destination is created with
// the same permissions as the source.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer srcFile.Close()

	si, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, si.Mode())
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copy data: %w", err)
	}
	return nil
}
