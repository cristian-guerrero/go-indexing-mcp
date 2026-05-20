<h2 align="center">
  go-indexing-mcp
  <br/>
  <sub>Semantic code search that agents <em>must</em> use</sub>
</h2>

<div align="center">

[What is it?](#what-is-it) •
[Quickstart](#quickstart) •
[Configure](#configure) •
[CLI](#cli) •
[MCP Server](#mcp-server) •
[Architecture](#architecture)

</div>

## What is it?

AI coding agents default to grep+read: search with a keyword, open every matching file, consume thousands of tokens reading irrelevant code, forget context, repeat. go-indexing-mcp fixes this by injecting semantic search **directly into the agent's instructions** — the agent **must** use `search_code` (BM25 + vector similarity) before falling back to grep.

- **Forces the habit**: writes a global `AGENTS.md` or `CLAUDE.md` with mandatory instructions — the agent reads them every session and cannot ignore them
- **Zero API keys**: everything runs locally on CPU or GPU with llama.cpp
- **Branch-aware**: each git branch keeps its own isolated index — switching branches is instant
- **Auto-indexes**: empty index → full index. New commits → incremental. Just works.
- **No lock-in**: works with Claude Code, OpenCode, KiloCode, Pi, and any MCP-compatible agent

## Quickstart

Download the latest binary from [releases](https://github.com/cristian-guerrero/go-indexing-mcp/releases), or build from source (see [Building from source](#building-from-source)). Then run:

```bash
./go-indexing-mcp.exe
```

The first run auto-downloads llama.cpp and the embedding model, copies the binary to PATH, and creates run scripts. Then configure your agent:

```bash
go-indexing-mcp --configure claude
go-indexing-mcp --configure opencode
go-indexing-mcp --configure kilocode
go-indexing-mcp --configure pi
```

## Configure

The `--configure` flag generates everything needed for a specific agent in one command:

```bash
go-indexing-mcp --configure claude
```

Each command:
1. Detects the binary path automatically
2. Adds `go-indexing-mcp` to the agent's MCP config (merges with existing servers)
3. Writes search instructions to the agent's global configuration (`AGENTS.md` or `CLAUDE.md`)
4. Is idempotent — running it again says "already up to date"

### What each agent receives

| Agent | Config file | Instructions file |
|---|---|---|
| **Claude Code** | `~/.config/claude/mcp.json` | `~/.claude/CLAUDE.md` |
| **OpenCode** | `~/.config/opencode/opencode.json` | `~/.config/opencode/AGENTS.md` |
| **KiloCode** | `~/.config/kilo/kilo.json` | `~/.config/kilo/AGENTS.md` |
| **Pi** | — | `~/.pi/agent/AGENTS.md` |

### Forcing Agents

The problem with AI coding agents is they default to grep+read. go-indexing-mcp solves this by **making semantic search the path of least resistance**. Every `--configure` target writes instructions that state:

> You **MUST** use `go-indexing-mcp_search_code` for ALL code searches.
> Always try it FIRST, before falling back to built-in grep/glob tools.

The agent reads this at session start as a system-level directive. It can still grep — but only after confirming with you that the search tool returned nothing useful.

## CLI

Full CLI for scripting and one-off operations:

```bash
# One-shot index with detailed report
go-indexing-mcp --generate

# Search from the terminal (default mode: hybrid)
go-indexing-mcp --query "authentication flow"
go-indexing-mcp --grep "func validate"
go-indexing-mcp --query "db pool config" --limit 10

# Graph queries (no llama needed)
go-indexing-mcp --symbol-info "Config"
go-indexing-mcp --find-imports "github.com/user/project"

# Free llama-server memory
go-indexing-mcp --free
```

### Flags

| Flag | Description |
|---|---|---|
| `--mcp` | Start MCP server (stdio) |
| `--generate` | One-shot index with report |
| `--query <text>` | Search the index (BM25 + vector similarity via RRF) |
| `--grep <text>` | Search using grep mode (literal/regex on chunks) |
| `--limit <n>` | Max results (1-50, default 25) |
| `--path-filter <glob>` | Path filter: prefix, exact file, or glob (e.g. `*.go`, `pkg/**`) |
| `--lang <lang>` | Filter by language (used with --grep) |
| `--case-sensitive` | Case-sensitive matching (used with --grep) |
| `--word` | Match whole words only (used with --grep) |
| `--list-files` | List all indexed files |
| `--symbol-info <name>` | Get detailed info about a symbol: definition, usages, callers, callees |
| `--find-imports <pattern>` | Find imports matching a module pattern |
| `--configure <target>` | Auto-setup for pi, opencode, kilocode, or claude |
| `--dir <path>` | Override project root directory |
| `--free` | Stop llama-server, free RAM |
| `--download-llama` | Force download llama.cpp (auto-detects GPU variant) |

## MCP Server

Run the server and any MCP-compatible agent connects instantly:

```bash
go-indexing-mcp --mcp
```

Or via the run script (created during auto-setup):

```bash
~/.go-mcp/indexing/run.bat
```

### Tools

| Tool | Description | Parameters |
|---|---|---|
| `search_code` | BM25 + vector similarity via RRF. Best for intent-based queries ("authentication flow"). Auto-indexes if needed | `query` (req), `path_filter`, `limit` |
| `grep_code` | Substring/regex match on cached chunks with line-level results. Definition lines (func, type, class) are boosted 2x. Supports glob path filters. Auto-indexes if empty | `query` (req), `path_filter`, `lang`, `case_sensitive`, `word_boundary`, `limit` |
| `symbol_info` | Complete view of a symbol: definition, usages, callers, and callees | `name` (req), `path_filter` |
| `find_imports` | Find all files importing a module/package. Supports partial matching | `pattern` (req) |

### Search modes

| Mode | How it works | Requires llama.cpp |
|---|---|---|
| `hybrid` (default) | BM25 + vector similarity fused with RRF (k=60). Best for intent and keywords | Yes |
| `grep` | Case-insensitive substring on cached chunks | No |
| `graph` | Knowledge graph queries (find_definition, find_usages, find_imports, symbol_info) | No |

### Intelligent indexing

On startup and every search, the index freshness is checked automatically:

1. **MCP startup** → detects git repo, checks index state, does full or incremental index immediately
2. **No index** → synchronous full index of the project
3. **New commits** → synchronous incremental index of changed files
4. **Uncommitted changes only** → incremental index in background, search returns instantly
5. **Branch switch** → saves current index, loads target branch's index from disk
6. **Periodic watch** → every 60s (configurable), checks for changes and re-indexes in background

### Config

The configuration file at `~/.go-mcp/indexing/config.json` supports these options:

| Field | Default | Description |
|---|---|---|
| `llama.port` | `56000` | llama-server port |
| `llama.ngl_layers` | `0` | GPU layers for llama.cpp (`-ngl`, 0 = CPU only) |
| `llama.ctx_size` | `4096` | Context size (`-c`) |
| `llama.batch_size` | `2048` | Batch size (`-b`) |
| `llama.ubatch_size` | `2048` | Micro-batch size (`--ubatch-size`) |
| `llama.pooling` | `"mean"` | Pooling mode (`--pooling`, e.g. `mean`) |
| `llama.extra_args` | `[]` | Additional llama-server CLI arguments |
| `watch_enabled` | `true` | Enable periodic background indexing |
| `watch_interval_secs` | `60` | Interval between background index checks |
| `idle_timeout_secs` | `300` | Seconds of inactivity before stopping llama.cpp (0 = disable) |

### Idle timeout

After the configured `idle_timeout_secs` of inactivity (default: 5 min), the server stops llama.cpp to free VRAM. The next search restarts it automatically. The periodic watcher keeps the server active and prevents idle timeout.

## Architecture

```
[FS] → [walker] → [ignore filter] → [chunker] ─→ [llama.cpp embeddings] → [storage]
                                       │               ↑
                                       ├─ small files ─┘
                                       └─ large files → [structural splitter]
```

- **Small files**: sliding window chunking
- **Large files**: structural splitter detects functions/classes/sections via regex + brace/indent counting per language
- **Storage**: gob-serialized vectors with BM25 inverted index, isolated per git branch. Stored under `~/.go-mcp/indexing/` with Pi-format encoded project paths.

### Building from source

```bash
go build -o bin/go-indexing-mcp.exe .
```

Requires Go 1.21+. CI builds multi-platform binaries on every tag and main branch push (see `.github/workflows/ci.yml`).

### Languages

Structural splitting for: `go`, `python`, `javascript`, `typescript`, `rust`, `java`, `c`, `cpp`, `csharp`, `ruby`, `php`, `swift`, `kotlin`, `scala`, `bash`, `json`, `yaml`, `toml`, `markdown`.

Indexing (sliding window fallback) for all the above plus: `sql`, `powershell`, `html`, `css`.

### Embeddings

Uses [jina-embeddings-v2-base-code](https://huggingface.co/jinaai/jina-embeddings-v2-base-code) (137M params, 768-dim) via llama.cpp. Runs locally with zero API keys — llama.cpp auto-detects GPU (CUDA, Metal, Vulkan) if available, otherwise falls back to CPU. No configuration needed.

### Branch-isolated indexes

Each git branch has its own index file (e.g. `vectors-{worktree}-{branch}.gob`). Switching branches and searching loads the correct index instantly — no waiting.

Index files are stored under `~/.go-mcp/indexing/vectors/{encoded-project-path}/`, where `{encoded-project-path}` follows the Pi agent folder format:
- Windows: `C:\project\apps\my-app` → `--C--project-apps-my-app--`
- Unix: `/home/user/project` → `---home-user-project--`
