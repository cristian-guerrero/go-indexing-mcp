// Package cli implements CLI-mode handlers for --generate, --query, --grep,
// --list-files, --find-imports, --symbol-info, and --configure flags. Each function returns an OS exit code.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/config"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/embedder"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/graph"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/indexer"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/llama"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/mcp"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/parser"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/storage"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/walker"
)

// RunGenerate performs a one-shot full index of the current directory.
// Starts llama-server if needed, walks files, chunks, embeds, and stores.
// Prints a detailed timing and statistics report on completion.
func RunGenerate(rootDir string) int {
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

	rootPath := resolveRootDir(rootDir, cfg.Indexing.RootPath)
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

	parserCfg := parser.ParserConfig{Enabled: "treesitter"}
	if p := parser.NewParser(parserCfg); p != nil {
		if _, ok := p.(*parser.StructuralParser); !ok {
			ch.Parser = p
		}
	}

	// Skip tree-sitter AST for chunking during indexing (Option A).
	// Regex structural + sliding window is fast enough for chunk quality,
	// while tree-sitter chunking creates CPU-bound gaps where llama-server sits idle.
	ch.SkipAST = true

	em := embedder.New(mgr.BaseURL(), cfg.Embedding.Dimensions, cfg.Embedding.BatchSize, cfg.Embedding.QueryPrefix)
	dbDir := config.StoragePath(rootPath)

	st, err := storage.New(dbDir, cfg.Embedding.Dimensions)
	if err != nil {
		slog.Error("storage init", "error", err)
		return 1
	}
	defer st.Close()

	branch := w.GetBranch()
	worktree := w.GetWorktreeName()
	if err := st.SwitchBranch(branch, worktree); err != nil {
		slog.Warn("branch switch failed, continuing", "error", err)
	}

	// Check for format version mismatch — clear if needed before full reindex
	if st.NeedsReindex() {
		slog.Warn("storage format version changed, clearing before reindex")
		if err := st.ClearAll(); err != nil {
			slog.Error("clear storage", "error", err)
			return 1
		}
	}

	idx := indexer.New(w, ch, em, st, nil, 0, 0, cfg.Indexing.IgnorePatterns)

	// Wire knowledge graph
	var graphQuery *graph.GraphQuery
	graphDir := filepath.Join(config.StorageDir(rootPath), "graph")
	if gq, gErr := graph.NewGraphQuery(graphDir); gErr == nil {
		ext := graph.NewExtractor()
		idx.WithGraph(gq, ext)
		graphQuery = gq
		if err := gq.SwitchBranch(branch, worktree); err != nil {
			slog.Warn("graph: branch switch failed, using default", "error", err)
		}
		if graphQuery.NeedsReindex() {
			slog.Warn("graph format version changed, clearing before reindex")
			if err := graphQuery.DB.Clear(); err != nil {
				slog.Error("clear graph", "error", err)
				return 1
			}
		}
	}

	defer func() {
		if graphQuery != nil {
			graphQuery.Close()
		}
	}()

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
		// Extract symbols for the knowledge graph (file-by-file, before chunking)
		if idx.Extractor != nil && idx.Graph != nil {
			content, rErr := os.ReadFile(fi.Path)
			if rErr != nil {
				slog.Warn("graph: read file", "file", fi.RelPath, "error", rErr)
			} else {
				symbols, refs, xErr := idx.Extractor.Extract(
					string(content), fi.Language, fi.Path, fi.RelPath, fi.Hash,
				)
				if xErr != nil {
					slog.Warn("graph: extract", "file", fi.RelPath, "lang", fi.Language, "error", xErr)
				} else if len(symbols) == 0 {
					slog.Info("graph: no symbols", "file", fi.RelPath, "lang", fi.Language)
				} else {
					if err := idx.Graph.StoreFile(fi.RelPath, symbols, refs); err != nil {
						slog.Warn("graph: store", "file", fi.RelPath, "error", err)
					}
				}
			}
		}

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
	fmt.Fprintf(pw, " Root path:  %s\n", absRoot)
	fmt.Fprintf(pw, " Files found:  %d\n", len(files))
	fmt.Fprintf(pw, " Files indexed: %d\n", len(chunksMap))
	fmt.Fprintf(pw, " Chunks created: %d\n", totalChunks)
	fmt.Fprintln(pw, " ─ Chunking method:")
	fmt.Fprintf(pw, " Structural:  %d files, %d chunks\n", chunkerStats.TreeSitterFiles, chunkerStats.TreeSitterChunks)
	fmt.Fprintf(pw, " Sliding window: %d files, %d chunks\n", chunkerStats.SlidingWinFiles, chunkerStats.SlidingWinChunks)
	fmt.Fprintln(pw, " ─ Timing:")
	fmt.Fprintf(pw, " Walk files:  %s\n", roundDuration(tWalkDone.Sub(tWalk)))
	fmt.Fprintf(pw, " Chunk:   %s\n", roundDuration(tChunkDone.Sub(tChunk)))
	fmt.Fprintf(pw, " Embed + store: %s\n", roundDuration(tEmbedDone.Sub(tEmbed)))
	fmt.Fprintf(pw, " Total:   %s\n", roundDuration(elapsed))
	fmt.Fprintln(pw, "======================")

	return 0
}

// RunListFiles lists all indexed files from the storage, grouped by branch.
func RunListFiles(rootDir string) int {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		return 1
	}

	rootPath := resolveRootDir(rootDir, cfg.Indexing.RootPath)
	if rootPath == "" {
		rootPath = "."
	}

	w := walker.New(rootPath, cfg.Indexing.IgnorePatterns)
	dbDir := config.StoragePath(rootPath)

	st, err := storage.New(dbDir, cfg.Embedding.Dimensions)
	if err != nil {
		slog.Error("storage init", "error", err)
		return 1
	}
	defer st.Close()

	branch := w.GetBranch()
	worktree := w.GetWorktreeName()
	if err := st.SwitchBranch(branch, worktree); err != nil {
		slog.Warn("branch switch failed", "error", err)
	}

	files := st.ListFiles()
	if len(files) == 0 {
		fmt.Println("No indexed files.")
		return 0
	}

	stats, _, _ := st.Stats()
	fmt.Printf("Indexed files: %d, Chunks: %d\n", len(files), stats)
	fmt.Println()
	for _, f := range files {
		fmt.Println(" ", f)
	}
	return 0
}

