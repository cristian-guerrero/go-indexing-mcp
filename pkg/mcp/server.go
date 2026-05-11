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
	"github.com/mark3labs/mcp-go/server"
	"github.com/mark3labs/mcp-go/mcp"
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
		mcp.WithDescription("Search code using different modes: 'semantic' (default) uses embedding vectors via llama.cpp for intent-based search; 'grep' does fast substring matching on cached chunks without llama; 'hybrid' fuses BM25 keyword ranking with vector similarity via RRF."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Search query — natural language for semantic/hybrid, literal text for grep"),
		),
		mcp.WithString("path_filter",
			mcp.Description("Optional path prefix filter to narrow results to a specific directory, e.g. 'pkg/', 'pkg/llama/', 'main.go'"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return (default: 10, max: 50)"),
		),
		mcp.WithString("mode",
			mcp.Description("Search mode: 'semantic' (default), 'grep' (literal substring), or 'hybrid' (BM25 + vector RRF)"),
		),
	)

	reindexTool := mcp.NewTool("reindex",
		mcp.WithDescription("Triggers a full re-index of all files in the configured root path. Walks every file, re-chunks, re-embeds, and replaces the entire index. Use this when files have changed significantly or the index is stale. Runs asynchronously in the background."),
	)

	indexPathTool := mcp.NewTool("index_path",
		mcp.WithDescription("Index a single specific file or all files in a directory. For files, reads and chunks the file then replaces its existing embeddings. For directories, walks and indexes all supported files within. Use this for incremental indexing without re-indexing everything."),
		mcp.WithString("path",
			mcp.Required(),
			mcp.Description("Relative path of the file (e.g. 'main.go') or directory (e.g. 'pkg/config/') to index"),
		),
	)

	debugListFilesTool := mcp.NewTool("_debug_index_files",
		mcp.WithDescription("[DEBUG] List all file paths currently stored in the index. For debugging the MCP server itself, not for general use."),
	)

	m.server.AddTool(searchTool, m.handleSearch)
	m.server.AddTool(reindexTool, m.handleReindex)
	m.server.AddTool(indexPathTool, m.handleIndexPath)
	m.server.AddTool(debugListFilesTool, m.handleListFiles)
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

		if m.mgr != nil && m.mgr.IsRunning() {
			slog.Info("idle timeout reached, stopping llama-server to free memory", "idle", m.idleTimeout)
			m.mgr.Stop()
		}
	}
}

func (m *MCPServer) handleSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	query, _ := args["query"].(string)
	pathFilter, _ := args["path_filter"].(string)

	limit := 10
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	mode := "semantic"
	if m, ok := args["mode"].(string); ok && m != "" {
		mode = m
	}

	slog.Debug("tool called: search_code",
		"query", query,
		"path_filter", pathFilter,
		"limit", limit,
		"mode", mode,
	)

	needsLlama := mode != "grep"
	if needsLlama {
		if err := m.ensureLlama(); err != nil {
			slog.Error("llama wake failed", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("llama-server wake failed: %s", err)), nil
		}
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
	if stats.TotalChunks == 0 {
		if needsLlama {
			slog.Info("no index found, indexing on first search")
			if err := m.indexer.IndexAll(); err != nil {
				slog.Error("initial index failed", "error", err)
				return mcp.NewToolResultError(fmt.Sprintf("initial index failed: %s", err)), nil
			}
		}
	} else if needsLlama && !stats.IsIndexing {
		lastSHA := m.indexer.Storage.GetCommitSHA()
		headSHA := m.indexer.Walker.GetHeadSHA()
		hasNewCommits := headSHA != "" && lastSHA != "" && headSHA != lastSHA

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

	results, err := m.indexer.Search(query, pathFilter, limit, mode)
	if err != nil {
		slog.Error("search failed", "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %s", err)), nil
	}

	slog.Debug("search completed",
		"query", query,
		"results", len(results),
		"path_filter", pathFilter,
		"mode", mode,
	)

	data, _ := json.MarshalIndent(results, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func (m *MCPServer) handleReindex(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	m.touchActivity()
	slog.Debug("tool called: reindex")
	go func() {
		slog.Info("reindex started in background")
		if err := m.indexer.IndexAll(); err != nil {
			slog.Error("reindex failed", "error", err)
			return
		}
		slog.Info("reindex completed")
	}()
	return mcp.NewToolResultText("Re-indexing started in background"), nil
}

func (m *MCPServer) handleIndexPath(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	path, _ := args["path"].(string)
	if path == "" {
		return mcp.NewToolResultError("path is required"), nil
	}

	slog.Debug("tool called: index_path", "path", path)

	if err := m.ensureLlama(); err != nil {
		slog.Error("llama wake failed", "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("llama-server wake failed: %s", err)), nil
	}

	if err := m.indexer.IndexPath(path); err != nil {
		slog.Error("index_path failed", "path", path, "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("index path failed: %s", err)), nil
	}

	slog.Info("index_path completed", "path", path)
	return mcp.NewToolResultText(fmt.Sprintf("Indexed: %s", path)), nil
}

func (m *MCPServer) handleListFiles(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	m.touchActivity()
	slog.Debug("tool called: _debug_index_files")
	files := m.indexer.ListFiles()
	data, _ := json.MarshalIndent(files, "", "  ")
	slog.Debug("listed indexed files", "count", len(files))
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
