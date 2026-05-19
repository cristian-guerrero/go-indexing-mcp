package graph

import (
	"encoding/gob"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

func init() {
	gob.Register(GraphSnapshot{})
}

// GraphQuery provides a unified query interface over the knowledge graph,
// backed by Pebble for persistence. Supports branch-isolated indexes.
type GraphQuery struct {
	DB           *GraphDB
	Cache        *KnowledgeGraph // in-memory cache
	DBPath       string          // current path (with branch suffix)
	storageDir   string          // base storage directory
	branchSuffix string          // empty for main, "-{name}" for other branches
	readOnly     bool            // true for CLI queries coexisting with MCP server
}

// branchSuffix builds a filename suffix for non-default branches.
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

// GraphSnapshot is a serializable snapshot of the knowledge graph cache.
// Used by read-only CLI processes to query the graph without opening Pebble
// (which holds an exclusive lock on Windows).
type GraphSnapshot struct {
	Symbols []Symbol
	Refs    []Reference
}

// snapshotPath returns the path to the snapshot file for the given directory.
func snapshotPath(dir string) string {
	return filepath.Join(dir, "graph.gob")
}

// SaveSnapshot writes the current in-memory cache to a GOB file at snapshotPath()
// so that read-only CLI processes can query the graph without opening Pebble.
func (g *GraphQuery) SaveSnapshot() error {
	g.Cache.mu.RLock()
	defer g.Cache.mu.RUnlock()

	snap := GraphSnapshot{
		Symbols: make([]Symbol, 0, len(g.Cache.Symbols)),
		Refs:    make([]Reference, len(g.Cache.Refs)),
	}
	for _, sym := range g.Cache.Symbols {
		snap.Symbols = append(snap.Symbols, *sym)
	}
	copy(snap.Refs, g.Cache.Refs)

	path := snapshotPath(g.DBPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("mkdir snapshot: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}
	defer f.Close()

	if err := gob.NewEncoder(f).Encode(snap); err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}

	slog.Info("graph snapshot saved", "file", path, "symbols", len(snap.Symbols), "refs", len(snap.Refs))
	return nil
}

// loadSnapshot loads a graph snapshot from a GOB file and rebuilds the in-memory
// KnowledgeGraph (indexes and all). Returns nil if no snapshot file exists.
func loadSnapshot(dir string) *KnowledgeGraph {
	path := snapshotPath(dir)
	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("graph: open snapshot", "file", path, "error", err)
		}
		return nil
	}
	defer f.Close()

	var snap GraphSnapshot
	if err := gob.NewDecoder(f).Decode(&snap); err != nil {
		slog.Warn("graph: decode snapshot", "file", path, "error", err)
		return nil
	}

	cache := NewGraph()
	for _, sym := range snap.Symbols {
		cache.AddSymbol(sym)
	}
	for _, ref := range snap.Refs {
		cache.AddReference(ref)
	}

	symCount, refCount := cache.Stats()
	slog.Info("graph: loaded from snapshot", "file", path, "symbols", symCount, "refs", refCount)
	return cache
}