// RunQuery performs a hybrid (BM25 + vector) or vector-only search on the index.
// Auto-indexes if the index is empty or outdated (new commits detected).
func RunQuery(query string, mode string, limit int, pathFilter string, rootDir string) int {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		return 1
	}

	pw := progressWriter{}
	tStart := time.Now()

	needsLlama := mode != "grep"

	var mgr *llama.Manager
	if needsLlama {
		mgr = llama.New(cfg)
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
		if !wasRunning && startedByUs {
			fmt.Fprintln(pw, "✓ llama-server started")
		} else {
			fmt.Fprintln(pw, "✓ llama-server already running, reusing")
		}
		defer func() {
			if startedByUs {
				fmt.Fprintln(pw, "Stopping llama-server...")
				mgr.Stop()
			}
		}()
	}

	rootPath := resolveRootDir(rootDir, cfg.Indexing.RootPath)
	if rootPath == "" {
		rootPath = "."
	}

	w := walker.New(rootPath, cfg.Indexing.IgnorePatterns)
	dbDir := config.StoragePath(rootPath)

	st, err := storage.New(dbDir, cfg.Embedding.Dimensions)
	if err != nil {
		slog.Error("storage init", "error", err)
		return 1
	}
	defer st.Close()

	branch := w.GetBranch()
	worktree := w.GetWorktreeName()
	if err := st.SwitchBranch(branch, worktree); err != nil {
		slog.Warn("branch switch failed", "error", err)
	}

	var em *embedder.Embedder
	if needsLlama {
		em = embedder.New(mgr.BaseURL(), cfg.Embedding.Dimensions, cfg.Embedding.BatchSize, cfg.Embedding.QueryPrefix)
	}

	ch := chunker.New(cfg.Indexing.ChunkSize, cfg.Indexing.ChunkOverlap)

	parserCfg := parser.ParserConfig{Enabled: "treesitter"}
	if p := parser.NewParser(parserCfg); p != nil {
		if _, ok := p.(*parser.StructuralParser); !ok {
			ch.Parser = p
		}
	}

	idx := indexer.New(w, ch, em, st, mgr, cfg.Indexing.MemoryFreeInterval, cfg.Indexing.MaxMemoryMB, cfg.Indexing.IgnorePatterns)

	var gq *graph.GraphQuery
	graphDir := filepath.Join(config.StorageDir(rootPath), "graph")
	if gq2, gErr := graph.NewGraphQuery(graphDir); gErr == nil {
		ext := graph.NewExtractor()
		idx.WithGraph(gq2, ext)
		gq = gq2
		if err := gq2.SwitchBranch(branch, worktree); err != nil {
			slog.Warn("graph: branch switch", "error", err)
		}
	}
	defer func() {
		if gq != nil {
			gq.Close()
		}
	}()

	// Check for format version mismatch — clear and reindex if needed
	if st.NeedsReindex() {
		slog.Warn("storage format version changed, clearing for reindex")
		if err := st.ClearAll(); err != nil {
			slog.Error("clear storage", "error", err)
			return 1
		}
	}
	if gq != nil && gq.NeedsReindex() {
		slog.Warn("graph format version changed, clearing for reindex")
		if err := gq.DB.Clear(); err != nil {
			slog.Error("clear graph", "error", err)
			return 1
		}
	}

	// Retry loop: if branch changes during indexing, re-detect the new branch and restart
	for attempts := 0; attempts < 3; attempts++ {
		branch = w.GetBranch()
		worktree = w.GetWorktreeName()
		if err := st.SwitchBranch(branch, worktree); err != nil {
			slog.Warn("branch switch failed", "error", err)
		}
		idx.SetExpectedBranch(branch, worktree)

		stats := idx.GetStats()

		// Try seeding if empty or incomplete (interrupted mid-index)
		if stats.TotalChunks == 0 || st.GetCommitSHA() == "" {
			if mcp.SeedBranchFrom(st, gq, w, branch, worktree) {
				stats = idx.GetStats()
				slog.Info("branch seeded from another branch")
			}
		}

		var indexErr error

		if stats.TotalChunks == 0 {
			if needsLlama {
				fmt.Fprintln(pw, "No index found, indexing before search...")
				indexErr = idx.IndexAll()
			} else {
				fmt.Fprintln(pw, "No index found. Run a hybrid search first to build the index.")
				fmt.Fprintln(pw, "======================")
				return 1
			}
		} else if needsLlama {
			lastSHA := st.GetCommitSHA()
			if lastSHA == "" {
				fmt.Fprintln(pw, "Partial index found, filling remaining gaps...")
				indexErr = idx.IndexAll()
			} else {
				headSHA := w.GetHeadSHA()
				if headSHA != "" && headSHA != lastSHA {
					fmt.Fprintln(pw, "New commits detected, updating index...")
					indexErr = idx.IndexChanged()
				} else {
					fmt.Fprintln(pw, "Checking for working tree changes...")
					indexErr = idx.IndexChanged()
				}
			}
		}

		if errors.Is(indexErr, indexer.ErrBranchChanged) {
			fmt.Fprintln(pw, "Branch changed during indexing, restarting on new branch...")
			continue
		}
		if indexErr != nil {
			slog.Error("index", "error", indexErr)
			return 1
		}

		// Check if ignore patterns changed — trigger full reindex if so
		if needsLlama && idx.CheckIgnoreHash() {
			fmt.Fprintln(pw, "Ignore patterns changed, reindexing to pick up newly unignored files...")
			if err := idx.IndexAll(); err != nil {
				if errors.Is(err, indexer.ErrBranchChanged) {
					fmt.Fprintln(pw, "Branch changed during indexing, restarting on new branch...")
					continue
				}
				slog.Error("reindex after ignore change", "error", err)
				return 1
			}
		}

		if limit <= 0 || limit > 50 {
			limit = 25
		}
		results, err := idx.Search(query, pathFilter, limit, mode)
		if err != nil {
			slog.Error("search", "error", err)
			return 1
		}

		fmt.Fprintln(pw)
		fmt.Fprintln(pw, "=== Search Results ===")
		fmt.Fprintf(pw, " Query: %s\n", query)
		fmt.Fprintf(pw, " Mode: %s\n", mode)
		fmt.Fprintf(pw, " Results: %d\n", len(results))
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
			fmt.Fprintf(pw, " %d. %s:%d-%d (%.2f)\n", i+1, rel, r.StartLine, r.EndLine, r.Score)
			fmt.Fprintf(pw, "  %s\n", preview)
			fmt.Fprintln(pw)
		}
		if len(results) == 0 {
			fmt.Fprintln(pw, " No results found.")
		}
		fmt.Fprintf(pw, " Total time: %s\n", roundDuration(time.Since(tStart)))
		fmt.Fprintln(pw, "======================")

		return 0
	}

	fmt.Fprintln(pw, "Search failed after retries (branch changed repeatedly).")
	return 1
}

