package graph

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// KnowledgeGraph holds symbols and references in memory with indexes for fast
// querying by name, file, kind, and reference target.
type KnowledgeGraph struct {
	mu         sync.RWMutex
	Symbols    map[string]*Symbol        // id -> symbol
	Refs       []Reference               // all references
	ByName     map[string][]string       // lowercase name -> symbol IDs
	ByFile     map[string][]string       // relpath -> symbol IDs
	ByKind     map[SymbolKind][]string   // kind -> symbol IDs
	ByTarget   map[string][]Reference    // target name -> references
}

// NewGraph creates an empty KnowledgeGraph.
func NewGraph() *KnowledgeGraph {
	return &KnowledgeGraph{
		Symbols:  make(map[string]*Symbol),
		ByName:   make(map[string][]string),
		ByFile:   make(map[string][]string),
		ByKind:   make(map[SymbolKind][]string),
		ByTarget: make(map[string][]Reference),
	}
}

// AddSymbol inserts a symbol into the graph, updating all indexes.
func (g *KnowledgeGraph) AddSymbol(s Symbol) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.Symbols[s.ID] = &s

	name := strings.ToLower(s.Name)
	g.ByName[name] = append(g.ByName[name], s.ID)

	g.ByFile[s.RelPath] = append(g.ByFile[s.RelPath], s.ID)

	g.ByKind[s.Kind] = append(g.ByKind[s.Kind], s.ID)
}

// AddReference inserts a reference into the graph, updating the target index.
func (g *KnowledgeGraph) AddReference(r Reference) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.Refs = append(g.Refs, r)

	targetName := strings.ToLower(r.TargetName)
	g.ByTarget[targetName] = append(g.ByTarget[targetName], r)
}

// refMatchesPath checks if a reference's FilePath matches the given relative path.
// References may store either a full path or a relative path, so we check both.
func refMatchesPath(refFilePath, relPath string) bool {
	if refFilePath == "" {
		return false
	}
	if refFilePath == relPath {
		return true
	}
	refSlash := filepath.ToSlash(refFilePath)
	relSlash := filepath.ToSlash(relPath)
	return strings.HasSuffix(refSlash, "/"+relSlash)
}

// RemoveFile deletes all symbols and references belonging to a file.
func (g *KnowledgeGraph) RemoveFile(relPath string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ids := g.ByFile[relPath]
	for _, id := range ids {
		sym, ok := g.Symbols[id]
		if ok {
			name := strings.ToLower(sym.Name)
			g.ByName[name] = removeFromSlice(g.ByName[name], id)
			if len(g.ByName[name]) == 0 {
				delete(g.ByName, name)
			}

			g.ByKind[sym.Kind] = removeFromSlice(g.ByKind[sym.Kind], id)
			if len(g.ByKind[sym.Kind]) == 0 {
				delete(g.ByKind, sym.Kind)
			}

			delete(g.Symbols, id)
		}
	}
	delete(g.ByFile, relPath)

	// Remove references for this file
	var kept []Reference
	for _, ref := range g.Refs {
		if refMatchesPath(ref.FilePath, relPath) {
			continue
		}
		kept = append(kept, ref)
	}
	g.Refs = kept

	// Clean up ByTarget index
	for targetName := range g.ByTarget {
		var keptRefs []Reference
		for _, ref := range g.ByTarget[targetName] {
			if !refMatchesPath(ref.FilePath, relPath) {
				keptRefs = append(keptRefs, ref)
			}
		}
		if len(keptRefs) > 0 {
			g.ByTarget[targetName] = keptRefs
		} else {
			delete(g.ByTarget, targetName)
		}
	}
}