// NewGraphQuery opens or creates a graph database at the given directory.
// Uses the base graphDir without branch isolation. Call SwitchBranch to
// switch to a branch-specific index.
// When readOnly is true, the DB is opened without exclusive locking so CLI
// queries can coexist with a running MCP server.
func NewGraphQuery(graphDir string, readOnly bool) (*GraphQuery, error) {
	if !readOnly {
		if err := os.MkdirAll(graphDir, 0755); err != nil {
			return nil, err
		}

		// Check for old BadgerDB format and drop it
		oldSub := filepath.Join(graphDir, "graph")
		if HasOldFormat(graphDir) {
			slog.Warn("graph: detected old BadgerDB format, dropping and recreating")
			os.RemoveAll(oldSub)
		}
		// Also check nested old format (from prior NewGraphQuery nesting)
		if fi, err := os.Stat(oldSub); err == nil && fi.IsDir() {
			os.RemoveAll(oldSub)
		}
	}

	if readOnly {
		// In read-only mode, prefer loading from snapshot to avoid Pebble's
		// exclusive lock (which prevents concurrent access on Windows).
		cache := loadSnapshot(graphDir)
		if cache != nil {
			return &GraphQuery{
				Cache:      cache,
				DBPath:     graphDir,
				storageDir: graphDir,
				readOnly:   true,
			}, nil
		}
		slog.Warn("graph: no snapshot available, trying Pebble read-only")
	}

	db, err := OpenGraph(graphDir, readOnly)
	if err != nil {
		return nil, err
	}

	cache := NewGraph()

	// Load existing entries into cache
	if err := db.LoadAll(cache); err != nil {
		slog.Warn("graph: failed to load existing data, starting fresh", "error", err)
	}

	symCount, refCount := cache.Stats()
	if symCount > 0 {
		slog.Info("graph: loaded existing index", "symbols", symCount, "refs", refCount)
	}

	gq := &GraphQuery{
		DB:         db,
		Cache:      cache,
		DBPath:     graphDir,
		storageDir: graphDir,
		readOnly:   readOnly,
	}

	// Save snapshot so read-only CLI processes can access the graph
	// without opening Pebble (which holds an exclusive lock on Windows).
	if !readOnly {
		if err := gq.SaveSnapshot(); err != nil {
			slog.Warn("graph: save snapshot", "error", err)
		}
	}

	return gq, nil
}

// SwitchBranch persists the current graph and switches to a branch-specific index.
// The new path is the parent of graphDir with a branch suffix appended.
// For branches other than "main", the graph is stored at e.g. 
// {storageDir}-{worktree}-{branch}/. For main, it reverts to {storageDir}/.
func (g *GraphQuery) SwitchBranch(branch, worktree string) error {
	// Close current DB
	if g.DB != nil {
		if err := g.DB.Close(); err != nil {
			slog.Warn("graph: close before branch switch", "error", err)
		}
		g.DB = nil
	}

	// Compute new path
	suffix := branchSuffix(branch, worktree)
	g.branchSuffix = suffix

	parentDir := g.storageDir
	// strip any existing suffix from parentDir
	// parentDir is like {storageDir}, so just append suffix
	newDir := parentDir + suffix

	if !g.readOnly {
		if err := os.MkdirAll(newDir, 0755); err != nil {
			return fmt.Errorf("mkdir graph branch: %w", err)
		}
	}

	g.Cache.Clear()

	if g.readOnly {
		// In read-only mode, try loading from snapshot to avoid Pebble lock
		cache := loadSnapshot(newDir)
		if cache != nil {
			g.Cache = cache
			g.DBPath = newDir
			symCount, refCount := g.Cache.Stats()
			slog.Info("graph: switched branch (snapshot)", "branch", branch, "worktree", worktree, "symbols", symCount, "refs", refCount)
			return nil
		}
		slog.Warn("graph: no snapshot for branch, trying Pebble read-only", "branch", branch)
	}

	db, err := OpenGraph(newDir, g.readOnly)
	if err != nil {
		return err
	}
	g.DB = db
	g.DBPath = newDir

	// Load existing data from this branch
	if err := db.LoadAll(g.Cache); err != nil {
		slog.Warn("graph: load branch", "branch", branch, "error", err)
	}

	symCount, refCount := g.Cache.Stats()
	slog.Info("graph: switched branch", "branch", branch, "worktree", worktree, "symbols", symCount, "refs", refCount)

	if !g.readOnly {
		if err := g.SaveSnapshot(); err != nil {
			slog.Warn("graph: save snapshot after branch switch", "error", err)
		}
	}

	return nil
}

// Close closes the underlying BadgerDB.
func (g *GraphQuery) Close() error {
	if g.DB != nil {
		return g.DB.Close()
	}
	return nil
}

// StoreFile stores symbols and references for a file atomically.
func (g *GraphQuery) StoreFile(relPath string, symbols []Symbol, refs []Reference) error {
	// Update in-memory cache
	g.Cache.RemoveFile(relPath)
	for _, sym := range symbols {
		g.Cache.AddSymbol(sym)
	}
	for _, ref := range refs {
		g.Cache.AddReference(ref)
	}

	// Persist to BadgerDB
	if g.DB != nil {
		if err := g.DB.StoreFile(relPath, symbols, refs); err != nil {
			return err
		}
	}

	return nil
}