// RunQueryGrep performs substring/regex matching on cached chunks.
// Requires an existing index (built by a prior --query). No llama.cpp needed.
func RunQueryGrep(query string, limit int, lang string, caseSensitive bool, wholeWord bool, pathFilter string, rootDir string) int {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		return 1
	}

	pw := progressWriter{}
	tStart := time.Now()

	rootPath := resolveRootDir(rootDir, cfg.Indexing.RootPath)
	if rootPath == "" {
		rootPath = "."
	}

	w := walker.New(rootPath, cfg.Indexing.IgnorePatterns)
	dbDir := config.StoragePath(rootPath)

	st, err := storage.New(dbDir, cfg.Embedding.Dimensions)
	if err != nil {
		slog.Error("storage init", "error", err)
		return 1
	}
	defer st.Close()

	branch := w.GetBranch()
	worktree := w.GetWorktreeName()
	if err := st.SwitchBranch(branch, worktree); err != nil {
		slog.Warn("branch switch failed", "error", err)
	}

	ch := chunker.New(cfg.Indexing.ChunkSize, cfg.Indexing.ChunkOverlap)

	parserCfg := parser.ParserConfig{Enabled: "treesitter"}
	if p := parser.NewParser(parserCfg); p != nil {
		if _, ok := p.(*parser.StructuralParser); !ok {
			ch.Parser = p
		}
	}

	idx := indexer.New(w, ch, nil, st, nil, 0, 0, cfg.Indexing.IgnorePatterns)

	var gq *graph.GraphQuery
	graphDir := filepath.Join(config.StorageDir(rootPath), "graph")
	if gq2, gErr := graph.NewGraphQuery(graphDir); gErr == nil {
		ext := graph.NewExtractor()
		idx.WithGraph(gq2, ext)
		gq = gq2
		if err := gq2.SwitchBranch(branch, worktree); err != nil {
			slog.Warn("graph: branch switch", "error", err)
		}
	}
	defer func() {
		if gq != nil {
			gq.Close()
		}
	}()

	// Check for format version mismatch — grep mode can't reindex, so warn and exit
	if st.NeedsReindex() {
		fmt.Fprintln(pw, "Storage format version changed. Run --query (hybrid) search to trigger reindex.")
		fmt.Fprintln(pw, "======================")
		return 1
	}

	stats := idx.GetStats()
	if stats.TotalChunks == 0 {
		fmt.Fprintln(pw, "No index found. Run a --query (hybrid) search first to build the index.")
		fmt.Fprintln(pw, "======================")
		return 1
	}

	if limit <= 0 || limit > 50 {
		limit = 25
	}

	results, err := idx.SearchGrep(storage.GrepOptions{
		Query:         query,
		Limit:         limit,
		CaseSensitive: caseSensitive,
		WholeWord:     wholeWord,
		Language:      lang,
	}, pathFilter)
	if err != nil {
		slog.Error("grep search", "error", err)
		return 1
	}

	fmt.Fprintln(pw)
	fmt.Fprintln(pw, "=== Grep Results ===")
	fmt.Fprintf(pw, " Query: %s\n", query)
	if lang != "" {
		fmt.Fprintf(pw, " Language: %s\n", lang)
	}
	if caseSensitive {
		fmt.Fprintf(pw, " Case: sensitive\n")
	}
	if wholeWord {
		fmt.Fprintf(pw, " Word boundary: yes\n")
	}
	fmt.Fprintf(pw, " Results: %d\n", len(results))
	fmt.Fprintln(pw)

	for i, r := range results {
		rel := r.RelPath
		if rel == "" {
			rel = r.FilePath
		}
		fmt.Fprintf(pw, " %d. %s:%d-%d (%.2f)\n", i+1, rel, r.StartLine, r.EndLine, r.Score)
		for _, m := range r.Matches {
			preview := m.Content
			if len(preview) > 120 {
				preview = preview[:120] + "..."
			}
			fmt.Fprintf(pw, "    L%d: %s\n", m.Line, preview)
		}
		fmt.Fprintln(pw)
	}
	if len(results) == 0 {
		fmt.Fprintln(pw, " No results found.")
	}
	fmt.Fprintf(pw, " Total time: %s\n", roundDuration(time.Since(tStart)))
	fmt.Fprintln(pw, "======================")

	return 0
}

// RunConfigure sets up the MCP server integration for Pi, OpenCode, or KiloCode.
// Writes the appropriate config file (opencode.json, kilo.json(c)) and a global AGENTS.md.
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
	case "kilocode":
		return configureKiloCode(exe)
	case "claude":
		return configureClaude(exe)
	default:
		fmt.Fprintf(os.Stderr, "Unknown target: %s. Use 'pi', 'opencode', 'kilocode', or 'claude'.\n", target)
		return 1
	}
}

// progressWriter is an io.Writer that forwards writes directly to stdout.
// Used for CLI status messages that should bypass slog.
type progressWriter struct{}

func (progressWriter) Write(p []byte) (int, error) { return os.Stdout.Write(p) }

// roundDuration formats a duration for human-readable output.
// Sub-second durations show milliseconds; longer durations show 10ms precision.
func roundDuration(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Millisecond * 10).String()
}

// userHomeDir returns the current user's home directory, exiting on failure.
func userHomeDir() string {
	d, err := os.UserHomeDir()
	if err != nil {
		slog.Error("get home directory", "error", err)
		os.Exit(1)
	}
	return d
}

