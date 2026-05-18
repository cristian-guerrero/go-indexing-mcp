# yzma Integration Plan for go-indexing-mcp

## Overview

Replace the `llama-server` subprocess (HTTP-based embeddings) with the [yzma](https://github.com/hybridgroup/yzma) library — a pure Go FFI binding to llama.cpp that runs in-process with zero CGo, zero subprocess, and full memory control over GPU/CPU memory.

`yzma` wraps **96%+ of llama.cpp's C API** (160+ functions) via `github.com/jupiterrider/ffi` (pure Go FFI, no CGo). It loads prebuilt shared libraries (`.dll`/`.so`) at runtime — same download model as the current llama-server approach, but without the subprocess.

---

## Why yzma over llama-server

| | **Current (llama-server)** | **yzma** |
|---|---|---|
| IPC | HTTP POST to `/v1/embeddings` | Direct FFI call, no network |
| Memory free | `ForceRestart()` — kill + restart server every 100 files (~2s) | `MemoryClear(mem)` — instant KV cache clear, no restart |
| Subprocess | Managed via `os/exec` + lock files | None |
| Crash recovery | Detect "connection refused", restart, retry | Not needed |
| Multi-process sharing | Lock file coordination | Each MCP loads its own model |
| Binary at runtime | Standalone executable on disk | Shared libraries (`.dll`/`.so`) loaded via FFI |
| Code removed | — | ~1100 lines (`pkg/llama/` + lock.go + subprocess logic) |

---

## Why yzma over llama-go

Two in-process alternatives were evaluated: `llama-go` (CGO-based static linking) and `yzma` (pure Go FFI with runtime shared libs).

| | **llama-go** | **yzma** |
|---|---|---|
| Build | CGO + CMake + C++17 required | **Pure Go** (`go build`) — no C compiler |
| Windows build | Painful (MSVC/MinGW + CGO) | Native, CI already passes |
| Distribution | 50–200MB static binary | Small binary + prebuilt libs (runtime download) |
| Binary size | Everything linked in | Libraries downloaded separately |
| GPU setup | CUDA Toolkit at build time | Auto-download prebuilt CUDA/Vulkan libs |
| Zig compatibility | Hard (CMake dependency chain) | Easy (swap prebuilt libs, no build changes) |
| llama.cpp coverage | ~40 functions | 160+ (96%+) — everything exposed |

**Decision:** yzma wins. The build simplicity (no CGo, no CMake, no C++17) + runtime download model + full memory control make it the right choice for `go-indexing-mcp`.

---

## Embeddings API

yzma provides low-level embedding functions that wrap `llama_get_embeddings_seq` and `llama_get_embeddings`:

```go
// Enable embeddings on context params
params.Embeddings = 1

// After decoding a batch of tokens:
embedding, err := llama.GetEmbeddings(ctx, nOutputs, nEmbeddings)
// Returns []float32 of dimension nEmbeddings (e.g., 768 for jina-v2-base-code)

// Per-sequence embeddings
emb, err := llama.GetEmbeddingsSeq(ctx, seqID, nVocab)

// Per-token embeddings
emb, err := llama.GetEmbeddingsIth(ctx, tokenIndex, nVocab)
```

yzma requires manual orchestration (tokenize → batch → decode → extract embeddings), which takes ~50 lines. The benefit is full control over batching, memory, and pooling strategies.

---

## Migration Plan

### Phase 1 — Add yzma dependency

```bash
go get github.com/hybridgroup/yzma@latest
```

Consider making it optional via build tag initially so existing flow still works during development.

### Phase 2 — Replace `pkg/embedder/embedder.go`

Remove HTTP client and JSON parsing. Replace with direct FFI calls:

```go
type Embedder struct {
    model   llama.Model
    context llama.Context
    vocab   llama.Vocab
    nEmbd   int
    mu      sync.Mutex
}

func (e *Embedder) EmbedBatch(texts []string) ([][]float32, error) {
    e.mu.Lock()
    defer e.mu.Unlock()

    // 1. Clear KV cache (instant — no process restart needed)
    mem, _ := llama.GetMemory(e.context)
    llama.MemoryClear(mem, nil)

    // 2. Tokenize all texts
    // 3. Fill batch with tokens across parallel sequences
    // 4. Decode
    // 5. Extract embeddings per sequence via GetEmbeddingsSeq()
}
```

### Phase 3 — Replace `pkg/llama/` (manager.go, lock.go)

Delete the entire subprocess management infrastructure. Replace with a lightweight model lifecycle wrapper:

```go
type Manager struct {
    libPath  string  // directory with llama.dll/llama.so
    model    llama.Model
    embedder *embedder.Embedder
}

func (m *Manager) Start() error {
    llama.Load(m.libPath)
    llama.Init()
    model, _ := llama.ModelLoadFromFile(modelPath, params)
    ctx, _ := llama.InitFromModel(model, ctxParams)
    m.embedder = embedder.New(model, ctx)
}
func (m *Manager) Stop() error {
    m.embedder.Close()
    llama.Close()
}
func (m *Manager) Restart() error {
    // Context only — no model reload needed
    m.embedder.RefreshContext()
}
```

- `Start()` → `llama.Load(libPath)` + `ModelLoadFromFile()` + `InitFromModel()`
- `Stop()` → `context.Free()` + `model.Free()` + `llama.Close()`
- `Restart()` → `context.Free()` + `InitFromModel()` — no model reload penalty
- Memory free → `MemoryClear(mem)` — instant, no process restart, no 2s wait

### Phase 4 — Simplify `pkg/indexer/indexer.go`

Remove `ForceRestart()` and crash recovery. Replace with instant memory clear:

```go
// Every MemoryFreeInterval files (was: ForceRestart with 2s penalty)
embedder.ClearMemory()  // calls llama.MemoryClear() — sub-millisecond
// No restart wait, no crash recovery needed, no port scanning
```

Remove:
- `restartLlama()` function
- "connection refused" detection and retry logic
- `ForceRestart()` calls
- `indexedSinceFree` counter (still needed for batching but no restart)

### Phase 5 — Update `pkg/selfsetup/setup.go`

Replace llama-server binary download with yzma shared library download:

```go
import "github.com/hybridgroup/yzma/pkg/download"

// Auto-detect GPU and download matching prebuilt libs
variant := detectGPU()  // "cuda", "vulkan", "cpu"
download.GetWithProgress("amd64", "windows", variant, version, destPath, progress)
```

Auto-detection functions already built into yzma:
- `download.HasCUDA()` — runs `nvidia-smi`
- `download.HasROCm()` — runs `rocminfo`
- Falls back to CPU build if no GPU detected

### Phase 6 — Update config

**Remove from `config.json`:**
- `bin_path` — no more llama-server binary
- `llama_variant.extra_args` — no CLI args to pass
- `pooling` field from profile (handled in context params directly)

**Add to `config.json`:**
- `llama_cpp_version` — pinned llama.cpp release (e.g., `b9180`)
- `lib_path` — directory for yzma shared libraries (`.dll`/`.so`)

---

## Files Changed

| File | Change |
|---|---|
| `go.mod` | Add `github.com/hybridgroup/yzma` |
| `pkg/embedder/embedder.go` | Rewrite: FFI calls instead of HTTP |
| `pkg/llama/manager.go` | Rewrite: model/context lifecycle instead of subprocess |
| `pkg/llama/lock.go` | **Delete** |
| `pkg/indexer/indexer.go` | Replace `ForceRestart()` with `ClearMemory()` |
| `pkg/selfsetup/setup.go` | Download shared libs instead of llama-server binary |
| `pkg/config/config.go` | Remove binary-specific fields, add lib path |
| `main.go` | Simplify manager init, remove lock file references |
| `internal/cli/handlers.go` | Update `--free` to call `Stop()` |
| `pkg/mcp/server.go` | Update idle timeout handler (no subprocess to kill) |

**Deleted entirely:**
- `pkg/llama/lock.go` (~150 lines)
- Subprocess management code in `manager.go` (~500 lines of `exec.Command`, port scanning, PID tracking, job objects)
- Crash recovery in `indexer/indexer.go` (~30 lines of "connection refused" retry)
- `detectGPU`/`findProcessByPort`/`isProcessAlive` in `manager.go`

**New:**
- ~50 lines embedding orchestration in `embedder.go`
- ~80 lines lifecycle wrapper in `manager.go`

---

## Embedding Orchestration Code (yzma)

Full reference implementation of the embedding loop using yzma:

```go
func (e *Embedder) EmbedBatch(texts []string) ([][]float32, error) {
    // 1. Clear KV cache (instant, no restart needed)
    mem, err := llama.GetMemory(e.ctx)
    if err != nil {
        return nil, fmt.Errorf("get memory: %w", err)
    }
    llama.MemoryClear(mem, nil)

    // 2. Tokenize all texts
    tokenized := make([][]llama.Token, len(texts))
    maxTokens := 0
    for i, text := range texts {
        tokens := llama.Tokenize(e.vocab, text, true, false)
        tokenized[i] = tokens
        if len(tokens) > maxTokens {
            maxTokens = len(tokens)
        }
    }

    // 3. Create batch with enough capacity
    nSeqs := int32(len(texts))
    batch := llama.BatchInit(
        int32(len(texts)*maxTokens), // nTokens
        0,                           // embd (0 for text tokens)
        nSeqs,                       // nSeqMax (parallel sequences)
    )
    defer llama.BatchFree(batch)

    // 4. Fill batch: each text gets its own seqID
    for seqID, tokens := range tokenized {
        for pos, tok := range tokens {
            batch.Add(tok, int32(pos), []int32{int32(seqID)}, true)
        }
    }

    // 5. Decode
    status, err := llama.Decode(e.ctx, batch)
    if err != nil || status != 0 {
        return nil, fmt.Errorf("decode failed: status=%d err=%w", status, err)
    }

    // 6. Extract embeddings per sequence
    results := make([][]float32, len(texts))
    for i := range texts {
        emb, err := llama.GetEmbeddingsSeq(e.ctx, int32(i), int32(e.nEmbd))
        if err != nil {
            return nil, fmt.Errorf("get embeddings seq %d: %w", i, err)
        }
        results[i] = emb
    }

    return results, nil
}
```

> **Note:** This uses `GetEmbeddingsSeq()` which returns token-level embeddings. For mean pooling (required for jina-v2-base-code models), the sequence embeddings need to be averaged across all tokens. Alternatively, set `PoolingType: Mean` in the context params and use `GetEmbeddings()` for the pre-pooled vector.

### Simplified version with built-in pooling

```go
func (e *Embedder) EmbedBatchPooled(texts []string) ([][]float32, error) {
    mem, _ := llama.GetMemory(e.ctx)
    llama.MemoryClear(mem, nil)

    tokenized := make([][]llama.Token, len(texts))
    maxLen := 0
    for i, t := range texts {
        tokenized[i] = llama.Tokenize(e.vocab, t, true, false)
        if len(tokenized[i]) > maxLen {
            maxLen = len(tokenized[i])
        }
    }

    nSeqs := int32(len(texts))
    batch := llama.BatchInit(int32(len(texts)*maxLen), 0, nSeqs)
    defer llama.BatchFree(batch)

    for seqID, tokens := range tokenized {
        for pos, tok := range tokens {
            batch.Add(tok, int32(pos), []int32{int32(seqID)}, true)
        }
    }

    llama.Decode(e.ctx, batch)

    // With PoolingType.Mean set in context params, GetEmbeddings
    // returns the pooled vector directly (1 vector per output)
    pooled, err := llama.GetEmbeddings(e.ctx, nSeqs, int32(e.nEmbd))
    if err != nil {
        return nil, err
    }

    // Split flat []float32 into per-text vectors
    results := make([][]float32, len(texts))
    for i := range texts {
        start := i * e.nEmbd
        results[i] = pooled[start : start+e.nEmbd]
    }
    return results, nil
}
```

---

## Memory Management Comparison

| Operation | Current (llama-server) | yzma |
|---|---|---|
| Free KV cache | Not possible (server opaque) | `MemoryClear(mem)` — instant |
| Free all memory | `ForceRestart()` — kill process + restart + health wait (~2s) | `ContextFree()` + `InitFromModel()` (~10ms, model stays loaded) |
| Free GPU memory | Kill process (model must reload) | `ModelFree()` — explicit, controlled |
| Crash recovery | Detect "connection refused", kill, restart, retry | Not applicable (no subprocess to crash) |
| Idle shutdown | Kill process via `Stop()` | `ModelFree()` — clean teardown |

---

## yzma Build and Deployment Guide (Windows)

### Development

```powershell
# Build — no CGo, no C compiler, no CMake needed
go build -o bin/go-indexing-mcp.exe .
go test -count=1 ./...
go vet ./...
```

### Runtime — first launch auto-setup

The `selfsetup` package downloads everything at first run:

```
1. Detect GPU:
   - nvidia-smi found  → variant = "cuda"
   - vulkaninfo found   → variant = "vulkan"
   - neither            → variant = "cpu"

2. Download llama.cpp shared libraries:
   URL: https://github.com/ggml-org/llama.cpp/releases/download/{tag}/
        llama-{tag}-bin-{os}-{variant}-{arch}.zip
   Extracts to: ~/.go-mcp/indexing/lib/
   Files: llama.dll, ggml.dll, ggml-base.dll, ggml-cpu.dll

3. Download embedding model (unchanged from current):
   URL: https://huggingface.co/.../jina-embeddings-v2-base-code-Q5_K_M.gguf
   Saves to: ~/.go-mcp/models/embeddings/
```

No CMake, no C++ compiler, no MSVC, no MinGW required — same experience as today, just different files downloaded.

### CI

```yaml
# .github/workflows/ci.yml — stays the same
steps:
  - uses: actions/setup-go@v5
  - run: go build -o bin/go-indexing-mcp .
  - run: go test ./...
  - run: go vet ./...
```

---

## Config Changes

### Before (current `config.json`)

```json
{
  "llama_variant": "cuda",
  "llama_profile": {
    "cuda": {
      "n_gpu_layers": 99,
      "ctx_size": 4096,
      "batch_size": 2048,
      "ubatch_size": 2048,
      "pooling": "mean",
      "extra_args": ["--no-webui", "-fa", "on"]
    }
  },
  "bin_path": "~/.go-mcp/llama-cpp/llama-server.exe",
  "model_path": "~/.go-mcp/models/embeddings/model.gguf"
}
```

### After (`config.json`)

```json
{
  "llama_cpp_version": "b9180",
  "lib_path": "~/.go-mcp/indexing/lib",
  "model_path": "~/.go-mcp/models/embeddings/model.gguf",
  "gpu_backend": "cuda",
  "n_gpu_layers": 99,
  "ctx_size": 4096,
  "batch_size": 2048,
  "ubatch_size": 2048
}
```

Fields removed: `bin_path`, `extra_args`, `pooling` (now context param), variant-specific profile nesting (flat config).

---

## Key Design Decisions

| Decision | Rationale |
|---|---|
| Pure Go FFI (no CGo) | Preserves `go build` simplicity, no C compiler, works on all platforms without toolchain setup |
| Runtime shared libs (not static linking) | Small binary, auto-download keeps current UX, users can swap libs for different backends |
| In-process (no subprocess) | No IPC overhead, no lock files, instant memory control, no crash recovery needed |
| Manual embedding orchestration | 50 lines of code for full control over batching, pooling, and memory — worth it vs opaque HTTP |
| Memory clearing instead of restart | `MemoryClear()` is sub-millisecond vs `ForceRestart()` at ~2 seconds — massive improvement for large indexing runs |
| Model stays loaded during restarts | New context is ~100MB allocation vs full model reload (~500MB+ from disk) |

---

## Summary

| | Before | After |
|---|---|---|
| Build command | `go build` | `go build` |
| C compiler needed | No | No |
| Subprocess | Yes (llama-server) | No |
| HTTP calls | Every embedding | None |
| Memory free | Kill + restart (~2s) | `MemoryClear()` (~1ms) |
| Lock files | Yes | No |
| Crash recovery | Yes | Not needed |
| Binary size | ~15MB | ~15MB |
| Runtime libs | llama-server.exe (auto-downloaded) | llama.dll, ggml.dll (auto-downloaded) |
| GPU variant | Auto-detected at setup | Auto-detected at setup |
| Zig compatible | No | Yes (swap prebuilt libs) |

**Net result:** Same build simplicity, same download model, much simpler runtime with no subprocess, no HTTP, no lock files, no IPC overhead, and instant memory clearing. ~1100 lines deleted, ~130 lines added.

---

## References

- [yzma GitHub](https://github.com/hybridgroup/yzma) — Go package, examples, benchmarks
- [yzma ROADMAP.md](https://github.com/hybridgroup/yzma/blob/main/ROADMAP.md) — 96%+ llama.cpp API coverage checklist
- [yzma BENCHMARKS.md](https://github.com/hybridgroup/yzma/blob/main/BENCHMARKS.md) — CPU, CUDA, Vulkan, Metal benchmarks
- [llama.cpp GitHub](https://github.com/ggml-org/llama.cpp) — upstream inference engine
- [jupiterrider/ffi](https://github.com/jupiterrider/ffi) — pure Go FFI library used by yzma
