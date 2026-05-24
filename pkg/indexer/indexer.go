// Package indexer orchestrates the full indexing pipeline:
// walk files → chunk → embed → store. It supports full reindex,
// incremental (changed files only), and single-path indexing.
package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/embedder"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/graph"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/llama"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/storage"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/walker"
)

// ErrBranchChanged is returned when the git branch or worktree changes
// during an indexing operation. Callers should retry on the new branch.
var ErrBranchChanged = errors.New("branch changed during indexing")

// expectedBranchInfo holds the branch and worktree that an indexing operation
// expects to remain constant. Stored in atomic.Value for lock-free reads
// during the indexing loop.
type expectedBranchInfo struct {
	branch   string
	worktree string
}

// Indexer ties together the walker, chunker, embedder, storage, and llama manager
// into a single pipeline. Supports periodic memory freeing (SaveAndFree + llama restart)
// to keep memory bounded during large indexing operations.
type Indexer struct {
	Walker             *walker.Walker
	Chunker            *chunker.Chunker
	Embedder           *embedder.Embedder
	Storage            *storage.Storage
	Llama              *llama.Manager
	MemoryFreeInterval int                // 0 = disabled; save+clear+restart every N files
	MaxMemoryMB        int                // 0 = disabled; llama-server memory threshold in MB
	Graph              *graph.GraphQuery  // knowledge graph (nil if not available)
	Extractor          *graph.Extractor   // AST extractor (nil without onnx tag)
	IgnorePatterns     []string           // active ignore patterns for change detection
	mu                 sync.Mutex
	Running            bool
	Stats              IndexStats
	expected           atomic.Value      // stores *expectedBranchInfo or nil
	cancelReq          atomic.Bool       // set by Cancel() to request mid-index cancellation
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
// Llama may be nil (when no llama-server management is needed).
// memoryFreeInterval: 0 disables periodic memory freeing.
// maxMemoryMB: 0 disables llama-server memory threshold restart.
// ignorePatterns: active ignore patterns for detecting when the ignore list changes.
func New(w *walker.Walker, ch *chunker.Chunker, em *embedder.Embedder, st *storage.Storage, lm *llama.Manager, memoryFreeInterval int, maxMemoryMB int, ignorePatterns ...[]string) *Indexer {
	var patterns []string
	if len(ignorePatterns) > 0 {
		patterns = ignorePatterns[0]
	}
	return &Indexer{
		Walker:             w,
		Chunker:            ch,
		Embedder:           em,
		Storage:            st,
		Llama:              lm,
		MemoryFreeInterval: memoryFreeInterval,
		MaxMemoryMB:        maxMemoryMB,
		IgnorePatterns:     patterns,
		Stats: IndexStats{
			LastIndexed: "never",
		},
	}
}

// WithGraph sets the knowledge graph components on the indexer.
func (idx *Indexer) WithGraph(g *graph.GraphQuery, ext *graph.Extractor) *Indexer {
	idx.Graph = g
	idx.Extractor = ext
	return idx
}

// SetExpectedBranch records the git branch and worktree that the next
// indexing operation expects to remain constant. If the branch changes
// during indexing, the operation is aborted with ErrBranchChanged.
// Call before IndexAll / IndexChanged.
func (idx *Indexer) SetExpectedBranch(branch, worktree string) {
	idx.expected.Store(&expectedBranchInfo{branch: branch, worktree: worktree})
	idx.cancelReq.Store(false)
}

// Cancel requests that a running IndexAll or IndexChanged abort as soon
// as possible. The operation will return ErrBranchChanged.
func (idx *Indexer) Cancel() {
	idx.cancelReq.Store(true)
}

// IndexAll performs a full index: walks all files, then chunks, embeds, and stores
// each file one at a time. Skips files already in the index with matching hashes
// (resume support). Periodically saves, clears in-memory records, and restarts
// llama-server to free memory when MemoryFreeInterval > 0.
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

	var totalChunks int
	var indexedFiles int
	var indexedSinceFree int

	for _, fi := range files {
		// Check for cancellation or branch change before processing each file
		if idx.cancelReq.Load() {
			idx.cancelReq.Store(false)
			slog.Info("full index cancelled by request")
			idx.Storage.SetCommitSHA("")
			idx.Storage.Save()
			return ErrBranchChanged
		}
		if v := idx.expected.Load(); v != nil {
			info := v.(*expectedBranchInfo)
			if info.branch != "" {
				branch := idx.Walker.GetBranch()
				worktree := idx.Walker.GetWorktreeName()
				if branch != info.branch || worktree != info.worktree {
					slog.Info("full index cancelled: branch changed", "expected", info.branch, "got", branch)
					idx.Storage.SetCommitSHA("")
					idx.Storage.Save()
					return ErrBranchChanged
				}
			}
		}

		// Extract symbols and references for the knowledge graph (always, even if
		// the file is already in the vector index — the graph is independent storage).
		if idx.Extractor != nil && idx.Graph != nil {
			content, rErr := os.ReadFile(fi.Path)
			if rErr != nil {
				slog.Warn("graph: read file", "file", fi.RelPath, "error", rErr)
			} else {
				symbols, refs, xErr := idx.Extractor.Extract(
					string(content), fi.Language, fi.Path, fi.RelPath, fi.Hash,
				)
				if xErr != nil {
					slog.Warn("graph: extract", "file", fi.RelPath, "lang", fi.Language, "error", xErr)
				} else if len(symbols) == 0 {
					slog.Debug("graph: no symbols", "file", fi.RelPath, "lang", fi.Language)
				} else {
					if err := idx.Graph.StoreFile(fi.RelPath, symbols, refs); err != nil {
						slog.Warn("graph: store", "file", fi.RelPath, "error", err)
					}
				}
			}
		}

		if idx.Storage.IsFileIndexed(fi.Path, fi.Hash) {
			slog.Debug("skipping already indexed file", "file", fi.RelPath)
			continue
		}

		chunks, err := idx.Chunker.ChunkFile(fi)
		if err != nil {
			slog.Warn("chunk file skipped", "file", fi.RelPath, "error", err)
			continue
		}
		if len(chunks) == 0 {
			continue
		}

		embeddings, err := idx.Embedder.EmbedChunks(chunks)
		if err != nil {
			// If llama crashed, try one restart + retry before giving up
			if idx.Llama != nil && strings.Contains(err.Error(), "connection refused") {
				slog.Warn("llama-server unresponsive, restarting and retrying", "file", fi.RelPath, "error", err)
				if rerr := idx.restartLlama(); rerr != nil {
					slog.Warn("restart after embed failure", "error", rerr)
				}
				embeddings, err = idx.Embedder.EmbedChunks(chunks)
			}
			if err != nil {
				return fmt.Errorf("embed %s: %w", fi.RelPath, err)
			}
		}

		// Remove old chunks for this file (hash changed) before upserting new ones
		idx.Storage.DeleteChunksByPath(fi.Path)

		if err := idx.Storage.UpsertChunks(chunks, embeddings); err != nil {
			return fmt.Errorf("store %s: %w", fi.RelPath, err)
		}

		totalChunks += len(chunks)
		indexedFiles++
		indexedSinceFree++
		slog.Debug("indexed file", "file", fi.RelPath, "chunks", len(chunks))

		// Periodic save for crash resilience
		if indexedFiles%10 == 0 {
			if err := idx.Storage.Save(); err != nil {
				slog.Warn("periodic save", "error", err)
			} else {
				slog.Info("index progress saved", "files", indexedFiles, "chunks", totalChunks)
			}
		}

		// Periodic memory free: save merged gob, clear Go memory, restart llama-server
		if idx.MemoryFreeInterval > 0 && indexedSinceFree >= idx.MemoryFreeInterval {
			slog.Info("memory free threshold reached, freeing memory",
				"files_indexed_so_far", indexedFiles, "interval", idx.MemoryFreeInterval)

			if err := idx.Storage.SaveAndFree(); err != nil {
				slog.Warn("save and free memory", "error", err)
			}

			if idx.Llama != nil {
				if err := idx.restartLlama(); err != nil {
					slog.Warn("restart llama-server after memory free", "error", err)
				}
			}

			indexedSinceFree = 0
			slog.Info("memory freed, continuing indexing", "files_indexed_so_far", indexedFiles)
		}

		// Memory threshold: restart llama-server if its RSS exceeds MaxMemoryMB
		if idx.MaxMemoryMB > 0 && idx.Llama != nil {
			usage, err := idx.Llama.MemoryUsageMB()
			if err != nil {
				slog.Debug("check memory threshold", "error", err)
			} else if usage >= uint64(idx.MaxMemoryMB) {
				slog.Warn("llama-server memory threshold exceeded, saving and restarting",
					"usage_mb", usage, "threshold_mb", idx.MaxMemoryMB)
				if err := idx.Storage.SaveAndFree(); err != nil {
					slog.Warn("save and free before memory restart", "error", err)
				}
				if err := idx.restartLlama(); err != nil {
					slog.Warn("restart after memory threshold", "error", err)
				}
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
		// Check for cancellation or branch change before processing each file
		if idx.cancelReq.Load() {
			idx.cancelReq.Store(false)
			slog.Info("incremental index cancelled by request")
			idx.Storage.Save()
			return ErrBranchChanged
		}
		if v := idx.expected.Load(); v != nil {
			info := v.(*expectedBranchInfo)
			if info.branch != "" {
				branch := idx.Walker.GetBranch()
				worktree := idx.Walker.GetWorktreeName()
				if branch != info.branch || worktree != info.worktree {
					slog.Info("incremental index cancelled: branch changed", "expected", info.branch, "got", branch)
					idx.Storage.Save()
					return ErrBranchChanged
				}
			}
		}

		if fi.Deleted {
			idx.Storage.DeleteChunksByPath(fi.Path)
			if idx.Graph != nil {
				idx.Graph.RemoveFile(fi.RelPath)
			}
			slog.Info("removed deleted file from index", "file", fi.RelPath)
			continue
		}

		// Extract symbols and references for the knowledge graph
		if idx.Extractor != nil && idx.Graph != nil {
			content, rErr := os.ReadFile(fi.Path)
			if rErr != nil {
				slog.Warn("graph: read file", "file", fi.RelPath, "error", rErr)
			} else {
				symbols, refs, xErr := idx.Extractor.Extract(
					string(content), fi.Language, fi.Path, fi.RelPath, fi.Hash,
				)
				if xErr != nil {
					slog.Warn("graph: extract", "file", fi.RelPath, "lang", fi.Language, "error", xErr)
				} else if len(symbols) == 0 {
					slog.Debug("graph: no symbols", "file", fi.RelPath, "lang", fi.Language)
				} else {
					if err := idx.Graph.StoreFile(fi.RelPath, symbols, refs); err != nil {
						slog.Warn("graph: store", "file", fi.RelPath, "error", err)
					}
				}
			}
		}

		// Skip expensive embedding if the file hash hasn't changed
		if idx.Storage.IsFileIndexed(fi.Path, fi.Hash) {
			slog.Debug("file already up to date, skipping", "file", fi.RelPath)
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

// PruneStaleEntries removes chunks and graph entries for files that no longer exist on disk.
func (idx *Indexer) PruneStaleEntries() {
	for _, relPath := range idx.Storage.ListFiles() {
		fullPath := filepath.Join(idx.Walker.Root, relPath)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			idx.Storage.DeleteChunksByPath(fullPath)
			if idx.Graph != nil {
				idx.Graph.RemoveFile(relPath)
			}
			slog.Info("pruned stale entry", "file", relPath)
		}
	}
}

// computeIgnoreHash returns a SHA256 hash of the active ignore patterns and .gitignore content.
// Used to detect when ignore rules change, triggering a reindex of newly unignored files.
func (idx *Indexer) computeIgnoreHash() string {
	h := sha256.New()
	sorted := append([]string{}, idx.IgnorePatterns...)
	sort.Strings(sorted)
	for _, p := range sorted {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	gitignorePath := filepath.Join(idx.Walker.Root, ".gitignore")
	if data, err := os.ReadFile(gitignorePath); err == nil {
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CheckIgnoreHash compares the current ignore patterns hash against the stored hash.
// Returns true if the ignore rules changed since the last index, signaling callers
// to trigger a full reindex so newly unignored files are picked up.
func (idx *Indexer) CheckIgnoreHash() bool {
	if len(idx.IgnorePatterns) == 0 {
		return false
	}
	currentHash := idx.computeIgnoreHash()
	storedHash := idx.Storage.GetIgnoredFilesHash()
	if storedHash == "" {
		idx.Storage.SetIgnoredFilesHash(currentHash)
		return false
	}
	if storedHash != currentHash {
		slog.Info("ignore patterns changed, triggering reindex",
			"old_hash", storedHash, "new_hash", currentHash)
		idx.Storage.SetIgnoredFilesHash(currentHash)
		return true
	}
	return false
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

// restartLlama attempts to force-restart llama-server to free its memory.
// Errors are logged as warnings and indexing continues without the restart.
func (idx *Indexer) restartLlama() error {
	if idx.Llama == nil {
		return nil
	}
	return idx.Llama.ForceRestart()
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