// configurePi writes the MCP config and AGENTS.md into ~/.pi/agent/.
func configurePi(exe string) int {
	pw := progressWriter{}
	agentsDir := filepath.Join(userHomeDir(), ".pi", "agent")
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
	).Replace("# Mandatory Directives\n\n## Code Search Tool (REQUIRED - DO NOT IGNORE)\n\nThe index is always up-to-date and searches are instant. You MUST use {CMD} for ALL code searches.\nYou MUST NOT use grep, find, rg, ag, glob, or any other literal-search tool for finding code.\nYou MUST NOT use ls or read to explore directories looking for files by content.\n\n### Search modes\n\n`--query` (BM25 + vector similarity via RRF). `--grep` does case-insensitive substring matching on cached chunks for exact symbols (\"func validate\") or regex patterns (\"type.*Downloader\"). Results include exact line numbers and definition lines (func, type, class) are boosted 2x.\n`--symbol-info` gets a complete 360-degree view of a specific symbol by its exact name (definition + usages + callers + callees). Requires the exact exported name (function, type, struct, const) — does NOT search keywords. Use `--grep` or `--query` first to discover symbol names. `--find-imports` finds files importing a module.\n\n### How to use the search tool\n\n{BACKTICK}\n{CMD} --query \"authentication flow\"\n{CMD} --query \"database connection pool\"\n{CMD} --grep \"func validate\"\n{CMD} --grep \"type.*Downloader\" --lang go\n{CMD} --grep \"get\" --word\n{CMD} --grep \"Error\" --case-sensitive\n{CMD} --query \"module\" --limit 50\n{CMD} --symbol-info \"Config\"\n{CMD} --find-imports \"fmt\"\n{BACKTICK}\n\n`--limit` controls max results (default: 25, max: 50).\n`--lang` filters by language (go, python, typescript, etc.).\n`--word` matches whole words only (e.g. `get` won't match `getter`).\n`--case-sensitive` enables case-sensitive matching.\n`--path-filter` supports glob patterns: `*.go`, `**/*_test.go`, `pkg/*.go`.\n\n### When to use each mode\n\n- **query**: you want to FIND code by what it DOES. Works for both intent and keywords.\n- **grep**: you know the EXACT name of a function, variable, or string. Fastest option.\n- **symbol-info**: you have the EXACT name of a symbol (found via --grep or --query) and need its definition, usages, callers, callees.\n- **find-imports**: you need to find which files depend on a specific package.\n\n### Search workflow (MANDATORY)\n\n1. **ALWAYS start** with {CMD} --query \"<description>\" - describe what the code DOES, not literal strings. If you need an exact symbol/identifier, use `--grep` instead.\n2. Use the returned chunks to understand the code. Only read full files when the chunk is insufficient.\n3. Once you have the exact exported name of a symbol (from --grep or --query results), you can use `{CMD} --symbol-info \"ExactName\"` for definition + usages + callers + callees.\n4. Only fall back to grep/find/ls/read when the search tool returns nothing useful AND you have confirmed with the user.\n\n### Summary for the lazy agent\n\n> {BT}grep \"foo\"{BT} {BT}find . -name \"*.ts\"{BT} {BT}ls src/{BT} {BT}rg \"bar\"{BT}\n> {BT}{CMD} --query \"search description\"{BT}\n> {BT}{CMD} --grep \"exact symbol\"{BT}\n> {BT}{CMD} --grep \"exact symbol\" --lang go --word{BT}\n> {BT}{CMD} --symbol-info \"Config\"{BT}\n> {BT}{CMD} --find-imports \"fmt\"{BT}\n")

	merged, err := mergeAgentsSection(agentsPath, agentsContent)
	if err != nil {
		slog.Error("write AGENTS.md", "error", err)
		return 1
	}
	if merged {
		fmt.Fprintln(pw, "✓ AGENTS.md updated")
	} else {
		fmt.Fprintln(pw, "✓ AGENTS.md already up to date")
	}
	fmt.Fprintln(pw, "Done. Start a new Pi session for changes to take effect.")
	return 0
}

// configureOpenCode adds the MCP server entry to ~/.config/opencode/opencode.json(c)
// and writes a global AGENTS.md with search_code/grep_code instructions.
func configureOpenCode(exe string) int {
	pw := progressWriter{}
	configDir := filepath.Join(userHomeDir(), ".config", "opencode")
	configPath := resolveOpenCodeConfig(configDir)

	if err := os.MkdirAll(configDir, 0755); err != nil {
		slog.Error("create opencode config dir", "error", err)
		return 1
	}

	mcpEntry := map[string]any{
		"command": []string{exe, "--mcp"},
		"type":    "local",
		"enabled": true,
		"timeout": 60000,
	}

	if err := mergeMCPIntoJSON(configPath, "go-indexing-mcp", mcpEntry); err != nil {
		slog.Error("update opencode config", "error", err)
		return 1
	}
	fmt.Fprintln(pw, "✓ OpenCode MCP server configured")

	agentsPath := filepath.Join(configDir, "AGENTS.md")
	agentsContent := strings.NewReplacer(
		"{BT}", "`",
	).Replace("# Code Search Tool (REQUIRED)\n\nYou MUST use the {BT}go-indexing-mcp_search_code{BT}, {BT}go-indexing-mcp_grep_code{BT}, {BT}go-indexing-mcp_symbol_info{BT}, or {BT}go-indexing-mcp_find_imports{BT} MCP tools for ALL code searches.\nAlways try them FIRST, before falling back to built-in grep/glob tools.\n\n## Available tools\n\n- {BT}go-indexing-mcp_search_code(query, path_filter?, limit?){BT} — BM25 + vector similarity via RRF. Best for intent-based queries (\"authentication flow\", \"database connection pool\"). Auto-indexes if needed. Requires llama.cpp.\n- {BT}go-indexing-mcp_grep_code(query, path_filter?, lang?, case_sensitive?, word_boundary?, limit?){BT} — Substring/regex match on cached chunks with line-level results. Definition lines (func, type, class) boosted 2x. Supports glob path filters ({BT}*.go{BT}, {BT}**/*_test.go{BT}). Auto-indexes if empty.\n- {BT}go-indexing-mcp_symbol_info(name, path_filter?){BT} — Get a complete 360-degree view of a **specific symbol by its exact name**: definition, usages, callers, and callees. Requires the exact exported name (function, type, struct, const). Does NOT do keyword search — use {BT}search_code{BT} or {BT}grep_code{BT} first to discover symbol names. No llama needed.\n- {BT}go-indexing-mcp_find_imports(pattern){BT} — Find all files that import a given module or package. Supports partial matching. No llama needed.\n\n## How to use the tools\n\n- {BT}go-indexing-mcp_search_code(query=\"authentication flow\"){BT}\n- {BT}go-indexing-mcp_search_code(query=\"database connection pool\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"func validate\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"type.*Downloader\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"func\", lang=\"go\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"get\", word_boundary=true){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"Error\", case_sensitive=true){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"DB\", path_filter=\"*.go\"){BT}\n- {BT}go-indexing-mcp_search_code(query=\"module\", limit=50){BT}\n- {BT}go-indexing-mcp_symbol_info(name=\"ValidateUser\"){BT}\n- {BT}go-indexing-mcp_find_imports(pattern=\"fmt\"){BT}\n\nUse the {BT}limit{BT} parameter to control result count (default: 25, max: 50). Use {BT}path_filter{BT} to narrow by prefix, exact file, or glob ({BT}*.go{BT}, {BT}**/*_test.go{BT}).\n\n## When to use each tool\n\n- **search_code**: you want to FIND code by what it DOES. Works for both intent and keywords.\n- **grep_code**: you know the EXACT name of a function, variable, or string. Fastest option.\n- **symbol_info**: you have the EXACT name of a symbol (found via grep_code/search_code) and need its definition, usages, callers, callees.\n- **find_imports**: you need to find which files depend on a specific package.\n\n## Search workflow (MANDATORY)\n\n1. **ALWAYS start** with {BT}go-indexing-mcp_search_code(query=\"<description>\"){BT} — describe what the code DOES, not literal strings. If you need an exact symbol/identifier, use {BT}go-indexing-mcp_grep_code{BT} instead.\n2. Use the returned chunks to understand the code. Only read full files when the chunk is insufficient.\n3. Once you have the exact exported name of a symbol (from grep_code/search_code results), you can use {BT}go-indexing-mcp_symbol_info(name=\"ExactName\"){BT} for definition + usages + callers + callees.\n4. Only fall back to grep/find/ls/read when the search tools return nothing useful AND you have confirmed with the user.\n\n### Summary for the lazy agent\n\n> {BT}grep \"foo\"{BT} {BT}find . -name \"*.ts\"{BT} {BT}ls src/{BT} {BT}rg \"bar\"{BT}\n> {BT}go-indexing-mcp_search_code(query=\"search description\"){BT}\n> {BT}go-indexing-mcp_grep_code(query=\"exact symbol\"){BT}\n> {BT}go-indexing-mcp_symbol_info(name=\"Config\"){BT}\n> {BT}go-indexing-mcp_find_imports(pattern=\"os\"){BT}\n")

	merged, err := mergeAgentsSection(agentsPath, agentsContent)
	if err != nil {
		slog.Error("write opencode AGENTS.md", "error", err)
		return 1
	}
	if merged {
		fmt.Fprintln(pw, "✓ Global AGENTS.md updated with search instructions")
	} else {
		fmt.Fprintln(pw, "✓ AGENTS.md already up to date")
	}
	fmt.Fprintln(pw, "Done. Restart OpenCode for changes to take effect.")
	return 0
}

