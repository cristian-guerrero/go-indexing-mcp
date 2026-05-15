# AGENTS.md — go-indexing-mcp

## Commands

- Build: `go build -o bin/go-indexing-mcp.exe .`
- Test: `go test -count=1 ./...` (avoids cache)
- Lint: `go vet ./...`
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
- `internal/cli/handlers.go` — `RunGenerate()`, `RunQuery()`, `RunConfigure()` + `configurePi()`, `configureOpenCode()`, `configureKiloCode()`
- `pkg/config/` — load/save `~/.go-mcp/indexing/config.json`. `McpDir()`, `ModelsDir()`, `LlamaCppDir()`, `McpBinDir()`, `EncodeProjectPath()`, `StoragePath()`
- `pkg/selfsetup/` — auto-setup on first run
- `pkg/llama/` — manager: download (auto-detects GPU → CUDA/Vulkan/CPU), llama-server subprocess, health check, `IsRunning()`, `StartedProcess()`
- `pkg/ignore/` — .gitignore filter + default patterns (nested levels)
- `pkg/walker/` — file walker with git diff, hash, branch and language detection
- `pkg/chunker/` — sliding window + structural splitter. `ChunkFile` single or `ChunkFiles` batch
- `pkg/structural/` — regex + brace/indent counting for structural block detection per language. Includes decorator/annotation backward scan. No external dependencies
- `pkg/embedder/` — HTTP client to llama.cpp `/v1/embeddings`. Uses connection pooling (`MaxIdleConns`) and buffer pool for reduced allocations
- `pkg/storage/` — gob persistence + normalized dot product + branch-isolated indices + BM25 inverted index (`bm25.go`). Vectors are L2-normalized at store time so cosine similarity reduces to dot product
- `pkg/storage/simd/` — AVX2+FMA-accelerated dot product (amd64), scalar fallback (all platforms). ~18x speedup for 768-dim vectors
- `pkg/indexer/` — orchestrator: walk → chunk → embed → store
- `pkg/mcp/` — MCP server with tools: search_code, grep_code

## search_code behavior

`search_code` (hybrid mode) keeps the index auto-updated:

1. **On MCP startup** (git repo detected) → auto-indexes if empty or outdated
2. Branch detected → `SwitchBranch()` if changed (branch-isolated index)
3. Empty index → synchronous `IndexAll()` (first time)
4. New commits since last saved SHA → synchronous `IndexChanged()`
5. Uncommitted changes only → `IndexChanged()` in background
6. `Search()` returns results

`grep_code` does NOT auto-index. If no index exists, you must run `search_code` first to build it.

### MCP tools

| Tool | Description | Requires llama.cpp |
|---|---|---|
| `search_code(query, path_filter?, limit?)` | BM25 + vector similarity via RRF. Best for intent-based queries | Yes |
| `grep_code(query, path_filter?, lang?, case_sensitive?, word_boundary?, limit?)` | Substring/regex match on cached chunks with line-level results. Definition lines (func, type, class) are boosted 2x. Supports glob path filters | No |

### Implementation

- `pkg/storage/bm25.go`: `tokenize()`, `buildBM25Index()`, `bm25Index.score()`, `SearchGrep()`, `SearchHybrid()`, `searchLocked()`, RRF fusion, `topK[T]` bounded min-heap for O(n log k) top-k selection
- `BM25`: in-memory inverted index (`map[string][]posting`), k1=1.2, b=0.75
- `grep_code`: line-level matching with `GrepOptions` (case_sensitive, whole_word, language filter). Results include `Matches` with exact line numbers. Definition lines (`func`, `type`, `class`, `interface`, etc.) get a 2x score boost. Supports glob path patterns (`*.go`, `**/*_test.go`)
- `search_code`: runs BM25 + vector search separately, fuses with Reciprocal Rank Fusion
- Vector similarity: stored vectors are L2-normalized; query vectors normalized at search time; dot product via `simd.Dot()` gives cosine similarity with half the math
- BM25 index invalidated (`s.bm25 = nil`) on UpsertChunks, DeleteChunksByPath, rebuildIndex

## Branch-isolated index

- Files: `vectors.gob` (main/default) or `vectors-{worktree}-{branch}.gob`
- Stored under `~/.go-mcp/indexing/vectors/{encoded-project-path}/` (e.g. `--C--project-apps-go-indexing-mcp--/`)
- `Storage.SwitchBranch(branch, worktree)` persists and loads automatically
- `CommitSHA` saved on each indexation for precise diff

## Chunking

- `pkg/chunker/` orchestrates file splitting into chunks
- `pkg/structural/` detects structural blocks per language using regex + brace counting
- `{}` languages: brace depth tracking to find block closure
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

## Documentation maintenance

Before committing or generating a commit message, check:
- `README.md` — did the interface change (flags, MCP tools, parameters)? Update examples and tools table.
- `AGENTS.md` — did structure, flags, search_code behavior change, or were files/packages added? Reflect changes here.

Do not modify README.md unless the public interface changes (flags, tools, configuration).

## Flags

- `--mcp` — starts MCP server over stdio
- `--free` — stops llama-server and frees memory
- `--download-llama` — force download llama.cpp (auto-detects GPU variant, skips PATH)
- `--generate` — one-shot index of current directory with detailed report
- `--query "<text>"` — search from CLI (BM25 + vector similarity via RRF), auto-indexes if needed
- `--grep "<text>"` — search using grep mode (literal/regex on chunks)
- `--limit <n>` — max results for --query or --grep (default: 25, max: 50)
- `--path-filter <glob>` — path filter: prefix, exact file, or glob (e.g. `*.go`, `pkg/**`)
- `--lang <lang>` — filter by language (used with --grep)
- `--case-sensitive` — case-sensitive matching (used with --grep)
- `--word` — match whole words only (used with --grep)
- `--list-files` — list all indexed files
- `--configure <pi|opencode|kilocode>` — configure integration with Pi, OpenCode, or KiloCode (writes MCP config + global AGENTS.md)
