# AGENTS.md — go-indexing-mcp

## Comandos

- Build: `go build -o bin/go-indexing-mcp.exe .`
- Test: `go test -count=1 ./...` (evita cache)
- Lint: `go vet ./...`
- CI: `.github/workflows/ci.yml` — test + build multiplataforma + release en tags y main
- Dependencias: `go mod tidy`

## Convenciones

- Documentar funciones inline brevemente (1-2 líneas) explicando qué hace y por qué
- Usar `fmt.Errorf("message: %w", err)` con wrapping
- Logging con `log/slog` (structured, nivel info/debug/error)
- Errores fatales con `slog.Error` + `os.Exit(1)`, nunca `log.Fatal`
- Paths: siempre usar `filepath.Join`, nunca strings concatenados
- Windows: validar paths con `filepath.Abs`

## Estructura

- `main.go` — solo flag parsing + routing a los handlers
- `generate.go` — `runGenerate()` para `--generate`
- `query.go` — `runQuery()` para `--query`
- `configure.go` — `runConfigure()`, `configurePi()`, `configureOpenCode()` para `--configure`
- `pkg/config/` — carga/guarda `~/.go-mcp/indexing/config.json`
- `pkg/selfsetup/` — auto-setup en primera ejecución
- `pkg/llama/` — manager: descarga, subproceso llama-server, health check, `IsRunning()`, `StartedProcess()`
- `pkg/ignore/` — filtro .gitignore + patrones por defecto (niveles anidados)
- `pkg/walker/` — file walker con git diff, hash, detección de branch y lenguaje
- `pkg/chunker/` — sliding window + structural splitter. `ChunkFile` individual o `ChunkFiles` batch
- `pkg/structural/` — regex + brace/indent counting para detectar bloques estructurales por lenguaje. Sin dependencias externas
- `pkg/embedder/` — cliente HTTP a llama.cpp `/v1/embeddings`
- `pkg/storage/` — persistencia gob + cosine similarity + índices por rama + BM25 inverted index (`bm25.go`)
- `pkg/indexer/` — orquesta: walk → chunk → embed → store
- `pkg/mcp/` — servidor MCP con tools: search_code, reindex, index_path, _debug_index_files

## Comportamiento de search_code

Cada búsqueda mantiene el índice actualizado automáticamente:

1. Branch detectada → `SwitchBranch()` si cambió (índice aislado por rama)
2. Índice vacío → `IndexAll()` síncrono (primera vez)
3. Commits nuevos desde último SHA guardado → `IndexChanged()` síncrono
4. Solo cambios sin commit → `IndexChanged()` en background
5. `Search()` devuelve resultados

### Modos de búsqueda

Parámetro `mode` del tool `search_code` (y flag `--mode` en CLI):

| Modo | Descripción | Requiere llama.cpp |
|---|---|---|
| `"semantic"` (default) | Embedding → cosine similarity. Busca por intención | Sí |
| `"grep"` | Substring match case-insensitive en chunks cacheados. Rankea por frecuencia | No |
| `"hybrid"` | BM25 + vector similarity fusionados con RRF (k=60) | Sí (para el vector) |

### Implementación

- `pkg/storage/bm25.go`: `tokenize()`, `buildBM25Index()`, `bm25Index.score()`, `SearchGrep()`, `SearchHybrid()`, `searchLocked()`, RRF fusion
- `BM25`: inverted index en memoria (`map[string][]posting`), k1=1.2, b=0.75
- `grep`: `strings.Count` de substring lowercase sobre `rec.Content`
- `hybrid`: corre BM25 + vector search por separado, fusiona con Reciprocal Rank Fusion
- BM25 index se invalida (`s.bm25 = nil`) en UpsertChunks, DeleteChunksByPath, rebuildIndex

## Índice por rama

- Archivos: `vectors.gob` (main/default) o `vectors-{branch}.gob`
- `Storage.SwitchBranch(branch)` persiste y carga automáticamente
- `CommitSHA` guardado en cada indexación para diff preciso

## Chunking

- `pkg/chunker/` orquesta el split de archivos en chunks
- `pkg/structural/` detecta bloques estructurales por lenguaje usando regex + brace counting
- Lenguajes con `{}`: se cuenta profundidad de llaves para encontrar el cierre del bloque
- Lenguajes por indentación (Python, Ruby, YAML): se detecta cuando la indentación regresa al nivel inicial
- Lenguajes por sección (TOML, Markdown): bloque termina en el siguiente header/sección
- JSON: soporta `{}` y `[]` como delimitadores de bloque
- Si no se detectan bloques estructurales, se usa sliding window clásico
- `ChunkFiles()` procesa en batch: archivos chicos → sliding window, archivos grandes → structural split

## Flujo auto-setup

1. `main.go` detecta flags
2. Sin flags → ejecuta `selfsetup.Run()`
3. `selfsetup.Run()`:
   - Re-lanza en terminal si es necesario
   - Lee/crea config
   - Verifica/descarga llama.cpp
   - Verifica/descarga modelo
   - Copia binario
   - Agrega al PATH
   - Crea run.bat/run.sh
4. Con `--mcp` → inicia `mcp.Serve(llamaManager)`

## Mantenimiento de documentación

Antes de hacer commit o generar mensaje de commit, verificar:
- `README.md` — ¿cambió la interfaz (flags, MCP tools, parámetros)? Actualizar ejemplos y tabla de tools.
- `AGENTS.md` — ¿cambió estructura, flags, comportamiento de search_code, o se agregaron archivos/paquetes? Reflejar los cambios aquí.

No modificar README.md a menos que cambie la interfaz pública (flags, tools, configuración).

## Flags

- `--mcp` — inicia servidor MCP por stdio
- `--free` — detiene llama-server y libera memoria
- `--generate` — one-shot index del directorio actual con reporte detallado
- `--query "<texto>"` — búsqueda desde CLI, auto-indexa si es necesario
- `--mode <semantic|grep|hybrid>` — modo de búsqueda (default: semantic, usado con --query)
- `--configure <pi|opencode>` — configura integración con Pi agent u OpenCode
