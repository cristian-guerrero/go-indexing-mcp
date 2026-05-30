// Package mcp implements the Model Context Protocol server.
// Registers search_code and grep_code tools, handles auto-indexing on startup,
// branch switching, idle timeout (llama-server memory free), and periodic watch.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/graph"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/indexer"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/llama"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/storage"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/walker"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// MCPServer wraps the MCP protocol server with indexer, llama manager,
// watch checker for auto-reindexing, and branch-isolated index switching.
type MCPServer struct {
	server          *server.MCPServer
	indexer         *indexer.Indexer
	mgr             *llama.Manager
	currentBranch   string
	currentWorktree string
	watchInterval   time.Duration
	stopped         atomic.Bool
}

// New creates an MCPServer, registers MCP tools, and starts background goroutines
// for periodic watch (if enabled) and startup index check. llama-server manages its
// own idle sleep via --sleep-idle-seconds, so no Go-level idle checker is needed.
func New(idx *indexer.Indexer, mgr *llama.Manager, watchEnabled bool, watchIntervalSecs int) *MCPServer {
	s := server.NewMCPServer(
		"go-indexing-mcp",
		"1.0.0",
	)

	if watchIntervalSecs <= 0 {
		watchIntervalSecs = 0
	}

	m := &MCPServer{
		server:        s,
		indexer:       idx,
		mgr:           mgr,
		watchInterval: time.Duration(watchIntervalSecs) * time.Second,
	}

	m.registerTools()
	if watchEnabled && watchIntervalSecs > 0 {
		go m.watchChecker()
	}
	go m.indexOnStartup()
	return m
}