// resolveOpenCodeConfig returns the path to opencode.jsonc or opencode.json, preferring jsonc.
func resolveOpenCodeConfig(configDir string) string {
	jsoncPath := filepath.Join(configDir, "opencode.jsonc")
	jsonPath := filepath.Join(configDir, "opencode.json")
	if _, err := os.Stat(jsoncPath); err == nil {
		return jsoncPath
	}
	if _, err := os.Stat(jsonPath); err == nil {
		return jsonPath
	}
	// Default to jsonc for new installs (OpenCode default format)
	return jsoncPath
}

// resolveKiloConfig returns the path to kilo.jsonc or kilo.json, preferring jsonc.
func resolveKiloConfig(configDir string) string {
	jsoncPath := filepath.Join(configDir, "kilo.jsonc")
	jsonPath := filepath.Join(configDir, "kilo.json")
	if _, err := os.Stat(jsoncPath); err == nil {
		return jsoncPath
	}
	if _, err := os.Stat(jsonPath); err == nil {
		return jsonPath
	}
	return jsoncPath
}

// loadJSONConfig reads a JSON file into a generic map, with support for
// trailing commas and // or /* */ comments. Returns an empty map if the file doesn't exist.
func loadJSONConfig(configPath string) (map[string]any, error) {
	cfg := make(map[string]any)

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config %s: %w", configPath, err)
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		cleaned := trailingCommaRe.ReplaceAllString(stripJSONComments(string(data)), "$1")
		if err := json.Unmarshal([]byte(cleaned), &cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", configPath, err)
		}
	}

	return cfg, nil
}

