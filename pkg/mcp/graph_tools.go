package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/graph"
	"github.com/mark3labs/mcp-go/mcp"
)

// registerGraphTools registers knowledge-graph query tools if the indexer has a graph.
func (m *MCPServer) registerGraphTools() {
	if m.indexer == nil || m.indexer.Graph == nil {
		return
	}

	findUsagesTool := mcp.NewTool("find_usages",
		mcp.WithDescription("Find all usages of a code symbol (function, class, variable) by name. Returns each usage with file, line, and reference type (calls, imports, extends, etc.)."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("Symbol name to search for, e.g. 'validate', 'main', 'Config'"),
		),
		mcp.WithString("path_filter",
			mcp.Description("Optional path prefix filter to narrow results, e.g. 'pkg/', 'internal/'"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results (default: 25, max: 50)"),
		),
	)

	findDefTool := mcp.NewTool("find_definition",
		mcp.WithDescription("Find where a code symbol is defined. Returns the definition location (file, line range), kind (function, class, struct, etc.), and signature."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("Symbol name to find, e.g. 'validate', 'Config', 'main'"),
		),
		mcp.WithString("path_filter",
			mcp.Description("Optional path prefix filter, e.g. 'pkg/', 'internal/'"),
		),
	)

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

	m.server.AddTool(findUsagesTool, m.handleFindUsages)
	m.server.AddTool(findDefTool, m.handleFindDefinition)
	m.server.AddTool(findImportsTool, m.handleFindImports)
	m.server.AddTool(symbolInfoTool, m.handleSymbolInfo)
}

func (m *MCPServer) handleFindUsages(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	name, _ := args["name"].(string)
	pathFilter, _ := args["path_filter"].(string)

	limit := 25
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	if name == "" {
		return mcp.NewToolResultError("name is required"), nil
	}

	m.touchActivity()

	g := m.indexer.Graph
	if g == nil {
		return mcp.NewToolResultText("Knowledge graph is not available. Build with -tags onnx to enable."), nil
	}

	refs := g.FindUsages(name, pathFilter)
	if limit > 0 && len(refs) > limit {
		refs = refs[:limit]
	}

	if len(refs) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("No usages found for '%s'.", name)), nil
	}

	type usageResult struct {
		TargetName string  `json:"target_name"`
		Kind       string  `json:"kind"`
		FilePath   string  `json:"file_path"`
		Line       int     `json:"line"`
		Confidence float64 `json:"confidence"`
	}

	results := make([]usageResult, len(refs))
	for i, r := range refs {
		results[i] = usageResult{
			TargetName: r.TargetName,
			Kind:       r.Kind.String(),
			FilePath:   r.FilePath,
			Line:       r.Line,
			Confidence: r.Confidence,
		}
	}

	data, _ := json.MarshalIndent(results, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func (m *MCPServer) handleFindDefinition(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	name, _ := args["name"].(string)
	pathFilter, _ := args["path_filter"].(string)

	if name == "" {
		return mcp.NewToolResultError("name is required"), nil
	}

	m.touchActivity()

	g := m.indexer.Graph
	if g == nil {
		return mcp.NewToolResultText("Knowledge graph is not available. Build with -tags onnx to enable."), nil
	}

	defs := g.FindDefinition(name, pathFilter)
	if len(defs) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("No definition found for '%s'.", name)), nil
	}

	type defResult struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		FilePath  string `json:"file_path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
		Signature string `json:"signature,omitempty"`
		Exported  bool   `json:"exported"`
	}

	results := make([]defResult, len(defs))
	for i, d := range defs {
		results[i] = defResult{
			Name: d.Name, Kind: d.Kind.String(),
			FilePath: d.FilePath, StartLine: d.StartLine, EndLine: d.EndLine,
			Signature: d.Signature, Exported: d.Exported,
		}
	}

	data, _ := json.MarshalIndent(results, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func (m *MCPServer) handleFindImports(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	pattern, _ := args["pattern"].(string)

	if pattern == "" {
		return mcp.NewToolResultError("pattern is required"), nil
	}

	m.touchActivity()

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

// ensure graph package is used
var _ = graph.Symbol{}
