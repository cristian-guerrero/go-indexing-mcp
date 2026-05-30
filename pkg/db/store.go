// Package db provides a unified SQLite-backed store for vector chunks,
// BM25/FTS5 full-text search, and knowledge graph (symbols + references).
// Uses sqlite-vec for vector similarity search and FTS5 for BM25 ranking.
package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
)

// Store wraps a SQLite database with schema management, branch isolation,
// and all chunk/vector/graph operations.
type Store struct {
	db         *sql.DB
	path       string // current db file path
	pathPrefix string // base path without extension
	dimensions int    // vector dimension
}

// Vec0LibPath returns the path to the sqlite-vec loadable extension, auto-downloading if missing.
func Vec0LibPath() string {
	path, err := EnsureVec0Lib()
	if err != nil {
		slog.Warn("vec0 extension not available, vector search disabled", "error", err)
		return ""
	}
	return path
}

// driverCounter ensures unique driver names for sql.Register.
var driverCounter atomic.Uint64

// Open creates or opens a SQLite database at the given path with the given
// vector dimensions. Creates the directory, loads the vec0 extension, and
// ensures all schema tables exist. Returns a Store ready for use.
func Open(dbPath string, dimensions int) (*Store, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=30000&_cache_size=-64000", dbPath)

	var (
		clientDB   *sql.DB
		driverName string
	)

	if dimensions > 0 {
		vec0Path := Vec0LibPath()
		driverName = fmt.Sprintf("sqlite3-vec-%d", driverCounter.Add(1))
		sql.Register(driverName, &sqlite3.SQLiteDriver{
			ConnectHook: func(conn *sqlite3.SQLiteConn) error {
				return conn.LoadExtension(vec0Path, "sqlite3_vec_init")
			},
		})
	} else {
		driverName = fmt.Sprintf("sqlite3-%d", driverCounter.Add(1))
		sql.Register(driverName, &sqlite3.SQLiteDriver{})
	}

	var openErr error
	clientDB, openErr = sql.Open(driverName, dsn)
	if openErr != nil {
		return nil, fmt.Errorf("open sqlite: %w", openErr)
	}

	clientDB.SetMaxOpenConns(1)
	clientDB.SetMaxIdleConns(1)

	s := &Store{
		db:         clientDB,
		path:       dbPath,
		pathPrefix: strings.TrimSuffix(dbPath, filepath.Ext(dbPath)),
		dimensions: dimensions,
	}

	if err := s.ensureSchema(); err != nil {
		clientDB.Close()
		return nil, err
	}

	return s, nil
}

// DB returns the underlying *sql.DB. Used by wrappers in storage and graph packages.
func (s *Store) DB() *sql.DB { return s.db }

// Path returns the current database file path.
func (s *Store) Path() string { return s.path }

