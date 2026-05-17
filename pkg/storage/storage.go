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

// StorageData is the on-disk format: records + commit SHA for git tracking.
type StorageData struct {
	Records   []ChunkRecord
	CommitSHA string
}

// Storage manages chunk records with O(1) lookups by ID and file path,
// branch-isolated persistence, an optional BM25 index for hybrid search,
// and a lightweight fileIndex (path→hash) for memory-free resume support.
// Thread-safe via sync.RWMutex.
type Storage struct {
	path       string
	basePath   string
	dimensions int
	mu         sync.RWMutex
	records    []ChunkRecord
	commitSHA  string
	byID       map[string]int
	byPath     map[string][]int
	dirty      bool
	bm25       *bm25Index
	fileIndex  map[string]string // filePath → fileHash (survives SaveAndFree)
}

// New creates or opens a Storage with the given gob file path and vector dimensions.
func New(dbPath string, dimensions int) (*Storage, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}

	s := &Storage{
		path:       dbPath,
		basePath:   dbPath,
		dimensions: dimensions,
		byID:       make(map[string]int),
		byPath:     make(map[string][]int),
		fileIndex:  make(map[string]string),
	}

	if err := s.load(); err != nil {
		return nil, fmt.Errorf("load storage: %w", err)
	}

	s.dirty = false
	return s, nil
}

