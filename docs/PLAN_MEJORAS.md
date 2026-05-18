# Plan de Mejoras: go-indexing-mcp

> Estrategia para migrar a `float32`, extraer interfaz `Index` con Cover Tree,
> y sincronizar accesos concurrentes con `sync.Cond`.

---

## Índice

1. [Fase 1: Migración a `float32`](#fase-1-migración-a-float32)
2. [Fase 2: Interfaz `Index` + Cover Tree](#fase-2-interfaz-index--cover-tree)
3. [Fase 3: Cache con `sync.Cond` para concurrencia](#fase-3-cache-con-synccond-para-concurrencia)
4. [Resumen de archivos modificados](#resumen-de-archivos-modificados)
5. [Estimación y riesgos](#estimación-y-riesgos)

---

## Fase 1: Migración a `float32`

**Meta**: Cambiar toda la pila de vectores de `[]float64` a `[]float32`.
Mitad de memoria, 2× throughput SIMD, pérdida de precisión despreciable (<1e-7).

### 1.1 `pkg/storage/simd/` — Migrar ASM de float64 → float32

**Archivos afectados:** `dot.go`, `dot_amd64.go`, `dot_amd64.s`, `dot_arm64.go`, `dot_generic.go`

**Cambios:**

| Archivo | Cambio |
|---|---|
| `dot.go` | `Dot(a, b []float64) float64` → `Dot32(a, b []float32) float32` |
| `dot_amd64.go` | `dot()` cambia a `float32`. `useAVX2` chequea ≥16 elementos (vs ≥8 antes). Eliminar `dotAVX2` legacy o mantener como `dotAVX2_64`. |
| `dot_amd64.s` | `VFMADD231PD` (double, 4/reg) → `VFMADD231PS` (single, 8/reg). Ajustar saltos: loop de 16→32, block de 4→8. |
| `dot_arm64.go` | `float64` → `float32` |
| `dot_generic.go` | `float64` → `float32` |

**Detalle ASM (`dot_amd64.s`):**

```asm
// float64:  VMOVUPD (AX), Y4     // 4 doubles
//           VFMADD231PD Y4, Y5, Y0
//           AVX2 4 registros × 4 doubles = 16 floats/iteración

// float32:  VMOVUPS (AX), Y4     // 8 singles
//           VFMADD231PS Y4, Y5, Y0
//           AVX2 4 registros × 8 floats = 32 floats/iteración
```

**Estrategia**: Crear `Dot32()` nueva. Mantener `Dot()` como wrapper que llama a `Dot32()` con conversión, por si algún caller externo usa la API vieja.

### 1.2 `pkg/storage/storage.go` — Tipos de datos

| Símbolo | Cambio |
|---|---|
| `ChunkRecord.Vector` | `[]float64` → `[]float32` |
| `SearchResult.Score` | Se mantiene `float64` (no es un vector) |
| `UpsertChunks(embeddings map[string][]float64)` | `embeddings map[string][]float32` |
| `Search(query []float64, limit int)` | `query []float32` |
| `searchLocked(query []float64, limit int)` | `query []float32` |
| `normalize(v []float64)` | `normalize32(v []float32)` |
| `dotProduct(a, b []float64)` | `dotProduct32(a, b []float32)` → `simd.Dot32` |

### 1.3 `pkg/embedder/embedder.go` — Conversión post-API

**Problema**: `embedResponse.Data[].Embedding` es `[]float64` (json.Unmarshal de float64).

**Solución**: Convertir en `EmbedChunks()` y `EmbedQuery()`:

```go
func (e *Embedder) EmbedChunks(chunks []chunker.Chunk) (map[string][]float32, error) {
    result := make(map[string][]float32, len(chunks))
    // ... llamada a embed() devuelve [][]float64 ...
    vec32 := make([]float32, len(vec64))
    for i, v := range vec64 {
        vec32[i] = float32(v)
    }
    result[ch.ID] = vec32
}
```

**Firma nueva**: `EmbedQuery(query string) ([]float32, error)`

### 1.4 `pkg/storage/bm25.go` — `SearchHybrid`

```go
func (s *Storage) SearchHybrid(queryVec []float32, query string, limit int) ([]SearchResult, error) {
    normalize32(queryVec)
    // ...
    score := dotProduct32(queryVec, rec.Vector)
    // ...
}
```

### 1.5 `pkg/indexer/indexer.go` — Pipeline

```go
embeddings, err := idx.Embedder.EmbedChunks(chunks)  // map[string][]float32
if err := idx.Storage.UpsertChunks(chunks, embeddings) // firma actualizada
```

### 1.6 `internal/cli/handlers.go` — CLI handlers

- `RunQuery()`: donde se obtiene `queryVec` vía `EmbedQuery()`, cambiar tipo.
- `RunGenerate()`: pasar `map[string][]float32` al storage.

### 1.7 Pruebas: `pkg/storage/storage_test.go`

- `makeEmbeddings()`: devuelve `map[string][]float32`.
- Vectores de test: `[]float32{1, 0, 0, 0}` en vez de `[]float64`.
- `TestDotProduct`, `TestNormalize`: migrados a float32.

### 1.8 Backward compatibility (gob legacy)

**Problema**: Al cambiar `ChunkRecord.Vector` de `[]float64` a `[]float32`, `encoding/gob` no podrá decodificar archivos `.gob` existentes.

**Solución en `load()`**:

```go
// Intentar cargar formato nuevo (float32)
var data StorageData
dec := gob.NewDecoder(f)
if err := dec.Decode(&data); err == nil {
    s.rebuildIndex(data.Records)
    s.commitSHA = data.CommitSHA
    return nil
}

// Fallback: intentar legacy (float64)
f.Seek(0, 0)
var records []ChunkRecordLegacy  // struct con Vector []float64
dec = gob.NewDecoder(f)
if err := dec.Decode(&records); err == nil {
    // Convertir float64 → float32
    converted := make([]ChunkRecord, len(records))
    for i, rec := range records {
        vec32 := make([]float32, len(rec.Vector))
        for j, v := range rec.Vector {
            vec32[j] = float32(v)
        }
        records[i].Vector = vec32
    }
    // Si no hay archivos legacy, simplemente reindexar
}
```

**Alternativa más simple**: Si detecta formato legacy, borrar el archivo y reindexar. El usuario ya tiene el pipeline de auto-index en startup.

### Orden de implementación sugerido

1. `pkg/storage/simd/` — ASM + Go wrappers float32
2. `pkg/storage/storage.go` — tipos, normalize32, dotProduct32
3. `pkg/embedder/embedder.go` — conversión post-API
4. `pkg/storage/bm25.go` — SearchHybrid
5. `pkg/indexer/indexer.go` — pipeline
6. `internal/cli/handlers.go` — CLI
7. Tests

---

## Fase 2: Interfaz `Index` + Cover Tree

### 2.1 Definir interfaz

**Archivo nuevo**: `pkg/storage/index.go`

```go
// Package storage — VectorIndex interface for swappable vector search backends.
package storage

// VectorIndex defines a generic vector index for kNN search.
// Implementations: brute-force (exact, default) and cover tree (ANN, for scale).
type VectorIndex interface {
    // Build constructs the index from the given records.
    Build(records []ChunkRecord) error

    // Query runs a kNN search against the index.
    // Returns results sorted by descending similarity.
    Query(query []float32, k int) ([]SearchResult, error)

    // Reset clears the index for rebuild.
    Reset()

    // Name returns a human-readable backend identifier.
    Name() string
}

// IndexKind selects the vector index backend.
type IndexKind string

const (
    IndexKindAuto      IndexKind = "auto"       // elige según tamaño
    IndexKindBruteForce IndexKind = "bruteforce" // fuerza bruta exacta
    IndexKindCover     IndexKind = "cover"      // cover tree ANN
)

// Option configures Storage behaviour.
type Option func(*Storage)

// WithIndexKind overrides the auto-selection of vector index backend.
func WithIndexKind(kind IndexKind) Option {
    return func(s *Storage) { s.indexKind = kind }
}
```

### 2.2 Refactor `Storage` para usar interfaz

```go
type Storage struct {
    // ... campos existentes ...
    vecIndex  VectorIndex // backend actual (puede ser nil = necesita rebuild)
    indexKind IndexKind   // "auto", "bruteforce", "cover"
}
```

**Auto-selección en `ensureVecIndex()`**:

```go
func (s *Storage) resolveIndexKind() IndexKind {
    if s.indexKind != "" && s.indexKind != IndexKindAuto {
        return s.indexKind
    }
    n := len(s.records)
    if n < 4000 {
        return IndexKindBruteForce
    }
    // Inferir dimensiones del primer vector
    if n == 0 {
        return IndexKindBruteForce
    }
    dim := len(s.records[0].Vector)
    if dim < 64 {
        return IndexKindBruteForce
    }
    density := float64(n) / float64(dim)
    if density < 16 {
        return IndexKindBruteForce
    }
    return IndexKindCover
}

func (s *Storage) ensureVecIndex() error {
    if s.vecIndex != nil {
        return nil
    }
    kind := s.resolveIndexKind()
    switch kind {
    case IndexKindCover:
        s.vecIndex = NewCoverIndex(1.3, CosineDistance)
    default:
        s.vecIndex = NewBruteForceIndex()
    }
    return s.vecIndex.Build(s.records)
}
```

**`searchLocked()` delegada**:

```go
func (s *Storage) searchLocked(query []float32, limit int) ([]SearchResult, error) {
    if err := s.ensureVecIndex(); err != nil {
        return nil, err
    }
    return s.vecIndex.Query(query, limit)
}
```

**Invalidación**: En `UpsertChunks()`, `DeleteChunksByPath()`, `SaveAndFree()`, `SwitchBranch()`, `rebuildIndex()` → `s.vecIndex = nil`.

### 2.3 `pkg/storage/bruteforce.go` — Extraer de `searchLocked()`

**Archivo nuevo**: `pkg/storage/bruteforce.go`

```go
type bruteForceIndex struct {
    records []ChunkRecord
}

func NewBruteForceIndex() VectorIndex {
    return &bruteForceIndex{}
}

func (idx *bruteForceIndex) Build(records []ChunkRecord) error {
    idx.records = records
    return nil
}

func (idx *bruteForceIndex) Query(query []float32, k int) ([]SearchResult, error) {
    normalize32(query)
    // topK heap idéntico al searchLocked actual
    tk := newTopK(k, func(a, b scored) bool { return a.score < b.score })
    for i, rec := range idx.records {
        score := dotProduct32(query, rec.Vector)
        tk.Push(scored{i, score})
    }
    // ... construir SearchResult desde tk.Result() ...
}

func (idx *bruteForceIndex) Reset()      { idx.records = nil }
func (idx *bruteForceIndex) Name() string { return "bruteforce" }
```

### 2.4 `pkg/storage/cover.go` — Cover Tree

**Archivo nuevo**: `pkg/storage/cover.go`

Adaptado de `C:\project\utils\databases\sqlite-vec\internal\cover\tree\tree.go` (~553 líneas).

```go
// coverIndex wraps a cover tree for ANN search.
type coverIndex struct {
    base     float32
    distance DistanceFunc
    tree     *coverTree
}
```

**Componentes a copiar/adaptar:**

| Componente sqlite-vec | Adaptación |
|---|---|
| `internal/cover/tree/tree.go` | Copiar, simplificar: quitar genéricos `T`, usar `int` como value (índice a `s.records`). |
| `internal/cover/tree/node.go` | Igual, sin cambios. |
| `internal/cover/tree/neighbor.go` | Igual (`heap.Interface`). |
| `internal/cover/tree/distance.go` | Usar `dotProduct32` + magnitud en vez de `search.Float32s`. |
| `internal/cover/tree/point.go` | Igual, `Vector []float32`. |
| `internal/cover/tree/values.go` | No necesario (usamos `s.records[índice]`). |
| `internal/cover/tree/*_binary.go` | No necesario para Fase 2 (Opción A: reconstruir en memoria). |

**Adaptación de distancia**:

```go
func cosineDistance(p1, p2 *coverPoint) float32 {
    dot := dotProduct32(p1.Vector, p2.Vector)
    // Si los vectores están L2-normalizados (como están en storage),
    // la magnitud es 1 y dot = cosine similarity.
    // Pero el cover tree inserta puntos sin normalizar, así que:
    mag := p1.Magnitude * p2.Magnitude
    if mag == 0 {
        return 1.0
    }
    return 1.0 - float64(dot)/float64(mag) // cosine distance = 1 - similarity
}
```

**Constructor**:

```go
func NewCoverIndex(base float32, distance DistanceFunc) VectorIndex {
    if base <= 1 {
        base = 1.3
    }
    if distance == nil {
        distance = CosineDistance
    }
    return &coverIndex{
        base:     base,
        distance: distance,
    }
}
```

**`Build()`**:

```go
func (idx *coverIndex) Build(records []ChunkRecord) error {
    idx.tree = newCoverTree(idx.base, idx.distance)
    for i, rec := range records {
        mag := magnitude32(rec.Vector)
        point := &coverPoint{
            Vector:    rec.Vector,
            Magnitude: mag,
            index:     int32(i),
        }
        idx.tree.Insert(point)
    }
    return nil
}
```

**`Query()`**: usar `KNearestNeighborsBestFirst()` del cover tree.

### 2.5 Pruebas

```go
func TestBruteForceIndexExact(t *testing.T)
func TestCoverIndexConsistency(t *testing.T)   // top-5 coincide con brute-force
func TestAutoSelectIndexThreshold(t *testing.T) // <4000 brute, >=4000 cover
func BenchmarkBruteForce_10k(t *testing.B)
func BenchmarkCoverTree_10k(t *testing.B)
```

### Orden de implementación

1. `pkg/storage/index.go` — interfaz + tipos
2. `pkg/storage/bruteforce.go` — extraer lógica
3. Refactor `Storage` + `ensureVecIndex()` + invalidación
4. `pkg/storage/cover.go` — implementación cover tree
5. Tests de consistencia y benchmark

---

## Fase 3: Cache con `sync.Cond` para concurrencia

**Problema**: Con `sync.RWMutex` simple, dos requests MCP concurrentes
pueden triggerear `ensureVecIndex()` duplicado.

### 3.1 `pkg/storage/cache.go` — Nuevo archivo

```go
package storage

import "sync"

// IndexCacheEntry manages concurrent lazy-build of a VectorIndex.
// Multiple goroutines queue on a single build instead of building duplicates.
type IndexCacheEntry struct {
    mu       sync.Mutex
    idx      VectorIndex
    building bool
    cond     *sync.Cond
}

func NewIndexCacheEntry() *IndexCacheEntry {
    e := &IndexCacheEntry{}
    e.cond = sync.NewCond(&e.mu)
    return e
}

// GetOrBuild returns cached index or runs builder. If another goroutine is
// already building, it waits and returns that result.
func (e *IndexCacheEntry) GetOrBuild(builder func() (VectorIndex, error)) (VectorIndex, error) {
    e.mu.Lock()

    // Fast path: ya construido
    if e.idx != nil {
        defer e.mu.Unlock()
        return e.idx, nil
    }

    // Otro hilo está construyendo → esperar
    if e.building {
        for e.building {
            e.cond.Wait()
        }
        idx := e.idx
        e.mu.Unlock()
        return idx, nil
    }

    // Nadie construye → nosotros lo hacemos
    e.building = true
    e.mu.Unlock()

    idx, err := builder()

    e.mu.Lock()
    e.idx = idx
    e.building = false
    e.cond.Broadcast() // despierta a los waiters
    e.mu.Unlock()
    return idx, err
}

// Invalidate clears the cached index.
func (e *IndexCacheEntry) Invalidate() {
    e.mu.Lock()
    e.idx = nil
    e.mu.Unlock()
}
```

### 3.2 Integración en `Storage`

```go
type Storage struct {
    // ...
    vecCache *IndexCacheEntry
}

func New(...) (*Storage, error) {
    // ...
    s.vecCache = NewIndexCacheEntry()
    // ...
}
```

**`ensureVecIndex()` simplificado**:

```go
func (s *Storage) ensureVecIndex() error {
    _, err := s.vecCache.GetOrBuild(func() (VectorIndex, error) {
        kind := s.resolveIndexKind()
        var idx VectorIndex
        switch kind {
        case IndexKindCover:
            idx = NewCoverIndex(defaultCoverBase, CosineDistance)
        default:
            idx = NewBruteForceIndex()
        }
        if err := idx.Build(s.records); err != nil {
            return nil, err
        }
        return idx, nil
    })
    return err
}
```

### 3.3 Invalidación en mutaciones

```go
func (s *Storage) UpsertChunks(...) error {
    // ...
    s.bm25 = nil
    s.trigrams = nil
    s.vecCache.Invalidate()
    return nil
}

func (s *Storage) DeleteChunksByPath(...) error {
    // ...
    s.bm25 = nil
    s.trigrams = nil
    s.vecCache.Invalidate()
    return nil
}

func (s *Storage) SaveAndFree() error {
    // ...
    s.vecCache.Invalidate()
    return nil
}

func (s *Storage) rebuildIndex(records []ChunkRecord) {
    // ...
    s.vecCache.Invalidate()
}
```

### 3.4 Flujo concurrente

```
Request A (search)              Request B (search)
       │                              │
       │ ensureVecIndex()             │ ensureVecIndex()
       │ GetOrBuild()                 │ GetOrBuild()
       │ → building=true              │ → building=true
       │   (nadie construye)          │   (alguien construye)
       │ Build(records...)            │ cond.Wait() ← bloquea
       │ ...                          │ ...
       │ idx = built                  │ ← cond.Broadcast()
       │ building=false               │ → idx != nil
       │ cond.Broadcast()             │ return idx
       │ return idx                   │
       │                              │
       │ Query(query, k)              │ Query(query, k)
       │ → resultados                 │ → resultados
```

### 3.5 Pruebas de concurrencia

```go
func TestConcurrentBuild(t *testing.T) {
    // 10 goroutines llaman Search() → solo 1 build
}

func TestBuildAfterInvalidate(t *testing.T) {
    // UpsertChunks invalida → próximo Search reconstruye
}

func TestCacheNoDeadlock(t *testing.T) {
    // Cond.Wait + Broadcast no produce deadlock
    // race detector: go test -race
}

func BenchmarkConcurrentSearch(b *testing.B) {
    // N goroutines concurrentes miden throughput
}
```

---

## Resumen de archivos modificados

| Archivo | F1: float32 | F2: Index | F3: Cache |
|---|---|---|---|
| `pkg/storage/simd/dot.go` | ✅ `Dot32` | — | — |
| `pkg/storage/simd/dot_amd64.go` | ✅ single ASM | — | — |
| `pkg/storage/simd/dot_amd64.s` | ✅ `PD→PS`, saltos | — | — |
| `pkg/storage/simd/dot_arm64.go` | ✅ float32 | — | — |
| `pkg/storage/simd/dot_generic.go` | ✅ float32 | — | — |
| `pkg/storage/storage.go` | ✅ tipos + normalize | ✅ interfaz + ensureVecIndex | ✅ vecCache |
| `pkg/storage/index.go` | — | **NUEVO** | — |
| `pkg/storage/bruteforce.go` | — | **NUEVO** | — |
| `pkg/storage/cover.go` | — | **NUEVO** | — |
| `pkg/storage/cache.go` | — | — | **NUEVO** |
| `pkg/storage/bm25.go` | ✅ SearchHybrid | — | — |
| `pkg/embedder/embedder.go` | ✅ conv 64→32 | — | — |
| `pkg/indexer/indexer.go` | ✅ tipos | — | — |
| `pkg/mcp/server.go` | — | — | ✅ (usa Storage) |
| `internal/cli/handlers.go` | ✅ tipos | — | — |
| `pkg/storage/storage_test.go` | ✅ vectores | ✅ tests | ✅ tests |

---

## Estimación y riesgos

### Estimación

| Fase | Archivos nuevos/modif. | Días |
|---|---|---|
| F1: float32 | ~8 + ASM | 2-3 |
| F2: Index + Cover Tree | 4 nuevos + refactor | 3-4 |
| F2: Tests consistencia | 2 nuevos | 1 |
| F3: Cache sync.Cond | 2 + tests | 1 |
| **Total** | | **7-9** |

### Riesgos

1. **ASM float32**: Verificar compilación y resultados con `go test -count=1 ./pkg/storage/simd/`. El assembly `dot_amd64.s` necesita cambios cuidadosos en offsets (cada float32 = 4 bytes, no 8).

2. **Cover Tree precisión**: Para datasets pequeños (<4000 docs) el cover tree puede diferir del brute-force por su naturaleza aproximada. La auto-selección lo evita: cover tree solo se activa ≥4000 docs.

3. **gob backward compat**: `ChunkRecord.Vector` cambia de `[]float64` a `[]float32`. `encoding/gob` rechazará archivos legacy. **Solución**: detectar legacy en `load()` y reindexar (el startup ya tiene auto-index). Alternativamente, mantener struct legacy para migración.

4. **`float32` precisión en RRF**: Los scores BM25+vector se mantienen en `float64` para RRF. Solo los vectores y dot product migran a `float32`. Sin impacto en ranking.

5. **Sync.Cond deadlock**: Verificar con `go test -race` que no hay condiciones de carrera entre `Invalidate()` + `GetOrBuild()`.