// IsLocked returns true if the database is currently locked by another writer.
// Used to avoid starting expensive work (embedding) when another process
// is actively indexing. Non-blocking — returns immediately.
func (s *Store) IsLocked() bool {
	// A quick write attempt with an immediate timeout detects if another
	// writer is active. If busy_timeout kicks in, we're not locked.
	_, err := s.db.Exec("PRAGMA busy_timeout=100")
	if err != nil {
		return true
	}
	// Try a harmless write (meta insert is fast and idempotent)
	_, err = s.db.Exec("INSERT OR REPLACE INTO meta(key, value) VALUES ('_lock_check', '1')")
	_, _ = s.db.Exec("PRAGMA busy_timeout=30000")
	return err != nil && strings.Contains(err.Error(), "database is locked")
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Checkpoint forces a WAL checkpoint, flushing all WAL data to the main
// database file. This ensures the main file is self-contained and safe to
// copy to another branch during branch seeding.
func (s *Store) Checkpoint() error {
	_, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// ensureSchema creates all tables, indices, and virtual tables.
// Handles dimension changes by recreating the vec0 table.
func (s *Store) ensureSchema() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Metadata
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("meta table: %w", err)
	}

	// Chunks
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS chunks (
		id         TEXT PRIMARY KEY,
		file_path  TEXT NOT NULL,
		rel_path   TEXT NOT NULL,
		language   TEXT DEFAULT '',
		start_line INTEGER DEFAULT 0,
		end_line   INTEGER DEFAULT 0,
		content    TEXT NOT NULL,
		file_hash  TEXT DEFAULT ''
	)`)
	if err != nil {
		return fmt.Errorf("chunks table: %w", err)
	}

	// FTS5 on chunks content (external content for deduplication)
	_, err = tx.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
		content,
		tokenize='porter unicode61',
		content=chunks,
		content_rowid=rowid
	)`)
	if err != nil {
		return fmt.Errorf("chunks_fts: %w", err)
	}

	// FTS5 sync triggers
	tx.Exec(`CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
		INSERT INTO chunks_fts(rowid, content) VALUES (new.rowid, new.content);
	END`)
	tx.Exec(`CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
		INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES('delete', old.rowid, old.content);
	END`)
	tx.Exec(`CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
		INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES('delete', old.rowid, old.content);
		INSERT INTO chunks_fts(rowid, content) VALUES (new.rowid, new.content);
	END`)

	// vec0 virtual table (skip if dimensions == 0, e.g. graph-only store)
	if s.dimensions > 0 {
		storedDimStr := s.getMeta(tx, "dimensions")
		dimChanged := false
		if storedDimStr != "" {
			storedDim, err := strconv.Atoi(storedDimStr)
			if err != nil || storedDim != s.dimensions {
				dimChanged = true
			}
		}

		vecTableExists := false
		var tableName string
		err = tx.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='vec_chunks'").Scan(&tableName)
		if err == nil {
			vecTableExists = true
		}

		if dimChanged && vecTableExists {
			slog.Info("vector dimension changed, recreating vec_chunks table",
				"old", storedDimStr, "new", s.dimensions)
			if _, err := tx.Exec("DROP TABLE IF EXISTS vec_chunks"); err != nil {
				return fmt.Errorf("drop vec_chunks: %w", err)
			}
			vecTableExists = false
		}

		if !vecTableExists {
			vecSQL := fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS vec_chunks USING vec0(
				chunk_id TEXT,
				vector FLOAT32[%d]
			)`, s.dimensions)
			if _, err := tx.Exec(vecSQL); err != nil {
				return fmt.Errorf("create vec_chunks (dim=%d): %w", s.dimensions, err)
			}
		}
	}

	// Symbols table
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS symbols (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		kind       INTEGER NOT NULL DEFAULT 0,
		file_path  TEXT NOT NULL,
		rel_path   TEXT NOT NULL,
		start_line INTEGER DEFAULT 0,
		end_line   INTEGER DEFAULT 0,
		signature  TEXT DEFAULT '',
		exported   INTEGER DEFAULT 0
	)`)
	if err != nil {
		return fmt.Errorf("symbols table: %w", err)
	}

	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_symbols_name ON symbols(name)`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_symbols_kind ON symbols(kind)`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_symbols_rel_path ON symbols(rel_path)`)
	if err != nil {
		return err
	}

	// FTS5 on symbols
	_, err = tx.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS symbols_fts USING fts5(
		name, signature,
		tokenize='unicode61',
		content=symbols,
		content_rowid=rowid
	)`)
	if err != nil {
		return fmt.Errorf("symbols_fts: %w", err)
	}

	// FTS5 sync triggers for symbols
	tx.Exec(`CREATE TRIGGER IF NOT EXISTS symbols_ai AFTER INSERT ON symbols BEGIN
		INSERT INTO symbols_fts(rowid, name, signature) VALUES (new.rowid, new.name, new.signature);
	END`)
	tx.Exec(`CREATE TRIGGER IF NOT EXISTS symbols_ad AFTER DELETE ON symbols BEGIN
		INSERT INTO symbols_fts(symbols_fts, rowid, name, signature) VALUES('delete', old.rowid, old.name, old.signature);
	END`)
	tx.Exec(`CREATE TRIGGER IF NOT EXISTS symbols_au AFTER UPDATE ON symbols BEGIN
		INSERT INTO symbols_fts(symbols_fts, rowid, name, signature) VALUES('delete', old.rowid, old.name, old.signature);
		INSERT INTO symbols_fts(rowid, name, signature) VALUES (new.rowid, new.name, new.signature);
	END`)

	// References table
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS refs (
		id           TEXT PRIMARY KEY,
		source_id    TEXT NOT NULL,
		target_name  TEXT DEFAULT '',
		target_id    TEXT DEFAULT '',
		kind         INTEGER NOT NULL DEFAULT 0,
		file_path    TEXT NOT NULL,
		line         INTEGER DEFAULT 0,
		confidence   REAL DEFAULT 1.0
	)`)
	if err != nil {
		return fmt.Errorf("refs table: %w", err)
	}
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_refs_source ON refs(source_id)`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_refs_target ON refs(target_id)`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_refs_kind ON refs(kind)`)
	if err != nil {
		return err
	}

	s.setMeta(tx, "dimensions", strconv.Itoa(s.dimensions))
	s.setMeta(tx, "storage_format_version", strconv.Itoa(StorageFormatVersion))
	s.setMeta(tx, "graph_format_version", strconv.Itoa(GraphFormatVersion))

	return tx.Commit()
}

