package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/cristian/go-indexing-mcp/pkg/indexer"
	"github.com/mark3labs/mcp-go/server"
	"github.com/mark3labs/mcp-go/mcp"
)

type MCPServer struct {
	server  *server.MCPServer
	indexer *indexer.Indexer
}

func New(idx *indexer.Indexer) *MCPServer {
	s := server.NewMCPServer(
		"go-indexing-mcp",
		"1.0.0",
	)

	m := &MCPServer{
		server:  s,
		indexer: idx,
	}

	m.registerTools()
	return m
}

func (m *MCPServer) registerTools() {
	searchTool := mcp.NewTool("search_code",
		mcp.WithDescription("Semantic code search. Converts the query into an embedding vector using llama.cpp and returns the most semantically similar code chunks ranked by cosine similarity. Results include file path, line range, language, and similarity score. Use this to find relevant code by intent, not by keyword matching."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Natural language query describing what you are looking for, e.g. 'database connection pool', 'error handling middleware', 'user authentication'"),
		),
		mcp.WithString("path_filter",
			mcp.Description("Optional path prefix filter to narrow results to a specific directory, e.g. 'pkg/', 'pkg/llama/', 'main.go'"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return (default: 10, max: 50)"),
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

func (m *MCPServer) handleSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	query, _ := args["query"].(string)
	pathFilter, _ := args["path_filter"].(string)

	limit := 10
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	slog.Debug("tool called: search_code",
		"query", query,
		"path_filter", pathFilter,
		"limit", limit,
	)

	stats := m.indexer.GetStats()
	if stats.TotalChunks == 0 {
		slog.Info("no index found, indexing on first search")
		if err := m.indexer.IndexAll(); err != nil {
			slog.Error("initial index failed", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("initial index failed: %s", err)), nil
		}
	}

	results, err := m.indexer.Search(query, pathFilter, limit)
	if err != nil {
		slog.Error("search failed", "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %s", err)), nil
	}

	slog.Debug("search completed",
		"query", query,
		"results", len(results),
		"path_filter", pathFilter,
	)

	data, _ := json.MarshalIndent(results, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func (m *MCPServer) handleReindex(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

	if err := m.indexer.IndexPath(path); err != nil {
		slog.Error("index_path failed", "path", path, "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("index path failed: %s", err)), nil
	}

	slog.Info("index_path completed", "path", path)
	return mcp.NewToolResultText(fmt.Sprintf("Indexed: %s", path)), nil
}

func (m *MCPServer) handleListFiles(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	return err
}