// indexOnStartup checks the index state on MCP startup (git repos only):
// empty → IndexAll, interrupted → IndexAll, new commits → IndexChanged, up to date → skip.
// Retries up to 3 times if the branch changes during indexing.
func (m *MCPServer) indexOnStartup() {
	if m.indexer == nil || m.indexer.Embedder == nil {
		return
	}

	branch := m.indexer.Walker.GetBranch()
	worktree := m.indexer.Walker.GetWorktreeName()
	if branch == "" {
		slog.Debug("not a git repository, skipping startup index")
		return
	}

	slog.Info("git repository detected, checking index state on startup", "branch", branch, "worktree", worktree)

	for attempts := 0; attempts < 3; attempts++ {
		// Re-detect branch each attempt (it may have changed)
		branch = m.indexer.Walker.GetBranch()
		worktree = m.indexer.Walker.GetWorktreeName()
		if branch == "" {
			slog.Debug("not a git repository, skipping startup index")
			return
		}
		if err := m.indexer.Storage.SwitchBranch(branch, worktree); err != nil {
			slog.Warn("branch switch failed on startup", "error", err)
		}
		if m.indexer.Graph != nil {
			if err := m.indexer.Graph.SwitchBranch(branch, worktree); err != nil {
				slog.Warn("graph: branch switch on startup", "error", err)
			}
		}
		m.currentBranch = branch
		m.currentWorktree = worktree
		m.indexer.SetExpectedBranch(branch, worktree)

		// Try seeding if the current branch has no index or was interrupted
		startupStats := m.indexer.GetStats()
		if startupStats.TotalChunks == 0 || m.indexer.Storage.GetCommitSHA() == "" {
			if m.seedBranchFrom(branch, worktree) {
				slog.Info("startup: branch seeded from another branch, will use incremental index")
			}
		}

		// Check for on-disk format version mismatch (breaking changes)
		storageNeedsReindex := m.indexer.Storage.NeedsReindex()
		var graphNeedsReindex bool
		if m.indexer.Graph != nil {
			graphNeedsReindex = m.indexer.Graph.NeedsReindex()
		}
		if storageNeedsReindex || graphNeedsReindex {
			slog.Warn("on-disk format version mismatch detected, triggering full reindex",
				"storage_needs_reindex", storageNeedsReindex,
				"graph_needs_reindex", graphNeedsReindex)
			if err := m.ensureLlama(); err != nil {
				slog.Error("llama not available for reindex on version mismatch", "error", err)
				return
			}
			m.runReindexAll()
			return
		}

		stats := m.indexer.GetStats()
		var indexErr error

		// If the knowledge graph is empty but index has data — populate it
		if stats.TotalChunks > 0 && m.indexer.Graph != nil && m.indexer.Extractor != nil {
			symCount, _ := m.indexer.Graph.Stats()
			if symCount == 0 {
				slog.Info("knowledge graph is empty, triggering full reindex to populate graph")
				if err := m.ensureLlama(); err != nil {
					slog.Error("llama not available for graph population", "error", err)
					return
				}
				indexErr = m.runIndexAll()
				if errors.Is(indexErr, indexer.ErrBranchChanged) {
					slog.Info("startup: branch changed during graph population, retrying", "attempt", attempts+1)
					continue
				}
				if indexErr != nil {
					slog.Warn("startup graph population aborted", "error", indexErr)
				}
				m.indexer.RunGraphExtraction()
				return
			}
		}

		if stats.TotalChunks == 0 {
			slog.Info("index is empty, indexing on startup")
			if err := m.ensureLlama(); err != nil {
				slog.Error("llama not available for startup index", "error", err)
				return
			}
			indexErr = m.runIndexAll()
			if errors.Is(indexErr, indexer.ErrBranchChanged) {
				slog.Info("startup: branch changed during empty index, retrying", "attempt", attempts+1)
				continue
			}
			if indexErr != nil {
					slog.Warn("startup full index failed", "error", indexErr)
				}
				m.indexer.RunGraphExtraction()
				return
		}

		lastSHA := m.indexer.Storage.GetCommitSHA()

		if lastSHA == "" {
			slog.Info("partial index found, filling remaining gaps")
			if err := m.ensureLlama(); err != nil {
				slog.Error("llama not available for startup reindex", "error", err)
				return
			}
			indexErr = m.runIndexAll()
			if errors.Is(indexErr, indexer.ErrBranchChanged) {
				slog.Info("startup: branch changed during interrupted index, retrying", "attempt", attempts+1)
				continue
			}
			if indexErr != nil {
					slog.Warn("startup interrupted index failed", "error", indexErr)
				}
				m.indexer.RunGraphExtraction()
				return
		}

		headSHA := m.indexer.Walker.GetHeadSHA()
		if headSHA != "" && headSHA != lastSHA {
			slog.Info("new commits detected, incremental index on startup", "last", lastSHA, "head", headSHA)
			if err := m.ensureLlama(); err != nil {
				slog.Error("llama not available for startup incremental index", "error", err)
				return
			}
			indexErr = m.runIndexChanged()
			if errors.Is(indexErr, indexer.ErrBranchChanged) {
				slog.Info("startup: branch changed during incremental index, retrying", "attempt", attempts+1)
				continue
			}
			if indexErr != nil {
				slog.Warn("startup incremental index failed", "error", indexErr)
			}
		} else {
			slog.Info("index is up to date, checking for working tree changes")
			if err := m.ensureLlama(); err != nil {
				slog.Warn("llama not available for working tree check on startup", "error", err)
			} else {
				indexErr = m.runIndexChanged()
				if errors.Is(indexErr, indexer.ErrBranchChanged) {
					slog.Info("startup: branch changed during working tree index, retrying", "attempt", attempts+1)
					continue
				}
				if indexErr != nil {
					slog.Warn("startup working tree index failed", "error", indexErr)
				}
			}
		}

		// Check if ignore patterns changed — trigger full reindex if so
		if m.indexer.CheckIgnoreHash() {
			slog.Info("ignore patterns changed, triggering full reindex on startup")
			if err := m.ensureLlama(); err != nil {
				slog.Error("llama not available for reindex after ignore change", "error", err)
				return
			}
			indexErr = m.runIndexAll()
			if errors.Is(indexErr, indexer.ErrBranchChanged) {
				slog.Info("startup: branch changed during ignore reindex, retrying", "attempt", attempts+1)
				continue
			}
			if indexErr != nil {
					slog.Warn("startup reindex after ignore change failed", "error", indexErr)
				}
		}

		m.indexer.RunGraphExtraction()
		return
	}

	slog.Warn("startup index aborted after 3 retries (branch changed repeatedly)")
}

// retryOnBranchChange calls fn, retrying up to 3 times on ErrBranchChanged.
// On each branch change, it re-detects the current branch and switches storage/graph.
func (m *MCPServer) retryOnBranchChange(fn func() error) error {
	for attempts := 0; attempts < 3; attempts++ {
		err := fn()
		if err == nil {
			return nil
		}
		if errors.Is(err, indexer.ErrBranchChanged) {
			branch := m.indexer.Walker.GetBranch()
			worktree := m.indexer.Walker.GetWorktreeName()
			if err := m.indexer.Storage.SwitchBranch(branch, worktree); err != nil {
				slog.Warn("retry: branch switch failed", "error", err)
			}
			if m.indexer.Graph != nil {
				if err := m.indexer.Graph.SwitchBranch(branch, worktree); err != nil {
					slog.Warn("retry: graph branch switch failed", "error", err)
				}
			}
			m.currentBranch = branch
			m.currentWorktree = worktree
			m.indexer.SetExpectedBranch(branch, worktree)
			slog.Info("retry: branch changed, retrying index on new branch", "branch", branch, "worktree", worktree, "attempt", attempts+1)
			continue
		}
		return err
	}
	return fmt.Errorf("index failed after 3 retries (branch changed repeatedly)")
}

