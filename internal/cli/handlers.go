package cli

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
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
	dbPath := config.StoragePath(rootPath)

	st, err := storage.New(dbPath, cfg.Embedding.Dimensions)
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

func RunListFiles() int {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		return 1
	}

	rootPath := cfg.Indexing.RootPath
	if rootPath == "" {
		rootPath = "."
	}

	w := walker.New(rootPath, cfg.Indexing.IgnorePatterns)
	dbPath := config.StoragePath(rootPath)

	st, err := storage.New(dbPath, cfg.Embedding.Dimensions)
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

func RunQuery(query string, mode string, limit int) int {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		return 1
	}

	pw := progressWriter{}

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
		if err := mgr.Start(); err != nil {
			slog.Error("start llama", "error", err)
			return 1
		}
	}

	rootPath := cfg.Indexing.RootPath
	if rootPath == "" {
		rootPath = "."
	}

	w := walker.New(rootPath, cfg.Indexing.IgnorePatterns)
	dbPath := config.StoragePath(rootPath)

	st, err := storage.New(dbPath, cfg.Embedding.Dimensions)
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
		em = embedder.New(mgr.BaseURL(), cfg.Embedding.Dimensions, cfg.Embedding.BatchSize)
	}

	ch := chunker.New(cfg.Indexing.ChunkSize, cfg.Indexing.ChunkOverlap)
	idx := indexer.New(w, ch, em, st)

	stats := idx.GetStats()
	if stats.TotalChunks == 0 {
		if needsLlama {
			fmt.Fprintln(pw, "No index found, indexing before search...")
			if err := idx.IndexAll(); err != nil {
				slog.Error("index", "error", err)
				return 1
			}
		} else {
			fmt.Fprintln(pw, "No index found. Run a hybrid search first to build the index, or use --mode hybrid.")
			fmt.Fprintln(pw, "======================")
			return 1
		}
	} else if needsLlama {
		lastSHA := st.GetCommitSHA()
		if lastSHA == "" {
			fmt.Fprintln(pw, "Index has no commit SHA, performing full reindex...")
			if err := idx.IndexAll(); err != nil {
				slog.Error("full reindex", "error", err)
				return 1
			}
		} else {
			headSHA := w.GetHeadSHA()
			if headSHA != "" && headSHA != lastSHA {
				fmt.Fprintln(pw, "New commits detected, updating index...")
				if err := idx.IndexChanged(); err != nil {
					slog.Warn("incremental index failed", "error", err)
				}
			}
		}
	}

	if limit <= 0 || limit > 50 {
		limit = 25
	}
	results, err := idx.Search(query, "", limit, mode)
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
	case "kilocode":
		return configureKiloCode(exe)
	default:
		fmt.Fprintf(os.Stderr, "Unknown target: %s. Use 'pi', 'opencode', or 'kilocode'.\n", target)
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
	).Replace("# Mandatory Directives\n\n## Code Search Tool (REQUIRED - DO NOT IGNORE)\n\nThe index is always up-to-date and searches are instant. You MUST use {CMD} for ALL code searches.\nYou MUST NOT use grep, find, rg, ag, glob, or any other literal-search tool for finding code.\nYou MUST NOT use ls or read to explore directories looking for files by content.\n\n### Search modes\n\n`--query` (BM25 + vector similarity via RRF). `--grep` does case-insensitive substring matching on cached chunks for exact symbols (\"func validate\") or regex patterns (\"type.*Downloader\").\n\n### How to use the search tool\n\n{BACKTICK}\n{CMD} --query \"authentication flow\"\n{CMD} --query \"database connection pool\"\n{CMD} --grep \"func validate\"\n{CMD} --query \"module\" --limit 50\n{BACKTICK}\n\n`--limit` controls max results (default: 25, max: 50).\n\n### When to use each mode\n\n- **query**: you want to FIND code by what it DOES. Works for both intent and keywords.\n- **grep**: you know the EXACT name of a function, variable, or string. Fastest option.\n\n### Search workflow (MANDATORY)\n\n1. **ALWAYS start** with {CMD} --query \"<description>\" - describe what the code DOES, not literal strings. If you need an exact symbol/identifier, use `--grep` instead.\n2. Use the returned chunks to understand the code. Only read full files when the chunk is insufficient.\n3. Only fall back to grep/find/ls/read when the search tool returns nothing useful AND you have confirmed with the user.\n\n### Summary for the lazy agent\n\n> {BT}grep \"foo\"{BT} {BT}find . -name \"*.ts\"{BT} {BT}ls src/{BT} {BT}rg \"bar\"{BT}\n> {BT}{CMD} --query \"search description\"{BT}\n> {BT}{CMD} --grep \"exact symbol\"{BT}\n")

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
		"type": "local",
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
	).Replace("# Code Search Tool (REQUIRED)\n\nYou MUST use the {BT}go-indexing-mcp_search_code{BT} or {BT}go-indexing-mcp_grep_code{BT} MCP tools for ALL code searches.\nAlways try them FIRST, before falling back to built-in grep/glob tools.\n\n## Available tools\n\n- {BT}go-indexing-mcp_search_code(query, path_filter?, limit?){BT} — BM25 + vector similarity via RRF. Best for intent-based queries (\"authentication flow\", \"database connection pool\"). Auto-indexes if needed. Requires llama.cpp.\n- {BT}go-indexing-mcp_grep_code(query, path_filter?, limit?){BT} — Case-insensitive substring or regex matching on cached chunks. Best for exact symbols (\"func validate\") or regex patterns (\"type.*Downloader\"). Auto-indexes if empty.\n\n## How to use the tools\n\n- {BT}go-indexing-mcp_search_code(query=\"authentication flow\"){BT}\n- {BT}go-indexing-mcp_search_code(query=\"database connection pool\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"func validate\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"type.*Downloader\"){BT}\n- {BT}go-indexing-mcp_search_code(query=\"module\", limit=50){BT}\n\nUse the {BT}limit{BT} parameter to control result count (default: 25, max: 50). Use {BT}path_filter{BT} to narrow to a specific directory.\n\n## When to use each tool\n\n- **search_code**: you want to FIND code by what it DOES. Works for both intent and keywords.\n- **grep_code**: you know the EXACT name of a function, variable, or string. Fastest option.\n\n## Search workflow (MANDATORY)\n\n1. **ALWAYS start** with {BT}go-indexing-mcp_search_code(query=\"<description>\"){BT} — describe what the code DOES, not literal strings. If you need an exact symbol/identifier, use {BT}go-indexing-mcp_grep_code{BT} instead.\n2. Use the returned chunks to understand the code. Only read full files when the chunk is insufficient.\n3. Only fall back to grep/find/ls/read when the search tools return nothing useful AND you have confirmed with the user.\n\n### Summary for the lazy agent\n\n> {BT}grep \"foo\"{BT} {BT}find . -name \"*.ts\"{BT} {BT}ls src/{BT} {BT}rg \"bar\"{BT}\n> {BT}go-indexing-mcp_search_code(query=\"search description\"){BT}\n> {BT}go-indexing-mcp_grep_code(query=\"exact symbol\"){BT}\n")

	if existing, err := os.ReadFile(agentsPath); err == nil && string(existing) == agentsContent {
		fmt.Fprintln(pw, "✓ AGENTS.md already up to date")
	} else if err := os.WriteFile(agentsPath, []byte(agentsContent), 0644); err != nil {
		slog.Error("write opencode AGENTS.md", "error", err)
		return 1
	} else {
		fmt.Fprintln(pw, "✓ Global AGENTS.md created with search instructions")
	}
	fmt.Fprintln(pw, "Done. Restart OpenCode for changes to take effect.")
	return 0
}

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

