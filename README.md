<h2 align="center">
  go-indexing-mcp
  <br/>
  <sub>Semantic code search that agents <em>must</em> use</sub>
</h2>

<div align="center">

[Quickstart](#quickstart) •
[MCP Server](#mcp-server) •
[Forcing Agents](#forcing-agents) •
[Configure](#configure) •
[CLI](#cli) •
[Architecture](#architecture)

</div>

An MCP server that indexes your codebase locally with embeddings and forces any AI coding agent (Claude Code, OpenCode, KiloCode, Cursor, Codex, Pi) to use semantic search instead of grep+read. It injects itself via `AGENTS.md` with mandatory instructions — the agent has no choice.

- **Forces the habit**: the AGENTS.md tells the agent it **MUST** use the search tool first
- **Zero API keys**: everything runs locally on CPU or GPU with llama.cpp
- **Branch-aware**: each git branch keeps its own isolated index
- **Auto-indexes**: empty index → full index. New commits → incremental. Just works.

## Quickstart

```bash
go build -o bin/go-indexing-mcp.exe .
./bin/go-indexing-mcp.exe
```

The first run auto-downloads llama.cpp and the embedding model, copies the binary to PATH, and creates run scripts. Then configure your agent:

```bash
# Force OpenCode to use it
./bin/go-indexing-mcp.exe --configure opencode

# Force KiloCode to use it
./bin/go-indexing-mcp.exe --configure kilocode

# Force Pi agent to use it
./bin/go-indexing-mcp.exe --configure pi
```

Each `--configure` target does two things:
1. **Adds the MCP server** to the agent's config (`opencode.json`, `kilo.json`, etc.)
2. **Writes a global AGENTS.md** with mandatory instructions — the agent reads it on every session and cannot ignore it

No more grep+read. The agent **must** call `go-indexing-mcp_search_code` first.

## Forcing Agents

The problem with AI coding agents is they default to grep+read: search with a keyword, open every matching file, consume thousands of tokens reading irrelevant code. Then they forget context and have to do it again.

go-indexing-mcp solves this by **making semantic search the path of least resistance**. Every `--configure` target writes an `AGENTS.md` that states:

> You **MUST** use `go-indexing-mcp_search_code` for ALL code searches.
> Always try it FIRST, before falling back to built-in grep/glob tools.

The agent reads this at session start as a system-level directive. It can still grep — but only after confirming with you that the search tool returned nothing useful.

### What agents receive

| Agent | Config file | AGENTS.md location |
|---|---|---|
| **OpenCode** | `~/.config/opencode/opencode.json` | `~/.config/opencode/AGENTS.md` |
| **KiloCode** | `~/.config/kilo/kilo.json` | `~/.config/kilo/AGENTS.md` |
| **Pi** | — | `~/.pi/agent/AGENTS.md` |

## MCP Server

Run the server and any MCP-compatible agent connects instantly:

```bash
./bin/go-indexing-mcp.exe --mcp
```

Or via a run script (created during auto-setup):

```bash
~/.go-mcp/indexing/run.bat
```

### Tools

| Tool | Description | Parameters |
|---|---|---|---|
| `search_code` | BM25 + vector similarity via RRF. Best for intent-based queries ("authentication flow"). Auto-indexes if needed | `query` (req), `path_filter`, `limit` |
| `grep_code` | Case-insensitive substring match on cached chunks. Best for exact symbols ("func validate") or regex patterns. Auto-indexes if empty | `query` (req), `path_filter`, `limit` |

### Search modes

| Mode | How it works | Requires llama.cpp |
|---|---|---|
| `hybrid` (default) | BM25 + vector similarity fused with RRF (k=60). Best for intent and keywords | Yes |
| `grep` | Case-insensitive substring on cached chunks | No |

### Intelligent indexing

Each `search_code` call checks index freshness automatically:

1. **No index** → synchronous full index of the project
2. **New commits** → synchronous incremental index of changed files
3. **Uncommitted changes only** → incremental index in background, search returns instantly
4. **Branch switch** → saves current index, loads target branch's index from disk

### Idle timeout

After 5 minutes of inactivity, the server stops llama.cpp to free VRAM. The next search restarts it automatically.

## Configure

The `--configure` flag generates everything needed for a specific agent:

```bash
go-indexing-mcp --configure opencode
go-indexing-mcp --configure kilocode
go-indexing-mcp --configure pi
```

Each command:
1. Detects the binary path automatically
2. Adds `go-indexing-mcp` to the agent's MCP config (merges with existing servers)
3. Writes a global `AGENTS.md` with the mandatory search instructions
4. Is idempotent — running it again says "already up to date"

## CLI

Full CLI for scripting and one-off operations:

```bash
# One-shot index with detailed report
go-indexing-mcp --generate

# Search from the terminal (default mode: hybrid)
go-indexing-mcp --query "authentication flow"
go-indexing-mcp --grep "func validate"
go-indexing-mcp --query "db pool config" --limit 10

# Free llama-server memory
go-indexing-mcp --free
```

### Flags

| Flag | Description |
|---|---|---|
| `--mcp` | Start MCP server (stdio) |
| `--generate` | One-shot index with report |
| `--query <text>` | Search the index (default mode: hybrid) |
| `--grep <text>` | Search using grep mode |
| `--mode <mode>` | Search mode: hybrid or grep (default: hybrid, used with --query) |
| `--limit <n>` | Max results (1-50, default 25) |
| `--configure <target>` | Auto-setup for pi, opencode, or kilocode |
| `--free` | Stop llama-server, free RAM |
| `--download-llama` | Force download llama.cpp (auto-detects GPU variant) |

## Architecture

```
[FS] → [walker] → [ignore filter] → [chunker] ─→ [llama.cpp embeddings] → [storage]
                                       │               ↑
                                       ├─ small files ─┘
                                       └─ large files → [structural splitter]
```

- **Small files**: sliding window chunking
- **Large files**: structural splitter detects functions/classes/sections via regex + brace/indent counting per language
- **Storage**: gob-serialized vectors with BM25 inverted index, isolated per git branch

### Languages

Structural splitting for: `go`, `python`, `javascript`, `typescript`, `rust`, `java`, `c`, `cpp`, `csharp`, `ruby`, `php`, `swift`, `kotlin`, `scala`, `bash`, `json`, `yaml`, `toml`, `markdown`.

Indexing (sliding window fallback) for all the above plus: `sql`, `powershell`, `html`, `css`.

### Embeddings

Uses [jina-embeddings-v2-base-code](https://huggingface.co/jinaai/jina-embeddings-v2-base-code) (137M params, 768-dim) via llama.cpp. Runs locally with zero API keys — llama.cpp auto-detects GPU (CUDA, Metal, Vulkan) if available, otherwise falls back to CPU. No configuration needed.

### Branch-isolated indexes

Each git branch has its own `vectors-{branch}.gob` file. Switching branches and searching loads the correct index instantly — no waiting.
