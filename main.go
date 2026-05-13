package main

import (
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/cristian/go-indexing-mcp/internal/cli"
	"github.com/cristian/go-indexing-mcp/pkg/config"
	"github.com/cristian/go-indexing-mcp/pkg/chunker"
	"github.com/cristian/go-indexing-mcp/pkg/embedder"
	"github.com/cristian/go-indexing-mcp/pkg/indexer"
	"github.com/cristian/go-indexing-mcp/pkg/llama"
	"github.com/cristian/go-indexing-mcp/pkg/mcp"
	"github.com/cristian/go-indexing-mcp/pkg/selfsetup"
	"github.com/cristian/go-indexing-mcp/pkg/storage"
	"github.com/cristian/go-indexing-mcp/pkg/walker"
)

func main() {
	mcpMode := flag.Bool("mcp", false, "Start MCP server (stdio)")
	freeMem := flag.Bool("free", false, "Stop llama-server and free memory")
	generateMode := flag.Bool("generate", false, "One-shot index of current directory")
	queryMode := flag.String("query", "", "Search the index (default mode: hybrid)")
	grepMode := flag.String("grep", "", "Search using grep mode (fast, no llama needed)")
	modeFlag := flag.String("mode", "hybrid", "Search mode: 'grep' or 'hybrid' (default: hybrid, used with --query)")
	limitFlag := flag.Int("limit", 25, "Max results (used with --query or --grep, default: 25, max: 50)")
	downloadLlama := flag.Bool("download-llama", false, "Force download llama.cpp (skip PATH, test GPU detection)")
	configureMode := flag.String("configure", "", "Configure integration: 'pi', 'opencode', or 'kilocode'")
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
		os.Exit(cli.RunQuery(*grepMode, "grep", *limitFlag))
	}

	if *queryMode != "" {
		setupConsoleLogger()
		os.Exit(cli.RunQuery(*queryMode, *modeFlag, *limitFlag))
	}

	if *generateMode {
		setupConsoleLogger()
		os.Exit(cli.RunGenerate())
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
	em := embedder.New(mgr.BaseURL(), cfg.Embedding.Dimensions, cfg.Embedding.BatchSize)
	dbPath := cfg.Storage.Path
	if !filepath.IsAbs(dbPath) {
		abs, err := filepath.Abs(dbPath)
		if err == nil {
			dbPath = abs
		}
	}

	st, err := storage.New(dbPath, cfg.Embedding.Dimensions)
	if err != nil {
		slog.Error("storage init", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	idx := indexer.New(w, ch, em, st)

	srv := mcp.New(idx, mgr, cfg.Indexing.IdleTimeoutSecs)
	slog.Info("starting MCP server")

	if err := srv.Serve(); err != nil {
		slog.Error("MCP server", "error", err)
		os.Exit(1)
	}
}

func setupConsoleLogger() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

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
