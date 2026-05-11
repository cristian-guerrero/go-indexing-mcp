# go-indexing-mcp — Plan de Implementación

## Estructura del proyecto

```
C:\project\apps\go-indexing-mcp\
├── main.go                     # Entry point: --mcp (MCP stdio) o auto-setup
├── go.mod
├── go.sum
├── AGENTS.md                   # Instrucciones para el agente
├── .gitignore
├── PLAN.md
├── pkg/
│   ├── config/
│   │   └── config.go           # Carga/guarda config en ~/.go-mcp/indexing/config.yaml
│   ├── selfsetup/
│   │   └── setup.go            # Auto-setup: descarga llama, modelos, PATH, scripts
│   ├── llama/
│   │   └── manager.go          # Detecta en PATH, descarga, subproceso
│   ├── ignore/
│   │   └── ignore.go           # .gitignore + defaults globales
│   ├── walker/
│   │   └── walker.go           # File walker con git awareness
│   ├── chunker/
│   │   └── chunker.go          # Sliding window con overlap
│   ├── embedder/
│   │   └── embedder.go         # Cliente HTTP a llama.cpp server
│   ├── storage/
│   │   └── storage.go          # LanceDB: upsert, delete, búsqueda
│   ├── indexer/
│   │   └── indexer.go          # Orquesta el pipeline completo
│   └── mcp/
│       ├── server.go           # mark3labs/mcp-go server
│       └── tools.go            # Tools: search_code, index_status, reindex
```

## Pipeline

```
[FS] → [walker/watcher] → [ignore filter] → [chunker] → [llama-server embeddings] → [LanceDB]
   ↑                                                                                     │
   └──────── git diff HEAD~1 (incremental) ──────────────────────────────────────────────┘
```

## Dependencias Go

| Paquete | Propósito |
|---------|-----------|
| `github.com/mark3labs/mcp-go` | MCP protocol server |
| `github.com/lancedb/lancedb/go` | Vector database local |
| `github.com/sabhiram/go-gitignore` | .gitignore parsing |
| `gopkg.in/yaml.v3` | Config parsing |
| `github.com/google/uuid` | IDs de chunks |

## Ignorados por defecto

`.git`, `.github`, `node_modules`, `__pycache__`, `.venv`, `.env`, `.next`, `.nuxt`, `build`, `dist`, `target`, `vendor`, `.idea`, `.vscode`, `*.exe`, `*.dll`, `*.so`, `*.dylib`, `*.bin`, `*.png`, `*.jpg`, `*.jpeg`, `*.gif`, `*.ico`, `*.svg`, `*.woff`, `*.ttf`, `*.eot`, `*.zip`, `*.tar.gz`, `*.7z`, `*.rar`, `*.pdf`, `*.min.js`, `*.min.css` + `.gitignore` del proyecto.

## Chunking

- Sliding window de ~50 líneas con overlap de 10 líneas por archivo
- Límite de 512 tokens por chunk
- Metadata: path, language, start_line, end_line, function_name (si se detecta), hash git

## llama.cpp management

1. Buscar `llama-server` en PATH. Si existe → usar ese path.
2. Si no → descargar latest release de `ggml-org/llama.cpp` a `~/.go-mcp/indexing/bin/`
3. Iniciar como subproceso en puerto libre (56000-57000):
   ```
   llama-server --port PORT --model MODEL --embedding --nobrowser --mlock
   ```
4. Matar subproceso al cerrar (`defer cmd.Process.Kill()`)

## MCP Tools

| Tool | Descripción |
|------|-------------|
| `search_code` | Búsqueda semántica: `query string, path_filter? string, limit? int` |
| `index_status` | Estado del índice: archivos, chunks, último sync |
| `reindex` | Re-indexar desde cero o un path específico |
| `index_path` | Indexar un archivo/directorio específico |

## Config (`~/.go-mcp/indexing/config.yaml`)

```yaml
llama:
  bin_path: ""
  model_path: "~/.go-mcp/indexing/models/jina-embeddings-v2-base-code.gguf"
  port: 0
  extra_args: ["--mlock", "--no-warmup"]
indexing:
  root_path: "."
  ignore_patterns: []
  chunk_size: 50
  chunk_overlap: 10
  git_enabled: true
  watch_enabled: false
storage:
  path: "~/.go-mcp/indexing/lancedb"
embedding:
  model: "jina-embeddings-v2-base-code"
  dimensions: 768
  batch_size: 8
```

## Auto-setup (primera ejecución)

1. Si no hay terminal interactiva → re-lanzar en `cmd /K` (Windows) o `xterm` (Linux)
2. Crear config.yaml con valores default si no existe
3. Verificar/descargar llama-server
4. Verificar/descargar modelo embedding GGUF
5. Copiar binario a `~/.go-mcp/indexing/bin/`
6. Agregar `~/.go-mcp/indexing/bin/` al PATH del sistema
7. Crear script de arranque `run.bat` (Windows) o `run.sh` (Linux)

## Fases

### Fase 1 — MVP
- [x] Estructura de directorios
- [ ] config/ — carga/guarda YAML
- [ ] llama/ — manager + descarga + subproceso
- [ ] ignore/ — patrones de ignorado
- [ ] walker/ — file walker + git diff
- [ ] chunker/ — sliding window
- [ ] embedder/ — HTTP client a llama-server
- [ ] storage/ — LanceDB
- [ ] indexer/ — pipeline orquestador
- [ ] mcp/ — servidor MCP + tools
- [ ] selfsetup/ — auto-setup
- [ ] main.go — entry point
