package main

import (
	"flag"
	"log/slog"
	"os"

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
	flag.Parse()

	if !*mcpMode {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
		if err := selfsetup.Run(); err != nil {
			slog.Error("setup failed", "error", err)
			os.Exit(1)
		}
		return
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
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
	defer mgr.Stop()

	w := walker.New(cfg.Indexing.RootPath, cfg.Indexing.IgnorePatterns)
	ch := chunker.New(cfg.Indexing.ChunkSize, cfg.Indexing.ChunkOverlap)
	em := embedder.New(mgr.BaseURL(), cfg.Embedding.Dimensions, cfg.Embedding.BatchSize)
	st, err := storage.New(cfg.Storage.Path, cfg.Embedding.Dimensions)
	if err != nil {
		slog.Error("storage init", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	idx := indexer.New(w, ch, em, st)

	srv := mcp.New(idx)
	slog.Info("starting MCP server")

	if err := srv.Serve(); err != nil {
		slog.Error("MCP server", "error", err)
		os.Exit(1)
	}
}
