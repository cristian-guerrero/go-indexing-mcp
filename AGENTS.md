# AGENTS.md — go-indexing-mcp

## Comandos

- Build: `go build -o bin/go-indexing-mcp.exe .`
- Test: `go test -count=1 ./...` (evita cache)
- Lint: `go vet ./...`
- CI: `.github/workflows/ci.yml` — test + build multiplataforma + release en tags y main
- Dependencias: `go mod tidy`

## Convenciones

- No agregar comentarios a menos que sea necesario
- Usar `fmt.Errorf("message: %w", err)` con wrapping
- Logging con `log/slog` (structured, nivel info/debug/error)
- Errores fatales con `slog.Error` + `os.Exit(1)`, nunca `log.Fatal`
- Paths: siempre usar `filepath.Join`, nunca strings concatenados
- Windows: validar paths con `filepath.Abs`

## Estructura

- `main.go` — flags `--mcp`, `--free`. Routing a setup o MCP server
- `pkg/config/` — carga/guarda `~/.go-mcp/indexing/config.json`
- `pkg/selfsetup/` — auto-setup en primera ejecución
- `pkg/llama/` — manager: descarga, subproceso llama-server, health check
- `pkg/ignore/` — filtro .gitignore + patrones por defecto (niveles anidados)
- `pkg/walker/` — file walker con git diff, hash, detección de branch y lenguaje
- `pkg/chunker/` — sliding window con overlap para dividir archivos en chunks
- `pkg/embedder/` — cliente HTTP a llama.cpp `/v1/embeddings`
- `pkg/storage/` — persistencia gob + cosine similarity + índices por rama
- `pkg/indexer/` — orquesta: walk → chunk → embed → store
- `pkg/mcp/` — servidor MCP con tools: search_code, reindex, index_path, _debug_index_files

## Comportamiento de search_code

Cada búsqueda mantiene el índice actualizado automáticamente:

1. Branch detectada → `SwitchBranch()` si cambió (índice aislado por rama)
2. Índice vacío → `IndexAll()` síncrono (primera vez)
3. Commits nuevos desde último SHA guardado → `IndexChanged()` síncrono
4. Solo cambios sin commit → `IndexChanged()` en background
5. `Search()` devuelve resultados

## Índice por rama

- Archivos: `vectors.gob` (main/default) o `vectors-{branch}.gob`
- `Storage.SwitchBranch(branch)` persiste y carga automáticamente
- `CommitSHA` guardado en cada indexación para diff preciso

## Flujo auto-setup

1. `main.go` detecta si hay `--mcp` flag
2. Sin `--mcp` → ejecuta `selfsetup.Run()`
3. `selfsetup.Run()`:
   - Re-lanza en terminal si es necesario
   - Lee/crea config
   - Verifica/descarga llama.cpp
   - Verifica/descarga modelo
   - Copia binario
   - Agrega al PATH
   - Crea run.bat/run.sh
4. Con `--mcp` → inicia `mcp.Serve(llamaManager)`

## Flags

- `--mcp` — inicia servidor MCP por stdio
- `--free` — detiene llama-server y libera memoria
