// main is the entry point for go-indexing-mcp.
// It parses CLI flags and routes to the appropriate handler:
// self-setup, MCP server, one-shot index, search, or query-by-grep.
package main

import (
	"flag"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cristian-guerrero/go-indexing-mcp/internal/cli"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/config"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/embedder"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/graph"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/indexer"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/llama"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/mcp"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/parser"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/selfsetup"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/storage"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/walker"
)

func main() {
	mcpMode := flag.Bool("mcp", false, "Start MCP server (stdio)")
	freeMem := flag.Bool("free", false, "Stop llama-server and free memory")
	generateMode := flag.Bool("generate", false, "One-shot index of current directory")
	queryMode := flag.String("query", "", "Search the index (default mode: hybrid)")
	grepMode := flag.String("grep", "", "Search using grep mode (fast, no llama needed)")
	limitFlag := flag.Int("limit", 25, "Max results (used with --query or --grep, default: 25, max: 50)")
	pathFilter := flag.String("path-filter", "", "Path filter: prefix ('pkg/'), exact file ('main.go'), or glob ('*.go', '**/*_test.go')")
	grepLang := flag.String("lang", "", "Filter by language (used with --grep, e.g. 'go', 'python', 'typescript')")
	grepCaseSensitive := flag.Bool("case-sensitive", false, "Case-sensitive matching (used with --grep)")
	grepWholeWord := flag.Bool("word", false, "Match whole words only (used with --grep)")
	listFiles := flag.Bool("list-files", false, "List all indexed files")
	findDef := flag.String("find-definition", "", "Find definition of a symbol (requires graph, build with -tags onnx)")
	findUsages := flag.String("find-usages", "", "Find usages of a symbol (requires graph, build with -tags onnx)")
	findImports := flag.String("find-imports", "", "Find imports matching a module pattern (requires graph, build with -tags onnx)")
	symbolInfo := flag.String("symbol-info", "", "Get detailed info about a symbol (requires graph, build with -tags onnx)")
	downloadLlama := flag.Bool("download-llama", false, "Force download llama.cpp (skip PATH, test GPU detection)")
	configureMode := flag.String("configure", "", "Configure integration: 'pi', 'opencode', or 'kilocode'")
	rootDir := flag.String("dir", "", "Project root directory (overrides config root_path)")
	flag.Parse()

	if *downloadLlama {
		setupConsoleLogger()
		cfg, err := config.Load()
		if err != nil {
			slog.Error("load config", "error", err)
			os.Exit(1)
		}
		mgr := llama.New(cfg)
		path, err := mgr.ForceDownloadLlama()
		if err != nil {
			slog.Error("download llama", "error", err)
			os.Exit(1)
		}
		slog.Info("llama.cpp downloaded", "path", path)
		return
	}

	if *configureMode != "" {
		setupConsoleLogger()
		os.Exit(cli.RunConfigure(*configureMode))
	}

	if *freeMem {
		setupConsoleLogger()
		cfg, err := config.Load()
		if err != nil {
			slog.Error("load config", "error", err)
			os.Exit(1)
		}
		mgr := llama.New(cfg)
		if err := mgr.KillByPort(); err != nil {
			slog.Error("free memory", "error", err)
			os.Exit(1)
		}
		slog.Info("llama-server stopped, memory freed")
		return
	}

	if *grepMode != "" {
		setupConsoleLogger()
		os.Exit(cli.RunQueryGrep(*grepMode, *limitFlag, *grepLang, *grepCaseSensitive, *grepWholeWord, *pathFilter, *rootDir))
	}

	if *queryMode != "" {
		setupConsoleLogger()
		os.Exit(cli.RunQuery(*queryMode, "hybrid", *limitFlag, *pathFilter, *rootDir))
	}

	if *findDef != "" {
		setupConsoleLogger()
		os.Exit(cli.RunFindDefinition(*findDef, *pathFilter, *rootDir))
	}

	if *findUsages != "" {
		setupConsoleLogger()
		os.Exit(cli.RunFindUsages(*findUsages, *pathFilter, *rootDir))
	}

	if *findImports != "" {
		setupConsoleLogger()
		os.Exit(cli.RunFindImports(*findImports, *rootDir))
	}

	if *symbolInfo != "" {
		setupConsoleLogger()
		os.Exit(cli.RunSymbolInfo(*symbolInfo, *pathFilter, *rootDir))
	}

	if *generateMode {
		setupConsoleLogger()
		os.Exit(cli.RunGenerate(*rootDir))
	}

	if *listFiles {
		setupConsoleLogger()
		os.Exit(cli.RunListFiles(*rootDir))
	}

	if !*mcpMode {
		setupConsoleLogger()
		if err := selfsetup.Run(); err != nil {
			slog.Error("setup failed", "error", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		setupConsoleLogger()
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	if err := setupFileLogger(cfg); err != nil {
		setupConsoleLogger()
		slog.Error("setup file logger", "error", err)
		os.Exit(1)
	}

	mgr := llama.New(cfg)

	if _, err := mgr.FindOrDownloadLlama(); err != nil {
		slog.Error("llama setup", "error", err)
		os.Exit(1)
	}

	if _, err := mgr.FindOrDownloadModel(); err != nil {
		slog.Error("model setup", "error", err)
		os.Exit(1)
	}

	if err := mgr.Start(); err != nil {
		slog.Error("start llama", "error", err)
		os.Exit(1)
	}

	w := walker.New(cfg.Indexing.RootPath, cfg.Indexing.IgnorePatterns)
	ch := chunker.New(cfg.Indexing.ChunkSize, cfg.Indexing.ChunkOverlap)

	// Wire tree-sitter parser if available (requires build tag onnx)
	parserCfg := parser.ParserConfig{Enabled: "treesitter"}
	if p := parser.NewParser(parserCfg); p != nil {
		if _, ok := p.(*parser.StructuralParser); !ok {
			ch.Parser = p
			slog.Info("tree-sitter parser enabled for chunking")
		}
	}

	em := embedder.New(mgr.BaseURL(), cfg.Embedding.Dimensions, cfg.Embedding.BatchSize, cfg.Embedding.QueryPrefix)
	dbDir := config.StorageDir(cfg.Indexing.RootPath)

	st, err := storage.New(dbDir, cfg.Embedding.Dimensions)
	if err != nil {
		slog.Error("storage init", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	idx := indexer.New(w, ch, em, st, mgr, cfg.Indexing.MemoryFreeInterval, cfg.Indexing.MaxMemoryMB)

	// Initialize knowledge graph
	graphDir := filepath.Join(config.StorageDir(cfg.Indexing.RootPath), "graph")
	if gq, err := graph.NewGraphQuery(graphDir); err == nil {
		ext := graph.NewExtractor()
		idx.WithGraph(gq, ext)
		slog.Info("knowledge graph enabled", "dir", graphDir)
	} else {
		slog.Warn("knowledge graph not available", "error", err)
	}

	srv := mcp.New(idx, mgr, cfg.Indexing.IdleTimeoutSecs, cfg.Indexing.WatchEnabled, cfg.Indexing.WatchIntervalSecs)
	slog.Info("starting MCP server")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("shutting down, releasing llama-server")
		mgr.Stop()
		os.Exit(0)
	}()

	if err := srv.Serve(); err != nil {
		slog.Error("MCP server", "error", err)
		os.Exit(1)
	}
}

// setupConsoleLogger configures slog to write structured JSON to stderr.
// Used for CLI-mode operations where no log file exists.
func setupConsoleLogger() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

// setupFileLogger configures dual-output logging to both stderr and
// ~/.go-mcp/indexing/server.log. Used in MCP server mode.
func setupFileLogger(cfg *config.Config) error {
	logDir := config.McpDir()
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return err
	}

	logPath := filepath.Join(logDir, "server.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	multi := io.MultiWriter(os.Stderr, f)
	slog.SetDefault(slog.New(slog.NewTextHandler(multi, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	slog.Info("logging initialized", "file", logPath, "time", time.Now().Format(time.RFC3339))
	return nil
}
