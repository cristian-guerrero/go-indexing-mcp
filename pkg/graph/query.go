package graph

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// GraphQuery provides a unified query interface over the knowledge graph,
// backed by Pebble for persistence. Supports branch-isolated indexes.
type GraphQuery struct {
	DB           *GraphDB
	Cache        *KnowledgeGraph // in-memory cache
	DBPath       string          // current path (with branch suffix)
	storageDir   string          // base storage directory
	branchSuffix string          // empty for main, "-{name}" for other branches
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

// NewGraphQuery opens or creates a graph database at the given directory.
// Uses the base graphDir without branch isolation. Call SwitchBranch to
// switch to a branch-specific index.
func NewGraphQuery(graphDir string) (*GraphQuery, error) {
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

	db, err := OpenGraph(graphDir)
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

	return &GraphQuery{
		DB:         db,
		Cache:      cache,
		DBPath:     graphDir,
		storageDir: graphDir,
	}, nil
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

	if err := os.MkdirAll(newDir, 0755); err != nil {
		return fmt.Errorf("mkdir graph branch: %w", err)
	}

	db, err := OpenGraph(newDir)
	if err != nil {
		return err
	}
	g.DB = db
	g.DBPath = newDir
	g.Cache.Clear()

	// Load existing data from this branch
	if err := db.LoadAll(g.Cache); err != nil {
		slog.Warn("graph: load branch", "branch", branch, "error", err)
	}

	symCount, refCount := g.Cache.Stats()
	slog.Info("graph: switched branch", "branch", branch, "worktree", worktree, "symbols", symCount, "refs", refCount)

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
