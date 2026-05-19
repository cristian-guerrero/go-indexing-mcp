package graph

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/storage"
)

// FileData holds all symbols and references for a single source file.
type FileData struct {
	Symbols []Symbol    `json:"symbols"`
	Refs    []Reference `json:"refs"`
}

// VersionFileName is the name of the file storing the graph format version.
const VersionFileName = "version.json"

// GraphDB persists symbol graph data as individual JSON files,
// one per indexed source file, under a directory. Each file is stored at:
//
//	{dir}/{relPath}.json
//
// Writes are atomic via tmp+rename so concurrent readers always see
// a complete file. No exclusive locks are held, allowing safe concurrent
// access from MCP server (writer) and CLI tools (readers).
type GraphDB struct {
	dir          string
	diskVersion  int // version read from disk (0 = pre-versioning)
}

// OpenGraph creates or opens a file-based graph store at the given directory.
// The directory is created if it doesn't exist. Checks and records the on-disk
// format version for detecting breaking changes that require reindex.
func OpenGraph(dir string) (*GraphDB, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create graph dir: %w", err)
	}

	db := &GraphDB{dir: dir}
	if err := db.readVersionLocked(); err != nil {
		slog.Info("graph: no version file found, writing current version", "dir", dir)
		db.diskVersion = storage.GraphFormatVersion
		if err := db.writeVersionLocked(); err != nil {
			slog.Warn("graph: write version file", "error", err)
		}
	}
	slog.Info("graph db opened", "dir", dir, "disk_version", db.diskVersion)
	return db, nil
}

// versionPath returns the path to the version file.
func (g *GraphDB) versionPath() string {
	return filepath.Join(g.dir, VersionFileName)
}

// readVersionLocked reads the format version from version.json.
// Returns error if the file doesn't exist or is unreadable (diskVersion stays 0).
func (g *GraphDB) readVersionLocked() error {
	raw, err := os.ReadFile(g.versionPath())
	if err != nil {
		return err
	}
	var v struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	g.diskVersion = v.Version
	return nil
}

// writeVersionLocked writes the current storage.GraphFormatVersion to version.json.
func (g *GraphDB) writeVersionLocked() error {
	v := struct {
		Version int `json:"version"`
	}{Version: storage.GraphFormatVersion}
	blob, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(g.versionPath(), blob, 0644)
}

// NeedsReindex returns true when the on-disk graph format version differs from
// the current code version, indicating that a breaking change requires clearing
// and re-extracting all graph data. Returns false for version 0 (pre-versioning).
func (g *GraphDB) NeedsReindex() bool {
	return g.diskVersion > 0 && g.diskVersion != storage.GraphFormatVersion
}

// Close is a no-op for the file-based store. No DB connection to close.
func (g *GraphDB) Close() error {
	return nil
}

// filePath returns the on-disk path for a given relative file path.
func (g *GraphDB) filePath(relPath string) string {
	return filepath.Join(g.dir, filepath.FromSlash(relPath)+".json")
}

// StoreFile atomically stores all symbols and references for a single file
// as one JSON file. Replaces any existing entry for the same file.
// Uses tmp+rename for atomicity (readers see a complete file or nothing).
func (g *GraphDB) StoreFile(relPath string, symbols []Symbol, refs []Reference) error {
	data := FileData{Symbols: symbols, Refs: refs}
	blob, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal filedata: %w", err)
	}

	path := g.filePath(relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("mkdir for file: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, blob, 0644); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write tmp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename tmp file: %w", err)
	}

	return nil
}

// LoadAll reads all stored files into the given KnowledgeGraph.
// Called on startup to rebuild in-memory indexes. Skips version.json.
func (g *GraphDB) LoadAll(knowledge *KnowledgeGraph) error {
	return filepath.WalkDir(g.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".json") {
			return nil
		}
		if filepath.Base(path) == VersionFileName {
			return nil
		}

		relPath := g.relPath(path)
		if relPath == "" {
			return nil
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("graph: failed to read file, skipping", "file", relPath, "error", err)
			return nil
		}

		var data FileData
		if err := json.Unmarshal(raw, &data); err != nil {
			slog.Warn("graph: failed to decode file data, skipping", "file", relPath, "error", err)
			return nil
		}

		for _, sym := range data.Symbols {
			knowledge.AddSymbol(sym)
		}
		for _, ref := range data.Refs {
			knowledge.AddReference(ref)
		}

		return nil
	})
}

// relPath extracts the relative file path from a stored JSON file path.
func (g *GraphDB) relPath(path string) string {
	rel := strings.TrimPrefix(path, g.dir)
	rel = strings.TrimPrefix(rel, string(filepath.Separator))
	rel = strings.TrimSuffix(rel, ".json")
	return filepath.ToSlash(rel)
}

// RemoveFile deletes all data for a given file.
func (g *GraphDB) RemoveFile(relPath string) error {
	path := g.filePath(relPath)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove file: %w", err)
	}
	return nil
}

// Stats returns the number of indexed source files (excluding version.json).
func (g *GraphDB) Stats() (files int, err error) {
	err = filepath.WalkDir(g.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".json") {
			return nil
		}
		if filepath.Base(path) == VersionFileName {
			return nil
		}
		files++
		return nil
	})
	return files, err
}

// Clear removes all data from the graph database while keeping the directory
// itself intact for subsequent writes. Effectively deletes all JSON files
// and resets the disk version. Rewrites version.json with the current version.
func (g *GraphDB) Clear() error {
	entries, err := os.ReadDir(g.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(g.dir, 0755)
		}
		return err
	}
	for _, entry := range entries {
		child := filepath.Join(g.dir, entry.Name())
		if err := os.RemoveAll(child); err != nil {
			return err
		}
	}
	g.diskVersion = 0
	if err := g.writeVersionLocked(); err != nil {
		return err
	}
	g.diskVersion = storage.GraphFormatVersion
	return nil
}

// HasOldFormat checks if this directory has a BadgerDB database from a
// previous version. Returns true if a BadgerDB MANIFEST file is found
// in a "graph" subdirectory.
func HasOldFormat(dir string) bool {
	oldPath := filepath.Join(dir, "graph")
	manifest := filepath.Join(oldPath, "MANIFEST")
	_, err := os.Stat(manifest)
	return err == nil
}

// DropGraphDB removes the entire graph database directory.
func DropGraphDB(dir string) error {
	oldPath := filepath.Join(dir, "graph")
	os.RemoveAll(oldPath) //nolint:errcheck

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove graph db: %w", err)
	}
	slog.Info("graph db dropped", "dir", dir)
	return nil
}