// configureKiloCode adds the MCP server and auto-approve permissions to
// ~/.config/kilo/kilo.json(c) and writes a global AGENTS.md.
func configureKiloCode(exe string) int {
	pw := progressWriter{}
	configDir := filepath.Join(userHomeDir(), ".config", "kilo")
	configPath := resolveKiloConfig(configDir)

	if err := os.MkdirAll(configDir, 0755); err != nil {
		slog.Error("create kilo config dir", "error", err)
		return 1
	}

	cfg, err := loadJSONConfig(configPath)
	if err != nil {
		slog.Error("load kilo config", "error", err)
		return 1
	}

	mcp, _ := cfg["mcp"].(map[string]any)
	if mcp == nil {
		mcp = make(map[string]any)
	}
	mcp["go-indexing-mcp"] = map[string]any{
		"command": []string{exe, "--mcp"},
		"type":    "local",
		"enabled": true,
		"timeout": 60000,
	}
	cfg["mcp"] = mcp
	fmt.Fprintln(pw, "✓ KiloCode MCP server configured")

	perm, _ := cfg["permission"].(map[string]any)
	if perm == nil {
		perm = make(map[string]any)
	}
	permAdded := false
	for _, k := range []string{"go-indexing-mcp_search_code", "go-indexing-mcp_grep_code"} {
		if _, exists := perm[k]; !exists {
			perm[k] = map[string]any{"*": "allow"}
			permAdded = true
		}
	}
	cfg["permission"] = perm
	if permAdded {
		fmt.Fprintln(pw, "✓ KiloCode MCP tools auto-approved")
	} else {
		fmt.Fprintln(pw, "✓ KiloCode MCP tool permissions already set")
	}

	data, err := json.MarshalIndent(cfg, "", " ")
	if err != nil {
		slog.Error("marshal kilo config", "error", err)
		return 1
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		slog.Error("write kilo config", "error", err)
		return 1
	}
	fmt.Fprintln(pw, "✓ KiloCode config saved")

	agentsPath := filepath.Join(configDir, "AGENTS.md")
	agentsContent := strings.NewReplacer(
		"{BT}", "`",
	).Replace("# Code Search Tool (REQUIRED)\n\nYou MUST use the {BT}go-indexing-mcp_search_code{BT}, {BT}go-indexing-mcp_grep_code{BT}, {BT}go-indexing-mcp_symbol_info{BT}, or {BT}go-indexing-mcp_find_imports{BT} MCP tools for ALL code searches.\nAlways try them FIRST, before falling back to built-in grep/glob tools.\n\n## Available tools\n\n- {BT}go-indexing-mcp_search_code(query, path_filter?, limit?){BT} — BM25 + vector similarity via RRF. Best for intent-based queries (\"authentication flow\", \"database connection pool\"). Auto-indexes if needed. Requires llama.cpp.\n- {BT}go-indexing-mcp_grep_code(query, path_filter?, lang?, case_sensitive?, word_boundary?, limit?){BT} — Substring/regex match on cached chunks with line-level results. Definition lines (func, type, class) boosted 2x. Supports glob path filters ({BT}*.go{BT}, {BT}**/*_test.go{BT}). Auto-indexes if empty.\n- {BT}go-indexing-mcp_symbol_info(name, path_filter?){BT} — Get a complete 360-degree view of a **specific symbol by its exact name**: definition, usages, callers, and callees. Requires the exact exported name (function, type, struct, const). Does NOT do keyword search — use {BT}search_code{BT} or {BT}grep_code{BT} first to discover symbol names. No llama needed.\n- {BT}go-indexing-mcp_find_imports(pattern){BT} — Find all files that import a given module or package. Supports partial matching. No llama needed.\n\n## How to use the tools\n\n- {BT}go-indexing-mcp_search_code(query=\"authentication flow\"){BT}\n- {BT}go-indexing-mcp_search_code(query=\"database connection pool\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"func validate\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"type.*Downloader\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"func\", lang=\"go\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"get\", word_boundary=true){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"Error\", case_sensitive=true){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"DB\", path_filter=\"*.go\"){BT}\n- {BT}go-indexing-mcp_search_code(query=\"module\", limit=50){BT}\n- {BT}go-indexing-mcp_symbol_info(name=\"ValidateUser\"){BT}\n- {BT}go-indexing-mcp_find_imports(pattern=\"fmt\"){BT}\n\nUse the {BT}limit{BT} parameter to control result count (default: 25, max: 50). Use {BT}path_filter{BT} to narrow by prefix, exact file, or glob ({BT}*.go{BT}, {BT}**/*_test.go{BT}).\n\n## When to use each tool\n\n- **search_code**: you want to FIND code by what it DOES. Works for both intent and keywords.\n- **grep_code**: you know the EXACT name of a function, variable, or string. Fastest option.\n- **symbol_info**: you have the EXACT name of a symbol (found via grep_code/search_code) and need its definition, usages, callers, callees.\n- **find_imports**: you need to find which files depend on a specific package.\n\n## Search workflow (MANDATORY)\n\n1. **ALWAYS start** with {BT}go-indexing-mcp_search_code(query=\"<description>\"){BT} — describe what the code DOES, not literal strings. If you need an exact symbol/identifier, use {BT}go-indexing-mcp_grep_code{BT} instead.\n2. Use the returned chunks to understand the code. Only read full files when the chunk is insufficient.\n3. Once you have the exact exported name of a symbol (from grep_code/search_code results), you can use {BT}go-indexing-mcp_symbol_info(name=\"ExactName\"){BT} for definition + usages + callers + callees.\n4. Only fall back to grep/find/ls/read when the search tools return nothing useful AND you have confirmed with the user.\n\n### Summary for the lazy agent\n\n> {BT}grep \"foo\"{BT} {BT}find . -name \"*.ts\"{BT} {BT}ls src/{BT} {BT}rg \"bar\"{BT}\n> {BT}go-indexing-mcp_search_code(query=\"search description\"){BT}\n> {BT}go-indexing-mcp_grep_code(query=\"exact symbol\"){BT}\n> {BT}go-indexing-mcp_symbol_info(name=\"Config\"){BT}\n> {BT}go-indexing-mcp_find_imports(pattern=\"os\"){BT}\n")

	merged, err := mergeAgentsSection(agentsPath, agentsContent)
	if err != nil {
		slog.Error("write kilo AGENTS.md", "error", err)
		return 1
	}
	if merged {
		fmt.Fprintln(pw, "✓ Global AGENTS.md updated with search instructions")
	} else {
		fmt.Fprintln(pw, "✓ AGENTS.md already up to date")
	}
	fmt.Fprintln(pw, "Done. Restart KiloCode for changes to take effect.")
	return 0
}

