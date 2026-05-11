# go-indexing-mcp

Servidor MCP (Model Context Protocol) para indexación semántica de código. Permite buscar código en lenguaje natural usando embeddings locales con llama.cpp.

## Requisitos

- Go 1.26+
- Conexión a internet (solo para primera descarga de modelos)

## Compilación

```bash
go build -o bin/go-indexing-mcp.exe .
```

## Uso

### Primera ejecución (auto-setup)

Sin flags, el binario ejecuta el auto-setup: descarga llama.cpp, descarga el modelo de embeddings, copia el binario al PATH y crea scripts de ejecución.

```bash
./bin/go-indexing-mcp.exe
```

### Servidor MCP

```bash
./bin/go-indexing-mcp.exe --mcp
```

El servidor se comunica por **stdio** siguiendo el protocolo MCP. Para usarlo con un cliente MCP (Claude Desktop, etc.):

```json
{
  "command": "ruta/al/go-indexing-mcp.exe",
  "args": ["--mcp"]
}
```

## Herramientas MCP

| Herramienta | Descripción | Parámetros |
|---|---|---|
| `search_code` | Búsqueda semántica de código | `query` (req), `path_filter` (opc), `limit` (opc, def: 10) |
| `index_status` | Estado del índice (chunks, archivos, ultima indexación) | — |
| `reindex` | Re-indexar todos los archivos en segundo plano | — |
| `index_path` | Indexar un archivo o directorio específico | `path` (req) |

## Lenguajes Soportados

`go`, `python`, `javascript`, `typescript`, `rust`, `java`, `c`, `cpp`, `csharp`, `ruby`, `php`, `swift`, `kotlin`, `scala`, `sql`, `bash`, `powershell`, `markdown`, `yaml`, `json`, `toml`, `html`, `css`

## Configuración

Archivo: `~/.go-mcp/indexing/config.json`

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

- **llama**: Configuración del servidor llama.cpp (binario, modelo, puerto)
- **indexing**: Root del proyecto, patrones a ignorar, tamaño de chunks
- **storage**: Ruta del archivo de vectores (`.go-mcp/vectors.gob` por proyecto)
- **embedding**: Modelo de embeddings, dimensiones y batch size

## Almacenamiento

El índice se guarda en `<proyecto>/.go-mcp/vectors.gob` usando `encoding/gob`. Cada proyecto tiene su propio índice aislado. Al iniciar el servidor, si no existe el archivo de índice se ejecuta una indexación completa automática.

## Arquitectura

```
[FS] → [walker] → [ignore filter] → [chunker] → [llama-server embeddings] → [storage]
   ^                                                                           |
   +── git diff (indexación incremental) ──────────────────────────────────────+
```

## Dependencias

- `github.com/mark3labs/mcp-go` — Implementación del protocolo MCP
- `github.com/sabhiram/go-gitignore` — Filtrado .gitignore