// RemoveFile removes all symbols and references for a file.
func (g *GraphQuery) RemoveFile(relPath string) {
	g.Cache.RemoveFile(relPath)
	if g.DB != nil {
		if err := g.DB.RemoveFile(relPath); err != nil {
			slog.Warn("graph: remove file", "file", relPath, "error", err)
		}
	}
}

// FindDefinition looks up a symbol definition by name.
func (g *GraphQuery) FindDefinition(name string, pathFilter string) []Symbol {
	symbols := g.Cache.FindByName(name)
	if pathFilter != "" {
		var filtered []Symbol
		for _, sym := range symbols {
			if matchesFilter(sym.RelPath, pathFilter) {
				filtered = append(filtered, *sym)
			}
		}
		return filtered
	}

	result := make([]Symbol, len(symbols))
	for i, s := range symbols {
		result[i] = *s
	}
	return result
}

// FindUsages returns all references to a symbol by name.
func (g *GraphQuery) FindUsages(name string, pathFilter string) []Reference {
	return g.Cache.FindUsages(name, pathFilter)
}

// FindImports returns import symbols matching a module path pattern.
func (g *GraphQuery) FindImports(pattern string) []*Symbol {
	return g.Cache.FindImports(pattern)
}

// GetCallers returns symbols that call a given function/method name.
func (g *GraphQuery) GetCallers(name string) []Reference {
	refs := g.Cache.FindUsages(name, "")
	var callRefs []Reference
	for _, r := range refs {
		if r.Kind == RefCalls {
			callRefs = append(callRefs, r)
		}
	}
	return callRefs
}

// GetCallees returns all function names called within a symbol.
func (g *GraphQuery) GetCallees(symID string) []Reference {
	var out []Reference
	for _, ref := range g.Cache.Refs {
		if ref.SourceID == symID && ref.Kind == RefCalls {
			out = append(out, ref)
		}
	}
	return out
}

// GetSymbolInfo returns a complete profile of a symbol including its definition,
// usages, callers, and callees.
func (g *GraphQuery) GetSymbolInfo(name string, pathFilter string) *SymbolInfo {
	defs := g.FindDefinition(name, pathFilter)
	usages := g.FindUsages(name, pathFilter)
	callers := g.GetCallers(name)

	var callees []Reference
	if len(defs) > 0 {
		callees = g.GetCallees(defs[0].ID)
	}

	// Filter usages by path if specified
	if pathFilter != "" {
		usages = filterRefsByPath(usages, pathFilter)
		callers = filterRefsByPath(callers, pathFilter)
	}

	return &SymbolInfo{
		Definitions: defs,
		Usages:      usages,
		Callers:     callers,
		Callees:     callees,
	}
}

// Stats returns graph statistics.
func (g *GraphQuery) Stats() (symbols, refs int) {
	return g.Cache.Stats()
}

// SymbolInfo is a complete profile of a code symbol.
type SymbolInfo struct {
	Definitions []Symbol    `json:"definitions"`
	Usages      []Reference `json:"usages"`
	Callers     []Reference `json:"callers"`
	Callees     []Reference `json:"callees"`
}

// matchesFilter checks if a relative path matches a path filter prefix.
func matchesFilter(relPath, filter string) bool {
	if filter == "" {
		return true
	}
	relPath = filepath.ToSlash(relPath)
	filter = filepath.ToSlash(filter)
	return len(relPath) >= len(filter) && relPath[:len(filter)] == filter
}

// filterRefsByPath filters references by a path filter prefix.
func filterRefsByPath(refs []Reference, pathFilter string) []Reference {
	var out []Reference
	for _, r := range refs {
		rel := filepath.ToSlash(r.FilePath)
		filter := filepath.ToSlash(pathFilter)
		if stringsHasPrefix(rel, filter) {
			out = append(out, r)
		}
	}
	return out
}

// stringsHasPrefix checks prefix without importing strings (to avoid import cycles
// in some edge cases — but we can use strings directly here).
func stringsHasPrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return s[:len(prefix)] == prefix
}

// Ensure init log is used
var _ = slog.Default
