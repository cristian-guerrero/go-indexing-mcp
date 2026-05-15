// Package indexer orchestrates the full indexing pipeline:
// walk files → chunk → embed → store. It supports full reindex,
// incremental (changed files only), and single-path indexing.
package indexer

import (
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/embedder"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/storage"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/walker"
)

// Indexer ties together the walker, chunker, embedder, and storage into a single pipeline.
// Thread-safe for concurrent access; prevents overlapping index operations via mu.
type Indexer struct {
	Walker   *walker.Walker
	Chunker  *chunker.Chunker
	Embedder *embedder.Embedder
	Storage  *storage.Storage
	mu       sync.Mutex
	Running  bool
	Stats    IndexStats
}

// IndexStats holds cumulative indexing statistics, updated after each operation.
type IndexStats struct {
	TotalChunks  int
	TotalFiles   int
	LastIndexed  string
	IsIndexing   bool
}

// New creates an Indexer from the given pipeline components.
// Embedder may be nil (for grep-only mode without llama.cpp).
func New(w *walker.Walker, ch *chunker.Chunker, em *embedder.Embedder, st *storage.Storage) *Indexer {
	return &Indexer{
		Walker:   w,
		Chunker:  ch,
		Embedder: em,
		Storage:  st,
		Stats: IndexStats{
			LastIndexed: "never",
		},
	}
}

// IndexAll performs a full index: walks all files, chunks, embeds, and stores them.
// Skips files already in the index with matching hashes (resume support).
// Saves progress periodically (every 10 files) so partial index survives shutdown.
func (idx *Indexer) IndexAll() error {
	idx.mu.Lock()
	if idx.Running {
		idx.mu.Unlock()
		return fmt.Errorf("indexing already in progress")
	}
	idx.Running = true
	idx.Stats.IsIndexing = true
	idx.mu.Unlock()

	defer func() {
		idx.mu.Lock()
		idx.Running = false
		idx.Stats.IsIndexing = false
		idx.mu.Unlock()
	}()

	slog.Info("starting full index")

	files, err := idx.Walker.Walk()
	if err != nil {
		return fmt.Errorf("walk files: %w", err)
	}

	slog.Info("found files to index", "count", len(files))

	idx.PruneStaleEntries()

	chunksMap, err := idx.Chunker.ChunkFiles(files)
	if err != nil {
		return fmt.Errorf("chunk files: %w", err)
	}

	var totalChunks int
	var indexedFiles int
	for i, fi := range files {
		// Skip files already in the index with matching hash (resume support)
		if idx.Storage.IsFileIndexed(fi.Path, fi.Hash) {
			slog.Debug("skipping already indexed file", "file", fi.RelPath)
			continue
		}

		chunks, ok := chunksMap[fi.Path]
		if !ok || len(chunks) == 0 {
			continue
		}

		embeddings, err := idx.Embedder.EmbedChunks(chunks)
		if err != nil {
			return fmt.Errorf("embed %s: %w", fi.RelPath, err)
		}

		if err := idx.Storage.UpsertChunks(chunks, embeddings); err != nil {
			return fmt.Errorf("store %s: %w", fi.RelPath, err)
		}

		totalChunks += len(chunks)
		indexedFiles++
		slog.Debug("indexed file", "file", fi.RelPath, "chunks", len(chunks))

		// Save progress periodically so partial index survives shutdown
		if i > 0 && i%10 == 0 {
			if err := idx.Storage.Save(); err != nil {
				slog.Warn("periodic save", "error", err)
			} else {
				slog.Info("index progress saved", "files", i+1, "chunks", totalChunks)
			}
		}
	}

	// Update stats
	idx.mu.Lock()
	idx.Stats.TotalChunks = totalChunks
	idx.Stats.TotalFiles = len(files)
	idx.Stats.LastIndexed = "just now"
	idx.mu.Unlock()

	// Final save with commit SHA
	if indexedFiles > 0 {
		headSHA := idx.Walker.GetHeadSHA()
		if headSHA != "" {
			idx.Storage.SetCommitSHA(headSHA)
		}
		if err := idx.Storage.Save(); err != nil {
			slog.Warn("final save", "error", err)
		}
		slog.Info("index complete", "files", indexedFiles, "chunks", totalChunks)
	} else {
		slog.Info("all files already up to date, nothing to index")
	}
	return nil
}

