# AGENTS.md — go-indexing-mcp

## Commands

- Build: `go build -tags "onnx sqlite_fts5" -ldflags="-X github.com/cristian-guerrero/go-indexing-mcp/pkg/version.Version=dev" -o bin/go-indexing-mcp.exe .`
- Test: `go test -count=1 -tags "sqlite_fts5" ./...` (avoids cache)
- Lint: `go vet -tags "sqlite_fts5" ./...`
- CI: `.github/workflows/ci.yml` — multi-platform test + build + release on tags and main
- Dependencies: `go mod tidy`

## Conventions

- Inline documentation for functions (1-2 lines) explaining what and why
- Use `fmt.Errorf("message: %w", err)` with error wrapping
- Logging with `log/slog` (structured, info/debug/error levels)
- Fatal errors: `slog.Error` + `os.Exit(1)`, never `log.Fatal`
- Paths: always use `filepath.Join`, never string concatenation
- Windows: validate paths with `filepath.Abs`
- Commits and comments in English

## Structure

- `main.go` — flag parsing + routing to handlers
- `internal/cli/handlers.go` — `RunGenerate()`, `RunQuery()`, `RunConfigure()` + `configurePi()`, `configureOpenCode()`, `configureKiloCode()`, `configureClaude()`
- `pkg/config/` — load/save `~/.go-mcp/indexing/config.json`. `McpDir()`, `ModelsDir()`, `LlamaCppDir()`, `McpBinDir()`, `EncodeProjectPath()`, `StorageDir()`, `StorageDbPath()`
- `pkg/version/` — `Version` var injected via ldflags at build time (default: "dev"). Dev builds skip update checks
- `pkg/updater/` — auto-update via GitHub Releases: `CheckForUpdate`, `DownloadUpdate`, `ApplyUpdate` (replace running binary), `WasJustUpdated`. Windows: rename .exe → .old, copy new, hide .old if in-use. Config: `indexing.auto_update` (default: true). Triggered on MCP startup in background goroutine
- `pkg/selfsetup/` — auto-setup on first run
- `pkg/llama/` — manager: download (auto-detects GPU → CUDA/Vulkan/CPU), llama-server subprocess, health check, `IsRunning()`, `StartedProcess()`. llama-server uses `--sleep-idle-seconds` (configurable via `idle_timeout_secs`) to unload model from GPU after inactivity instead of killing the process. Memory monitoring: `MemoryUsageMB()` with `meminfo_windows.go` (psapi.dll + `golang.org/x/sys/windows`) and `meminfo_unix.go` (`/proc/<pid>/status` + `ps` fallback)
- `pkg/ignore/` — .gitignore filter + default patterns (nested levels)
- `pkg/walker/` — file walker with git diff, hash, branch and language detection
- `pkg/chunker/` — sliding window + structural splitter. `ChunkFile` single or `ChunkFiles` batch
- `pkg/structural/` — regex + brace/indent counting for structural block detection per language. Includes decorator/annotation backward scan. No external dependencies
- `pkg/embedder/` — HTTP client to llama.cpp `/v1/embeddings`. Uses connection pooling (`MaxIdleConns`) and buffer pool for reduced allocations
- `pkg/db/` — Core SQLite store: single `.sqlite` file per branch with tables for chunks, FTS5 (BM25), vec0 (ANN vectors), symbols, and references. Auto-downloads sqlite-vec extension from GitHub Releases. WAL mode, 30s busy timeout, cross-process lock detection
- `pkg/storage/` — Thin wrapper around `pkg/db/Store`. Backward-compatible API (Search, SearchHybrid, SearchGrep, etc.). Save/SaveAndFree/SaveSnapshot are no-ops (SQLite writes immediately)
- `pkg/indexer/` — orchestrator: walk → chunk → embed → store. `Llama` manager and `MemoryFreeInterval` control periodic llama-server restart during large indexing. `IndexAll()` processes files one at a time with cross-file batching (up to 32 chunks) for bounded memory. Graph extraction (tree-sitter) is deferred to `PendingGraph` and processed later by `RunGraphExtraction()`. `IsLocked()` prevents duplicate work when multiple processes index the same project
- `pkg/graph/` — knowledge graph: Tree-sitter AST extractor (build tag onnx), SQLite-backed storage (same `.sqlite` file as vectors), GraphQuery API (FindDefinition, FindUsages, FindImports, GetCallers, GetCallees, GetSymbolInfo), MCP tools (find_imports, symbol_info). Supported languages: go, python, typescript, javascript, tsx, c, cpp, php, rust, zig. Incremental extraction via `graph_commit_sha` tracking
- `pkg/mcp/` — MCP server with tools: search_code, grep_code, find_imports, symbol_info. llama-server starts lazily (`ensureLlama()` on first tool call) and stays alive — model sleep via `--sleep-idle-seconds` replaces the old kill-on-idle logic

