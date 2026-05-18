// Package storage provides persistent vector storage with gob serialization,
// L2-normalized vectors (so cosine similarity = dot product), branch-isolated indices,
// and BM25 inverted index for hybrid search. All vectors are normalized at store time.
package storage

import (
	"encoding/gob"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/config"
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

// StorageData is the on-disk format for the legacy single-file layout:
// records + commit SHA for git tracking.
type StorageData struct {
	Records   []ChunkRecord
	CommitSHA string
}

// StorageManifest is the on-disk per-file format: file index + commit SHA.
// The heavy chunk records are stored in separate per-file gobs under chunks/.
type StorageManifest struct {
	FileIndex map[string]string // filePath → fileHash
	CommitSHA string
}

// Storage manages chunk records with O(1) lookups by ID and file path,
// branch-isolated persistence, an optional BM25 index for hybrid search,
// a trigram index for fast grep pre-filtering, and a lightweight fileIndex
// (path→hash) for memory-free resume support. Thread-safe via sync.RWMutex.
//
// Disk layout:
//
//	{baseDir}/
//	  manifest.gob              # StorageManifest (fileIndex + commitSHA)
//	  manifest-{branch}.gob     # branch-specific manifest
//	  chunks/                   # per-file chunk gobs (main branch)
//	    {encoded-rel-path}.gob
//	  chunks-{branch}/          # per-file chunk gobs (other branches)
//	    {encoded-rel-path}.gob
type Storage struct {
	baseDir      string
	manifestPath string
	chunksDir    string
	trigramsPath string
	dimensions   int
	mu           sync.RWMutex
	records      []ChunkRecord
	commitSHA    string
	byID         map[string]int
	byPath       map[string][]int
	dirtyFiles   map[string]bool // relPath → needs re-saving on disk; true = dirty
	dirty        bool            // manifest-level dirty (commitSHA change)
	bm25         *bm25Index
	trigrams     *trigramIndex
	fileIndex    map[string]string // filePath → fileHash (survives SaveAndFree)
}

// New creates or opens a Storage with the given base directory and vector dimensions.
// The base directory follows the format from config.StorageDir.
// Supports automatic migration from the legacy single-file format.
func New(baseDir string, dimensions int) (*Storage, error) {
	chunksDir := filepath.Join(baseDir, "chunks")
	if err := os.MkdirAll(chunksDir, 0755); err != nil {
		return nil, fmt.Errorf("create chunks dir: %w", err)
	}

	s := &Storage{
		baseDir:      baseDir,
		manifestPath: filepath.Join(baseDir, "manifest.gob"),
		chunksDir:    chunksDir,
		trigramsPath: filepath.Join(baseDir, "trigrams.gob"),
		dimensions:   dimensions,
		byID:         make(map[string]int),
		byPath:       make(map[string][]int),
		fileIndex:    make(map[string]string),
		dirtyFiles:   make(map[string]bool),
	}

	if err := s.load(); err != nil {
		return nil, fmt.Errorf("load storage: %w", err)
	}
	s.loadTrigrams()

	s.dirty = false
	return s, nil
}

// UpsertChunks inserts or updates chunks. Vectors are L2-normalized before storing.
// On update (same chunk ID), the old record is replaced. Invalidates the BM25 cache,
// updates the lightweight fileIndex for memory-free resume support, and marks the
// file's chunk gob as dirty for the next per-file save.
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
		// Mark this file's chunk gob as dirty
		if chunks[0].RelPath != "" {
			s.dirtyFiles[chunks[0].RelPath] = true
		}
	}

	s.dirty = true
	s.bm25 = nil
	s.trigrams = nil
	return nil
}

