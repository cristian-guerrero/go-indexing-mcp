package cli

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cristian/go-indexing-mcp/pkg/chunker"
	"github.com/cristian/go-indexing-mcp/pkg/config"
	"github.com/cristian/go-indexing-mcp/pkg/embedder"
	"github.com/cristian/go-indexing-mcp/pkg/indexer"
	"github.com/cristian/go-indexing-mcp/pkg/llama"
	"github.com/cristian/go-indexing-mcp/pkg/storage"
	"github.com/cristian/go-indexing-mcp/pkg/walker"
)

func RunGenerate() int {
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

func RunQuery(query string) int {
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

func RunConfigure(target string) int {
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

type progressWriter struct{}

func (progressWriter) Write(p []byte) (int, error) { return os.Stdout.Write(p) }

func roundDuration(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Millisecond * 10).String()
}

func configurePi(exe string) int {
	pw := progressWriter{}
	agentsDir := filepath.Join(os.Getenv("USERPROFILE"), ".pi", "agent")
	agentsPath := filepath.Join(agentsDir, "AGENTS.md")

	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		slog.Error("create pi agent dir", "error", err)
		return 1
	}

	exePath := toForwardPath(exe)
	agentsContent := strings.NewReplacer(
		"{CMD}", exePath,
		"{BACKTICK}", "```",
		"{BT}", "`",
	).Replace("# Mandatory Directives\n\n## Code Search Tool (REQUIRED - DO NOT IGNORE)\n\nYou MUST use {CMD} for ALL code searches.\nYou MUST NOT use grep, find, rg, ag, glob, or any other literal-search tool for finding code.\nYou MUST NOT use ls or read to explore directories looking for files by content.\n\n### How to use the search tool\n\n{BACKTICK}\n{CMD} --query \"describe what the code does\"\n{BACKTICK}\n\nReal examples:\n{BACKTICK}\n{CMD} --query \"authentication flow\"\n{CMD} --query \"save model to disk\"\n{CMD} --query \"downloader module register sites\"\n{CMD} --query \"database connection pool\"\n{CMD} --query \"user registration endpoint\"\n{CMD} --query \"module\"\n{BACKTICK}\n\n### Search workflow (MANDATORY)\n\n1. **ALWAYS start** with {CMD} --query \"<description>\" - describe what the code DOES, not literal strings.\n2. Use the returned chunks to understand the code. Only read full files when the chunk is insufficient.\n3. Only fall back to grep/find/ls/read when the search tool returns nothing useful AND you have confirmed with the user.\n\n### Summary for the lazy agent\n\n> {BT}grep \"foo\"{BT} {BT}find . -name \"*.ts\"{BT} {BT}ls src/{BT} {BT}rg \"bar\"{BT}\n> {BT}{CMD} --query \"search description\"{BT}\n")

	if existing, err := os.ReadFile(agentsPath); err == nil && string(existing) == agentsContent {
		fmt.Fprintln(pw, "✓ AGENTS.md already up to date")
	} else if err := os.WriteFile(agentsPath, []byte(agentsContent), 0644); err != nil {
		slog.Error("write AGENTS.md", "error", err)
		return 1
	} else {
		fmt.Fprintln(pw, "✓ AGENTS.md created")
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