// configureClaude adds the MCP server to ~\.config\claude\mcp.json
// and writes CLAUDE.md with search instructions at ~\.claude\CLAUDE.md
// (user-level global instructions for Claude Code).
func configureClaude(exe string) int {
	pw := progressWriter{}

	// MCP config: ~/.config/claude/mcp.json
	configDir := filepath.Join(userHomeDir(), ".config", "claude")
	configPath := filepath.Join(configDir, "mcp.json")

	if err := os.MkdirAll(configDir, 0755); err != nil {
		slog.Error("create claude config dir", "error", err)
		return 1
	}

	var cfg map[string]any
	if data, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			cleaned := trailingCommaRe.ReplaceAllString(stripJSONComments(string(data)), "$1")
			if err := json.Unmarshal([]byte(cleaned), &cfg); err != nil {
				slog.Error("parse claude mcp.json", "error", err)
				return 1
			}
		}
	}
	if cfg == nil {
		cfg = make(map[string]any)
	}

	mcpServers, _ := cfg["mcpServers"].(map[string]any)
	if mcpServers == nil {
		mcpServers = make(map[string]any)
	}
	mcpServers["go-indexing-mcp"] = map[string]any{
		"command": exe,
		"args":    []string{"--mcp"},
		"type":    "local",
		"timeout": 60000,
	}
	cfg["mcpServers"] = mcpServers

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		slog.Error("marshal claude mcp.json", "error", err)
		return 1
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		slog.Error("write claude mcp.json", "error", err)
		return 1
	}
	fmt.Fprintln(pw, "✓ Claude Code MCP server configured")

	// Global instructions: ~/.claude/CLAUDE.md
	claudeDir := filepath.Join(userHomeDir(), ".claude")
	claudePath := filepath.Join(claudeDir, "CLAUDE.md")

	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		slog.Error("create .claude dir", "error", err)
		return 1
	}

	claudeContent := strings.NewReplacer(
		"{BT}", "`",
	).Replace("# Code Search Tool (REQUIRED)\n\nYou MUST use the {BT}go-indexing-mcp_search_code{BT}, {BT}go-indexing-mcp_grep_code{BT}, {BT}go-indexing-mcp_symbol_info{BT}, or {BT}go-indexing-mcp_find_imports{BT} MCP tools for ALL code searches.\nAlways try them FIRST, before falling back to built-in grep/glob tools.\n\n## Available tools\n\n- {BT}go-indexing-mcp_search_code(query, path_filter?, limit?){BT} — BM25 + vector similarity via RRF. Best for intent-based queries (\"authentication flow\", \"database connection pool\"). Auto-indexes if needed. Requires llama.cpp.\n- {BT}go-indexing-mcp_grep_code(query, path_filter?, lang?, case_sensitive?, word_boundary?, limit?){BT} — Substring/regex match on cached chunks with line-level results. Definition lines (func, type, class) boosted 2x. Supports glob path filters ({BT}*.go{BT}, {BT}**/*_test.go{BT}). Auto-indexes if empty.\n- {BT}go-indexing-mcp_symbol_info(name, path_filter?){BT} — Get a complete 360-degree view of a **specific symbol by its exact name**: definition, usages, callers, and callees. Requires the exact exported name (function, type, struct, const). Does NOT do keyword search — use {BT}search_code{BT} or {BT}grep_code{BT} first to discover symbol names. No llama needed.\n- {BT}go-indexing-mcp_find_imports(pattern){BT} — Find all files that import a given module or package. Supports partial matching. No llama needed.\n\n## How to use the tools\n\n- {BT}go-indexing-mcp_search_code(query=\"authentication flow\"){BT}\n- {BT}go-indexing-mcp_search_code(query=\"database connection pool\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"func validate\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"type.*Downloader\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"func\", lang=\"go\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"get\", word_boundary=true){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"Error\", case_sensitive=true){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"DB\", path_filter=\"*.go\"){BT}\n- {BT}go-indexing-mcp_search_code(query=\"module\", limit=50){BT}\n- {BT}go-indexing-mcp_symbol_info(name=\"ValidateUser\"){BT}\n- {BT}go-indexing-mcp_find_imports(pattern=\"fmt\"){BT}\n\nUse the {BT}limit{BT} parameter to control result count (default: 25, max: 50). Use {BT}path_filter{BT} to narrow by prefix, exact file, or glob ({BT}*.go{BT}, {BT}**/*_test.go{BT}).\n\n## When to use each tool\n\n- **search_code**: you want to FIND code by what it DOES. Works for both intent and keywords.\n- **grep_code**: you know the EXACT name of a function, variable, or string. Fastest option.\n- **symbol_info**: you have the EXACT name of a symbol (found via grep_code/search_code) and need its definition, usages, callers, callees.\n- **find_imports**: you need to find which files depend on a specific package.\n\n## Search workflow (MANDATORY)\n\n1. **ALWAYS start** with {BT}go-indexing-mcp_search_code(query=\"<description>\"){BT} — describe what the code DOES, not literal strings. If you need an exact symbol/identifier, use {BT}go-indexing-mcp_grep_code{BT} instead.\n2. Use the returned chunks to understand the code. Only read full files when the chunk is insufficient.\n3. Once you have the exact exported name of a symbol (from grep_code/search_code results), you can use {BT}go-indexing-mcp_symbol_info(name=\"ExactName\"){BT} for definition + usages + callers + callees.\n4. Only fall back to grep/find/ls/read when the search tools return nothing useful AND you have confirmed with the user.\n\n### Summary for the lazy agent\n\n> {BT}grep \"foo\"{BT} {BT}find . -name \"*.ts\"{BT} {BT}ls src/{BT} {BT}rg \"bar\"{BT}\n> {BT}go-indexing-mcp_search_code(query=\"search description\"){BT}\n> {BT}go-indexing-mcp_grep_code(query=\"exact symbol\"){BT}\n> {BT}go-indexing-mcp_symbol_info(name=\"Config\"){BT}\n> {BT}go-indexing-mcp_find_imports(pattern=\"os\"){BT}\n")

	merged, err := mergeAgentsSection(claudePath, claudeContent)
	if err != nil {
		slog.Error("write .claude/CLAUDE.md", "error", err)
		return 1
	}
	if merged {
		fmt.Fprintln(pw, "✓ ~/.claude/CLAUDE.md updated with search instructions")
	} else {
		fmt.Fprintln(pw, "✓ ~/.claude/CLAUDE.md already up to date")
	}
	fmt.Fprintln(pw, "Done. Restart Claude Code for changes to take effect.")
	return 0
}

// trailingCommaRe matches trailing commas before closing brackets/braces in JSON.
var trailingCommaRe = regexp.MustCompile(`,(\s*[}\]])`)

// stripJSONComments removes // and /* */ style comments from a JSON string,
// respecting string literal boundaries to avoid false positives.
func stripJSONComments(s string) string {
	var result []byte
	i := 0
	inString := false

	for i < len(s) {
		c := s[i]

		if inString {
			result = append(result, c)
			if c == '\\' && i+1 < len(s) {
				i++
				result = append(result, s[i])
			} else if c == '"' {
				inString = false
			}
			i++
			continue
		}

		if c == '"' {
			inString = true
			result = append(result, c)
			i++
			continue
		}

		if c == '/' && i+1 < len(s) {
			if s[i+1] == '/' {
				for i < len(s) && s[i] != '\n' {
					i++
				}
				continue
			}
			if s[i+1] == '*' {
				i += 2
				for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
					i++
				}
				i += 2
				continue
			}
		}

		result = append(result, c)
		i++
	}

	return string(result)
}

// mergeMCPIntoJSON reads a JSON config file (creating it if missing), merges
// an MCP server entry under the "mcp" key, and writes it back.
func mergeMCPIntoJSON(configPath, serverName string, entry map[string]any) error {
	var cfg map[string]any

	if data, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			cleaned := trailingCommaRe.ReplaceAllString(stripJSONComments(string(data)), "$1")
			if err := json.Unmarshal([]byte(cleaned), &cfg); err != nil {
				return fmt.Errorf("parse config %s: %w", configPath, err)
			}
		}
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

	data, err := json.MarshalIndent(cfg, "", " ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(configPath, data, 0644)
}