// NeedsReindex returns true when the on-disk format version differs from
// the current code version, requiring a full reindex.
func (s *Store) NeedsReindex() bool {
	raw := s.getMeta(nil, "storage_format_version")
	if raw == "" {
		return false
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return false
	}
	return v > 0 && v != StorageFormatVersion
}

// ListSymbolFiles returns the distinct rel_path values from the symbols table.
func (s *Store) ListSymbolFiles() ([]string, error) {
	rows, err := s.db.Query("SELECT DISTINCT rel_path FROM symbols ORDER BY rel_path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []string
	for rows.Next() {
		var f string
		rows.Scan(&f)
		files = append(files, f)
	}
	return files, rows.Err()
}

// HasOldFormat checks if a directory contains old BadgerDB format files.
func HasOldFormat(dir string) bool {
	manifest := filepath.Join(dir, "MANIFEST")
	if fi, err := os.Stat(manifest); err == nil && !fi.IsDir() {
		return true
	}
	return false
}

// Clear drops all graph data and resets format version. Used by callers
// that previously referenced GraphDB.Clear().
func (s *Store) Clear() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tx.Exec("DELETE FROM symbols")
	tx.Exec("DELETE FROM symbols_fts")
	tx.Exec("DELETE FROM refs")
	s.setMeta(tx, "graph_format_version", strconv.Itoa(GraphFormatVersion))
	return tx.Commit()
}

// ClearAll drops all data from the database, effectively starting fresh.
// Preserves the schema but removes all rows.
func (s *Store) ClearAll() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, table := range []string{"chunks", "chunks_fts", "vec_chunks", "symbols", "symbols_fts", "refs", "meta"} {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			// Virtual tables may not support DELETE
			tx.Exec("DROP TABLE IF EXISTS " + table)
		}
	}

	s.setMeta(tx, "storage_format_version", strconv.Itoa(StorageFormatVersion))
	s.setMeta(tx, "graph_format_version", strconv.Itoa(GraphFormatVersion))
	return tx.Commit()
}

// getMeta reads a metadata value. If tx is non-nil, runs within the transaction.
func (s *Store) getMeta(tx *sql.Tx, key string) string {
	var value string
	if tx != nil {
		tx.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&value)
	} else {
		s.db.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&value)
	}
	return value
}

// setMeta writes a metadata value. Must be called within a transaction.
func (s *Store) setMeta(tx *sql.Tx, key, value string) {
	tx.Exec("INSERT OR REPLACE INTO meta(key, value) VALUES (?, ?)", key, value)
}

// BranchSuffix builds the filename suffix for non-main git branches.
// Returns "" for main, "-{worktree}-{branch}" for other branches.
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

func sanitizeName(s string) string {
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, "..", "-")
	return s
}

// BranchPath returns the SQLite file path for a given branch/worktree.
func (s *Store) BranchPath(branch, worktree string) string {
	return s.pathPrefix + BranchSuffix(branch, worktree) + ".sqlite"
}

// SwitchBranch saves the current state and loads a branch-specific database.
func (s *Store) SwitchBranch(branch, worktree string) error {
	// Close current connection if open (may already be closed by caller)
	if s.db != nil {
		s.db.Close()
	}

	newPath := s.BranchPath(branch, worktree)
	s.path = newPath
	return s.reopen()
}