// UpsertChunks inserts or updates chunks. Vectors are L2-normalized before storing.
// On update (same chunk ID), the old record is replaced. Invalidates the BM25 cache
// and updates the lightweight fileIndex for memory-free resume support.
func (s *Storage) UpsertChunks(chunks []chunker.Chunk, embeddings map[string][]float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, ch := range chunks {
		emb, ok := embeddings[ch.ID]
		if !ok {
			continue
		}
		normalize(emb)

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

	// Update lightweight fileIndex for memory-free resume
	if len(chunks) > 0 {
		if s.fileIndex == nil {
			s.fileIndex = make(map[string]string)
		}
		s.fileIndex[chunks[0].FilePath] = chunks[0].FileHash
	}

	s.dirty = true
	s.bm25 = nil
	return nil
}

// DeleteChunksByPath removes all chunks belonging to a file path.
// Rebuilds the byID/byPath indices, invalidates BM25 cache, and
// removes the entry from the lightweight fileIndex.
func (s *Storage) DeleteChunksByPath(filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove from fileIndex when chunks are present
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
	return nil
}

// Search performs a pure vector similarity search using dot product on normalized vectors.
// Returns up to `limit` results ranked by similarity score.
func (s *Storage) Search(query []float64, limit int) ([]SearchResult, error) {
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

// SwitchBranch persists the current index and loads the index for a different
// git branch/worktree. Branch files follow the pattern:
// vectors.gob (main) or vectors-{worktree}-{branch}.gob (other branches).
func (s *Storage) SwitchBranch(branch string, worktree string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dirty {
		if err := s.saveLocked(); err != nil {
			return fmt.Errorf("save current branch: %w", err)
		}
	}

	ext := filepath.Ext(s.basePath)
	base := s.basePath[:len(s.basePath)-len(ext)]

	parts := []string{base}
	if worktree != "" {
		parts = append(parts, worktree)
	}
	if branch != "" && branch != "main" {
		parts = append(parts, branch)
	}
	s.path = strings.Join(parts, "-") + ext

	// Clean up any leftover temp file from a crash during write
	if _, err := os.Stat(s.path + ".tmp"); err == nil {
		os.Remove(s.path + ".tmp")
	}

	s.records = nil
	s.byID = make(map[string]int)
	s.byPath = make(map[string][]int)
	s.commitSHA = ""
	s.dirty = false

	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	var data StorageData
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&data); err == nil {
		s.rebuildIndex(data.Records)
		s.commitSHA = data.CommitSHA
		return nil
	}

	f.Seek(0, 0)
	var records []ChunkRecord
	dec = gob.NewDecoder(f)
	if err := dec.Decode(&records); err != nil {
		return nil
	}

	s.rebuildIndex(records)
	return nil
}

// Close persists the dirty index to disk.
func (s *Storage) Close() error {
	return s.save()
}

// Save flushes the current state to disk immediately.
// Called periodically during indexing to preserve progress on crash.
func (s *Storage) Save() error {
	return s.save()
}

// IsFileIndexed checks if all chunks for a file are already in the index
// with matching hashes. Used to skip already-indexed files on resume after crash.
// First checks the lightweight fileIndex (survives SaveAndFree), then falls back
// to byPath for normal (non-cleared) operation.
func (s *Storage) IsFileIndexed(filePath, fileHash string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check lightweight fileIndex first — works even after SaveAndFree cleared records
	if s.fileIndex != nil {
		if hash, ok := s.fileIndex[filePath]; ok {
			return hash == fileHash
		}
	}

	// Fall back to byPath (normal operation, records are in memory)
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

// load reads the gob file from disk and rebuilds in-memory indices.
// Supports both StorageData format (with CommitSHA) and legacy []ChunkRecord format.
func (s *Storage) load() error {
	// Clean up any leftover temp file from a crash during write
	if _, err := os.Stat(s.path + ".tmp"); err == nil {
		os.Remove(s.path + ".tmp")
	}

	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	var data StorageData
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&data); err == nil {
		s.rebuildIndex(data.Records)
		s.commitSHA = data.CommitSHA
		s.dirty = false
		return nil
	}

	f.Seek(0, 0)
	var records []ChunkRecord
	dec = gob.NewDecoder(f)
	if err := dec.Decode(&records); err != nil {
		return err
	}

	s.rebuildIndex(records)
	s.dirty = false
	return nil
}

// save persists the index to disk (merged with existing on disk). Thread-safe.
func (s *Storage) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// saveLocked writes all records (merged with existing on disk) atomically.
// Caller must hold s.mu write lock.
func (s *Storage) saveLocked() error {
	if !s.dirty && len(s.records) == 0 {
		return nil
	}

	// Load existing records from disk and merge — ensures periodic/final saves
	// don't overwrite data that was already persisted by SaveAndFree.
	existing := s.loadRecordsFromDiskLocked()
	merged := mergeRecordsLocked(existing, s.records)

	// Commit SHA: prefer current, fall back to persisted
	sha := s.commitSHA
	if sha == "" {
		_ = s.loadCommitSHALocked(&sha)
	}

	tmpPath := s.path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	enc := gob.NewEncoder(f)
	if err := enc.Encode(StorageData{
		Records:   merged,
		CommitSHA: sha,
	}); err != nil {
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

// SaveAndFree persists all records (merged with existing on disk), then clears
// in-memory records, byID, byPath, and BM25 to free Go memory. The lightweight
// fileIndex is preserved so IsFileIndexed continues to work for resume support.
// Should be called periodically during large indexing operations.
func (s *Storage) SaveAndFree() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.saveLocked(); err != nil {
		return err
	}

	// Clear in-memory records (keep fileIndex for resume)
	s.records = nil
	s.byID = make(map[string]int)
	s.byPath = make(map[string][]int)
	s.bm25 = nil
	s.dirty = false

	return nil
}

// loadRecordsFromDiskLocked reads existing records from the gob file.
// Caller must hold s.mu (write lock). Returns nil if file doesn't exist or on error.
func (s *Storage) loadRecordsFromDiskLocked() []ChunkRecord {
	f, err := os.Open(s.path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var data StorageData
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&data); err == nil {
		return data.Records
	}

	f.Seek(0, 0)
	var records []ChunkRecord
	dec = gob.NewDecoder(f)
	if err := dec.Decode(&records); err != nil {
		return nil
	}
	return records
}

// loadCommitSHALocked reads the commit SHA from the gob file if not already set.
// Caller must hold s.mu (write lock).
func (s *Storage) loadCommitSHALocked(sha *string) error {
	f, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer f.Close()

	var data StorageData
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&data); err != nil {
		return err
	}
	*sha = data.CommitSHA
	return nil
}

// mergeRecordsLocked merges incoming records into existing, with incoming taking
// precedence by chunk ID. Returns a new slice without modifying inputs.
func mergeRecordsLocked(existing, incoming []ChunkRecord) []ChunkRecord {
	if len(existing) == 0 {
		out := make([]ChunkRecord, len(incoming))
		copy(out, incoming)
		return out
	}
	if len(incoming) == 0 {
		out := make([]ChunkRecord, len(existing))
		copy(out, existing)
		return out
	}

	// Build index of existing records by ID
	byID := make(map[string]int, len(existing))
	for i, rec := range existing {
		byID[rec.ID] = i
	}

	// Start with a copy of existing
	merged := make([]ChunkRecord, len(existing))
	copy(merged, existing)

	// Merge incoming: update existing or append new
	for _, rec := range incoming {
		if idx, ok := byID[rec.ID]; ok {
			merged[idx] = rec
		} else {
			byID[rec.ID] = len(merged)
			merged = append(merged, rec)
		}
	}

	return merged
}

// rebuildIndex replaces all in-memory data structures with the given records,
// rebuilding byID, byPath, and fileIndex lookup maps. Invalidates the BM25 cache.
func (s *Storage) rebuildIndex(records []ChunkRecord) {
	s.records = records
	s.byID = make(map[string]int)
	s.byPath = make(map[string][]int)
	s.bm25 = nil
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

// normalize L2-normalizes a vector in place. After normalization, dot product
// equals cosine similarity, enabling efficient SIMD-accelerated similarity search.
func normalize(v []float64) {
	var norm float64
	for i := range v {
		norm += v[i] * v[i]
	}
	if norm == 0 {
		return
	}
	invNorm := 1.0 / math.Sqrt(norm)
	for i := range v {
		v[i] *= invNorm
	}
}

// dotProduct delegates to the SIMD-accelerated (or scalar fallback) dot product.
func dotProduct(a, b []float64) float64 {
	return simd.Dot(a, b)
}
