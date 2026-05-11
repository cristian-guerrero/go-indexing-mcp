package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// progressWriter writes directly to stdout without slog formatting.
type progressWriter struct{}

func (progressWriter) Write(p []byte) (int, error) { return os.Stdout.Write(p) }

func main() {
	mcpMode := flag.Bool("mcp", false, "Start MCP server (stdio)")
	freeMem := flag.Bool("free", false, "Stop llama-server and free memory")
	generateMode := flag.Bool("generate", false, "One-shot index of current directory")
	queryMode := flag.String("query", "", "Search the index for a natural language query")
	configureMode := flag.String("configure", "", "Configure integration: 'pi' or 'opencode'")
	flag.Parse()

	if *configureMode != "" {
		setupConsoleLogger()
		os.Exit(runConfigure(*configureMode))
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

	if *queryMode != "" {
		setupConsoleLogger()
		os.Exit(runQuery(*queryMode))
	}

	if *generateMode {
		setupConsoleLogger()
		os.Exit(runGenerate())
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

func runGenerate() int {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		return 1
	}

	mgr := llama.New(cfg)
	pw := progressWriter{}

	tStart := time.Now()

	fmt.Fprintln(pw, "Preparing llama-server...")
	if _, err := mgr.FindOrDownloadLlama(); err != nil {
		slog.Error("llama setup", "error", err)
		return 1
	}
	if _, err := mgr.FindOrDownloadModel(); err != nil {
		slog.Error("model setup", "error", err)
		return 1
	}

	wasRunning := mgr.IsRunning()
	if err := mgr.Start(); err != nil {
		slog.Error("start llama", "error", err)
		return 1
	}
	startedByUs := mgr.StartedProcess()
	if !wasRunning {
		if startedByUs {
			fmt.Fprintln(pw, "✓ llama-server started")
		} else {
			fmt.Fprintln(pw, "✓ llama-server already running, reusing")
		}
	} else {
		fmt.Fprintln(pw, "✓ llama-server already running, reusing")
	}
	defer func() {
		if startedByUs {
			fmt.Fprintln(pw, "Stopping llama-server...")
			mgr.Stop()
		}
	}()

	rootPath := cfg.Indexing.RootPath
	if rootPath == "" {
		rootPath = "."
	}

	tWalk := time.Now()
	w := walker.New(rootPath, cfg.Indexing.IgnorePatterns)
	files, err := w.Walk()
	if err != nil {
		slog.Error("walk files", "error", err)
		return 1
	}
	tWalkDone := time.Now()

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
		return 1
	}
	defer st.Close()

	idx := indexer.New(w, ch, em, st)

	tChunk := time.Now()
	chunksMap, err := idx.Chunker.ChunkFiles(files)
	if err != nil {
		slog.Error("chunk files", "error", err)
		return 1
	}
	tChunkDone := time.Now()
	chunkerStats := idx.Chunker.Stats()

	var totalChunks int
	tEmbed := time.Now()
	for _, fi := range files {
		chunks, ok := chunksMap[fi.Path]
		if !ok || len(chunks) == 0 {
			continue
		}

		embeddings, err := em.EmbedChunks(chunks)
		if err != nil {
			slog.Error("embed", "file", fi.RelPath, "error", err)
			continue
		}

		if err := st.UpsertChunks(chunks, embeddings); err != nil {
			slog.Error("store", "file", fi.RelPath, "error", err)
			continue
		}

		totalChunks += len(chunks)
	}
	tEmbedDone := time.Now()

	headSHA := w.GetHeadSHA()
	if headSHA != "" {
		st.SetCommitSHA(headSHA)
	}

	elapsed := time.Since(tStart)
	absRoot, _ := filepath.Abs(rootPath)
	fmt.Fprintln(pw)
	fmt.Fprintln(pw, "=== Index Complete ===")
	fmt.Fprintf(pw, "  Root path:        %s\n", absRoot)
	fmt.Fprintf(pw, "  Files found:      %d\n", len(files))
	fmt.Fprintf(pw, "  Files indexed:    %d\n", len(chunksMap))
	fmt.Fprintf(pw, "  Chunks created:   %d\n", totalChunks)
	fmt.Fprintln(pw, "  ─ Chunking method:")
	fmt.Fprintf(pw, "    Structural:     %d files, %d chunks\n", chunkerStats.TreeSitterFiles, chunkerStats.TreeSitterChunks)
	fmt.Fprintf(pw, "    Sliding window: %d files, %d chunks\n", chunkerStats.SlidingWinFiles, chunkerStats.SlidingWinChunks)
	fmt.Fprintln(pw, "  ─ Timing:")
	fmt.Fprintf(pw, "    Walk files:     %s\n", roundDuration(tWalkDone.Sub(tWalk)))
	fmt.Fprintf(pw, "    Chunk:          %s\n", roundDuration(tChunkDone.Sub(tChunk)))
	fmt.Fprintf(pw, "    Embed + store:  %s\n", roundDuration(tEmbedDone.Sub(tEmbed)))
	fmt.Fprintf(pw, "    Total:          %s\n", roundDuration(elapsed))
	fmt.Fprintln(pw, "======================")

	return 0
}

func runQuery(query string) int {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		return 1
	}

	mgr := llama.New(cfg)
	pw := progressWriter{}

	if _, err := mgr.FindOrDownloadLlama(); err != nil {
		slog.Error("llama setup", "error", err)
		return 1
	}
	if _, err := mgr.FindOrDownloadModel(); err != nil {
		slog.Error("model setup", "error", err)
		return 1
	}

	if err := mgr.Start(); err != nil {
		slog.Error("start llama", "error", err)
		return 1
	}

	rootPath := cfg.Indexing.RootPath
	if rootPath == "" {
		rootPath = "."
	}

	w := walker.New(rootPath, cfg.Indexing.IgnorePatterns)
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
		return 1
	}
	defer st.Close()

	idx := indexer.New(w, ch, em, st)

	branch := w.GetBranch()
	if err := st.SwitchBranch(branch); err != nil {
		slog.Warn("branch switch failed", "error", err)
	}

	stats := idx.GetStats()
	if stats.TotalChunks == 0 {
		fmt.Fprintln(pw, "No index found, indexing before search...")
		if err := idx.IndexAll(); err != nil {
			slog.Error("index", "error", err)
			return 1
		}
	} else {
		lastSHA := st.GetCommitSHA()
		headSHA := w.GetHeadSHA()
		if headSHA != "" && lastSHA != "" && headSHA != lastSHA {
			fmt.Fprintln(pw, "New commits detected, updating index...")
			if err := idx.IndexChanged(); err != nil {
				slog.Warn("incremental index failed", "error", err)
			}
		}
	}

	results, err := idx.Search(query, "", 10)
	if err != nil {
		slog.Error("search", "error", err)
		return 1
	}

	fmt.Fprintln(pw)
	fmt.Fprintln(pw, "=== Search Results ===")
	fmt.Fprintf(pw, "  Query:   %s\n", query)
	fmt.Fprintf(pw, "  Results: %d\n", len(results))
	fmt.Fprintln(pw)

	for i, r := range results {
		preview := r.Content
		if len(preview) > 120 {
			preview = preview[:120] + "..."
		}
		rel := r.RelPath
		if rel == "" {
			rel = r.FilePath
		}
		fmt.Fprintf(pw, "  %d. %s:%d-%d  (%.2f)\n", i+1, rel, r.StartLine, r.EndLine, r.Score)
		fmt.Fprintf(pw, "     %s\n", preview)
		fmt.Fprintln(pw)
	}
	if len(results) == 0 {
		fmt.Fprintln(pw, "  No results found.")
	}
	fmt.Fprintln(pw, "======================")

	return 0
}