// branchSource describes a candidate branch whose index can be used
// to seed another branch during branch switching.
type branchSource struct {
	branch       string
	worktree     string
	gobPath      string
	graphDir     string
	records      int
	hasCommitSHA bool // true if the source index was ever fully completed
}

// findBestSource looks for the best branch to seed from when the target
// branch has no existing index or its index is incomplete. Priority:
// main → other complete branches → largest index. Skips the target branch
// itself (seeding from the same branch is a no-op).
// Returns nil if no suitable source is found.
func findBestSource(st *storage.Storage, gq *graph.GraphQuery, w *walker.Walker, targetBranch, targetWorktree string) *branchSource {
	var candidates []branchSource

	tryBranch := func(branch, worktree string) {
		if branch == targetBranch && worktree == targetWorktree {
			return
		}
		gobPath := st.GobPath(branch, worktree)
		if _, err := os.Stat(gobPath); err != nil {
			return
		}
		if !w.BranchExists(branch) {
			return
		}
		data := storage.LoadGob(gobPath)
		if data == nil {
			return
		}
		var graphDir string
		if gq != nil {
			graphDir = gq.BranchDir(branch, worktree)
		}
		candidates = append(candidates, branchSource{
			branch: branch, worktree: worktree,
			gobPath: gobPath, graphDir: graphDir,
			records: data.Records,
			hasCommitSHA: data.CommitSHA != "",
		})
	}

	// 1. Try main (preferred)
	tryBranch(walker.DefaultBranch, "")
	// 2. If target is not main, try any other branch via git branch list
	if targetBranch != walker.DefaultBranch {
		branches := listLocalBranches(w.Root)
		for _, b := range branches {
			if b == targetBranch || b == walker.DefaultBranch {
				continue
			}
			tryBranch(b, "")
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	// Pick the candidate with the most records; prefer complete sources on ties
	best := &candidates[0]
	for i := 1; i < len(candidates); i++ {
		ci, bi := &candidates[i], best
		if ci.records > bi.records || (ci.records == bi.records && ci.hasCommitSHA && !bi.hasCommitSHA) {
			best = ci
		}
	}
	slog.Info("branch seeding: selected source",
		"branch", best.branch, "worktree", best.worktree,
		"records", best.records)
	return best
}

// listLocalBranches returns all local git branch names (without the "refs/heads/" prefix).
func listLocalBranches(root string) []string {
	out, err := execGit(root, "branch", "--format=%(refname:short)")
	if err != nil {
		return nil
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			branches = append(branches, line)
		}
	}
	return branches
}

// execGit runs a git command and returns stdout. Used internally.
func execGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// SeedBranchFrom copies the index from the best available source branch
// to the target branch so that only files that differ between the two
// branches need re-indexing. Returns true if seeding was performed and
// the storage/graph have been switched to the target branch.
//
// Seeding applies in two cases:
//  1. The target gob doesn't exist yet (first visit to this branch).
//  2. The target gob exists but is incomplete (commitSHA == ""), and the
//     source branch has more records — allowing the partial index to be
//     upgraded instead of starting from scratch.
//
// After seeding: target gob contains all source records minus files that
// differ between merge-base and target HEAD, and commitSHA is set to the
// merge-base so IndexChanged correctly diffs only changed files.
//
// This is a standalone function usable from both MCPServer and CLI handlers.
func SeedBranchFrom(st *storage.Storage, gq *graph.GraphQuery, w *walker.Walker, targetBranch, targetWorktree string) bool {
	targetGob := st.GobPath(targetBranch, targetWorktree)

	// Check target gob state
	targetSize := 0
	if data := storage.LoadGob(targetGob); data != nil {
		if data.CommitSHA != "" {
			return false // already complete
		}
		targetSize = data.Records // incomplete — may still benefit from seeding
	}

	source := findBestSource(st, gq, w, targetBranch, targetWorktree)
	if source == nil {
		return false
	}

	// Don't seed from the same branch (would be a no-op)
	if source.branch == targetBranch && source.worktree == targetWorktree {
		slog.Debug("branch seeding: only available source is the target itself, skipping",
			"branch", targetBranch)
		return false
	}

	// If target has a partial index, only seed if source is strictly better
	if targetSize > 0 && source.records <= targetSize {
		slog.Info("branch seeding: source not better than partial target, skipping",
			"source", source.branch, "source_records", source.records,
			"target_records", targetSize)
		return false
	}

	mergeBase := w.GetMergeBase(source.branch, targetBranch)
	if mergeBase == "" {
		slog.Warn("branch seeding: no merge-base found", "source", source.branch, "target", targetBranch)
		return false
	}

	changedOutput, err := execGit(w.Root, "diff", "--name-only", mergeBase, targetBranch)
	if err != nil {
		slog.Warn("branch seeding: git diff failed", "error", err)
		return false
	}
	var changedFiles []string
	for _, line := range strings.Split(strings.TrimSpace(changedOutput), "\n") {
		if line != "" {
			changedFiles = append(changedFiles, line)
		}
	}

	if source.records > 0 && len(changedFiles)*2 > source.records {
		slog.Info("branch seeding: too many changed files, falling back to full index",
			"source", source.branch, "changed", len(changedFiles), "source_records", source.records)
		return false
	}

	slog.Info("branch seeding: copying index",
		"source", source.branch, "target", targetBranch,
		"source_records", source.records, "changed_files", len(changedFiles))

	input, err := os.ReadFile(source.gobPath)
	if err != nil {
		slog.Warn("branch seeding: read source gob failed", "error", err)
		return false
	}
	if err := os.WriteFile(targetGob, input, 0644); err != nil {
		slog.Warn("branch seeding: write target gob failed", "error", err)
		return false
	}

	if source.graphDir != "" && gq != nil {
		tgtGraphDir := gq.BranchDir(targetBranch, targetWorktree)
		if err := os.RemoveAll(tgtGraphDir); err != nil {
			slog.Warn("branch seeding: remove target graph dir", "error", err)
		}
		if err := copyDir(source.graphDir, tgtGraphDir); err != nil {
			slog.Warn("branch seeding: copy graph dir failed", "error", err)
		}
	}

	if err := st.SwitchBranch(targetBranch, targetWorktree); err != nil {
		slog.Warn("branch seeding: switch branch failed", "error", err)
		return false
	}
	if gq != nil {
		if err := gq.SwitchBranch(targetBranch, targetWorktree); err != nil {
			slog.Warn("branch seeding: graph switch branch failed", "error", err)
		}
	}

	for _, relPath := range changedFiles {
		fullPath := filepath.Join(w.Root, relPath)
		st.DeleteChunksByPath(fullPath)
		if gq != nil {
			gq.RemoveFile(relPath)
		}
	}

	if source.hasCommitSHA {
		st.SetCommitSHA(mergeBase)
		slog.Info("branch seeding: complete (complete source)",
			"source", source.branch,
			"stale_files_removed", len(changedFiles),
			"commit_sha", mergeBase[:8]+"...")
	} else {
		// Source was incomplete (interrupted index). Leave commitSHA empty
		// so the caller fills remaining gaps via IndexAll.
		st.SetCommitSHA("")
		slog.Info("branch seeding: complete (partial source, gaps will be filled)",
			"source", source.branch,
			"stale_files_removed", len(changedFiles))
	}
	st.Save()
	return true
}

// seedBranchFrom delegates to the standalone SeedBranchFrom and updates
// MCPServer state on success.
func (m *MCPServer) seedBranchFrom(targetBranch, targetWorktree string) bool {
	if !SeedBranchFrom(m.indexer.Storage, m.indexer.Graph, m.indexer.Walker, targetBranch, targetWorktree) {
		return false
	}
	m.currentBranch = targetBranch
	m.currentWorktree = targetWorktree
	m.indexer.SetExpectedBranch(targetBranch, targetWorktree)
	return true
}

// copyDir recursively copies a directory from src to dst.
// Used during branch seeding to copy graph data between branches.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
}