// reopen closes and re-opens the database at the current path.
func (s *Store) reopen() error {
	if s.db != nil {
		s.db.Close()
	}

	var driverName string
	if s.dimensions > 0 {
		vec0Path := Vec0LibPath()
		driverName = fmt.Sprintf("sqlite3-vec-%d", driverCounter.Add(1))
		sql.Register(driverName, &sqlite3.SQLiteDriver{
			ConnectHook: func(conn *sqlite3.SQLiteConn) error {
				return conn.LoadExtension(vec0Path, "sqlite3_vec_init")
			},
		})
	} else {
		driverName = fmt.Sprintf("sqlite3-%d", driverCounter.Add(1))
		sql.Register(driverName, &sqlite3.SQLiteDriver{})
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create branch dir: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=30000", s.path)
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return fmt.Errorf("open branch db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s.db = db

	return s.ensureSchema()
}

// ---- Metadata helpers ----

func (s *Store) SetCommitSHA(sha string) {
	s.db.Exec("INSERT OR REPLACE INTO meta(key, value) VALUES ('commit_sha', ?)", sha)
}

func (s *Store) GetCommitSHA() string {
	return s.getMeta(nil, "commit_sha")
}

func (s *Store) SetIgnoredFilesHash(hash string) {
	s.db.Exec("INSERT OR REPLACE INTO meta(key, value) VALUES ('ignored_files_hash', ?)", hash)
}

func (s *Store) GetIgnoredFilesHash() string {
	return s.getMeta(nil, "ignored_files_hash")
}

func (s *Store) SetGraphCommitSHA(sha string) {
	s.db.Exec("INSERT OR REPLACE INTO meta(key, value) VALUES ('graph_commit_sha', ?)", sha)
}

func (s *Store) GetGraphCommitSHA() string {
	return s.getMeta(nil, "graph_commit_sha")
}

// ---- Chunk helpers ----

// UpsertChunks inserts or updates chunks and their embeddings.
// Uses a single transaction for the batch. Vectors stored as raw float32 bytes.
// If the database is locked by another process, returns an error immediately
// (the caller should skip the batch and retry later — no corruption risk).
func (s *Store) UpsertChunks(chunks []chunker.Chunk, embeddings map[string][]float32) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	chunkStmt, err := tx.Prepare(`INSERT OR REPLACE INTO chunks
		(id, file_path, rel_path, language, start_line, end_line, content, file_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer chunkStmt.Close()

	vecStmt, err := tx.Prepare(`INSERT OR REPLACE INTO vec_chunks
		(chunk_id, vector) VALUES (?, ?)`)
	if err != nil {
		// vec0 table may not exist for graph-only stores
		if strings.Contains(err.Error(), "no such table") {
			vecStmt = nil
		} else {
			return err
		}
	}
	if vecStmt != nil {
		defer vecStmt.Close()
	}

	for _, ch := range chunks {
		emb, ok := embeddings[ch.ID]
		if !ok {
			continue
		}
		vecBytes := float32sToBytes(emb)

		if _, err := chunkStmt.Exec(ch.ID, ch.FilePath, ch.RelPath, ch.Language,
			ch.StartLine, ch.EndLine, ch.Content, ch.FileHash); err != nil {
			return fmt.Errorf("upsert chunk %s: %w", ch.ID, err)
		}
		if vecStmt != nil {
			if _, err := vecStmt.Exec(ch.ID, vecBytes); err != nil {
				return fmt.Errorf("upsert vector %s: %w", ch.ID, err)
			}
		}
	}

	return tx.Commit()
}

// DeleteChunksByPath removes all chunks and vectors for a file path.
func (s *Store) DeleteChunksByPath(filePath string) error {
	rows, err := s.db.Query("SELECT id FROM chunks WHERE file_path = ?", filePath)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()

	if len(ids) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	delChunk, _ := tx.Prepare("DELETE FROM chunks WHERE id = ?")
	for _, id := range ids {
		delChunk.Exec(id)
	}

	// Delete from vec_chunks (may not exist for graph-only stores)
	if _, err := tx.Exec("DELETE FROM vec_chunks WHERE chunk_id IN (SELECT id FROM chunks WHERE file_path = ?)", filePath); err != nil {
		// Ignore "no such table" error for graph-only stores
		if !strings.Contains(err.Error(), "no such table") {
			return err
		}
	}

	return tx.Commit()
}

// ListFiles returns unique relative file paths in the index.
func (s *Store) ListFiles() ([]string, error) {
	rows, err := s.db.Query("SELECT DISTINCT rel_path FROM chunks ORDER BY rel_path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []string
	for rows.Next() {
		var f string
		rows.Scan(&f)
		files = append(files, f)
	}
	return files, rows.Err()
}

// IsFileIndexed checks if all chunks for a file are already indexed with matching hash.
func (s *Store) IsFileIndexed(filePath, fileHash string) (bool, error) {
	if fileHash == "" {
		var count int
		err := s.db.QueryRow("SELECT COUNT(*) FROM chunks WHERE file_path = ?", filePath).Scan(&count)
		return count > 0, err
	}
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM chunks WHERE file_path = ? AND file_hash = ?",
		filePath, fileHash).Scan(&count)
	return count > 0, err
}

// Stats returns total chunk count and unique file count.
func (s *Store) Stats() (chunks, files int, err error) {
	err = s.db.QueryRow("SELECT COUNT(*), COUNT(DISTINCT file_path) FROM chunks").Scan(&chunks, &files)
	return
}

// fileIndexHash returns a map of file_path -> file_hash for all files.
// Used for fast resume checking without per-chunk queries.
func (s *Store) fileIndexHash() (map[string]string, error) {
	rows, err := s.db.Query("SELECT DISTINCT file_path, file_hash FROM chunks WHERE file_hash != ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var path, hash string
		rows.Scan(&path, &hash)
		result[path] = hash
	}
	return result, rows.Err()
}

// float32sToBytes serializes a float32 slice to a byte blob for vec0 storage.
func float32sToBytes(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		bits := math.Float32bits(f)
		b[i*4+0] = byte(bits)
		b[i*4+1] = byte(bits >> 8)
		b[i*4+2] = byte(bits >> 16)
		b[i*4+3] = byte(bits >> 24)
	}
	return b
}