func runConfigure(target string) int {
	exe, err := os.Executable()
	if err != nil {
		slog.Error("get executable path", "error", err)
		return 1
	}

	switch target {
	case "pi":
		return configurePi(exe)
	case "opencode":
		return configureOpenCode(exe)
	default:
		fmt.Fprintf(os.Stderr, "Unknown target: %s. Use 'pi' or 'opencode'.\n", target)
		return 1
	}
}

func configurePi(exe string) int {
	pw := progressWriter{}
	agentsDir := filepath.Join(os.Getenv("USERPROFILE"), ".pi", "agent")
	agentsPath := filepath.Join(agentsDir, "AGENTS.md")
	mcpPath := filepath.Join(os.Getenv("USERPROFILE"), ".config", "mcp", "mcp.json")

	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		slog.Error("create pi agent dir", "error", err)
		return 1
	}

	agentsContent := fmt.Sprintf("## REQUIRED: Semantic search before reading code\n\nBefore using \"find\", \"grep\", \"rg\", \"ls\", or \"read\" to search for code, you MUST run:\n\n    %s --query \"<describe what you need>\"\n\nDo NOT cd to a different directory first. Only use grep/find/ls/read after semantic search returns no relevant results.\n", toForwardPath(exe))

	if err := os.WriteFile(agentsPath, []byte(agentsContent), 0644); err != nil {
		slog.Error("write AGENTS.md", "error", err)
		return 1
	}
	fmt.Fprintln(pw, "✓ AGENTS.md created")

	if err := addToMCPServer(mcpPath, "go-indexing-mcp", exe, []string{"--mcp"}); err != nil {
		fmt.Fprintf(pw, "⚠ MCP config: %s\n", err)
	} else {
		fmt.Fprintln(pw, "✓ MCP server added to config")
	}

	fmt.Fprintln(pw, "Done. Start a new Pi session for changes to take effect.")
	return 0
}