// runIndexAll calls IndexAll with up to 3 retries on failure.
// Returns ErrBranchChanged without retrying — the caller should adapt to the new branch.
// Other errors are retried up to 3 times.
func (m *MCPServer) runIndexAll() error {
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(2 * time.Second)
			slog.Warn("retrying full index", "attempt", attempt+1)
			if err := m.ensureLlama(); err != nil {
				slog.Error("llama unavailable for retry", "error", err)
				continue
			}
		}
		if err := m.indexer.IndexAll(); err != nil {
			if errors.Is(err, indexer.ErrBranchChanged) {
				slog.Info("full index cancelled: branch change detected")
				return err
			}
			slog.Error("full index failed", "attempt", attempt+1, "error", err)
			continue
		}
		slog.Info("full index complete")
		return nil
	}
	return fmt.Errorf("full index failed after 3 attempts")
}

// runReindexAll clears both vector and graph databases, then runs a full index
// with graph extraction. Used when a format version mismatch is detected.
func (m *MCPServer) runReindexAll() {
	if err := m.indexer.Storage.ClearAll(); err != nil {
		slog.Error("clear storage for reindex", "error", err)
	}
	if m.indexer.Graph != nil {
		if err := m.indexer.Graph.Store.Clear(); err != nil {
			slog.Error("clear graph for reindex", "error", err)
		}
	}
	// Re-read stats after clearing (GetStats refreshes from storage)
	m.indexer.GetStats()
	if err := m.runIndexAll(); err != nil {
		slog.Warn("reindex aborted", "error", err)
	}
	m.indexer.RunGraphExtraction()
}

