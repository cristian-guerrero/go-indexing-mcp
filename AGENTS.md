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
- `generate.go` — `runGenerate()` for `--generate`
- `query.go` — `runQuery()` for `--query`
- `configure.go` — `runConfigure()`, `configurePi()`, `configureOpenCode()` for `--configure`
- `pkg/config/` — load/save `~/.go-mcp/indexing/config.json`
- `pkg/selfsetup/` — auto-setup on first run
- `pkg/llama/` — manager: download, llama-server subprocess, health check, `IsRunning()`, `StartedProcess()`
- `pkg/ignore/` — .gitignore filter + default patterns (nested levels)
- `pkg/walker/` — file walker with git diff, hash, branch and language detection
- `pkg/chunker/` — sliding window + structural splitter. `ChunkFile` single or `ChunkFiles` batch
- `pkg/structural/` — regex + brace/indent counting for structural block detection per language. No external dependencies
- `pkg/embedder/` — HTTP client to llama.cpp `/v1/embeddings`
- `pkg/storage/` — gob persistence + cosine similarity + branch-isolated indices + BM25 inverted index (`bm25.go`)
- `pkg/indexer/` — orchestrator: walk → chunk → embed → store
- `pkg/mcp/` — MCP server with tools: search_code, reindex, index_path, _debug_index_files

## search_code behavior

Each search keeps the index auto-updated:

1. Branch detected → `SwitchBranch()` if changed (branch-isolated index)
2. Empty index → synchronous `IndexAll()` (first time)
3. New commits since last saved SHA → synchronous `IndexChanged()`
4. Uncommitted changes only → `IndexChanged()` in background
5. `Search()` returns results

### Search modes

`mode` parameter of `search_code` tool (and `--mode` CLI flag):

| Mode | Description | Requires llama.cpp |
|---|---|---|
| `"semantic"` (default) | Embedding → cosine similarity. Intent-based search | Yes |
| `"grep"` | Case-insensitive substring match on cached chunks. Ranked by frequency | No |
| `"hybrid"` | BM25 + vector similarity fused with RRF (k=60) | Yes (for vector) |

### Implementation

- `pkg/storage/bm25.go`: `tokenize()`, `buildBM25Index()`, `bm25Index.score()`, `SearchGrep()`, `SearchHybrid()`, `searchLocked()`, RRF fusion
- `BM25`: in-memory inverted index (`map[string][]posting`), k1=1.2, b=0.75
- `grep`: `strings.Count` of lowercase substring on `rec.Content`
- `hybrid`: runs BM25 + vector search separately, fuses with Reciprocal Rank Fusion
- BM25 index invalidated (`s.bm25 = nil`) on UpsertChunks, DeleteChunksByPath, rebuildIndex

## Branch-isolated index

- Files: `vectors.gob` (main/default) or `vectors-{branch}.gob`
- `Storage.SwitchBranch(branch)` persists and loads automatically
- `CommitSHA` saved on each indexation for precise diff

## Chunking

- `pkg/chunker/` orchestrates file splitting into chunks
- `pkg/structural/` detects structural blocks per language using regex + brace counting
- `{}` languages: brace depth tracking to find block closure
- Indentation-based (Python, Ruby, YAML): detects when indentation returns to the initial level
- Section-based (TOML, Markdown): block ends at next header/section
- JSON: supports `{}` and `[]` as block delimiters
- If no structural blocks detected, falls back to classic sliding window
- `ChunkFiles()` processes in batch: small files → sliding window, large files → structural split

## Auto-setup flow

1. `main.go` detects flags
2. No flags → executes `selfsetup.Run()`
3. `selfsetup.Run()`:
   - Re-launches in terminal if needed
   - Reads/creates config
   - Verifies/downloads llama.cpp
   - Verifies/downloads model
   - Copies binary
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
- `--generate` — one-shot index of current directory with detailed report
- `--query "<text>"` — search from CLI, auto-indexes if needed
- `--mode <semantic|grep|hybrid>` — search mode (default: semantic, used with --query)
- `--configure <pi|opencode>` — configure integration with Pi agent or OpenCode