## Performance: indexing hot path

- **SkipAST**: `Chunker.SkipAST = true` during indexing — tree-sitter AST parsing is skipped in favor of regex structural + sliding window chunking. Tree-sitter is CPU-intensive and creates idle gaps where llama-server waits. Graph extraction uses tree-sitter independently via `RunGraphExtraction()`
- **Cross-file batching**: chunks from multiple files are accumulated (up to 32) before sending to llama-server `/v1/embeddings`, reducing HTTP overhead and keeping 4 embed slots busy
- **SQLite immediate writes**: UpsertChunks uses a single transaction per batch. No periodic Save/SaveSnapshot needed — every write is immediately durable. Save/SaveAndFree/SaveSnapshot are no-ops
- **Cross-process lock detection**: `IsLocked()` uses a quick write test with 100ms timeout. When another process is indexing, IndexAll/IndexChanged return immediately — no wasted embedding work

## search_code behavior

1. **On MCP startup** (git repo detected) → auto-indexes if empty or outdated
2. **Format version mismatch** → clears DB and triggers `IndexAll()` (reindex on breaking changes)
3. Branch detected → `SwitchBranch()` if changed (branch-isolated index)
4. Empty index → synchronous `IndexAll()` (first time)
5. New commits since last saved SHA → synchronous `IndexChanged()`
6. Uncommitted changes + untracked files → `IndexChanged()` detects moved/renamed files in new directories
7. Ignore pattern changes → auto-detected via hash comparison, triggers `IndexAll()`
8. Stale entries → pruned from both vector index and knowledge graph on every search
9. `Search()` returns results

`grep_code` does NOT auto-index. If no index exists, you must run `search_code` first to build it.

## Auto-extraction on CLI graph queries

`--symbol-info` and `--find-imports` auto-extract the knowledge graph from source files when the graph is empty (fresh clone or new branch). Also auto-extracts incrementally when `graph_commit_sha` differs from vector `commit_sha` (stale graph). Uses only the tree-sitter extractor — no llama-server needed. Works when built with `-tags onnx`.

### MCP tools

| Tool | Description | Requires llama.cpp |
|---|---|---|
| `search_code(query, path_filter?, limit?)` | BM25 (FTS5) + vector (sqlite-vec) similarity via RRF. Best for intent-based queries | Yes |
| `grep_code(query, path_filter?, lang?, case_sensitive?, word_boundary?, limit?)` | Substring/regex match on cached chunks with line-level results. Definition lines (func, type, class) are boosted 2x. Supports glob path filters | No |
| `symbol_info(name, path_filter?)` | Get detailed info about a symbol (definition + usages + callers + callees) | No (graph only) |
| `find_imports(pattern)` | Find all imports matching a package pattern | No (graph only) |

### Implementation

- `pkg/db/store.go`: SQLite wrapper with WAL mode, vec0 extension loading, schema management, branch isolation
- `pkg/db/search.go`: `Search()` via `vec0` KNN, `SearchHybrid()` via FTS5 BM25 + vec0 fused with RRF, `SearchGrep()` via FTS5 pre-filter + Go regex post-processing with definition-line boost
- `pkg/db/graph.go`: Symbol/reference CRUD via SQLite tables, `ResolveRefs()` cross-file reference resolution
- `pkg/db/types.go`: Shared types (`SearchResult`, `GrepResult`, `Symbol`, `Reference`, `SymbolInfo`)
- `pkg/db/download.go`: Auto-download of sqlite-vec extension from GitHub Releases (per-platform)
- FTS5 sync: triggers on `chunks` and `symbols` tables keep FTS5 indices in sync automatically
- `grep_code`: line-level matching with `GrepOptions` (case_sensitive, whole_word, language filter). Results include `Matches` with exact line numbers. Definition lines (`func`, `type`, `class`, `interface`, etc.) get a 2x score boost. Supports glob path patterns (`*.go`, `**/*_test.go`)

## Branch-isolated index

- Single SQLite file per branch: `index.sqlite` (main) or `index-{worktree}-{branch}.sqlite` (other branches)
- Stored under `~/.go-mcp/indexing/vectors/{encoded-project-path}/` (e.g. `--C--project-apps-go-indexing-mcp--/`)
- `Store.SwitchBranch(branch, worktree)` persist and load automatically
- `CommitSHA` saved on each indexation for precise diff
- `graph_commit_sha` saved separately for incremental graph extraction

## Chunking