// runIndexChanged calls IndexChanged with up to 3 retries on failure.
// Returns ErrBranchChanged without retrying — the caller should adapt to the new branch.
// Other errors are retried up to 3 times.
func (m *MCPServer) runIndexChanged() error {
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(2 * time.Second)
			slog.Warn("retrying incremental index", "attempt", attempt+1)
			if err := m.ensureLlama(); err != nil {
				slog.Error("llama unavailable for retry", "error", err)
				continue
			}
		}
		if err := m.indexer.IndexChanged(); err != nil {
			if errors.Is(err, indexer.ErrBranchChanged) {
				slog.Info("incremental index cancelled: branch change detected")
				return err
			}
			slog.Warn("incremental index failed", "attempt", attempt+1, "error", err)
			continue
		}
		slog.Info("incremental index complete")
		return nil
	}
	return fmt.Errorf("incremental index failed after 3 attempts")
}

// registerTools registers the search_code and grep_code MCP tools with the server.
func (m *MCPServer) registerTools() {
	searchTool := mcp.NewTool("search_code",
		mcp.WithDescription("Search code by intent using BM25 keyword ranking fused with vector similarity via RRF (k=60). Best for queries like 'authentication flow', 'database connection pool', 'user registration'. Returns up to 25 results ranked by relevance"),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Natural language description of what the code does, e.g. 'authentication flow', 'save model to disk', 'database connection pool'"),
		),
		mcp.WithString("path_filter",
			mcp.Description("Optional path prefix filter to narrow results to a specific directory, e.g. 'pkg/', 'pkg/llama/', 'main.go'"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return (default: 25, max: 50)"),
		),
	)

	grepTool := mcp.NewTool("grep_code",
		mcp.WithDescription("Fast literal or regex substring search on cached code chunks. Best for exact symbols like 'func validate', 'DB_HOST', or regex patterns like 'type.*Downloader'. Returns results with exact line numbers and match locations ranked by frequency. Results on definition lines (func, type, class, interface) are boosted 2x. Auto-indexes if the index is empty"),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Literal text or regex pattern to search for. Case-insensitive by default. Examples: 'func validate', 'DB_HOST', 'type.*Downloader'"),
		),
		mcp.WithString("path_filter",
			mcp.Description("Path filter — supports prefix ('pkg/'), exact file ('main.go'), or glob patterns ('*.go', '**/*_test.go', 'pkg/*.go')"),
		),
		mcp.WithString("lang",
			mcp.Description("Filter by language: go, python, typescript, javascript, rust, java, etc."),
		),
		mcp.WithBoolean("case_sensitive",
			mcp.Description("Case-sensitive matching (default: false)"),
		),
		mcp.WithBoolean("word_boundary",
			mcp.Description("Match whole words only, e.g. 'get' won't match 'getter' (default: false)"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return (default: 25, max: 50)"),
		),
	)

	m.server.AddTool(searchTool, m.handleSearch)
	m.server.AddTool(grepTool, m.handleGrepSearch)

	m.registerGraphTools()
}

// ensureLlama checks if llama-server is running and starts it if not.
// After starting, it updates the embedder's BaseURL to match the actual server port.
func (m *MCPServer) ensureLlama() error {
	if m.mgr == nil {
		return nil
	}
	if m.mgr.IsRunning() {
		// Sync embedder URL in case it was constructed before Start() set the port.
		if m.indexer != nil && m.indexer.Embedder != nil {
			m.indexer.Embedder.BaseURL = m.mgr.BaseURL()
		}
		return nil
	}
	slog.Info("llama-server not running, starting it")
	if err := m.mgr.Start(); err != nil {
		return err
	}
	// Update embedder with the actual URL after successful start.
	if m.indexer != nil && m.indexer.Embedder != nil {
		m.indexer.Embedder.BaseURL = m.mgr.BaseURL()
	}
	return nil
}

