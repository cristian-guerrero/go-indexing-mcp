# Arquitectura

## Pipeline de Indexación

```
[FS] → [walker] → [ignore filter] → [chunker] → [embedder] → [storage]
                                        │
                                   [structural]
                                   (regex + brace)
```

### Fases

1. **Walk** (`pkg/walker/`): Recorre el árbol de directorios aplicando filtros `.gitignore` + patrones por defecto. Detecta lenguaje por extensión, calcula hash SHA-256 del archivo, detecta branch git actual y obtiene diff contra el último commit indexado.

2. **Chunk** (`pkg/chunker/`): Divide cada archivo en fragmentos (chunks) para embedding:
   - Archivos ≤ `chunk_size` líneas → un solo chunk (sliding window)
   - Archivos grandes → pasa por `pkg/structural/` para detectar bloques estructurales
   - Si se encuentran bloques → cada bloque se chunkee respetando sus límites
   - Si no se encuentran bloques → sliding window como fallback

3. **Embed** (`pkg/embedder/`): Envía cada chunk a llama.cpp (`/v1/embeddings`) y obtiene un vector de 768 dimensiones.

4. **Store** (`pkg/storage/`): Persiste chunks + vectores en disco usando `encoding/gob`. Índice aislado por rama git.

## Structural Splitter (`pkg/structural/`)

### Criterios por lenguaje

| Tipo | Lenguajes | Detección |
|------|-----------|-----------|
| Brace `{}` | Go, JS/TS, Rust, Java, C, C++, C#, PHP, Swift, Kotlin, Scala, Zig, Bash | Regex de inicio + conteo de `{}` con manejo de strings/comentarios |
| Brace `{}[]` | JSON | Regex de clave top-level + conteo de `{}` y `[]` |
| Indentación | Python, Ruby, YAML | Regex de inicio + detección de indentación decreciente |
| Sección | TOML, Markdown | Regex de header + bloque hasta el siguiente header |
| SQL `;` | SQL | Regex de DDL + `;` semántico con manejo de `BEGIN`/`END` |

### Estrategias de fin de bloque

- **findBraceEnd**: Cuenta profundidad de `{}`, ignorando strings y comentarios.
- **findBraceEndAny**: Como el anterior pero también cuenta `[]` (para JSON).
- **findIndentEnd**: Escanea líneas subsiguientes; cuando la indentación regresa al nivel inicial, el bloque termina.
- **findSectionEnd**: Termina cuando encuentra el siguiente match del patrón de inicio (para TOML y Markdown).

Si ninguna estrategia encuentra un bloque válido, se usa sliding window clásico como fallback.

## Chunking

### Sliding Window

Parámetros configurables: `chunk_size` (def: 50 líneas) y `chunk_overlap` (def: 10).

```
Líneas: [1][2][3][4][5][6][7][8][9][10]...
Chunk 1: [1][2][3][4][5]
Chunk 2:           [4][5][6][7][8]
Chunk 3:                     [7][8][9][10]
```

### Structural Split

Cuando un archivo excede `chunk_size` y se detectan bloques estructurales:

```
Archivo con funciones:
  [imports] [func foo() { ... }] [gap] [func bar() { long... }] [gap]
       ↓
  Chunk 1: imports (sliding window si > chunk_size)
  Chunk 2: func foo() { ... } (1 chunk si ≤ chunk_size)
  Chunk 3: gap (sliding window si > chunk_size)
  Chunk 4-6: func bar() { long... } (subdividido respetando límites)
  Chunk 7: trailing gap (sliding window si > chunk_size)
```

## Índice por Rama

Cada rama git tiene su propio archivo de índice. Esto permite cambiar de rama sin re-indexar:

- `vectors.gob` — rama por defecto (main/master)
- `vectors-{branch}.gob` — otras ramas

Al detectar un cambio de rama (via `git rev-parse --abbrev-ref HEAD`), `Storage.SwitchBranch()` persiste el índice actual en disco y carga el de la nueva rama.

## Commit SHA Tracking

Después de cada indexación, se guarda el `HEAD` SHA actual en el storage. En la próxima búsqueda/indexación:

1. Si `lastSHA ≠ headSHA` → hay commits nuevos → `IndexChanged()` sincrónico
2. Si `lastSHA == headSHA` → solo cambios sin commit → `IndexChanged()` en background

`IndexChanged()` usa `git diff {lastSHA}..HEAD --name-only` para detectar archivos modificados y `git diff HEAD --name-only` para cambios sin commit.

## Auto-setup

En la primera ejecución (sin flags), `selfsetup.Run()`:

1. Verifica si la terminal es interactiva; si no, relanza en una nueva terminal
2. Lee o crea `~/.go-mcp/indexing/config.json`
3. Descarga llama.cpp (binario precompilado) si no está en PATH ni en el directorio bin
4. Descarga el modelo de embeddings GGUF (fallback: jina-code → nomic → bge)
5. Copia el binario a `~/.go-mcp/indexing/bin/`
6. Agrega el directorio bin al PATH del usuario
7. Crea scripts `run.bat`/`run.sh` para lanzar el MCP server

## CLI Flags

| Flag | Descripción |
|------|-------------|
| `--mcp` | Inicia servidor MCP por stdio |
| `--free` | Detiene llama-server y libera VRAM |
| `--generate` | One-shot index del directorio actual con reporte |
| `--query "<texto>"` | Búsqueda semántica desde CLI (auto-indexa) |
| (sin flags) | Ejecuta auto-setup |

## MCP Tools

| Tool | Descripción |
|------|-------------|
| `search_code` | Búsqueda semántica con auto-indexación |
| `reindex` | Re-indexa todo en background |
| `index_path` | Indexa un archivo/directorio específico |
| `_debug_index_files` | Lista archivos en el índice (debug) |

## Gestión de llama-server

- Puerto por defecto: 56000 (configurable)
- Varios procesos MCP pueden compartir el mismo llama-server
- `IsRunning()` verifica health endpoint HTTP
- `StartedProcess()` indica si el manager local inició el proceso (vs reutilizar uno existente)
- `--free` mata el proceso por puerto usando `netstat -ano`
- En MCP mode: llama-server se inicia **lazy** (al primer tool call vía `ensureLlama()`) y nunca se detiene — el proceso queda vivo para la próxima sesión
- En `--generate` y `--query`: igual — se inicia si no corre, pero ya no se detiene al terminar
- `--sleep-idle-seconds N`: llama-server descarga el modelo de VRAM tras N segundos inactivo; el proceso sigue vivo y recarga automáticamente en el próximo request (configurable vía `idle_timeout_secs`)

## Dependencias Externas

- `github.com/mark3labs/mcp-go` — Implementación del protocolo MCP
- `github.com/sabhiram/go-gitignore` — Parseo de archivos .gitignore
- `gopkg.in/yaml.v3` — YAML (solo para config legacy)
- llama.cpp — Servidor de embeddings (binario externo, descarga automática)