// IndexPath indexes a single file by path, replacing any existing chunks for that file.
func (idx *Indexer) IndexPath(path string) error {
	files, err := idx.Walker.Walk()
	if err != nil {
		return err
	}

	for _, fi := range files {
		if fi.RelPath != path && fi.Path != path {
			continue
		}

		chunks, err := idx.Chunker.ChunkFile(fi)
		if err != nil {
			return err
		}

		embeddings, err := idx.Embedder.EmbedChunks(chunks)
		if err != nil {
			return err
		}

		idx.Storage.DeleteChunksByPath(fi.Path)
		return idx.Storage.UpsertChunks(chunks, embeddings)
	}

	return fmt.Errorf("file not found: %s", path)
}

// IndexChanged performs an incremental index by diffing the current working tree
// against the last saved commit SHA. Handles added, modified, and deleted files.
func (idx *Indexer) IndexChanged() error {
	sinceSHA := idx.Storage.GetCommitSHA()
	files, err := idx.Walker.GetChangedFiles(sinceSHA)
	if err != nil {
		return fmt.Errorf("get changed files: %w", err)
	}

	if len(files) == 0 {
		return nil
	}

	slog.Info("incremental index", "files", len(files), "since", sinceSHA)

	chunksMap, err := idx.Chunker.ChunkFiles(files)
	if err != nil {
		return fmt.Errorf("chunk changed files: %w", err)
	}

	for _, fi := range files {
		if fi.Deleted {
			idx.Storage.DeleteChunksByPath(fi.Path)
			slog.Info("removed deleted file from index", "file", fi.RelPath)
			continue
		}

		chunks, ok := chunksMap[fi.Path]
		if !ok || len(chunks) == 0 {
			continue
		}

		embeddings, err := idx.Embedder.EmbedChunks(chunks)
		if err != nil {
			return fmt.Errorf("embed changed %s: %w", fi.RelPath, err)
		}

		idx.Storage.DeleteChunksByPath(fi.Path)
		if err := idx.Storage.UpsertChunks(chunks, embeddings); err != nil {
			return fmt.Errorf("store changed %s: %w", fi.RelPath, err)
		}

		slog.Info("re-indexed changed file", "file", fi.RelPath, "chunks", len(chunks))
	}

	headSHA := idx.Walker.GetHeadSHA()
	if headSHA != "" {
		idx.Storage.SetCommitSHA(headSHA)
	}

	if err := idx.Storage.Save(); err != nil {
		slog.Warn("index changed save", "error", err)
	} else {
		slog.Info("incremental index complete", "files", len(files), "sha", headSHA)
	}

	return nil
}

// GetStats returns the current indexing statistics, refreshed from storage.
func (idx *Indexer) GetStats() IndexStats {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.Storage != nil {
		chunks, files, err := idx.Storage.Stats()
		if err == nil {
			idx.Stats.TotalChunks = chunks
			idx.Stats.TotalFiles = files
		}
	}

	return idx.Stats
}

// ListFiles returns all indexed file paths from storage.
func (idx *Indexer) ListFiles() []string {
	if idx.Storage == nil {
		return nil
	}
	return idx.Storage.ListFiles()
}

// PruneStaleEntries removes chunks for files that no longer exist on disk.
func (idx *Indexer) PruneStaleEntries() {
	for _, relPath := range idx.Storage.ListFiles() {
		fullPath := filepath.Join(idx.Walker.Root, relPath)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			idx.Storage.DeleteChunksByPath(fullPath)
			slog.Info("pruned stale entry", "file", relPath)
		}
	}
}