// watchChecker runs periodically (watchInterval) and:
// 1. Detects branch/worktree switches → SwitchBranch
// 2. Empty index → IndexAll, 3. New commits → IndexChanged,
// 4. Uncommitted changes → background IndexChanged.
func (m *MCPServer) watchChecker() {
	ticker := time.NewTicker(m.watchInterval)
	defer ticker.Stop()

	for range ticker.C {
		if m.stopped.Load() {
			return
		}

		branch := m.indexer.Walker.GetBranch()
		worktree := m.indexer.Walker.GetWorktreeName()
		if branch == "" {
			continue
		}

		if m.indexer.Embedder == nil {
			continue
		}

		stats := m.indexer.GetStats()
		if stats.IsIndexing {
			// If branch changed during indexing, cancel and let the next tick restart
			if branch != m.currentBranch || worktree != m.currentWorktree {
				slog.Info("watch: branch changed during indexing, cancelling",
					"from", m.currentBranch+"/"+m.currentWorktree,
					"to", branch+"/"+worktree,
				)
				m.indexer.Cancel()
				for m.indexer.GetStats().IsIndexing {
					time.Sleep(100 * time.Millisecond)
				}
				if err := m.indexer.Storage.SwitchBranch(branch, worktree); err != nil {
					slog.Warn("watch: branch switch after cancel failed", "error", err)
				}
				if m.indexer.Graph != nil {
					if err := m.indexer.Graph.SwitchBranch(branch, worktree); err != nil {
						slog.Warn("watch: graph branch switch after cancel failed", "error", err)
					}
				}
				m.currentBranch = branch
				m.currentWorktree = worktree
				m.indexer.SetExpectedBranch(branch, worktree)
				// Don't start indexing now — the next tick will handle the new branch
			}
			continue
		}

		if branch != m.currentBranch || worktree != m.currentWorktree {
			slog.Info("watch: branch/worktree changed", "from", m.currentBranch+"/"+m.currentWorktree, "to", branch+"/"+worktree)
			if err := m.indexer.Storage.SwitchBranch(branch, worktree); err != nil {
				slog.Warn("watch: branch switch failed", "error", err)
			}
			if m.indexer.Graph != nil {
				if err := m.indexer.Graph.SwitchBranch(branch, worktree); err != nil {
					slog.Warn("watch: graph branch switch failed", "error", err)
				}
			}
			m.currentBranch = branch
			m.currentWorktree = worktree
			m.indexer.SetExpectedBranch(branch, worktree)

			// Refresh stats after switch and try seeding if empty or incomplete
			stats = m.indexer.GetStats()
			if stats.TotalChunks == 0 || m.indexer.Storage.GetCommitSHA() == "" {
				if m.seedBranchFrom(branch, worktree) {
					stats = m.indexer.GetStats()
				}
			}
		}

		// Check for on-disk format version mismatch
		if m.indexer.Storage.NeedsReindex() {
			slog.Warn("watch: storage format version changed, triggering full reindex")
			if err := m.ensureLlama(); err != nil {
				slog.Warn("watch: llama not available for reindex", "error", err)
				continue
			}
			m.runReindexAll()
			continue
		}
		if m.indexer.Graph != nil && m.indexer.Graph.NeedsReindex() {
			slog.Warn("watch: graph format version changed, triggering full reindex")
			if err := m.ensureLlama(); err != nil {
				slog.Warn("watch: llama not available for reindex", "error", err)
				continue
			}
			m.runReindexAll()
			continue
		}

		// Check if ignore patterns changed — trigger full reindex if so
		if m.indexer.CheckIgnoreHash() {
			slog.Info("watch: ignore patterns changed, triggering full reindex")
			if err := m.ensureLlama(); err != nil {
				slog.Warn("watch: llama not available for reindex after ignore change", "error", err)
				continue
			}
			if err := m.retryOnBranchChange(m.runIndexAll); err != nil {
				slog.Warn("watch: full index aborted", "error", err)
			}
			m.indexer.RunGraphExtraction()
			continue
		}

		// If the graph is empty but index has data and extractor is available,
		// do a full reindex to populate the graph (upgrade from non-onnx build).
		if stats.TotalChunks > 0 && m.indexer.Graph != nil && m.indexer.Extractor != nil {
			symCount, _ := m.indexer.Graph.Stats()
			if symCount == 0 {
				slog.Info("watch: knowledge graph is empty, triggering full reindex to populate graph")
				if err := m.ensureLlama(); err != nil {
					slog.Warn("watch: llama not available for graph population", "error", err)
					continue
				}
				if err := m.retryOnBranchChange(m.runIndexAll); err != nil {
					slog.Warn("watch: graph population index aborted", "error", err)
				}
				m.indexer.RunGraphExtraction()
				continue
			}
		}

		if stats.TotalChunks == 0 {
			slog.Info("watch: index is empty, performing full index")
			if err := m.ensureLlama(); err != nil {
				slog.Warn("watch: llama not available for full index", "error", err)
				continue
			}
			if err := m.retryOnBranchChange(m.runIndexAll); err != nil {
				slog.Warn("watch: full index aborted", "error", err)
			}
			m.indexer.RunGraphExtraction()
			continue
		}

		lastSHA := m.indexer.Storage.GetCommitSHA()
		headSHA := m.indexer.Walker.GetHeadSHA()

		if lastSHA == "" {
			slog.Info("watch: partial index found, filling remaining gaps")
			if err := m.ensureLlama(); err != nil {
				slog.Warn("watch: llama not available for reindex", "error", err)
				continue
			}
			if err := m.retryOnBranchChange(m.runIndexAll); err != nil {
				slog.Warn("watch: full index aborted", "error", err)
			}
			m.indexer.RunGraphExtraction()
		} else if headSHA != "" && headSHA != lastSHA {
			slog.Info("watch: new commits detected, indexing changes", "last", lastSHA, "head", headSHA)
			if err := m.ensureLlama(); err != nil {
				slog.Warn("watch: llama not available for incremental index", "error", err)
				continue
			}
			if err := m.retryOnBranchChange(m.runIndexChanged); err != nil {
				slog.Warn("watch: incremental index aborted", "error", err)
			}
			m.indexer.RunGraphExtraction()
		} else {
			slog.Debug("watch: checking for uncommitted changes")
			if err := m.ensureLlama(); err != nil {
				slog.Warn("watch: llama not available for background index", "error", err)
				continue
			}
			go func() {
				if err := m.retryOnBranchChange(m.runIndexChanged); err != nil {
					slog.Warn("watch: background index aborted", "error", err)
				}
				m.indexer.RunGraphExtraction()
			}()
		}
	}
}