// toForwardPath converts backslashes to forward slashes for cross-platform paths.
func toForwardPath(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

// sectionStart and sectionEnd delimit the go-indexing-mcp managed block in AGENTS.md files.
const (
	sectionStart = "<!-- go-indexing-mcp:start -->"
	sectionEnd   = "<!-- go-indexing-mcp:end -->"
)

// mergeAgentsSection replaces or appends a managed section in an AGENTS.md file.
// Returns (changed=true) if the file was modified.
func mergeAgentsSection(agentsPath string, section string) (changed bool, err error) {
	existing, err := os.ReadFile(agentsPath)
	if err != nil {
		if os.IsNotExist(err) {
			content := sectionStart + "\n" + section + "\n" + sectionEnd + "\n"
			return true, os.WriteFile(agentsPath, []byte(content), 0644)
		}
		return false, err
	}

	content := string(existing)
	startIdx := strings.Index(content, sectionStart)
	endIdx := strings.Index(content, sectionEnd)

	if startIdx >= 0 && endIdx > startIdx {
		endIdx += len(sectionEnd)
		oldBlock := content[startIdx:endIdx]
		newBlock := sectionStart + "\n" + section + "\n" + sectionEnd
		if oldBlock == newBlock {
			return false, nil
		}
		merged := content[:startIdx] + newBlock + content[endIdx:]
		if !strings.HasSuffix(merged, "\n") {
			merged += "\n"
		}
		return true, os.WriteFile(agentsPath, []byte(merged), 0644)
	}

	newContent := strings.TrimRight(content, "\r\n \t")
	if newContent != "" {
		newContent += "\n\n"
	}
	newContent += sectionStart + "\n" + section + "\n" + sectionEnd + "\n"
	return true, os.WriteFile(agentsPath, []byte(newContent), 0644)
}

// RunFindImports finds imports matching a module pattern using the knowledge graph.
func RunFindImports(pattern, rootDir string) int {
	return runGraphQuery("find-imports", func(g *graph.GraphQuery) {
		imports := g.FindImports(pattern)
		if len(imports) == 0 {
			fmt.Printf("No imports matching '%s' found.\n", pattern)
			return
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		for _, imp := range imports {
			_ = enc.Encode(struct {
				ModulePath string `json:"module_path"`
				FilePath   string `json:"file_path"`
				Line       int    `json:"line"`
				Signature  string `json:"signature,omitempty"`
			}{imp.Name, imp.FilePath, imp.StartLine, imp.Signature})
		}
	}, rootDir)
}

// RunSymbolInfo returns full symbol info (definitions, usages, callers, callees).
func RunSymbolInfo(name, pathFilter, rootDir string) int {
	return runGraphQuery("symbol-info", func(g *graph.GraphQuery) {
		info := g.GetSymbolInfo(name, pathFilter)
		data, _ := json.MarshalIndent(info, "", "  ")
		fmt.Println(string(data))
	}, rootDir)
}

// pruneStaleGraphEntries removes graph entries for files that no longer exist on disk.
func pruneStaleGraphEntries(w *walker.Walker, gq *graph.GraphQuery) {
	for relPath := range gq.Cache.ByFile {
		fullPath := filepath.Join(w.Root, relPath)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			gq.RemoveFile(relPath)
			slog.Info("pruned stale graph entry", "file", relPath)
		}
	}
}

// runGraphQuery opens the graph, runs fn, and returns an exit code.
func runGraphQuery(label string, fn func(*graph.GraphQuery), rootDir string) int {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		return 1
	}

	rootPath := resolveRootDir(rootDir, cfg.Indexing.RootPath)
	if rootPath == "" {
		rootPath = "."
	}

	w := walker.New(rootPath, cfg.Indexing.IgnorePatterns)

	graphDir := filepath.Join(config.StorageDir(rootPath), "graph")
	gq, err := graph.NewGraphQuery(graphDir)
	if err != nil {
		slog.Error("open graph", "error", err)
		return 1
	}
	defer gq.Close()

	branch := w.GetBranch()
	worktree := w.GetWorktreeName()
	if err := gq.SwitchBranch(branch, worktree); err != nil {
		slog.Warn("graph: branch switch in query", "error", err)
	}

	if gq.NeedsReindex() {
		slog.Warn("graph format version changed, clearing and re-extracting")
		if err := gq.DB.Clear(); err != nil {
			slog.Error("clear graph", "error", err)
			return 1
		}
		// Re-open the cleared graph so Cache is fresh
		gq.Cache.Clear()
	}

	// Prune stale graph entries for files that no longer exist on disk
	pruneStaleGraphEntries(w, gq)

	symCount, _ := gq.Cache.Stats()
	if symCount == 0 {
		ext := graph.NewExtractor()
		if ext != nil {
			slog.Info("knowledge graph is empty, auto-extracting from source files")
			files, wErr := w.Walk()
			if wErr != nil {
				slog.Warn("graph: walk files for auto-extract", "error", wErr)
			} else {
				extracted := 0
				for _, fi := range files {
					content, rErr := os.ReadFile(fi.Path)
					if rErr != nil {
						continue
					}
					symbols, refs, xErr := ext.Extract(
						string(content), fi.Language, fi.Path, fi.RelPath, fi.Hash,
					)
					if xErr != nil {
						slog.Debug("graph: extract skip", "file", fi.RelPath, "error", xErr)
						continue
					}
					if len(symbols) > 0 {
						if err := gq.StoreFile(fi.RelPath, symbols, refs); err != nil {
							slog.Warn("graph: store", "file", fi.RelPath, "error", err)
						}
						extracted++
					}
				}
				slog.Info("graph auto-extraction complete", "files_with_symbols", extracted)
			}
		}
	}

	symCount, refCount := gq.Cache.Stats()
	if symCount == 0 {
		fmt.Fprintln(os.Stderr, "Knowledge graph is empty. Run --generate or use --mcp to index files with graph extraction (requires build with -tags onnx).")
		return 1
	}

	slog.Debug("graph loaded", "symbols", symCount, "refs", refCount, "branch", branch)

	fn(gq)
	return 0
}

// resolveRootDir returns the CLI-provided root directory if set, otherwise the config value.
func resolveRootDir(cliDir, configDir string) string {
	if cliDir != "" {
		return cliDir
	}
	return configDir
}
