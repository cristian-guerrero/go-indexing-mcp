package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/indexer"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/llama"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type MCPServer struct {
	server          *server.MCPServer
	indexer         *indexer.Indexer
	mgr             *llama.Manager
	currentBranch   string
	currentWorktree string
	lastActivity    atomic.Int64
	idleTimeout     time.Duration
	watchInterval   time.Duration
	stopped         atomic.Bool
}

func New(idx *indexer.Indexer, mgr *llama.Manager, idleTimeoutSecs int, watchEnabled bool, watchIntervalSecs int) *MCPServer {
	s := server.NewMCPServer(
		"go-indexing-mcp",
		"1.0.0",
	)

	if idleTimeoutSecs <= 0 {
		idleTimeoutSecs = 300
	}
	if watchIntervalSecs <= 0 {
		watchIntervalSecs = 0
	}

	m := &MCPServer{
		server:        s,
		indexer:       idx,
		mgr:           mgr,
		idleTimeout:   time.Duration(idleTimeoutSecs) * time.Second,
		watchInterval: time.Duration(watchIntervalSecs) * time.Second,
	}
	m.lastActivity.Store(time.Now().UnixNano())

	m.registerTools()
	go m.idleChecker()
	if watchEnabled && watchIntervalSecs > 0 {
		go m.watchChecker()
	}
	go m.indexOnStartup()
	return m
}

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
	if err := m.indexer.Storage.SwitchBranch(branch, worktree); err != nil {
		slog.Warn("branch switch failed on startup", "error", err)
	}
	m.currentBranch = branch
	m.currentWorktree = worktree

	stats := m.indexer.GetStats()
	if stats.TotalChunks == 0 {
		slog.Info("index is empty, indexing on startup")
		if err := m.ensureLlama(); err != nil {
			slog.Error("llama not available for startup index", "error", err)
			return
		}
		m.runIndexAll()
		return
	}

	lastSHA := m.indexer.Storage.GetCommitSHA()

	if lastSHA == "" {
		slog.Info("interrupted index detected, resuming partial index")
		if err := m.ensureLlama(); err != nil {
			slog.Error("llama not available for startup reindex", "error", err)
			return
		}
		m.runIndexAll()
	} else {
		headSHA := m.indexer.Walker.GetHeadSHA()
		if headSHA != "" && headSHA != lastSHA {
			slog.Info("new commits detected, incremental index on startup", "last", lastSHA, "head", headSHA)
			if err := m.ensureLlama(); err != nil {
				slog.Error("llama not available for startup incremental index", "error", err)
				return
			}
			m.runIndexChanged()
		} else {
			slog.Info("index is up to date")
		}
	}
}

func (m *MCPServer) runIndexAll() {
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
			slog.Error("full index failed", "attempt", attempt+1, "error", err)
			continue
		}
		return
	}
}

func (m *MCPServer) runIndexChanged() {
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
			slog.Warn("incremental index failed", "attempt", attempt+1, "error", err)
			continue
		}
		return
	}
}

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
}

func (m *MCPServer) touchActivity() {
	m.lastActivity.Store(time.Now().UnixNano())
}

func (m *MCPServer) ensureLlama() error {
	m.touchActivity()
	if m.mgr == nil {
		return nil
	}
	if m.mgr.IsRunning() {
		return nil
	}
	slog.Info("llama-server not running, starting it")
	return m.mgr.Start()
}

func (m *MCPServer) idleChecker() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if m.stopped.Load() {
			return
		}

		last := time.Unix(0, m.lastActivity.Load())
		if time.Since(last) < m.idleTimeout {
			continue
		}

		if m.mgr != nil && m.mgr.IsRunning() && m.mgr.StartedProcess() {
			slog.Info("idle timeout reached, stopping llama-server to free memory", "idle", m.idleTimeout)
			m.mgr.Stop()
		}
	}
}

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
			continue
		}

		m.touchActivity()

		if branch != m.currentBranch || worktree != m.currentWorktree {
			slog.Info("watch: branch/worktree changed", "from", m.currentBranch+"/"+m.currentWorktree, "to", branch+"/"+worktree)
			if err := m.indexer.Storage.SwitchBranch(branch, worktree); err != nil {
				slog.Warn("watch: branch switch failed", "error", err)
			}
			m.currentBranch = branch
			m.currentWorktree = worktree
		}

		if stats.TotalChunks == 0 {
			slog.Info("watch: index is empty, performing full index")
			if err := m.ensureLlama(); err != nil {
				slog.Warn("watch: llama not available for full index", "error", err)
				continue
			}
			m.runIndexAll()
			continue
		}

		lastSHA := m.indexer.Storage.GetCommitSHA()
		headSHA := m.indexer.Walker.GetHeadSHA()

		if lastSHA == "" {
			slog.Info("watch: interrupted index detected, resuming partial index")
			if err := m.ensureLlama(); err != nil {
				slog.Warn("watch: llama not available for reindex", "error", err)
				continue
			}
			m.runIndexAll()
		} else if headSHA != "" && headSHA != lastSHA {
			slog.Info("watch: new commits detected, indexing changes", "last", lastSHA, "head", headSHA)
			if err := m.ensureLlama(); err != nil {
				slog.Warn("watch: llama not available for incremental index", "error", err)
				continue
			}
			m.runIndexChanged()
		} else {
			slog.Debug("watch: checking for uncommitted changes")
			if err := m.ensureLlama(); err != nil {
				slog.Warn("watch: llama not available for background index", "error", err)
				continue
			}
			go m.runIndexChanged()
		}
	}
}

const (
	msgIndexBuilding = "Index build is in progress, please retry your search in a moment"
	msgNoIndex       = "No index found. The index could not be built (check that llama.cpp and the model are available, or that the project is a git repository)"
)

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

	branch := m.indexer.Walker.GetBranch()
	worktree := m.indexer.Walker.GetWorktreeName()
	if branch != m.currentBranch || worktree != m.currentWorktree {
		slog.Info("branch/worktree changed", "from", m.currentBranch+"/"+m.currentWorktree, "to", branch+"/"+worktree)
		if err := m.indexer.Storage.SwitchBranch(branch, worktree); err != nil {
			slog.Warn("branch switch failed, continuing", "error", err)
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
	data, _ := json.MarshalIndent(results, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

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

	branch := m.indexer.Walker.GetBranch()
	worktree := m.indexer.Walker.GetWorktreeName()
	if branch != m.currentBranch || worktree != m.currentWorktree {
		slog.Info("branch/worktree changed", "from", m.currentBranch+"/"+m.currentWorktree, "to", branch+"/"+worktree)
		if err := m.indexer.Storage.SwitchBranch(branch, worktree); err != nil {
			slog.Warn("branch switch failed, continuing", "error", err)
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
	data, _ := json.MarshalIndent(results, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func (m *MCPServer) Serve() error {
	slog.Info("starting MCP server (stdio)")
	err := server.ServeStdio(m.server)
	if err != nil {
		slog.Info("MCP server stopped", "error", err)
	} else {
		slog.Info("MCP server stopped (client disconnected)")
	}
	m.stopped.Store(true)
	if m.mgr != nil && m.mgr.IsRunning() {
		slog.Info("stopping llama-server on MCP server shutdown")
		m.mgr.Stop()
	}
	return err
}