const (
	msgIndexBuilding = "Index build is in progress, please retry your search in a moment"
	msgNoIndex       = "No index found. The index could not be built (check that llama.cpp and the model are available, or that the project is a git repository)"
)

// handleSearch is the MCP tool handler for search_code.
// Wakes llama-server, switches branch if needed, cancels any in-progress index
// on a stale branch, waits for in-progress indexing on the current branch, and
// returns JSON results.  Retries from scratch if the branch changes mid-index.
func (m *MCPServer) handleSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	query, _ := args["query"].(string)
	pathFilter, _ := args["path_filter"].(string)

	limit := 25
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	slog.Debug("tool called: search_code",
		"query", query,
		"path_filter", pathFilter,
		"limit", limit,
	)

	if err := m.ensureLlama(); err != nil {
		slog.Error("llama wake failed", "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("llama-server wake failed: %s", err)), nil
	}

	for attempts := 0; attempts < 3; attempts++ {
		branch := m.indexer.Walker.GetBranch()
		worktree := m.indexer.Walker.GetWorktreeName()

		if branch != m.currentBranch || worktree != m.currentWorktree {
			slog.Info("search: branch/worktree changed", "from", m.currentBranch+"/"+m.currentWorktree, "to", branch+"/"+worktree)
			// Cancel any in-progress index on the stale branch
			m.indexer.Cancel()
			if err := m.indexer.Storage.SwitchBranch(branch, worktree); err != nil {
				slog.Warn("search: branch switch failed", "error", err)
			}
			if m.indexer.Graph != nil {
				if err := m.indexer.Graph.SwitchBranch(branch, worktree); err != nil {
					slog.Warn("search: graph branch switch failed", "error", err)
				}
			}
			m.currentBranch = branch
			m.currentWorktree = worktree
		}

		stats := m.indexer.GetStats()

		if stats.TotalChunks == 0 {
			if stats.IsIndexing {
				for i := 0; i < 50; i++ {
					time.Sleep(500 * time.Millisecond)
					stats = m.indexer.GetStats()
					if !stats.IsIndexing {
						break
					}
				}
				if stats.TotalChunks == 0 {
					return mcp.NewToolResultText(msgIndexBuilding), nil
				}
			} else {
				return mcp.NewToolResultText(msgNoIndex), nil
			}
		}

		// Set expected branch so the indexer can detect mid-index branch switches
		m.indexer.SetExpectedBranch(branch, worktree)

		// Incremental index for uncommitted changes and untracked files
		lastSHA := m.indexer.Storage.GetCommitSHA()
		headSHA := m.indexer.Walker.GetHeadSHA()
		var indexErr error
		if headSHA != "" && headSHA != lastSHA {
			slog.Info("search: new commits detected, updating index")
			indexErr = m.runIndexChanged()
		} else {
			indexErr = m.runIndexChanged()
		}

		if errors.Is(indexErr, indexer.ErrBranchChanged) {
			slog.Info("search: index cancelled due to branch change, retrying handler")
			m.currentBranch = "" // force branch re-detection on next attempt
			continue
		}
		if indexErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("incremental index failed: %s", indexErr)), nil
		}

		// Check if ignore patterns changed
		if m.indexer.CheckIgnoreHash() {
			slog.Info("search: ignore patterns changed, triggering full reindex")
			if err := m.runIndexAll(); err != nil {
				if errors.Is(err, indexer.ErrBranchChanged) {
					slog.Info("search: full index cancelled due to branch change, retrying handler")
					m.currentBranch = ""
					continue
				}
				slog.Warn("search: full index aborted", "error", err)
			}
		}

		results, err := m.indexer.Search(query, pathFilter, limit, "hybrid")
		if err != nil {
			slog.Error("search failed", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %s", err)), nil
		}

		slog.Debug("search completed",
			"query", query,
			"results", len(results),
			"path_filter", pathFilter,
		)

		if len(results) == 0 {
			return mcp.NewToolResultText("No results found. The index may not contain files from this path. Check that the path_filter matches indexed files, or that the project has been indexed."), nil
		}
		var maxScore float64
		for _, r := range results {
			if r.Score > maxScore {
				maxScore = r.Score
			}
		}
		for i := range results {
			if maxScore > 0 {
				results[i].Score = (results[i].Score / maxScore) * 100
			} else {
				results[i].Score = 0
			}
		}
		data, _ := json.MarshalIndent(results, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}

	return mcp.NewToolResultError("search failed after retries (branch changed repeatedly)"), nil
}