// DeleteChunksByPath removes all chunks belonging to a file path.
// Rebuilds the byID/byPath indices, invalidates BM25 cache, marks the
// file's chunk gob for deletion on next save, and removes the entry
// from the lightweight fileIndex.
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

	// Capture relPath before rebuilding (use first chunk as representative)
	var relPath string
	for _, idx := range indices {
		if s.records[idx].RelPath != "" {
			relPath = s.records[idx].RelPath
			break
		}
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

	// Mark for chunk file deletion on next save
	if relPath != "" {
		s.dirtyFiles[relPath] = true
	}

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

// branchSuffix builds the filename suffix for non-main git branches.
// Returns "" for main, "-{worktree}-{branch}" for other branches.
func branchSuffix(branch, worktree string) string {
	var parts []string
	if worktree != "" {
		parts = append(parts, worktree)
	}
	if branch != "" && branch != "main" {
		parts = append(parts, branch)
	}
	if len(parts) == 0 {
		return ""
	}
	return "-" + strings.Join(parts, "-")
}

// SwitchBranch persists the current index and loads the index for a different
// git branch/worktree. Uses branch-specific manifest and chunks directories:
// manifest.gob + chunks/ (main) or manifest-{branch}.gob + chunks-{branch}/.
func (s *Storage) SwitchBranch(branch string, worktree string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dirty {
		if err := s.saveLocked(); err != nil {
			return fmt.Errorf("save current branch: %w", err)
		}
	}

	suffix := branchSuffix(branch, worktree)
	s.manifestPath = filepath.Join(s.baseDir, "manifest"+suffix+".gob")
	s.chunksDir = filepath.Join(s.baseDir, "chunks"+suffix)
	s.trigramsPath = filepath.Join(s.baseDir, "trigrams"+suffix+".gob")

	if err := os.MkdirAll(s.chunksDir, 0755); err != nil {
		return fmt.Errorf("create branch chunks dir: %w", err)
	}

	// Clean up leftover temp files
	cleanupTempFiles(s.chunksDir)

	s.records = nil
	s.byID = make(map[string]int)
	s.byPath = make(map[string][]int)
	s.commitSHA = ""
	s.dirty = false
	s.dirtyFiles = make(map[string]bool)
	s.trigrams = nil

	if err := s.loadPerFile(); err != nil {
		return err
	}
	s.loadTrigrams()
	return nil
}

// Close persists the dirty index to disk.
func (s *Storage) Close() error {
	if err := s.save(); err != nil {
		return err
	}
	return s.saveTrigrams()
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

// load reads the index from disk. Tries the per-file format first (manifest.gob + chunks/),
// then falls back to the legacy single-file format (vectors.gob) and migrates it.
func (s *Storage) load() error {
	// Try per-file format first
	if err := s.loadPerFile(); err == nil {
		return nil
	}

	// Fallback: try legacy single-file format and migrate
	legacyPath := filepath.Join(s.baseDir, "vectors.gob")
	f, err := os.Open(legacyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return nil // no legacy file, start fresh
	}
	defer f.Close()

	slog.Info("migrating from legacy single-file format to per-file layout",
		"legacy", legacyPath, "chunks_dir", s.chunksDir)

	var data StorageData
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&data); err == nil {
		s.rebuildIndex(data.Records)
		s.commitSHA = data.CommitSHA
	} else {
		f.Seek(0, 0)
		var records []ChunkRecord
		dec = gob.NewDecoder(f)
		if err := dec.Decode(&records); err != nil {
			return nil // corrupt legacy file, start fresh
		}
		s.rebuildIndex(records)
	}

	// Migrate: save in new format, then remove legacy file
	if len(s.records) > 0 {
		if err := s.saveLocked(); err != nil {
			slog.Warn("migration save failed, keeping legacy file", "error", err)
			return nil
		}
		os.Remove(legacyPath)
		os.Remove(filepath.Join(s.baseDir, "vectors.gob.tmp"))
		slog.Info("migration complete, legacy file removed", "path", legacyPath)
	}

	s.dirty = false
	return nil
}

// loadPerFile reads the index from the per-file format (manifest.gob + chunks/*.gob).
// Returns an error if the manifest file doesn't exist or the format is incompatible.
func (s *Storage) loadPerFile() error {
	f, err := os.Open(s.manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	var manifest StorageManifest
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&manifest); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}

	s.fileIndex = manifest.FileIndex
	s.commitSHA = manifest.CommitSHA

	// Iterate all gob files in chunksDir and load records
	entries, err := os.ReadDir(s.chunksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var allRecords []ChunkRecord
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".gob" {
			continue
		}
		chunkPath := filepath.Join(s.chunksDir, entry.Name())
		records, err := loadChunkFile(chunkPath)
		if err != nil {
			slog.Warn("skipping corrupt chunk file", "path", chunkPath, "error", err)
			continue
		}
		allRecords = append(allRecords, records...)
	}

	s.rebuildIndex(allRecords)
	return nil
}

// save persists dirty chunk files and the manifest to disk. Thread-safe.
func (s *Storage) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.saveLocked(); err != nil {
		return err
	}
	return s.saveTrigramsLocked()
}

