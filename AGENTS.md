# AGENTS.md — go-indexing-mcp

## Comandos

- Build: `go build -o bin/go-indexing-mcp.exe .`
- Test: `go test ./...`
- Lint: `go vet ./...`
- Dependencias: `go mod tidy`

## Convenciones

- No agregar comentarios a menos que sea necesario
- Usar `fmt.Errorf("message: %w", err)` con wrapping
- Logging con `log/slog` (structured, nivel info/debug/error)
- Errores fatales con `slog.Error` + `os.Exit(1)`, nunca `log.Fatal`
- Paths: siempre usar `filepath.Join`, nunca strings concatenados
- Windows: validar paths con `filepath.Abs`

## Estructura

- `pkg/*` — lógica reutilizable, sin dependencia de `main`
- `main.go` — solo parsing de flags y routing a setup o MCP server
- `selfsetup/setup.go` — primera ejecución, descargas, PATH, scripts

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