func configureKiloCode(exe string) int {
	pw := progressWriter{}
	configDir := filepath.Join(os.Getenv("USERPROFILE"), ".config", "kilo")
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
		"type": "local",
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
	).Replace("# Code Search Tool (REQUIRED)\n\nYou MUST use the {BT}go-indexing-mcp_search_code{BT} or {BT}go-indexing-mcp_grep_code{BT} MCP tools for ALL code searches.\nAlways try them FIRST, before falling back to built-in grep/glob tools.\n\n## Available tools\n\n- {BT}go-indexing-mcp_search_code(query, path_filter?, limit?){BT} — BM25 + vector similarity via RRF. Best for intent-based queries (\"authentication flow\", \"database connection pool\"). Auto-indexes if needed. Requires llama.cpp.\n- {BT}go-indexing-mcp_grep_code(query, path_filter?, limit?){BT} — Case-insensitive substring or regex matching on cached chunks. Best for exact symbols (\"func validate\") or regex patterns (\"type.*Downloader\"). Auto-indexes if empty.\n\n## How to use the tools\n\n- {BT}go-indexing-mcp_search_code(query=\"authentication flow\"){BT}\n- {BT}go-indexing-mcp_search_code(query=\"database connection pool\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"func validate\"){BT}\n- {BT}go-indexing-mcp_grep_code(query=\"type.*Downloader\"){BT}\n- {BT}go-indexing-mcp_search_code(query=\"module\", limit=50){BT}\n\nUse the {BT}limit{BT} parameter to control result count (default: 25, max: 50). Use {BT}path_filter{BT} to narrow to a specific directory.\n\n## When to use each tool\n\n- **search_code**: you want to FIND code by what it DOES. Works for both intent and keywords.\n- **grep_code**: you know the EXACT name of a function, variable, or string. Fastest option.\n\n## Search workflow (MANDATORY)\n\n1. **ALWAYS start** with {BT}go-indexing-mcp_search_code(query=\"<description>\"){BT} — describe what the code DOES, not literal strings. If you need an exact symbol/identifier, use {BT}go-indexing-mcp_grep_code{BT} instead.\n2. Use the returned chunks to understand the code. Only read full files when the chunk is insufficient.\n3. Only fall back to grep/find/ls/read when the search tools return nothing useful AND you have confirmed with the user.\n\n### Summary for the lazy agent\n\n> {BT}grep \"foo\"{BT} {BT}find . -name \"*.ts\"{BT} {BT}ls src/{BT} {BT}rg \"bar\"{BT}\n> {BT}go-indexing-mcp_search_code(query=\"search description\"){BT}\n> {BT}go-indexing-mcp_grep_code(query=\"exact symbol\"){BT}\n")

	if existing, err := os.ReadFile(agentsPath); err == nil && string(existing) == agentsContent {
		fmt.Fprintln(pw, "✓ AGENTS.md already up to date")
	} else if err := os.WriteFile(agentsPath, []byte(agentsContent), 0644); err != nil {
		slog.Error("write kilo AGENTS.md", "error", err)
		return 1
	} else {
		fmt.Fprintln(pw, "✓ Global AGENTS.md created with search instructions")
	}
	fmt.Fprintln(pw, "Done. Restart KiloCode for changes to take effect.")
	return 0
}

var trailingCommaRe = regexp.MustCompile(`,(\s*[}\]])`)

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

func toForwardPath(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}
