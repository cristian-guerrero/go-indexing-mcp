package graph

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/db"
)

// GraphQuery provides a unified query interface over the knowledge graph,
// backed by SQLite. Supports branch-isolated indexes via db.Store.
type GraphQuery struct {
	Store      *db.Store
	DBPath     string
	storageDir string
}

// NewGraphQuery opens or creates a graph database backed by SQLite.
// Takes either a path to a .sqlite file, or a directory path (in which
// case graph.sqlite is created inside it).
func NewGraphQuery(graphPath string) (*GraphQuery, error) {
	ext := filepath.Ext(graphPath)
	if ext != ".sqlite" {
		if err := os.MkdirAll(graphPath, 0755); err != nil {
			return nil, err
		}
		graphPath = filepath.Join(graphPath, "graph.sqlite")
	}

	store, err := db.Open(graphPath, 0)
	if err != nil {
		return nil, fmt.Errorf("open graph store: %w", err)
	}

	gq := newFromStore(store)
	return gq, nil
}

// NewGraphQueryFromStore wraps an existing db.Store for graph queries.
// Used when sharing a Store with vector storage (single DB file).
func NewGraphQueryFromStore(store *db.Store) *GraphQuery {
	return newFromStore(store)
}

func newFromStore(store *db.Store) *GraphQuery {
	gq := &GraphQuery{
		Store:      store,
		DBPath:     store.Path(),
		storageDir: filepath.Dir(store.Path()),
	}
	if gq.NeedsReindex() {
		slog.Warn("graph: database has old format version, will re-extract on next index")
	}
	return gq
}

// NeedsReindex returns true when the on-disk graph format version differs from
// the current code version. Triggers a full re-extraction of graph data.
func (g *GraphQuery) NeedsReindex() bool {
	return g.Store.GraphNeedsReindex()
}

// SwitchBranch persists the current graph and switches to a branch-specific index.
func (g *GraphQuery) SwitchBranch(branch, worktree string) error {
	if g.Store == nil {
		return nil
	}
	suffix := branchSuffix(branch, worktree)

	// Derive the branch-specific path from the current directory
	base := strings.TrimSuffix(g.DBPath, ".sqlite")
	newPath := base + suffix + ".sqlite"

	oldStore := g.Store
	g.Store = nil

	// Close old store
	if err := oldStore.Close(); err != nil {
		slog.Warn("graph: close old store", "error", err)
	}

	store, err := db.Open(newPath, 0)
	if err != nil {
		return fmt.Errorf("open branch graph store: %w", err)
	}
	g.Store = store
	g.DBPath = newPath

	return nil
}

// BranchDir returns the graph directory path for the given branch/worktree.
func (g *GraphQuery) BranchDir(branch, worktree string) string {
	base := strings.TrimSuffix(g.DBPath, ".sqlite")
	return base + branchSuffix(branch, worktree) + ".sqlite"
}

// Close closes the underlying store.
func (g *GraphQuery) Close() error {
	if g.Store == nil {
		return nil
	}
	return g.Store.Close()
}

// StoreFile stores symbols and references for a file atomically.
func (g *GraphQuery) StoreFile(relPath string, symbols []Symbol, refs []Reference) error {
	return g.Store.StoreFile(relPath, symbols, refs)
}

// HasFile returns true if the graph already has symbols extracted for the given
// relative file path. Used by IndexAll to skip redundant tree-sitter parsing.
func (g *GraphQuery) HasFile(relPath string) bool {
	if g.Store == nil {
		return false
	}
	ok, _ := g.Store.HasFile(relPath)
	return ok
}

// ResolveRefs resolves all unresolved references (empty TargetID) in the cache
// and persists the resolved refs to disk. Returns the number of resolved refs.
func (g *GraphQuery) ResolveRefs() int {
	if g.Store == nil {
		return 0
	}
	resolved, err := g.Store.ResolveRefs()
	if err != nil {
		slog.Warn("graph: resolve refs error", "error", err)
		return 0
	}
	return resolved
}

// RemoveFile removes all symbols and references for a file.
func (g *GraphQuery) RemoveFile(relPath string) {
	if g.Store == nil {
		return
	}
	if err := g.Store.RemoveFile(relPath); err != nil {
		slog.Warn("graph: remove file", "file", relPath, "error", err)
	}
}

// FindDefinition looks up a symbol definition by name.
func (g *GraphQuery) FindDefinition(name string, pathFilter string) []Symbol {
	if g.Store == nil {
		return nil
	}
	symbols, err := g.Store.FindDefinition(name, pathFilter)
	if err != nil {
		slog.Warn("graph: find definition", "name", name, "error", err)
		return nil
	}
	return symbols
}

// FindUsages returns all references to a symbol by name.
func (g *GraphQuery) FindUsages(name string, pathFilter string) []Reference {
	if g.Store == nil {
		return nil
	}
	refs, err := g.Store.FindUsages(name, pathFilter)
	if err != nil {
		slog.Warn("graph: find usages", "name", name, "error", err)
		return nil
	}
	return refs
}

// FindImports returns import symbols matching a module path pattern.
func (g *GraphQuery) FindImports(pattern string) []*Symbol {
	if g.Store == nil {
		return nil
	}
	syms, err := g.Store.FindImports(pattern)
	if err != nil {
		slog.Warn("graph: find imports", "pattern", pattern, "error", err)
		return nil
	}
	return syms
}

// GetCallers returns symbols that call a given function/method name.
func (g *GraphQuery) GetCallers(name string) []Reference {
	if g.Store == nil {
		return nil
	}
	refs, err := g.Store.GetCallers(name)
	if err != nil {
		slog.Warn("graph: get callers", "name", name, "error", err)
		return nil
	}
	return refs
}

// GetCallees returns all function names called within a symbol.
func (g *GraphQuery) GetCallees(symID string) []Reference {
	if g.Store == nil {
		return nil
	}
	refs, err := g.Store.GetCallees(symID)
	if err != nil {
		slog.Warn("graph: get callees", "symID", symID, "error", err)
		return nil
	}
	return refs
}

// GetSymbolInfo returns a complete profile of a symbol including its definition,
// usages, callers, and callees.
func (g *GraphQuery) GetSymbolInfo(name string, pathFilter string) *SymbolInfo {
	if g.Store == nil {
		return nil
	}
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
	if g.Store == nil {
		return 0, 0
	}
	symbols, refs, _ = g.Store.GraphStats()
	return
}

func branchSuffix(branch, worktree string) string {
	return db.BranchSuffix(branch, worktree)
}

func matchesFilter(relPath, filter string) bool {
	if filter == "" {
		return true
	}
	relPath = filepath.ToSlash(relPath)
	filter = filepath.ToSlash(filter)
	return len(relPath) >= len(filter) && relPath[:len(filter)] == filter
}

func filterRefsByPath(refs []Reference, pathFilter string) []Reference {
	var out []Reference
	for _, r := range refs {
		if matchesFilter(r.FilePath, pathFilter) {
			out = append(out, r)
		}
	}
	return out
}

// ListSymbolFiles returns distinct rel_path values from the symbols table.
func (g *GraphQuery) ListSymbolFiles() ([]string, error) {
	if g.Store == nil {
		return nil, nil
	}
	return g.Store.ListSymbolFiles()
}
