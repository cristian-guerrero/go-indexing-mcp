package graph

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/storage"
)

// GraphQuery provides a unified query interface over the knowledge graph,
// backed by file-based JSON persistence. Supports branch-isolated indexes.
type GraphQuery struct {
	DB           *GraphDB
	Cache        *KnowledgeGraph
	DBPath       string
	storageDir   string
	branchSuffix string
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
	if fi, err := os.Stat(oldSub); err == nil && fi.IsDir() {
		os.RemoveAll(oldSub)
	}

	db, err := OpenGraph(graphDir)
	if err != nil {
		return nil, err
	}

	if db.NeedsReindex() {
		slog.Warn("graph: database has old format version, will re-extract on next index",
			"disk_version", db.diskVersion, "current_version", storage.GraphFormatVersion)
	}

	cache := NewGraph()
	if err := db.LoadAll(cache); err != nil {
		slog.Warn("graph: failed to load existing data, starting fresh", "error", err)
	}

	symCount, refCount := cache.Stats()
	if symCount > 0 {
		slog.Info("graph: loaded existing index", "symbols", symCount, "refs", refCount)
		// Resolve cross-file references in memory (persistence happens on next extraction).
		if resolved, _ := cache.ResolveRefs(); resolved > 0 {
			slog.Info("graph: cross-file references resolved on load", "count", resolved)
		}
	}

	return &GraphQuery{
		DB:         db,
		Cache:      cache,
		DBPath:     graphDir,
		storageDir: graphDir,
	}, nil
}

// NeedsReindex returns true when the on-disk graph format version differs from
// the current code version. Triggers a full re-extraction of graph data.
func (g *GraphQuery) NeedsReindex() bool {
	if g.DB == nil {
		return false
	}
	return g.DB.NeedsReindex()
}

// SwitchBranch persists the current graph and switches to a branch-specific index.
// The new path is the parent of graphDir with a branch suffix appended.
// For branches other than "main", the graph is stored at e.g.
// {storageDir}-{worktree}-{branch}/. For main, it reverts to {storageDir}/.
func (g *GraphQuery) SwitchBranch(branch, worktree string) error {
	if g.DB != nil {
		if err := g.DB.Close(); err != nil {
			slog.Warn("graph: close before branch switch", "error", err)
		}
		g.DB = nil
	}

	suffix := branchSuffix(branch, worktree)
	g.branchSuffix = suffix

	newDir := g.storageDir + suffix

	g.Cache.Clear()

	db, err := OpenGraph(newDir)
	if err != nil {
		return err
	}
	g.DB = db
	g.DBPath = newDir

	if db.NeedsReindex() {
		slog.Warn("graph: branch has old format version, clearing and re-extracting on next index",
			"branch", branch, "disk_version", db.diskVersion, "current_version", storage.GraphFormatVersion)
	}

	if err := db.LoadAll(g.Cache); err != nil {
		slog.Warn("graph: load branch", "branch", branch, "error", err)
	}

	symCount, refCount := g.Cache.Stats()
	slog.Info("graph: switched branch", "branch", branch, "worktree", worktree, "symbols", symCount, "refs", refCount)

	// Resolve cross-file references in memory after loading the branch
	if symCount > 0 {
		if resolved, _ := g.Cache.ResolveRefs(); resolved > 0 {
			slog.Info("graph: cross-file references resolved after branch switch", "count", resolved)
		}
	}

	return nil
}

// BranchDir returns the graph directory path for the given branch/worktree.
// For main: {storageDir}/, for others: {storageDir}-{worktree}-{branch}/.
// Used by MCPServer to check directory existence and copy graph data during branch seeding.
func (g *GraphQuery) BranchDir(branch, worktree string) string {
	return g.storageDir + branchSuffix(branch, worktree)
}

// Close closes the underlying DB.
func (g *GraphQuery) Close() error {
	if g.DB != nil {
		return g.DB.Close()
	}
	return nil
}

// StoreFile stores symbols and references for a file atomically.
func (g *GraphQuery) StoreFile(relPath string, symbols []Symbol, refs []Reference) error {
	g.Cache.RemoveFile(relPath)
	for _, sym := range symbols {
		g.Cache.AddSymbol(sym)
	}
	for _, ref := range refs {
		g.Cache.AddReference(ref)
	}

	if g.DB != nil {
		if err := g.DB.StoreFile(relPath, symbols, refs); err != nil {
			return err
		}
	}

	return nil
}

// HasFile returns true if the graph already has symbols extracted for the given
// relative file path. Used by IndexAll to skip redundant tree-sitter parsing.
func (g *GraphQuery) HasFile(relPath string) bool {
	if g.Cache == nil {
		return false
	}
	g.Cache.mu.RLock()
	defer g.Cache.mu.RUnlock()
	_, ok := g.Cache.ByFile[relPath]
	return ok
}

// ResolveRefs resolves all unresolved references (empty TargetID) in the cache
// and persists the resolved refs to disk. Returns the number of resolved refs.
// Should be called after RunGraphExtraction() completes.
func (g *GraphQuery) ResolveRefs() int {
	if g.Cache == nil {
		return 0
	}
	resolved, modifiedPaths := g.Cache.ResolveRefs()
	if resolved == 0 {
		return 0
	}
	for _, relPath := range modifiedPaths {
		refs := g.Cache.GetFileRefs(relPath)
		if g.DB != nil {
			if err := g.DB.SaveFileRefs(relPath, refs); err != nil {
				slog.Warn("graph: save resolved refs", "file", relPath, "error", err)
			}
		}
	}
	slog.Info("graph: cross-file references resolved",
		"count", resolved, "files_updated", len(modifiedPaths))
	return resolved
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
