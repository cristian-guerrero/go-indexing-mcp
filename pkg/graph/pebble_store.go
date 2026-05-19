package graph

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cockroachdb/pebble"
)

const prefixFile = "f:"

// FileData holds all symbols and references for a single source file.
type FileData struct {
	Symbols []Symbol    `json:"symbols"`
	Refs    []Reference `json:"refs"`
}

// GraphDB wraps Pebble for persistent symbol graph storage.
// Each file is stored as a single key (f:{relPath}) with all its symbols
// and references as a JSON blob. On startup, all entries are loaded into
// the in-memory KnowledgeGraph for fast querying.
type GraphDB struct {
	db *pebble.DB
}

// OpenGraph opens or creates a Pebble-backed graph at the given directory.
func OpenGraph(dir string) (*GraphDB, error) {
	db, err := pebble.Open(dir, &pebble.Options{
		Logger: nil,
	})
	if err != nil {
		return nil, fmt.Errorf("open graph db: %w", err)
	}

	slog.Info("graph db opened", "dir", dir)

	return &GraphDB{db: db}, nil
}

// Close closes the underlying Pebble database.
func (g *GraphDB) Close() error {
	return g.db.Close()
}

// StoreFile atomically stores all symbols and references for a single file
// as one key-value entry. Replaces any existing entry for the same file.
func (g *GraphDB) StoreFile(relPath string, symbols []Symbol, refs []Reference) error {
	data := FileData{Symbols: symbols, Refs: refs}
	blob, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal filedata: %w", err)
	}

	key := prefixFile + relPath
	return g.db.Set([]byte(key), blob, nil)
}

// LoadAll reads all stored files into the given KnowledgeGraph.
// Called on startup to rebuild in-memory indexes.
func (g *GraphDB) LoadAll(knowledge *KnowledgeGraph) error {
	prefix := []byte(prefixFile)
	iter, err := g.db.NewIter(&pebble.IterOptions{})
	if err != nil {
		return err
	}
	defer iter.Close()

	for iter.SeekGE(prefix); iter.Valid(); iter.Next() {
		key := iter.Key()
		if len(key) < len(prefix) || string(key[:len(prefix)]) != prefixFile {
			break
		}
		relPath := string(key[len(prefix):])

		val := iter.Value()
		if val == nil {
			slog.Warn("graph: nil value for file, skipping", "file", relPath)
			continue
		}
		raw := make([]byte, len(val))
		copy(raw, val)

		var data FileData
		if err := json.Unmarshal(raw, &data); err != nil {
			slog.Warn("graph: failed to decode file data, skipping", "file", relPath, "error", err)
			continue
		}

		for _, sym := range data.Symbols {
			knowledge.AddSymbol(sym)
		}
		for _, ref := range data.Refs {
			knowledge.AddReference(ref)
		}
	}
	return nil
}

// RemoveFile deletes all data for a given file.
func (g *GraphDB) RemoveFile(relPath string) error {
	key := prefixFile + relPath
	return g.db.Delete([]byte(key), nil)
}

// Stats returns the number of files stored.
func (g *GraphDB) Stats() (files int, err error) {
	prefix := []byte(prefixFile)
	iter, err := g.db.NewIter(&pebble.IterOptions{})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	for iter.SeekGE(prefix); iter.Valid(); iter.Next() {
		key := iter.Key()
		if len(key) < len(prefix) || string(key[:len(prefix)]) != prefixFile {
			break
		}
		files++
	}
	return files, iter.Error()
}

// Clear removes all data from the graph database.
func (g *GraphDB) Clear() error {
	// Mark all f: keys for deletion via range delete
	if err := g.db.DeleteRange([]byte(prefixFile), []byte{prefixFile[0] + 1}, nil); err != nil {
		return err
	}
	return g.db.Flush()
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
	// Also clean up any old BadgerDB subdirectory
	oldPath := filepath.Join(dir, "graph")
	os.RemoveAll(oldPath) //nolint:errcheck

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove graph db: %w", err)
	}
	slog.Info("graph db dropped", "dir", dir)
	return nil
}