// saveLocked writes dirty chunk files as per-file gobs and updates the manifest.
// Caller must hold s.mu write lock.
func (s *Storage) saveLocked() error {
	if !s.dirty && len(s.dirtyFiles) == 0 {
		return nil
	}

	// Write dirty chunk files (or delete if no records remain for that file)
	for relPath := range s.dirtyFiles {
		if relPath == "" {
			continue
		}

		// Find records for this file by matching RelPath
		var fileRecords []ChunkRecord
		for _, rec := range s.records {
			if rec.RelPath == relPath {
				fileRecords = append(fileRecords, rec)
			}
		}

		chunkPath := filepath.Join(s.chunksDir, config.EncodeFilePath(relPath))
		if len(fileRecords) == 0 {
			// File was deleted
			os.Remove(chunkPath)
			os.Remove(chunkPath + ".tmp")
		} else {
			if err := writeChunkFile(chunkPath, fileRecords); err != nil {
				return fmt.Errorf("write chunk file %s: %w", relPath, err)
			}
		}
	}

	// Build fileIndex from current records
	s.fileIndex = make(map[string]string, len(s.records))
	for _, rec := range s.records {
		s.fileIndex[rec.FilePath] = rec.FileHash
	}

	// Write manifest with current commit SHA
	sha := s.commitSHA
	manifest := StorageManifest{
		FileIndex: s.fileIndex,
		CommitSHA: sha,
	}
	if err := writeManifestFile(s.manifestPath, manifest); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	s.dirtyFiles = make(map[string]bool)
	s.dirty = false
	return nil
}

// SaveAndFree persists all dirty chunk files and the manifest, then clears
// in-memory records, byID, byPath, BM25, and dirtyFiles to free Go memory.
// The lightweight fileIndex is preserved so IsFileIndexed continues to work
// for resume support. Should be called periodically during large indexing.
func (s *Storage) SaveAndFree() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.saveLocked(); err != nil {
		return err
	}
	if err := s.saveTrigramsLocked(); err != nil {
		return err
	}

	// Rebuild fileIndex before clearing records (saveLocked already did this,
	// but ensure it's current even if saveLocked was a no-op)
	if s.fileIndex == nil || len(s.fileIndex) == 0 {
		s.fileIndex = make(map[string]string, len(s.records))
		for _, rec := range s.records {
			s.fileIndex[rec.FilePath] = rec.FileHash
		}
	}

	// Clear in-memory records (keep fileIndex for resume)
	s.records = nil
	s.byID = make(map[string]int)
	s.byPath = make(map[string][]int)
	s.bm25 = nil
	s.trigrams = nil
	s.dirtyFiles = make(map[string]bool)
	s.dirty = false

	return nil
}

// writeChunkFile atomically writes a slice of ChunkRecords to a per-file gob.
func writeChunkFile(path string, records []ChunkRecord) error {
	tmpPath := path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	enc := gob.NewEncoder(f)
	if err := enc.Encode(records); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, path)
}

// loadChunkFile reads a per-file gob containing []ChunkRecord.
func loadChunkFile(path string) ([]ChunkRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var records []ChunkRecord
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&records); err != nil {
		return nil, err
	}
	return records, nil
}

// writeManifestFile atomically writes a StorageManifest to a gob file.
func writeManifestFile(path string, manifest StorageManifest) error {
	tmpPath := path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	enc := gob.NewEncoder(f)
	if err := enc.Encode(manifest); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, path)
}

// cleanupTempFiles removes any leftover .tmp files in the given directory.
func cleanupTempFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// rebuildIndex replaces all in-memory data structures with the given records,
// rebuilding byID, byPath, and fileIndex lookup maps. Invalidates the BM25 cache.
func (s *Storage) rebuildIndex(records []ChunkRecord) {
	s.records = records
	s.byID = make(map[string]int)
	s.byPath = make(map[string][]int)
	s.bm25 = nil
	s.trigrams = nil
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

// trigramsPathFromVectorsPath derives the trigrams gob path from the vectors gob path.
// e.g. ".../vectors.gob" → ".../trigrams.gob"
// e.g. ".../vectors-main-work.gob" → ".../trigrams-main-work.gob"
func trigramsPathFromVectorsPath(vecPath string) string {
	dir := filepath.Dir(vecPath)
	name := filepath.Base(vecPath)
	tName := "trigrams" + name[len("vectors"):]
	return filepath.Join(dir, tName)
}

// loadTrigrams reads the trigram index from disk if it exists and matches current records.
func (s *Storage) loadTrigrams() {
	f, err := os.Open(s.trigramsPath)
	if err != nil {
		return
	}
	defer f.Close()

	var data TrigramData
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&data); err != nil {
		return
	}

	if data.DocCount != len(s.records) {
		return
	}

	s.trigrams = &trigramIndex{index: data.Index}
}

// saveTrigrams is a thread-safe wrapper for persisting the trigram index.
func (s *Storage) saveTrigrams() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveTrigramsLocked()
}

// saveTrigramsLocked persists the trigram index to disk atomically.
// Caller must hold s.mu write lock.
func (s *Storage) saveTrigramsLocked() error {
	if s.trigrams == nil {
		return nil
	}

	tmpPath := s.trigramsPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	enc := gob.NewEncoder(f)
	if err := enc.Encode(TrigramData{
		Index:    s.trigrams.index,
		DocCount: len(s.records),
	}); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, s.trigramsPath)
}
