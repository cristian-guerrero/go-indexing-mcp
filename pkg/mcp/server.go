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
		mcp.WithDescription("Search code semantically. Returns relevant code chunks ranked by relevance."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The search query in natural language"),
		),
		mcp.WithString("path_filter",
			mcp.Description("Optional path filter (e.g. 'pkg/')"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results (default: 10)"),
		),
	)

	statusTool := mcp.NewTool("index_status",
		mcp.WithDescription("Get the current indexing status: total chunks, files, last index time."),
	)

	reindexTool := mcp.NewTool("reindex",
		mcp.WithDescription("Trigger a full re-index of all files."),
	)

	indexPathTool := mcp.NewTool("index_path",
		mcp.WithDescription("Index a specific file or directory."),
		mcp.WithString("path",
			mcp.Required(),
			mcp.Description("Relative path of the file or directory to index"),
		),
	)

	m.server.AddTool(searchTool, m.handleSearch)
	m.server.AddTool(statusTool, m.handleStatus)
	m.server.AddTool(reindexTool, m.handleReindex)
	m.server.AddTool(indexPathTool, m.handleIndexPath)
}

func (m *MCPServer) handleSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	query, _ := args["query"].(string)
	pathFilter, _ := args["path_filter"].(string)

	limit := 10
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	results, err := m.indexer.Search(query, pathFilter, limit)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %s", err)), nil
	}

	data, _ := json.MarshalIndent(results, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func (m *MCPServer) handleStatus(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	stats := m.indexer.GetStats()
	data, _ := json.MarshalIndent(stats, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func (m *MCPServer) handleReindex(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	go func() {
		if err := m.indexer.IndexAll(); err != nil {
			slog.Error("reindex failed", "error", err)
		}
	}()
	return mcp.NewToolResultText("Re-indexing started in background"), nil
}

func (m *MCPServer) handleIndexPath(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	path, _ := args["path"].(string)
	if path == "" {
		return mcp.NewToolResultError("path is required"), nil
	}

	if err := m.indexer.IndexPath(path); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("index path failed: %s", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Indexed: %s", path)), nil
}

func (m *MCPServer) Serve() error {
	slog.Info("starting MCP server (stdio)")
	return server.ServeStdio(m.server)
}