// handleGrepSearch is the MCP tool handler for grep_code.
// Performs substring/regex matching on cached chunks. Does NOT need llama.cpp.
// Retries from scratch if the branch changes mid-request.
func (m *MCPServer) handleGrepSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	query, _ := args["query"].(string)
	pathFilter, _ := args["path_filter"].(string)
	lang, _ := args["lang"].(string)
	caseSensitive, _ := args["case_sensitive"].(bool)
	wordBoundary, _ := args["word_boundary"].(bool)

	limit := 25
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	if query == "" {
		return mcp.NewToolResultError("query is required"), nil
	}

	slog.Debug("tool called: grep_code",
		"query", query,
		"path_filter", pathFilter,
		"lang", lang,
		"case_sensitive", caseSensitive,
		"word_boundary", wordBoundary,
		"limit", limit,
	)

	for attempts := 0; attempts < 3; attempts++ {
		branch := m.indexer.Walker.GetBranch()
		worktree := m.indexer.Walker.GetWorktreeName()

		if branch != m.currentBranch || worktree != m.currentWorktree {
			slog.Info("grep: branch/worktree changed", "from", m.currentBranch+"/"+m.currentWorktree, "to", branch+"/"+worktree)
			// Cancel any in-progress index on the stale branch
			m.indexer.Cancel()
			if err := m.indexer.Storage.SwitchBranch(branch, worktree); err != nil {
				slog.Warn("grep: branch switch failed", "error", err)
			}
			if m.indexer.Graph != nil {
				if err := m.indexer.Graph.SwitchBranch(branch, worktree); err != nil {
					slog.Warn("grep: graph branch switch failed", "error", err)
				}
			}
			m.currentBranch = branch
			m.currentWorktree = worktree
		}

		stats := m.indexer.GetStats()

		if stats.TotalChunks == 0 || stats.TotalFiles == 0 {
			if stats.IsIndexing {
				for i := 0; i < 50; i++ {
					time.Sleep(500 * time.Millisecond)
					stats = m.indexer.GetStats()
					if !stats.IsIndexing {
						break
					}
				}
				if stats.TotalChunks == 0 || stats.TotalFiles == 0 {
					return mcp.NewToolResultText(msgIndexBuilding), nil
				}
			} else {
				return mcp.NewToolResultText(msgNoIndex), nil
			}
		}

		results, err := m.indexer.SearchGrep(storage.GrepOptions{
			Query:         query,
			Limit:         limit,
			CaseSensitive: caseSensitive,
			WholeWord:     wordBoundary,
			Language:      lang,
		}, pathFilter)
		if err != nil {
			slog.Error("grep search failed", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("grep search failed: %s", err)), nil
		}

		slog.Debug("grep search completed",
			"query", query,
			"results", len(results),
			"path_filter", pathFilter,
		)

		if len(results) == 0 {
			return mcp.NewToolResultText("No matches found."), nil
		}
		for i := range results {
			results[i].Score *= 100
		}
		data, _ := json.MarshalIndent(results, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}

	return mcp.NewToolResultError("grep search failed after retries (branch changed repeatedly)"), nil
}

// Serve starts the MCP stdio server. Blocks until the client disconnects.
// On shutdown, releases the lock reference — llama-server stays alive if other
// MCP processes are still using it.
func (m *MCPServer) Serve() error {
	slog.Info("starting MCP server (stdio)")
	err := server.ServeStdio(m.server)
	if err != nil {
		slog.Info("MCP server stopped", "error", err)
	} else {
		slog.Info("MCP server stopped (client disconnected)")
	}
	m.stopped.Store(true)
	slog.Info("MCP server shut down, llama-server left running for reuse")
	return err
}
