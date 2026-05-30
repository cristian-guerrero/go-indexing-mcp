// Package storage provides a high-level interface for vector+fulltext storage,
// backed by SQLite via sqlite-vec and FTS5. Branch-isolated indices via
// separate .sqlite files per git branch. Thread-safe via SQLite's WAL mode.
package storage

import (
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/db"
)

// SearchResult is a public search result with score for ranking.
type SearchResult = db.SearchResult

// GrepMatch represents a single matching line within a chunk.
type GrepMatch = db.GrepMatch

// GrepResult is a search result from grep mode with per-line matches.
type GrepResult = db.GrepResult

// GrepOptions configures a grep search.
type GrepOptions = db.GrepOptions

// StorageFormatVersion is the current on-disk format version for the vector index.
const StorageFormatVersion = db.StorageFormatVersion

// GraphFormatVersion is the current on-disk format version for the graph index.
const GraphFormatVersion = db.GraphFormatVersion

// Storage manages chunk records with SQLite-backed persistence,
// branch-isolated indices, BM25 (via FTS5), and ANN vector search (via sqlite-vec).
type Storage struct {
	store *db.Store
}

// New creates a Storage backed by a SQLite database at the given path.
func New(path string, dimensions int) (*Storage, error) {
	st, err := db.Open(path, dimensions)
	if err != nil {
		return nil, err
	}
	return &Storage{store: st}, nil
}

// NewFromStore wraps an existing db.Store. Used when sharing a Store with GraphQuery.
func NewFromStore(store *db.Store) *Storage {
	return &Storage{store: store}
}

// Store returns the underlying db.Store.
func (s *Storage) Store() *db.Store { return s.store }

// UpsertChunks inserts or updates chunks and their embeddings.
func (s *Storage) UpsertChunks(chunks []chunker.Chunk, embeddings map[string][]float32) error {
	return s.store.UpsertChunks(chunks, embeddings)
}

// DeleteChunksByPath removes all chunks belonging to a file path.
func (s *Storage) DeleteChunksByPath(filePath string) error {
	return s.store.DeleteChunksByPath(filePath)
}

// Search performs a pure vector similarity search using sqlite-vec ANN.
func (s *Storage) Search(query []float32, limit int) ([]SearchResult, error) {
	return s.store.Search(query, limit)
}

// SearchHybrid performs BM25 + vector search fused via RRF.
func (s *Storage) SearchHybrid(queryVec []float32, query string, limit int) ([]SearchResult, error) {
	return s.store.SearchHybrid(queryVec, query, limit)
}

// SearchGrep performs substring/regex matching on chunk content.
func (s *Storage) SearchGrep(opts GrepOptions) ([]GrepResult, error) {
	return s.store.SearchGrep(opts)
}

// Stats returns total chunk count and unique file count.
func (s *Storage) Stats() (chunks, files int, err error) {
	return s.store.Stats()
}

// ListFiles returns unique relative file paths in the index.
func (s *Storage) ListFiles() []string {
	files, _ := s.store.ListFiles()
	return files
}

// IsFileIndexed checks if all chunks for a file are already indexed with matching hash.
func (s *Storage) IsFileIndexed(filePath, fileHash string) bool {
	ok, _ := s.store.IsFileIndexed(filePath, fileHash)
	return ok
}

// BranchSuffix builds the filename suffix for non-main git branches.
func BranchSuffix(branch, worktree string) string {
	return db.BranchSuffix(branch, worktree)
}

// GobPath returns the database path for a given branch/worktree (backward compat name).
func (s *Storage) GobPath(branch, worktree string) string {
	return s.store.BranchPath(branch, worktree)
}

// SwitchBranch persists the current index and loads a branch-specific database.
func (s *Storage) SwitchBranch(branch string, worktree string) error {
	return s.store.SwitchBranch(branch, worktree)
}

// Close persists any dirty state and closes the database.
func (s *Storage) Close() error {
	return s.store.Close()
}

// Save is a no-op in SQLite mode (changes are written immediately).
func (s *Storage) Save() error { return nil }

// SavePeriodic is a no-op in SQLite mode (changes are written immediately).
func (s *Storage) SavePeriodic() error { return nil }

// SaveSnapshot is a no-op in SQLite mode (changes are written immediately).
func (s *Storage) SaveSnapshot() error { return nil }

// SaveAndFree is a no-op in SQLite mode (changes are written immediately).
func (s *Storage) SaveAndFree() error { return nil }

// NeedsReindex returns true when the on-disk format version differs from current.
func (s *Storage) NeedsReindex() bool {
	return s.store.NeedsReindex()
}

// ClearAll removes all data from the index.
func (s *Storage) ClearAll() error {
	return s.store.ClearAll()
}

// SetCommitSHA records the git commit SHA.
func (s *Storage) SetCommitSHA(sha string) {
	s.store.SetCommitSHA(sha)
}

// GetCommitSHA returns the last indexed git commit SHA.
func (s *Storage) GetCommitSHA() string {
	return s.store.GetCommitSHA()
}

// SetIgnoredFilesHash records the hash of current ignore patterns.
func (s *Storage) SetIgnoredFilesHash(hash string) {
	s.store.SetIgnoredFilesHash(hash)
}

// GetIgnoredFilesHash returns the stored ignore pattern hash.
func (s *Storage) GetIgnoredFilesHash() string {
	return s.store.GetIgnoredFilesHash()
}

// LegacyGobData contains enough info for branch seeding.
type LegacyGobData struct {
	Records   int
	CommitSHA string
}

// LoadGob reads commit SHA from a SQLite database for branch seeding.
// Returns nil if the file doesn't exist or can't be read.
func LoadGob(gobPath string) *LegacyGobData {
	// The "gobPath" is actually a .sqlite path in our new system.
	// Read the commit_sha from metadata.
	store, err := db.Open(gobPath, 0)
	if err != nil {
		return nil
	}
	defer store.Close()

	sha := store.GetCommitSHA()
	return &LegacyGobData{
		CommitSHA: sha,
		Records:   0, // unknown without counting
	}
}
