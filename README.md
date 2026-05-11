# go-indexing-mcp

Servidor MCP (Model Context Protocol) para indexación semántica de código. Busca código en lenguaje natural usando embeddings locales con llama.cpp.

## Requisitos

- Go 1.26+
- Conexión a internet (solo para primera descarga)

## Compilación

```bash
go build -o bin/go-indexing-mcp.exe .
```

## Uso

### Primera ejecución (auto-setup)

Sin flags ejecuta el auto-setup: descarga llama.cpp, descarga el modelo de embeddings, copia el binario al PATH y crea scripts:

```bash
./bin/go-indexing-mcp.exe
```

### Servidor MCP

```bash
./bin/go-indexing-mcp.exe --mcp
```

En un cliente MCP (Claude Desktop, etc.):

```json
{
  "command": "ruta/al/go-indexing-mcp.exe",
  "args": ["--mcp"]
}
```

### Liberar memoria

Detiene llama-server y libera la RAM usada por el modelo:

```bash
./bin/go-indexing-mcp.exe --free
```

## Herramientas MCP

| Herramienta | Descripción | Parámetros |
|---|---|---|
| `search_code` | Búsqueda semántica con indexación automática | `query` (req), `path_filter` (opc), `limit` (opc, def: 10) |
| `reindex` | Re-indexar todos los archivos en segundo plano | — |
| `index_path` | Indexar un archivo o directorio específico | `path` (req) |
| `_debug_index_files` | Listar archivos indexados (debug) | — |

### search_code — comportamiento inteligente

Cada búsqueda mantiene el índice actualizado automáticamente:

1. Si el índice está vacío → indexa todo (síncrono)
2. Si hay commits nuevos desde la última indexación → indexa cambios (síncrono)
3. Si solo hay cambios sin commit → indexa en background, búsqueda instantánea
4. Si cambiaste de rama → carga el índice de esa rama al instante

## Índice por rama

Cada rama de git tiene su propio archivo de índice (`vectors-{rama}.gob`). Al cambiar de rama y buscar, el servidor guarda el índice actual y carga el de la nueva rama automáticamente, sin re-indexar.

## Lenguajes Soportados

`go`, `python`, `javascript`, `typescript`, `rust`, `java`, `c`, `cpp`, `csharp`, `ruby`, `php`, `swift`, `kotlin`, `scala`, `sql`, `bash`, `powershell`, `markdown`, `yaml`, `json`, `toml`, `html`, `css`

## Configuración

Archivo: `~/.go-mcp/indexing/config.json` (se crea automáticamente en la primera ejecución)

```json
{
  "llama": {
    "bin_path": "",
    "model_path": "~/.go-mcp/indexing/models/nomic-embed-text-v1.5.Q4_K_M.gguf",
    "port": 0,
    "extra_args": []
  },
  "indexing": {
    "root_path": ".",
    "ignore_patterns": [],
    "chunk_size": 50,
    "chunk_overlap": 10,
    "git_enabled": true
  },
  "storage": {
    "path": ".go-mcp/vectors.gob"
  },
  "embedding": {
    "model": "jina-embeddings-v2-base-code",
    "dimensions": 768,
    "batch_size": 8
  }
}
```

### Secciones

- **llama**: Configuración de llama.cpp (binario, modelo, puerto, argumentos extra)
- **indexing**: Raíz del proyecto, patrones a ignorar, tamaño de chunks
- **storage**: Ruta base del archivo de vectores (se genera `vectors-{rama}.gob` por rama)
- **embedding**: Modelo de embeddings, dimensiones y batch size

## Arquitectura

```
[FS] → [walker] → [ignore filter] → [chunker] → [llama-server embeddings] → [storage]
   ↑                                                                              |
   ├── git diff <last_sha> (cambios committed + working tree) ────────────────────+
   └── git branch detection (índice aislado por rama)
```

El índice se guarda en `<proyecto>/.go-mcp/vectors-{rama}.gob` usando `encoding/gob`. Cada rama tiene su propio índice. Si no existe, se indexa automáticamente en la primera búsqueda.

## Dependencias

- `github.com/mark3labs/mcp-go` — Protocolo MCP
- `github.com/sabhiram/go-gitignore` — Filtrado .gitignore