// Search dispatches to hybrid (BM25 + vector) or grep mode based on the mode string.
// Prunes stale entries before each search to keep results current.
func (idx *Indexer) Search(query string, pathFilter string, limit int, mode string) ([]storage.SearchResult, error) {
	idx.PruneStaleEntries()

	switch mode {
	case "grep":
		results, err := idx.Storage.SearchGrep(storage.GrepOptions{Query: query, Limit: limit})
		if err != nil {
			return nil, fmt.Errorf("grep search: %w", err)
		}
		filtered := idx.filterGrepByPath(results, pathFilter)
		out := make([]storage.SearchResult, len(filtered))
		for i, r := range filtered {
			out[i] = storage.SearchResult{
				ID: r.ID, FilePath: r.FilePath, RelPath: r.RelPath,
				Language: r.Language, StartLine: r.StartLine, EndLine: r.EndLine,
				Content: r.Content, Score: r.Score,
			}
		}
		return out, nil

	default:
		queryVec, err := idx.Embedder.EmbedQuery(query)
		if err != nil {
			return nil, fmt.Errorf("embed query: %w", err)
		}
		results, err := idx.Storage.SearchHybrid(queryVec, query, limit)
		if err != nil {
			return nil, fmt.Errorf("hybrid search: %w", err)
		}
		return idx.filterByPath(results, pathFilter), nil
	}
}

// SearchGrep performs a grep-style search on indexed chunks with the given options.
func (idx *Indexer) SearchGrep(opts storage.GrepOptions, pathFilter string) ([]storage.GrepResult, error) {
	idx.PruneStaleEntries()

	results, err := idx.Storage.SearchGrep(opts)
	if err != nil {
		return nil, fmt.Errorf("grep search: %w", err)
	}
	return idx.filterGrepByPath(results, pathFilter), nil
}

// filterByPath filters search results by path using glob/prefix matching.
func (idx *Indexer) filterByPath(results []storage.SearchResult, pathFilter string) []storage.SearchResult {
	if pathFilter == "" {
		return results
	}
	var filtered []storage.SearchResult
	for _, r := range results {
		if matchesPath(r.RelPath, pathFilter) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// filterGrepByPath filters grep results by path using glob/prefix matching.
func (idx *Indexer) filterGrepByPath(results []storage.GrepResult, pathFilter string) []storage.GrepResult {
	if pathFilter == "" {
		return results
	}
	var filtered []storage.GrepResult
	for _, r := range results {
		if matchesPath(r.RelPath, pathFilter) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// matchesPath checks if relPath matches the given path filter.
// Supports exact prefix match (case-insensitive), filepath.Match globs,
// and **/ prefix globs for recursive directory matching.
func matchesPath(relPath, filter string) bool {
	relPath = filepath.ToSlash(relPath)
	filter = filepath.ToSlash(filter)

	if hasGlobChars(filter) {
		match, _ := path.Match(filter, relPath)
		if match {
			return true
		}
		if !strings.Contains(filter, "/") {
			base := filepath.Base(relPath)
			match, _ := path.Match(filter, base)
			return match
		}
		if strings.HasPrefix(filter, "**/") {
			suffix := filter[3:]
			match, _ := path.Match(suffix, relPath)
			if match {
				return true
			}
			for i := 0; i < len(relPath); i++ {
				if relPath[i] == '/' {
					match, _ := path.Match(suffix, relPath[i+1:])
					if match {
						return true
					}
				}
			}
		}
		return false
	}

	if len(relPath) < len(filter) {
		return false
	}
	return strings.EqualFold(relPath[:len(filter)], filter)
}

// hasGlobChars returns true if the string contains glob metacharacters (*, ?, [).
func hasGlobChars(s string) bool {
	for _, r := range s {
		if r == '*' || r == '?' || r == '[' {
			return true
		}
	}
	return false
}
