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
	"time"

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
	Extractor          *graph.Extractor   // AST extractor (nil without CGO_ENABLED=1)
	IgnorePatterns     []string           // active ignore patterns for change detection
	PendingGraph       []walker.FileInfo  // files queued for tree-sitter extraction (Phase 2)
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

// IndexAll performs a full index: walks all files, chunks, embeds, and stores.
// Skips files already in the index with matching hashes (resume support). Graph
// extraction (tree-sitter) is deferred to PendingGraph for later processing by
// RunGraphExtraction(), so vector indexing is never blocked by slow tree-sitter
// parsing. Uses SkipAST mode (regex structural chunking, no tree-sitter) during
// the hot path to avoid CPU-bound gaps that stall llama-server. Cross-file
// embedding batching accumulates up to 32 chunks before sending to llama-server,
// reducing per-file HTTP overhead and keeping the embedding endpoint busy longer.
// Periodically saves, clears in-memory records, and restarts llama-server to
// free memory when MemoryFreeInterval > 0.
func (idx *Indexer) IndexAll() error {
	idx.mu.Lock()
	if idx.Running {
		idx.mu.Unlock()
		return fmt.Errorf("indexing already in progress")
	}
	// Skip if another process is actively writing to the database
	if idx.Storage != nil && idx.Storage.IsLocked() {
		idx.mu.Unlock()
		slog.Warn("index: database locked by another process, skipping full index")
		return nil
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

	slog.Info("starting index")

	files, err := idx.Walker.Walk()
	if err != nil {
		return fmt.Errorf("walk files: %w", err)
	}

	slog.Info("found files to index", "count", len(files))

	tPrune := time.Now()
	idx.PruneStaleEntries()
	slog.Info("prune stale entries",
		"ms", time.Since(tPrune).Milliseconds(),
		"storage_files", len(idx.Storage.ListFiles()),
	)

	var totalChunks int
	var indexedFiles int
	var indexedSinceFree int
	var pendingGraph []walker.FileInfo
	processedFiles := 0
	loopStart := time.Now()

	// windowFiles tracks files indexed since the last branch check, so we can
	// discard them if the branch changes mid-window.
	var windowFiles []walker.FileInfo

	// Option A: skip tree-sitter AST during indexing hot path.
	// Use regex structural + sliding window for chunking instead.
	// Tree-sitter is still used for graph extraction (RunGraphExtraction).
	idx.Chunker.SkipAST = true

	// Option C: cross-file embedding batching.
	// Accumulate chunks from multiple files before sending to llama-server,
	// reducing per-file HTTP overhead and keeping llama-server busy longer.
	const crossFileBatchChunks = 32

	type pendingFile struct {
		fi     walker.FileInfo
		chunks []chunker.Chunk
	}

	var batchChunks []chunker.Chunk
	var batchFiles []pendingFile

	// saveWg tracks background periodic saves. SaveSnapshot copies records
	// under the lock (fast, ~3ms) then serializes WITHOUT holding the lock,
	// so the main goroutine never blocks on serialization.
	var saveWg sync.WaitGroup
	var saveInFlight atomic.Bool

	flushBatch := func() error {
		if len(batchChunks) == 0 {
			return nil
		}

		embeddings, err := idx.Embedder.EmbedChunks(batchChunks)
		if err != nil {
			if idx.Llama != nil && strings.Contains(err.Error(), "connection refused") {
				slog.Warn("llama-server unresponsive, restarting and retrying batch",
					"chunks", len(batchChunks))
				if rerr := idx.restartLlama(); rerr != nil {
					slog.Warn("restart after embed failure", "error", rerr)
				}
				embeddings, err = idx.Embedder.EmbedChunks(batchChunks)
			}
			if err != nil {
				return fmt.Errorf("embed batch of %d chunks: %w", len(batchChunks), err)
			}
		}

		var wantsSave bool
		for _, pf := range batchFiles {
			fileEmb := make(map[string][]float32, len(pf.chunks))
			for _, ch := range pf.chunks {
				if v, ok := embeddings[ch.ID]; ok {
					fileEmb[ch.ID] = v
				}
			}

			idx.Storage.DeleteChunksByPath(pf.fi.Path)
			if err := idx.Storage.UpsertChunks(pf.chunks, fileEmb); err != nil {
				return fmt.Errorf("store %s: %w", pf.fi.RelPath, err)
			}
			windowFiles = append(windowFiles, pf.fi)
			totalChunks += len(pf.chunks)
			indexedFiles++
			indexedSinceFree++

			if indexedFiles%10 == 0 {
				wantsSave = true
			}

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

			if idx.MaxMemoryMB > 0 && idx.Llama != nil {
				usage, mErr := idx.Llama.MemoryUsageMB()
				if mErr != nil {
					slog.Debug("check memory threshold", "error", mErr)
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

		// Start async periodic save in background. SaveSnapshot copies
		// records under the lock (~3ms) then serializes WITHOUT holding the
		// lock, so the main goroutine never blocks. The serialization runs
		// during the next batch's ChunkFile + EmbedChunks (HTTP wait).
		if wantsSave && saveInFlight.CompareAndSwap(false, true) {
			f := indexedFiles
			saveWg.Add(1)
			go func() {
				defer saveWg.Done()
				defer saveInFlight.Store(false)
				if err := idx.Storage.SaveSnapshot(); err != nil {
					slog.Warn("async periodic save", "error", err)
				} else {
					slog.Info("index progress saved", "files", f)
				}
			}()
		}

		batchChunks = batchChunks[:0]
		batchFiles = batchFiles[:0]
		return nil
	}

	for _, fi := range files {
		processedFiles++

		if processedFiles%500 == 0 {
			slog.Info("index progress",
				"processed", processedFiles,
				"total", len(files),
				"indexed", indexedFiles,
				"elapsed_s", int(time.Since(loopStart).Seconds()),
			)
		}
		// Check for cancellation on every file (atomic, cheap)
		if idx.cancelReq.Load() {
			idx.cancelReq.Store(false)
			slog.Info("full index cancelled by request")
			idx.Storage.SetCommitSHA("")
			idx.Storage.Save()
			return ErrBranchChanged
		}

		alreadyIndexed := idx.Storage.IsFileIndexed(fi.Path, fi.Hash)
		alreadyExtracted := idx.Graph != nil && idx.Graph.HasFile(fi.RelPath)

		if alreadyIndexed && alreadyExtracted {
			slog.Debug("skipping already indexed file", "file", fi.RelPath)
			continue
		}

		// Defer graph extraction — re-extract if graph is missing or content changed
		if !alreadyExtracted || !alreadyIndexed {
			pendingGraph = append(pendingGraph, fi)
		}

		if alreadyIndexed {
			continue
		}

		// Branch check only for files that will actually be indexed (git is expensive)
		if processedFiles%20 == 0 {
			v := idx.expected.Load()
			if v != nil {
				info := v.(*expectedBranchInfo)
				if info.branch != "" {
					branch := idx.Walker.GetBranch()
					worktree := idx.Walker.GetWorktreeName()
					if branch != info.branch || worktree != info.worktree {
						// Discard files indexed since the last good check — they may
						// be from the old or new branch, we can't tell which.
						discarded := len(windowFiles)
						for _, wf := range windowFiles {
							idx.Storage.DeleteChunksByPath(wf.Path)
							slog.Debug("discarded file from interrupted window", "file", wf.RelPath)
						}
						windowFiles = nil
						slog.Info("full index cancelled: branch changed", "expected", info.branch, "got", branch,
							"discarded", discarded)
						idx.Storage.SetCommitSHA("")
						idx.Storage.Save()
						return ErrBranchChanged
					}
				}
			}
			// Check passed — safe to commit, clear window
			windowFiles = nil
		}

		tChunk := time.Now()
		chunks, err := idx.Chunker.ChunkFile(fi)
		tChunkDone := time.Now()
		chunkDur := tChunkDone.Sub(tChunk)
		if err != nil {
			slog.Warn("chunk file skipped", "file", fi.RelPath, "error", err, "chunk_ms", chunkDur.Milliseconds())
			continue
		}
		if len(chunks) == 0 {
			continue
		}
		if chunkDur > time.Second {
			slog.Warn("slow chunk", "file", fi.RelPath, "lang", fi.Language, "ms", chunkDur.Milliseconds())
		}

		batchChunks = append(batchChunks, chunks...)
		batchFiles = append(batchFiles, pendingFile{fi: fi, chunks: chunks})

		if len(batchChunks) >= crossFileBatchChunks {
			if err := flushBatch(); err != nil {
				return err
			}
		}
	}

	// Flush any remaining batch
	if err := flushBatch(); err != nil {
		return err
	}

	// Wait for all background saves before the final save (with trigrams)
	saveWg.Wait()

	idx.mu.Lock()
	idx.Stats.TotalChunks = totalChunks
	idx.Stats.TotalFiles = len(files)
	idx.Stats.LastIndexed = "just now"
	idx.mu.Unlock()

	idx.PendingGraph = pendingGraph

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
	// Skip if another process is actively writing to the database
	if idx.Storage != nil && idx.Storage.IsLocked() {
		slog.Warn("index: database locked by another process, skipping incremental index")
		return nil
	}

	sinceSHA := idx.Storage.GetCommitSHA()
	files, err := idx.Walker.GetChangedFiles(sinceSHA)
	if err != nil {
		return fmt.Errorf("get changed files: %w", err)
	}

	if len(files) == 0 {
		return nil
	}

	// Skip AST (tree-sitter) during chunking — regex structural is fast enough
	// and avoids CPU pauses that stall llama-server between embedding batches.
	idx.Chunker.SkipAST = true

	// First pass: filter out already-indexed files before the expensive chunking phase
	var pendingGraph []walker.FileInfo
	var needsIndex []walker.FileInfo
	processedFiles := 0
	var indexedCount int
	var deletedCount int

	for _, fi := range files {
		if fi.Deleted {
			idx.Storage.DeleteChunksByPath(fi.Path)
			if idx.Graph != nil {
				idx.Graph.RemoveFile(fi.RelPath)
			}
			slog.Info("removed deleted file from index", "file", fi.RelPath)
			deletedCount++
			continue
		}

		alreadyIndexed := idx.Storage.IsFileIndexed(fi.Path, fi.Hash)
		alreadyExtracted := idx.Graph != nil && idx.Graph.HasFile(fi.RelPath)

		if alreadyIndexed && alreadyExtracted {
			slog.Debug("file already up to date, skipping", "file", fi.RelPath)
			continue
		}

		// File needs graph re-extraction if missing OR content changed
		if !alreadyExtracted || !alreadyIndexed {
			pendingGraph = append(pendingGraph, fi)
		}

		if alreadyIndexed {
			continue
		}

		needsIndex = append(needsIndex, fi)
	}

	idx.PendingGraph = pendingGraph

	if len(needsIndex) == 0 && deletedCount == 0 {
		slog.Debug("incremental index: no changes to persist", "files_checked", len(files))
		return nil
	}

	if len(needsIndex) > 0 {
		slog.Info("incremental index", "files", len(needsIndex), "deleted", deletedCount, "since", sinceSHA)

		chunksMap, err := idx.Chunker.ChunkFiles(needsIndex)
		if err != nil {
			return fmt.Errorf("chunk changed files: %w", err)
		}

		for _, fi := range needsIndex {
			processedFiles++

			// Check for cancellation on every file (atomic, cheap)
			if idx.cancelReq.Load() {
				idx.cancelReq.Store(false)
				slog.Info("incremental index cancelled by request")
				if indexedCount > 0 || deletedCount > 0 {
					idx.Storage.SavePeriodic()
				}
				return ErrBranchChanged
			}

			// Branch check only for files that will actually be indexed (git is expensive)
			if processedFiles%20 == 0 {
				v := idx.expected.Load()
				if v != nil {
					info := v.(*expectedBranchInfo)
					if info.branch != "" {
						branch := idx.Walker.GetBranch()
						worktree := idx.Walker.GetWorktreeName()
						if branch != info.branch || worktree != info.worktree {
							slog.Info("incremental index cancelled: branch changed", "expected", info.branch, "got", branch)
							if indexedCount > 0 || deletedCount > 0 {
								idx.Storage.SavePeriodic()
							}
							return ErrBranchChanged
						}
					}
				}
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
			indexedCount++
		}
	}

	headSHA := idx.Walker.GetHeadSHA()
	if headSHA != "" {
		idx.Storage.SetCommitSHA(headSHA)
	}

	// SavePeriodic skips trigram persistence — trigrams are expensive and only
	// needed by grep queries, which build them lazily via ensureTrigrams().
	if err := idx.Storage.SavePeriodic(); err != nil {
		slog.Warn("index changed save", "error", err)
	} else {
		slog.Info("incremental index complete", "files", len(files), "indexed", indexedCount, "deleted", deletedCount, "sha", headSHA)
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

// RunGraphExtraction processes files queued by IndexAll or IndexChanged for
// tree-sitter symbol extraction (Phase 2). This is called explicitly by index
// orchestration paths (startup, watch, reindex) but NOT by search/query/grep,
// so those respond fast without waiting for slow tree-sitter parsing.
func (idx *Indexer) RunGraphExtraction() {
	pendingGraph := idx.PendingGraph
	idx.PendingGraph = nil

	// Detect stale graph data: compare graph_commit_sha with vector commit_sha.
	// If they differ, diff the two commits and re-extract changed files.
	graphSHA := idx.Storage.GetGraphCommitSHA()
	vecSHA := idx.Storage.GetCommitSHA()
	if len(pendingGraph) == 0 && vecSHA != "" && graphSHA != vecSHA {
		slog.Info("graph: commit SHA mismatch, detecting stale files",
			"graph_sha", graphSHA, "vector_sha", vecSHA)

		changedFiles, err := idx.Walker.GetChangedFiles(graphSHA)
		if err == nil {
			for _, fi := range changedFiles {
				if !fi.Deleted {
					pendingGraph = append(pendingGraph, fi)
				}
			}
		}

		// If GetChangedFiles failed, fall back to gap detection
		if len(pendingGraph) == 0 {
			vecFiles := idx.Storage.ListFiles()
			graphFiles, listErr := idx.Graph.ListSymbolFiles()
			if listErr == nil {
				graphSet := make(map[string]bool, len(graphFiles))
				for _, f := range graphFiles {
					graphSet[f] = true
				}
				for _, vf := range vecFiles {
					if !graphSet[vf] {
						if fi, ok := idx.walkerFile(vf); ok {
							pendingGraph = append(pendingGraph, fi)
						}
					}
				}
			}
		}
	}

	if len(pendingGraph) == 0 || idx.Extractor == nil || idx.Graph == nil {
		return
	}
	slog.Info("graph: extracting symbols for pending files", "count", len(pendingGraph))
	for _, fi := range pendingGraph {
		if idx.cancelReq.Load() {
			idx.cancelReq.Store(false)
			slog.Info("graph: extraction cancelled")
			return
		}

		content, rErr := os.ReadFile(fi.Path)
		if rErr != nil {
			slog.Warn("graph: read file", "file", fi.RelPath, "error", rErr)
			continue
		}
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
	slog.Info("graph: extraction complete", "files", len(pendingGraph))

	// Save graph commit SHA to match current vector commit SHA
	if vecSHA != "" {
		idx.Storage.SetGraphCommitSHA(vecSHA)
	}

	// Cross-file reference resolution
	if idx.Graph != nil {
		idx.Graph.ResolveRefs()
	}
}

// walkerFile constructs a walker.FileInfo from a relative path by reading file details.
func (idx *Indexer) walkerFile(relPath string) (walker.FileInfo, bool) {
	fullPath := filepath.Join(idx.Walker.Root, relPath)
	if _, err := os.Stat(fullPath); err != nil {
		return walker.FileInfo{}, false
	}
	lang := detectLanguage(relPath)
	return walker.FileInfo{
		Path:     fullPath,
		RelPath:  relPath,
		Hash:     "",
		Language: lang,
	}, true
}

// detectLanguage maps a file extension to a language name.
func detectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js":
		return "javascript"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".rs":
		return "rust"
	case ".c":
		return "c"
	case ".cpp", ".cc", ".cxx":
		return "cpp"
	case ".php":
		return "php"
	case ".zig":
		return "zig"
	default:
		return ""
	}
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