- `pkg/chunker/` orchestrates file splitting into chunks
- `pkg/structural/` detects structural blocks per language using regex + brace counting
- `{}` languages (Go, JS/TS, Rust, Java, C, C++, C#, PHP, Swift, Kotlin, Scala, Zig, Bash): brace depth tracking to find block closure
- SQL: `;`-terminated DDL detection with `BEGIN`/`END` nesting for PL/SQL
- Markdown: heading-based sections PLUS fenced code block (```/~~~) detection
- Indentation-based (Python, Ruby, YAML): detects when indentation returns to the initial level
- Section-based (TOML, Markdown): block ends at next header/section
- JSON: supports `{}` and `[]` as block delimiters
- **Decorator detection**: structural blocks extend backward to include preceding decorators/annotations:
  - `@Decorator` (JS/TS, Python, Java, Kotlin)
  - `[Attribute]` (C#)
  - `#[Attribute]` (PHP, Rust)
  - Blank lines between decorators and structural start are skipped
  - Non-decorator lines break the backward scan (no false positives with unrelated indented code)
- If no structural blocks detected, falls back to classic sliding window
- `ChunkFiles()` processes in batch: small files → sliding window, large files → structural split

## Auto-setup flow

1. `main.go` detects flags
2. No flags → executes `selfsetup.Run()`
3. `selfsetup.Run()`:
   - Re-launches in terminal if needed
   - Reads/creates config
   - Verifies/downloads llama.cpp (auto-detects GPU → CUDA/Vulkan/CPU variant on Windows)
   - Verifies/downloads model
   - Copies binary to `~/.go-mcp/indexing/bin/`
   - Adds to PATH
   - Creates run.bat/run.sh
4. With `--mcp` → starts `mcp.Serve(llamaManager)`

## Database format versioning

Both the vector index and the graph index have independent format version numbers. When a code change makes the on-disk format incompatible, increment the relevant constant:

- **Vector index**: `StorageFormatVersion` in `pkg/db/types.go` — increment when changing embedding dimensions, chunking logic, or any other breaking change to the vector persistence format
- **Graph index**: `GraphFormatVersion` in `pkg/db/types.go` — increment when changing Symbol, Reference, Tree-sitter extraction logic, or any other breaking change to the graph persistence format

Version `0` (pre-versioning legacy format) is treated as compatible and does NOT trigger reindex. Versions `> 0` that differ from the current constant trigger automatic clearing and full reindex on next startup (MCP mode) or CLI operation (`--query`, `--generate`).

## Documentation maintenance

Before committing or generating a commit message, check:
- `README.md` — did the interface change (flags, MCP tools, parameters)? Update examples and tools table.
- `AGENTS.md` — did structure, flags, search_code behavior change, or were files/packages added? Reflect changes here.

Do not modify README.md unless the public interface changes (flags, tools, configuration).

## Code documentation

- Every exported and unexported function must have a 1-2 line doc comment explaining what it does and why (not how).
- Every exported type and struct field must be documented.
- Every algorithm (BM25 via FTS5, sqlite-vec ANN, RRF fusion, structural parsing, chunking) must have a top-level doc comment explaining its purpose and approach.
- When adding new packages, files, or types, document them in the same style: concise, inline, English.
- When adding new flags, MCP tools, or search modes, update both AGENTS.md and README.md.

## Flags

- `--mcp` — starts MCP server over stdio
- `--free` — stops llama-server and frees memory
- `--download-llama` — force download llama.cpp (auto-detects GPU variant, skips PATH)
- `--generate` — one-shot index of current directory with detailed report (both vectors + graph)
- `--query "<text>"` — search from CLI (BM25 + vector similarity via RRF), auto-indexes if needed (vectors only)
- `--grep "<text>"` — search using grep mode (literal/regex on chunks)
- `--limit <n>` — max results for --query or --grep (default: 25, max: 50)
- `--path-filter <glob>` — path filter: prefix, exact file, or glob (e.g. `*.go`, `pkg/**`)
- `--lang <lang>` — filter by language (used with --grep)
- `--case-sensitive` — case-sensitive matching (used with --grep)
- `--word` — match whole words only (used with --grep)
- `--list-files` — list all indexed files
- `--symbol-info <name>` — get detailed info about a symbol: definition, usages, callers, callees (graph, no llama needed). Auto-extracts graph incrementally if stale
- `--find-imports <pattern>` — find imports matching module pattern (graph, no llama needed). Auto-extracts graph incrementally if stale
- `--configure <pi|opencode|kilocode|claude|zed>` — configure integration with Pi, OpenCode, KiloCode, Claude Code, or Zed (writes MCP config + global AGENTS.md)
- `--update` — check and apply update immediately (interactive CLI mode)
