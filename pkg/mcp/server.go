package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/cristian/go-indexing-mcp/pkg/indexer"
	"github.com/cristian/go-indexing-mcp/pkg/llama"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type MCPServer struct {
	server        *server.MCPServer
	indexer       *indexer.Indexer
	mgr           *llama.Manager
	currentBranch string
	lastActivity  atomic.Int64
	idleTimeout   time.Duration
	stopped       atomic.Bool
}

func New(idx *indexer.Indexer, mgr *llama.Manager, idleTimeoutSecs int) *MCPServer {
	s := server.NewMCPServer(
		"go-indexing-mcp",
		"1.0.0",
	)

	if idleTimeoutSecs <= 0 {
		idleTimeoutSecs = 300
	}

	m := &MCPServer{
		server:      s,
		indexer:     idx,
		mgr:         mgr,
		idleTimeout: time.Duration(idleTimeoutSecs) * time.Second,
	}
	m.lastActivity.Store(time.Now().UnixNano())

	m.registerTools()
	go m.idleChecker()
	return m
}

func (m *MCPServer) registerTools() {
	searchTool := mcp.NewTool("search_code",
		mcp.WithDescription("Search code by intent using BM25 keyword ranking fused with vector similarity via RRF (k=60). Best for queries like 'authentication flow', 'database connection pool', 'user registration'. Returns up to 25 results ranked by relevance. If the index is outdated or empty, it automatically re-indexes the project first. Use grep_code for fast literal symbol/pattern matching without llama."),
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
		mcp.WithDescription("Fast literal or regex substring search on cached code chunks. Best for exact symbols like 'func validate', 'DB_HOST', or regex patterns like 'type.*Downloader'. Returns up to 25 results ranked by match frequency within each chunk. Auto-indexes if the index is empty. Use search_code for intent-based semantic search."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Literal text or regex pattern to search for. Case-insensitive. Examples: 'func validate', 'DB_HOST', 'type.*Downloader'"),
		),
		mcp.WithString("path_filter",
			mcp.Description("Optional path prefix filter to narrow results to a specific directory, e.g. 'pkg/', 'pkg/llama/', 'main.go'"),
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
	slog.Info("waking llama-server from idle sleep")
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
	if branch != m.currentBranch {
		slog.Info("branch changed", "from", m.currentBranch, "to", branch)
		if err := m.indexer.Storage.SwitchBranch(branch); err != nil {
			slog.Warn("branch switch failed, continuing", "error", err)
		}
		m.currentBranch = branch
	}

	stats := m.indexer.GetStats()
	didFullIndex := false

	if stats.TotalChunks == 0 {
		slog.Info("no index found, indexing on first search")
		if err := m.indexer.IndexAll(); err != nil {
			slog.Error("initial index failed", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("initial index failed: %s", err)), nil
		}
		didFullIndex = true
	} else if !stats.IsIndexing {
		lastSHA := m.indexer.Storage.GetCommitSHA()

		if lastSHA == "" {
			slog.Info("index has no commit SHA (legacy/interrupted), performing full reindex")
			if err := m.indexer.IndexAll(); err != nil {
				slog.Error("full reindex failed", "error", err)
			}
			didFullIndex = true
		} else {
			headSHA := m.indexer.Walker.GetHeadSHA()
			hasNewCommits := headSHA != "" && headSHA != lastSHA

			if hasNewCommits {
				slog.Info("new commits detected, indexing changes before search", "last", lastSHA, "head", headSHA)
				if err := m.indexer.IndexChanged(); err != nil {
					slog.Warn("incremental index failed, continuing with existing index", "error", err)
				}
			} else {
				slog.Debug("triggering background incremental index for uncommitted changes")
				go func() {
					if err := m.indexer.IndexChanged(); err != nil {
						slog.Warn("background incremental index failed", "error", err)
					}
				}()
			}
		}
	}

	results, err := m.indexer.Search(query, pathFilter, limit, "hybrid")
	if err != nil {
		slog.Error("search failed", "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %s", err)), nil
	}

	if len(results) == 0 && !didFullIndex {
		slog.Info("no results found, performing full reindex and retrying search")
		if err := m.indexer.IndexAll(); err != nil {
			slog.Error("reindex on empty results failed", "error", err)
		} else {
			results, err = m.indexer.Search(query, pathFilter, limit, "hybrid")
			if err != nil {
				slog.Error("retry search failed", "error", err)
			}
		}
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
		"limit", limit,
	)

	branch := m.indexer.Walker.GetBranch()
	if branch != m.currentBranch {
		slog.Info("branch changed", "from", m.currentBranch, "to", branch)
		if err := m.indexer.Storage.SwitchBranch(branch); err != nil {
			slog.Warn("branch switch failed, continuing", "error", err)
		}
		m.currentBranch = branch
	}

	stats := m.indexer.GetStats()
	if stats.TotalChunks == 0 || stats.TotalFiles == 0 {
		slog.Info("no index found, indexing before grep")
		if err := m.ensureLlama(); err != nil {
			slog.Error("llama wake failed", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("llama-server wake failed: %s", err)), nil
		}
		if err := m.indexer.IndexAll(); err != nil {
			slog.Error("initial index failed", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("initial index failed: %s", err)), nil
		}
	}

	results, err := m.indexer.Search(query, pathFilter, limit, "grep")
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