// FindByName returns all symbols whose name matches (case-insensitive).
func (g *KnowledgeGraph) FindByName(name string, kind ...SymbolKind) []*Symbol {
	g.mu.RLock()
	defer g.mu.RUnlock()

	name = strings.ToLower(name)
	ids, ok := g.ByName[name]
	if !ok {
		return nil
	}

	var results []*Symbol
	for _, id := range ids {
		sym := g.Symbols[id]
		if sym == nil {
			continue
		}
		if len(kind) > 0 {
			match := false
			for _, k := range kind {
				if sym.Kind == k {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		results = append(results, sym)
	}
	return results
}

// FindUsages returns all references targeting a symbol by name.
func (g *KnowledgeGraph) FindUsages(name string, pathFilter string) []Reference {
	g.mu.RLock()
	defer g.mu.RUnlock()

	name = strings.ToLower(name)
	refs, ok := g.ByTarget[name]
	if !ok {
		return nil
	}

	if pathFilter == "" {
		return copyRefs(refs)
	}

	var filtered []Reference
	for _, r := range refs {
		rel := r.FilePath
		if pathFilter == "" || strings.HasPrefix(strings.ToLower(rel), strings.ToLower(pathFilter)) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// FindImports returns all import symbols matching a module path pattern.
func (g *KnowledgeGraph) FindImports(pattern string) []*Symbol {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var results []*Symbol
	for _, sym := range g.Symbols {
		if sym.Kind != SymbolImport {
			continue
		}
		if pattern == "" || strings.Contains(strings.ToLower(sym.Name), strings.ToLower(pattern)) {
			results = append(results, sym)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Name < results[j].Name
	})
	return results
}

// GetFileSymbols returns all symbols defined in a file.
func (g *KnowledgeGraph) GetFileSymbols(relPath string) []*Symbol {
	g.mu.RLock()
	defer g.mu.RUnlock()

	ids := g.ByFile[relPath]
	var results []*Symbol
	for _, id := range ids {
		if sym := g.Symbols[id]; sym != nil {
			results = append(results, sym)
		}
	}
	return results
}

// GetFileRefs returns all references belonging to a file.
func (g *KnowledgeGraph) GetFileRefs(relPath string) []Reference {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var out []Reference
	for _, ref := range g.Refs {
		if refMatchesPath(ref.FilePath, relPath) {
			out = append(out, ref)
		}
	}
	return out
}

// ResolveRefs attempts to resolve all unresolved references (empty TargetID)
// by matching TargetName against known symbol definitions. When exactly one
// symbol matches the name, TargetID is set to that symbol's ID. Ambiguous
// names (multiple matches) are skipped to avoid false positives.
// Returns the count of resolved references and the list of modified file paths.
func (g *KnowledgeGraph) ResolveRefs() (resolved int, modifiedRelPaths []string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	seenFiles := make(map[string]bool)

	for i := range g.Refs {
		if g.Refs[i].TargetID != "" {
			continue
		}
		name := strings.ToLower(g.Refs[i].TargetName)
		ids := g.ByName[name]
		if len(ids) != 1 {
			continue
		}

		// Exactly one candidate — resolve
		g.Refs[i].TargetID = ids[0]

		// Update ByTarget index copy too
		targetRefs := g.ByTarget[name]
		for j := range targetRefs {
			if targetRefs[j].ID == g.Refs[i].ID {
				targetRefs[j].TargetID = ids[0]
				break
			}
		}
		g.ByTarget[name] = targetRefs
		resolved++

		// Track which source file changed
		for rel := range g.ByFile {
			if refMatchesPath(g.Refs[i].FilePath, rel) {
				if !seenFiles[rel] {
					seenFiles[rel] = true
					modifiedRelPaths = append(modifiedRelPaths, rel)
				}
				break
			}
		}
	}
	return
}

// Stats returns graph statistics.
func (g *KnowledgeGraph) Stats() (symbols, refs int) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.Symbols), len(g.Refs)
}

// Clear removes all symbols and references.
func (g *KnowledgeGraph) Clear() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Symbols = make(map[string]*Symbol)
	g.Refs = nil
	g.ByName = make(map[string][]string)
	g.ByFile = make(map[string][]string)
	g.ByKind = make(map[SymbolKind][]string)
	g.ByTarget = make(map[string][]Reference)
}

// copyRefs returns a copy of a reference slice.
func copyRefs(refs []Reference) []Reference {
	out := make([]Reference, len(refs))
	copy(out, refs)
	return out
}

// removeFromSlice removes a value from a string slice.
func removeFromSlice(slice []string, val string) []string {
	var out []string
	for _, s := range slice {
		if s != val {
			out = append(out, s)
		}
	}
	return out
}
