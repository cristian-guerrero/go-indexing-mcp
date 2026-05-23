package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/graph"
	"github.com/mark3labs/mcp-go/mcp"
)

// registerGraphTools registers knowledge-graph query tools if the indexer has a graph.
func (m *MCPServer) registerGraphTools() {
	if m.indexer == nil || m.indexer.Graph == nil {
		return
	}

	findImportsTool := mcp.NewTool("find_imports",
		mcp.WithDescription("Find all files that import a given module or package. Supports partial matching."),
		mcp.WithString("pattern",
			mcp.Required(),
			mcp.Description("Module path to search for, e.g. 'fmt', 'os', 'github.com/user/project'"),
		),
	)

	symbolInfoTool := mcp.NewTool("symbol_info",
		mcp.WithDescription("Get a complete 360-degree view of a code symbol: its definition, all usages, callers, and callees."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("Symbol name to inspect, e.g. 'validate', 'Config', 'main'"),
		),
		mcp.WithString("path_filter",
			mcp.Description("Optional path prefix filter to narrow results"),
		),
	)

	m.server.AddTool(findImportsTool, m.handleFindImports)
	m.server.AddTool(symbolInfoTool, m.handleSymbolInfo)
}

func (m *MCPServer) handleFindImports(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	pattern, _ := args["pattern"].(string)

	if pattern == "" {
		return mcp.NewToolResultError("pattern is required"), nil
	}

	m.touchActivity()
	m.pruneGraph()

	g := m.indexer.Graph
	if g == nil {
		return mcp.NewToolResultText("Knowledge graph is not available. Build with -tags onnx to enable."), nil
	}

	imports := g.FindImports(pattern)
	if len(imports) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("No imports matching '%s' found.", pattern)), nil
	}

	type impResult struct {
		ModulePath string `json:"module_path"`
		FilePath   string `json:"file_path"`
		Line       int    `json:"line"`
		Signature  string `json:"signature,omitempty"`
	}

	results := make([]impResult, len(imports))
	for i, imp := range imports {
		results[i] = impResult{
			ModulePath: imp.Name,
			FilePath:   imp.FilePath,
			Line:       imp.StartLine,
			Signature:  imp.Signature,
		}
	}

	data, _ := json.MarshalIndent(results, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func (m *MCPServer) handleSymbolInfo(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	name, _ := args["name"].(string)
	pathFilter, _ := args["path_filter"].(string)

	if name == "" {
		return mcp.NewToolResultError("name is required"), nil
	}

	m.touchActivity()
	m.pruneGraph()

	g := m.indexer.Graph
	if g == nil {
		return mcp.NewToolResultText("Knowledge graph is not available. Build with -tags onnx to enable."), nil
	}

	info := g.GetSymbolInfo(name, pathFilter)

	type symbolInfoResult struct {
		Definition  []struct {
			Name      string `json:"name"`
			Kind      string `json:"kind"`
			FilePath  string `json:"file_path"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
			Signature string `json:"signature,omitempty"`
		} `json:"definitions"`
		Usages []struct {
			TargetName string  `json:"target_name"`
			Kind       string  `json:"kind"`
			FilePath   string  `json:"file_path"`
			Line       int     `json:"line"`
			Confidence float64 `json:"confidence"`
		} `json:"usages"`
		Callers []struct {
			TargetName string  `json:"target_name"`
			Kind       string  `json:"kind"`
			FilePath   string  `json:"file_path"`
			Line       int     `json:"line"`
		} `json:"callers"`
		Callees []struct {
			TargetName string  `json:"target_name"`
			Kind       string  `json:"kind"`
			FilePath   string  `json:"file_path"`
			Line       int     `json:"line"`
		} `json:"callees"`
	}

	var result symbolInfoResult

	for _, d := range info.Definitions {
		result.Definition = append(result.Definition, struct {
			Name      string `json:"name"`
			Kind      string `json:"kind"`
			FilePath  string `json:"file_path"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
			Signature string `json:"signature,omitempty"`
		}{
			Name: d.Name, Kind: d.Kind.String(),
			FilePath: d.FilePath, StartLine: d.StartLine, EndLine: d.EndLine,
			Signature: d.Signature,
		})
	}
	for _, u := range info.Usages {
		result.Usages = append(result.Usages, struct {
			TargetName string  `json:"target_name"`
			Kind       string  `json:"kind"`
			FilePath   string  `json:"file_path"`
			Line       int     `json:"line"`
			Confidence float64 `json:"confidence"`
		}{
			TargetName: u.TargetName, Kind: u.Kind.String(),
			FilePath: u.FilePath, Line: u.Line, Confidence: u.Confidence,
		})
	}
	for _, c := range info.Callers {
		result.Callers = append(result.Callers, struct {
			TargetName string  `json:"target_name"`
			Kind       string  `json:"kind"`
			FilePath   string  `json:"file_path"`
			Line       int     `json:"line"`
		}{
			TargetName: c.TargetName, Kind: c.Kind.String(),
			FilePath: c.FilePath, Line: c.Line,
		})
	}
	for _, c := range info.Callees {
		result.Callees = append(result.Callees, struct {
			TargetName string  `json:"target_name"`
			Kind       string  `json:"kind"`
			FilePath   string  `json:"file_path"`
			Line       int     `json:"line"`
		}{
			TargetName: c.TargetName, Kind: c.Kind.String(),
			FilePath: c.FilePath, Line: c.Line,
		})
	}

	data, _ := json.MarshalIndent(result, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

// pruneGraph removes stale entries from the knowledge graph (files no longer on disk).
func (m *MCPServer) pruneGraph() {
	if m.indexer == nil || m.indexer.Graph == nil {
		return
	}
	root := m.indexer.Walker.Root
	for relPath := range m.indexer.Graph.Cache.ByFile {
		fullPath := filepath.Join(root, relPath)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			m.indexer.Graph.RemoveFile(relPath)
			slog.Info("pruned stale graph entry", "file", relPath)
		}
	}
}

// ensure graph package is used
var _ = graph.Symbol{}