func configureOpenCode(exe string) int {
	pw := progressWriter{}
	configDir := filepath.Join(os.Getenv("USERPROFILE"), ".config", "opencode")
	configPath := filepath.Join(configDir, "opencode.json")

	if err := os.MkdirAll(configDir, 0755); err != nil {
		slog.Error("create opencode config dir", "error", err)
		return 1
	}

	mcpEntry := map[string]any{
		"command": []string{exe, "--mcp"},
		"type":    "local",
		"enabled": true,
	}

	if err := mergeMCPIntoJSON(configPath, "go-indexing-mcp", mcpEntry); err != nil {
		slog.Error("update opencode config", "error", err)
		return 1
	}
	fmt.Fprintln(pw, "✓ OpenCode MCP server configured")
	return 0
}

func addToMCPServer(mcpPath, serverName, command string, args []string) error {
	var cfg struct {
		MCP map[string]any `json:"mcpServers"`
	}

	if data, err := os.ReadFile(mcpPath); err == nil {
		json.Unmarshal(data, &cfg)
	}

	if cfg.MCP == nil {
		cfg.MCP = make(map[string]any)
	}

	entry := map[string]any{
		"command": command,
	}
	if len(args) > 0 {
		entry["args"] = args
	}

	cfg.MCP[serverName] = entry

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mcp config: %w", err)
	}

	return os.WriteFile(mcpPath, data, 0644)
}

func mergeMCPIntoJSON(configPath, serverName string, entry map[string]any) error {
	var cfg map[string]any

	if data, err := os.ReadFile(configPath); err == nil {
		json.Unmarshal(data, &cfg)
	}

	if cfg == nil {
		cfg = make(map[string]any)
	}

	mcp, _ := cfg["mcp"].(map[string]any)
	if mcp == nil {
		mcp = make(map[string]any)
	}
	mcp[serverName] = entry
	cfg["mcp"] = mcp

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(configPath, data, 0644)
}

func toForwardPath(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

func roundDuration(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Millisecond * 10).String()
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
