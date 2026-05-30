// Package storage provides persistent vector storage with gob serialization,
// L2-normalized vectors (so cosine similarity = dot product), branch-isolated indices,
// and BM25 inverted index for hybrid search. All vectors are normalized at store time.
package storage

import (
	"encoding/gob"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/storage/simd"
)

// ChunkRecord is a persisted chunk with its embedding vector, file metadata, and line range.
type ChunkRecord struct {
	ID        string
	FilePath  string
	RelPath   string
	Language  string
	StartLine int
	EndLine   int
	Content   string
	FileHash  string
	Vector    []float32
}

// ChunkRecordLegacy is used for backward-compatible loading of gob files
// that were serialized with []float64 vectors before the float32 migration.
type ChunkRecordLegacy struct {
	ID        string
	FilePath  string
	RelPath   string
	Language  string
	StartLine int
	EndLine   int
	Content   string
	FileHash  string
	Vector    []float64
}

// SearchResult is the public search result with score for ranking.
type SearchResult struct {
	ID        string  `json:"id"`
	FilePath  string  `json:"file_path"`
	RelPath   string  `json:"rel_path"`
	Language  string  `json:"language"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Content   string  `json:"content"`
	Score     float64 `json:"score"`
}

// StorageFormatVersion is the current on-disk format version for the vector index.
// Increment when making breaking changes to StorageData, ChunkRecord, or the
// embedding pipeline (chunking logic, vector dimensions, normalization, etc.).
// Old-format files (Version==0) are treated as compatible and do NOT force reindex.
const StorageFormatVersion = 1

// GraphFormatVersion is the current on-disk format version for the graph index.
// Increment when making breaking changes to Symbol, Reference, FileData, or the
// extraction pipeline (Tree-sitter grammars, AST traversal, symbol structure).
// Old-format directories (without version.json) are treated as compatible.
const GraphFormatVersion = 1

// StorageData is the on-disk format: records + commit SHA for git tracking.
type StorageData struct {
	Version          int
	Records          []ChunkRecord
	CommitSHA        string
	Trigrams         *TrigramData // persisted trigram index; nil on legacy files
	IgnoredFilesHash string       // hash of ignore patterns for change detection
}

// Storage manages chunk records with O(1) lookups by ID and file path,
// branch-isolated persistence (via filename suffix), an optional BM25 index
// for hybrid search, and a lightweight fileIndex (path→hash) for memory-free
// resume support. Thread-safe via sync.RWMutex.
//
// Disk format: a single gob file per branch:
//
//	~/.go-mcp/indexing/vectors/{encoded-project}/
//	  vectors.gob                     (main branch)
//	  vectors-{worktree}-{branch}.gob (other branches)
type Storage struct {
	path             string // current gob file path (may include branch suffix)
	pathPrefix       string // base path without .gob, used for branch switching
	dimensions       int
	mu               sync.RWMutex
	records          []ChunkRecord
	commitSHA        string
	ignoredFilesHash string
	diskVersion      int // version read from disk (0 = pre-versioning or compatible)
	byID           map[string]int
	byPath         map[string][]int
	dirty          bool
	recordsPartial bool // true after SaveAndFree; saveLocked must merge with disk
	bm25           *bm25Index
	fileIndex      map[string]string // filePath → fileHash (survives SaveAndFree)
	vecIndex       VectorIndex       // lazy-built vector index (brute-force or cover tree)
	vecCache       *IndexCacheEntry  // concurrent build coordination
	indexKind      IndexKind         // "auto", "bruteforce", "cover"
	trigrams       *trigramIndex     // lazy-built trigram index for grep pre-filtering
}

// New creates or opens a Storage with the given gob file path and vector dimensions.
// The directory is created if it doesn't exist.
func New(path string, dimensions int) (*Storage, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}

	s := &Storage{
		path:           path,
		pathPrefix:     strings.TrimSuffix(path, ".gob"),
		dimensions:     dimensions,
		byID:           make(map[string]int),
		byPath:         make(map[string][]int),
		fileIndex:      make(map[string]string),
		vecCache:       NewIndexCacheEntry(),
		recordsPartial: false,
	}

	if err := s.load(); err != nil {
		return nil, fmt.Errorf("load storage: %w", err)
	}

	s.dirty = false
	return s, nil
}

// UpsertChunks inserts or updates chunks. Vectors are L2-normalized before storing.
// Invalidates the BM25 cache and updates the lightweight fileIndex for resume support.
func (s *Storage) UpsertChunks(chunks []chunker.Chunk, embeddings map[string][]float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, ch := range chunks {
		emb, ok := embeddings[ch.ID]
		if !ok {
			continue
		}
		normalize32(emb)

		if idx, exists := s.byID[ch.ID]; exists {
			s.records[idx] = ChunkRecord{
				ID:        ch.ID,
				FilePath:  ch.FilePath,
				RelPath:   ch.RelPath,
				Language:  ch.Language,
				StartLine: ch.StartLine,
				EndLine:   ch.EndLine,
				Content:   ch.Content,
				FileHash:  ch.FileHash,
				Vector:    emb,
			}
		} else {
			idx := len(s.records)
			s.records = append(s.records, ChunkRecord{
				ID:        ch.ID,
				FilePath:  ch.FilePath,
				RelPath:   ch.RelPath,
				Language:  ch.Language,
				StartLine: ch.StartLine,
				EndLine:   ch.EndLine,
				Content:   ch.Content,
				FileHash:  ch.FileHash,
				Vector:    emb,
			})
			s.byID[ch.ID] = idx
		}
	}

	if len(chunks) > 0 {
		if s.fileIndex == nil {
			s.fileIndex = make(map[string]string)
		}
		s.fileIndex[chunks[0].FilePath] = chunks[0].FileHash
	}

	s.dirty = true
	s.bm25 = nil
	s.trigrams = nil
	s.vecCache.Invalidate()
	return nil
}

// DeleteChunksByPath removes all chunks belonging to a file path.
// Rebuilds the byID/byPath indices, invalidates BM25 cache, and updates fileIndex.
func (s *Storage) DeleteChunksByPath(filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	indices, ok := s.byPath[filePath]
	if !ok {
		indices = s.findIndicesByPath(filePath)
	}
	if len(indices) == 0 {
		return nil
	}

	del := make(map[int]bool)
	for _, idx := range indices {
		del[idx] = true
	}

	var kept []ChunkRecord
	for i, rec := range s.records {
		if !del[i] {
			kept = append(kept, rec)
		}
	}

	s.rebuildIndex(kept)
	s.dirty = true
	s.bm25 = nil
	s.trigrams = nil
	s.vecCache.Invalidate()

	return nil
}

// Search performs a pure vector similarity search using dot product on normalized vectors.
// Returns up to `limit` results ranked by similarity score.
func (s *Storage) Search(query []float32, limit int) ([]SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.searchLocked(query, limit)
}

// Stats returns the total number of chunks and unique files in the index.
func (s *Storage) Stats() (chunks, files int, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fileSet := make(map[string]bool)
	for _, rec := range s.records {
		fileSet[rec.FilePath] = true
	}

	return len(s.records), len(fileSet), nil
}

// ListFiles returns the unique relative file paths in the index.
func (s *Storage) ListFiles() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]bool)
	var files []string
	for _, rec := range s.records {
		if !seen[rec.RelPath] {
			seen[rec.RelPath] = true
			files = append(files, rec.RelPath)
		}
	}
	return files
}

// sanitizeName replaces filesystem-unsafe characters in branch/worktree names
// with hyphens, preventing unintended subdirectory creation.
func sanitizeName(s string) string {
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, "..", "-")
	return s
}

// BranchSuffix builds the filename suffix for non-main git branches.
// Returns "" for main, "-{worktree}-{branch}" for other branches.
// Branch and worktree names are sanitized to prevent subdirectory injection
// (e.g. "feature/HU-123" → "feature-HU-123").
// Exported for branch seeding in MCPServer.
func BranchSuffix(branch, worktree string) string {
	var parts []string
	w := sanitizeName(worktree)
	b := sanitizeName(branch)
	if w != "" {
		parts = append(parts, w)
	}
	if b != "" && b != "main" {
		parts = append(parts, b)
	}
	if len(parts) == 0 {
		return ""
	}
	return "-" + strings.Join(parts, "-")
}

// GobPath returns the full gob file path for a given branch/worktree,
// using the storage's base path prefix. Used by MCPServer to check file
// existence and copy gob data during branch seeding.
func (s *Storage) GobPath(branch, worktree string) string {
	return s.pathPrefix + BranchSuffix(branch, worktree) + ".gob"
}

// SwitchBranch persists the current index and loads the index for a different
// git branch/worktree. Uses a branch-specific gob filename:
// vectors.gob (main) or vectors-{worktree}-{branch}.gob (other branches).
func (s *Storage) SwitchBranch(branch string, worktree string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dirty {
		if err := s.saveLocked(false); err != nil {
			return fmt.Errorf("save current branch: %w", err)
		}
	}

	suffix := BranchSuffix(branch, worktree)
	s.path = s.pathPrefix + suffix + ".gob"

	s.records = nil
	s.byID = make(map[string]int)
	s.byPath = make(map[string][]int)
	s.commitSHA = ""
	s.bm25 = nil
	s.trigrams = nil
	s.fileIndex = make(map[string]string)
	s.vecCache.Invalidate()
	s.recordsPartial = false

	return s.load()
}

// Close persists the dirty index to disk.
func (s *Storage) Close() error {
	return s.save()
}

// Save flushes the current state to disk immediately.
func (s *Storage) Save() error {
	return s.save()
}

// SavePeriodic is like Save but skips trigram persistence.
// Trigrams are expensive to rebuild (full content scan) and only needed by grep
// queries, which never run during active indexing. Periodic saves during
// indexing skip trigrams to avoid multi-second pauses. The final save after
// indexing persists trigrams so grep works on reload.
func (s *Storage) SavePeriodic() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(true)
}

// SaveSnapshot copies all records under the lock (fast, ~3ms for 10k records),
// releases the lock immediately, then serializes the copy to disk in the
// background WITHOUT holding the lock. This means the main indexing goroutine
// never blocks on serialization (gob encoding 30MB+ of embeddings).
//
// Safe because Go strings are immutable and []float32 embeddings are never
// modified in place — UpsertChunks always creates new records. The on-disk
// state is at most one batch behind memory, which is fine for crash recovery.
func (s *Storage) SaveSnapshot() error {
	s.mu.Lock()

	if !s.dirty && !s.recordsPartial {
		s.mu.Unlock()
		return nil
	}

	var recordsToWrite []ChunkRecord
	sha := s.commitSHA

	if s.recordsPartial {
		diskData := s.readDiskStateLocked()
		merged := make(map[string]ChunkRecord, len(diskData.Records)+len(s.records))
		for _, rec := range diskData.Records {
			merged[rec.ID] = rec
		}
		for _, rec := range s.records {
			merged[rec.ID] = rec
		}
		recordsToWrite = make([]ChunkRecord, 0, len(merged))
		for _, rec := range merged {
			recordsToWrite = append(recordsToWrite, rec)
		}
		if sha == "" {
			sha = diskData.CommitSHA
		}
	} else {
		recordsToWrite = make([]ChunkRecord, len(s.records))
		copy(recordsToWrite, s.records)
	}
	ihash := s.ignoredFilesHash

	s.dirty = false
	s.mu.Unlock()

	// Serialize WITHOUT holding the lock (this is the expensive part)
	data := StorageData{
		Version:          StorageFormatVersion,
		Records:          recordsToWrite,
		CommitSHA:        sha,
		Trigrams:         nil,
		IgnoredFilesHash: ihash,
	}

	tmpPath := s.path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}

	enc := gob.NewEncoder(f)
	if err := enc.Encode(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("encode: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	// Retry rename on Windows — file handles can transiently hold the lock
	// after closing, causing ERROR_ACCESS_DENIED on rename.
	for retries := 0; retries < 3; retries++ {
		if err := os.Rename(tmpPath, s.path); err != nil {
			if retries < 2 {
				time.Sleep(time.Duration(10*(retries+1)) * time.Millisecond)
				continue
			}
			return fmt.Errorf("rename after %d retries: %w", retries, err)
		}
		break
	}
	return nil
}

// NeedsReindex returns true when the on-disk format version differs from the
// current code version, indicating that a breaking change requires a full reindex.
// Returns false for version 0 (pre-versioning format, treated as compatible).
func (s *Storage) NeedsReindex() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.diskVersion > 0 && s.diskVersion != StorageFormatVersion
}

// ClearAll removes all in-memory records and resets the disk state, effectively
// clearing the entire index. Used before a full reindex triggered by version mismatch.
func (s *Storage) ClearAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.records = nil
	s.byID = make(map[string]int)
	s.byPath = make(map[string][]int)
	s.fileIndex = make(map[string]string)
	s.bm25 = nil
	s.trigrams = nil
	s.vecCache.Invalidate()
	s.vecIndex = nil
	s.commitSHA = ""
	s.diskVersion = 0
	s.dirty = false
	s.recordsPartial = false

	// Remove the gob file from disk
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove storage file: %w", err)
	}
	return nil
}

// IsFileIndexed checks if all chunks for a file are already in the index
// with matching hashes. Used to skip already-indexed files on resume after crash.
func (s *Storage) IsFileIndexed(filePath, fileHash string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.fileIndex != nil {
		if hash, ok := s.fileIndex[filePath]; ok {
			return hash == fileHash
		}
	}

	indices, ok := s.byPath[filePath]
	if !ok {
		return false
	}
	for _, idx := range indices {
		if s.records[idx].FileHash != fileHash {
			return false
		}
	}
	return true
}

// SetCommitSHA records the git commit SHA that the current index represents.
func (s *Storage) SetCommitSHA(sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commitSHA = sha
	s.dirty = true
}

// GetCommitSHA returns the last indexed git commit SHA for change detection.
func (s *Storage) GetCommitSHA() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.commitSHA
}

// SetIgnoredFilesHash records the hash of current ignore patterns for change detection.
func (s *Storage) SetIgnoredFilesHash(hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ignoredFilesHash = hash
	s.dirty = true
}

// LoadGob reads a gob file from the given path and returns its decoded
// StorageData. Returns nil on any error. Used by MCPServer to inspect
// branch gobs during branch seeding.
func LoadGob(gobPath string) *StorageData {
	f, err := os.Open(gobPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	var data StorageData
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&data); err != nil {
		return nil
	}
	return &data
}

// GetIgnoredFilesHash returns the stored ignore pattern hash.
func (s *Storage) GetIgnoredFilesHash() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ignoredFilesHash
}

// load reads the index from disk. Tries the current gob file path, then
// falls back to legacy formats for backward compatibility.
func (s *Storage) load() error {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return nil // corrupt or missing, start fresh
	}
	defer f.Close()

	var data StorageData
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&data); err == nil {
		s.rebuildIndex(data.Records)
		s.commitSHA = data.CommitSHA
		s.ignoredFilesHash = data.IgnoredFilesHash
		s.diskVersion = data.Version
		s.recordsPartial = false

		// Restore persisted trigram index if available and still valid
		if data.Trigrams != nil && data.Trigrams.DocCount == len(data.Records) {
			s.trigrams = &trigramIndex{index: data.Trigrams.Index}
		}
		return nil
	}

	f.Seek(0, 0)
	var records []ChunkRecord
	dec = gob.NewDecoder(f)
	if err := dec.Decode(&records); err != nil {
		f.Seek(0, 0)
		var legacy []ChunkRecordLegacy
		dec = gob.NewDecoder(f)
		if err := dec.Decode(&legacy); err != nil {
			return nil // corrupt file, start fresh
		}
		converted := make([]ChunkRecord, len(legacy))
		for i, rec := range legacy {
			vec32 := make([]float32, len(rec.Vector))
			for j, v := range rec.Vector {
				vec32[j] = float32(v)
			}
			converted[i] = ChunkRecord{
				ID: rec.ID, FilePath: rec.FilePath, RelPath: rec.RelPath,
				Language: rec.Language, StartLine: rec.StartLine, EndLine: rec.EndLine,
				Content: rec.Content, FileHash: rec.FileHash, Vector: vec32,
			}
		}
		s.rebuildIndex(converted)
	} else {
		s.rebuildIndex(records)
	}
	s.diskVersion = 0 // pre-versioning format
	s.recordsPartial = false // records freshly loaded from disk = complete
	return nil
}

// save persists dirty state to disk. Thread-safe.
func (s *Storage) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(false)
}

// saveLocked writes all records to the single gob file atomically.
// When recordsPartial is true (after SaveAndFree), it reads the existing
// gob from disk and merges with in-memory records to avoid data loss.
// When skipTrigrams is true, the trigram index is not rebuilt/persisted
// (safe during indexing since grep queries never run mid-index).
// Caller must hold s.mu write lock.
func (s *Storage) saveLocked(skipTrigrams bool) error {
	if !s.dirty {
		return nil
	}

	// Determine which records to persist
	var recordsToWrite []ChunkRecord
	commitSHA := s.commitSHA

	if s.recordsPartial {
		// Read existing disk records and merge with partial in-memory records
		diskData := s.readDiskStateLocked()
		merged := make(map[string]ChunkRecord, len(diskData.Records)+len(s.records))
		for _, rec := range diskData.Records {
			merged[rec.ID] = rec
		}
		for _, rec := range s.records {
			merged[rec.ID] = rec
		}
		recordsToWrite = make([]ChunkRecord, 0, len(merged))
		for _, rec := range merged {
			recordsToWrite = append(recordsToWrite, rec)
		}
		if commitSHA == "" {
			commitSHA = diskData.CommitSHA
		}
	} else {
		recordsToWrite = s.records
	}

	var trigramData *TrigramData
	if !skipTrigrams {
		if s.trigrams == nil {
			s.trigrams = buildTrigramIndex(recordsToWrite)
		}
		trigramData = &TrigramData{
			Index:    s.trigrams.index,
			DocCount: len(recordsToWrite),
		}
	}

	data := StorageData{
		Version:          StorageFormatVersion,
		Records:          recordsToWrite,
		CommitSHA:        commitSHA,
		Trigrams:         trigramData,
		IgnoredFilesHash: s.ignoredFilesHash,
	}

	tmpPath := s.path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	enc := gob.NewEncoder(f)
	if err := enc.Encode(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	if err := os.Rename(tmpPath, s.path); err != nil {
		return err
	}

	s.dirty = false
	return nil
}

// readDiskStateLocked reads the current gob file from disk and returns
// the stored records and commit SHA. Returns empty data on any error.
// Caller must hold s.mu write lock (or ensure no concurrent writes).
func (s *Storage) readDiskStateLocked() StorageData {
	f, err := os.Open(s.path)
	if err != nil {
		return StorageData{}
	}
	defer f.Close()

	var data StorageData
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&data); err != nil {
		return StorageData{}
	}
	return data
}

// SaveAndFree persists all records to disk, then clears in-memory state
// to free Go memory. The lightweight fileIndex is preserved so
// IsFileIndexed continues to work for resume support.
func (s *Storage) SaveAndFree() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.saveLocked(false); err != nil {
		return err
	}

	if s.fileIndex == nil || len(s.fileIndex) == 0 {
		s.fileIndex = make(map[string]string, len(s.records))
		for _, rec := range s.records {
			s.fileIndex[rec.FilePath] = rec.FileHash
		}
	}

	s.records = nil
	s.byID = make(map[string]int)
	s.byPath = make(map[string][]int)
	s.bm25 = nil
	s.trigrams = nil
	s.vecCache.Invalidate()
	s.recordsPartial = true
	s.dirty = false

	return nil
}

// rebuildIndex replaces all in-memory data structures with the given records,
// rebuilding byID, byPath, and fileIndex lookup maps. Invalidates the BM25 cache.
func (s *Storage) rebuildIndex(records []ChunkRecord) {
	s.records = records
	s.byID = make(map[string]int)
	s.byPath = make(map[string][]int)
	s.bm25 = nil
	s.trigrams = nil
	s.vecCache.Invalidate()
	s.fileIndex = make(map[string]string, len(records))

	for i, rec := range records {
		s.byID[rec.ID] = i
		s.byPath[rec.FilePath] = append(s.byPath[rec.FilePath], i)
		s.fileIndex[rec.FilePath] = rec.FileHash
	}
}

// findIndicesByPath does a linear scan to find all chunk indices for a file path.
// Used as fallback when byPath map doesn't have the entry.
func (s *Storage) findIndicesByPath(filePath string) []int {
	var indices []int
	for i, rec := range s.records {
		if rec.FilePath == filePath || rec.RelPath == filePath {
			indices = append(indices, i)
		}
	}
	return indices
}

// normalize32 L2-normalizes a float32 vector in place. After normalization, dot product
// equals cosine similarity, enabling efficient SIMD-accelerated similarity search.
func normalize32(v []float32) {
	var norm float64
	for i := range v {
		norm += float64(v[i]) * float64(v[i])
	}
	if norm == 0 {
		return
	}
	invNorm := 1.0 / math.Sqrt(norm)
	for i := range v {
		v[i] = float32(float64(v[i]) * invNorm)
	}
}

// dotProduct32 delegates to the SIMD-accelerated (or scalar fallback) dot product.
func dotProduct32(a, b []float32) float32 {
	return simd.Dot32(a, b)
}

// resolveIndexKind selects the appropriate index backend based on dataset size
// and the configured preference (if any). Thresholds (4000 docs, 64 dims, density 16)
// match sqlite-vec's auto-select heuristics for cover tree vs brute force.
func (s *Storage) resolveIndexKind() IndexKind {
	if s.indexKind != "" && s.indexKind != IndexKindAuto {
		return s.indexKind
	}
	n := len(s.records)
	if n < 4000 {
		return IndexKindBruteForce
	}
	if n == 0 {
		return IndexKindBruteForce
	}
	dim := len(s.records[0].Vector)
	if dim < 64 {
		return IndexKindBruteForce
	}
	density := float64(n) / float64(dim)
	if density < 16 {
		return IndexKindBruteForce
	}
	return IndexKindCover
}

// ensureVecIndex builds the vector index lazily with sync.Cond coordination.
// Concurrent calls serialize through IndexCacheEntry so only one build happens.
func (s *Storage) ensureVecIndex() error {
	idx, err := s.vecCache.GetOrBuild(func() (VectorIndex, error) {
		kind := s.resolveIndexKind()
		var idx VectorIndex
		switch kind {
		case IndexKindCover:
			idx = NewCoverIndex(defaultCoverBase, CosineDistance)
		default:
			idx = NewBruteForceIndex()
		}
		if err := idx.Build(s.records); err != nil {
			return nil, err
		}
		return idx, nil
	})
	if err == nil {
		s.vecIndex = idx
	}
	return err
}
