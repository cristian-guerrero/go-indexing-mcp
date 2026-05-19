# Configuración por Arquitectura

## Resumen

go-indexing-mcp auto-detecta la variante de hardware en `config.go:114` y aplica un perfil óptimo para llama-server (`--batch-size`, `--ctx-size`, etc.). Sin embargo, hay otros parámetros del pipeline (embedder `BatchSize`, `ChunkSize`, `MemoryFreeInterval`, `maxInputLength`) que tienen un valor global único y **no escalan según la arquitectura**, lo que puede dejar rendimiento sobre la mesa o causar reinicios innecesarios.

## Variantes de hardware y valores por defecto

| Variante | `--batch-size` | `--ubatch-size` | `--ctx-size` | `--n-gpu-layers` | Pooling | Extra flags |
|---|---|---|---|---|---|---|
| **CUDA** | 2048 | 2048 | 4096 | 99 | mean | `-fa on` |
| **Vulkan** | 512 | 512 | 4096 | 99 | mean | `-fa on` |
| **CPU (AVX2)** | 256 | 256 | 2048 | 0 | mean | `--mlock` |
| **Metal** | 1024 | 1024 | 4096 | 99 | mean | `--no-mmap` |

> Definidos en `pkg/config/config.go:77-110`.

## Parámetros del pipeline que NO escalan por arquitectura

| Parámetro | Default global | Problema |
|---|---|---|
| `embedder.BatchSize` | 8 | Diseñado para CPU, infrautiliza GPU |
| `ChunkSize` | 50 líneas | Ajuste fino: 50 es universal, pero en GPUs rápidas se puede subir |
| `ChunkOverlap` | 10 líneas (20%) | 20% es alto; con bloques estructurales sobra |
| `MemoryFreeInterval` | 100 archivos | Provoca reinicios innecesarios en GPU (cada 100 archivos) |
| `maxInputLength` | 1200 caracteres | Trunca chunks largos sin aviso |

---

## Perfiles recomendados

### CUDA (NVIDIA GPU con suficiente VRAM ≥ 4GB)

```jsonc
// ~/.go-mcp/indexing/config.json (solo fragmento relevante)
{
  "llama": {
    "batch_size": 2048,
    "ubatch_size": 2048,
    "ctx_size": 4096,
    "ngl_layers": 99,
    "pooling": "mean",
    "extra_args": ["--no-webui", "-fa", "on"]
  },
  "embedding": {
    "batch_size": 64,
    "dimensions": 768
  },
  "indexing": {
    "chunk_size": 50,
    "chunk_overlap": 5,
    "memory_free_interval": 500
  }
}
```

**Razonamiento:**
- `embedder.BatchSize=64`: aprovecha el `--batch-size 2048` de llama.cpp. 64 chunks × ~300 tokens ≈ 19,200 tokens → ~10 pases internos de 2048. Mucho mejor que los ~300 pases con batch_size=8.
- `chunk_overlap=5` (10%): la GPU procesa tan rápido que el solapamiento extra no compensa el costo. 5 líneas es suficiente para capturar contexto de borde.
- `memory_free_interval=500`: la VRAM de GPU no se fragmenta, no tiene sentido reiniciar cada 100 archivos.
- `maxInputLength=4096`: subirlo a 4096 o 8192 para evitar truncamiento.

**Modelo recomendado**: `jina-embeddings-v2-base-code-Q5_K_M` (768d). Con VRAM ≥ 6GB, probar `jina-embeddings-v2-base-code-Q8_0` para mejor calidad.

---

### Vulkan (GPU sin CUDA, ej. AMD, Intel ARC)

```jsonc
{
  "llama": {
    "batch_size": 512,
    "ubatch_size": 512,
    "ctx_size": 4096,
    "ngl_layers": 99,
    "pooling": "mean",
    "extra_args": ["--no-webui", "-fa", "on"]
  },
  "embedding": {
    "batch_size": 32,
    "dimensions": 768
  },
  "indexing": {
    "chunk_size": 50,
    "chunk_overlap": 5,
    "memory_free_interval": 300
  }
}
```

**Razonamiento:**
- `embedder.BatchSize=32`: 32 chunks × ~300 tokens ≈ 9,600 tokens → ~19 pases internos de 512. Vulkan tiene más overhead de kernel que CUDA, pero batches más grandes siguen siendo beneficiosos.
- `memory_free_interval=300`: Vulkan puede tener leaks de memoria en algunas implementaciones de drivers, pero 100 es demasiado conservador.
- `maxInputLength=4096`: igual que CUDA.

> **Nota Vulkan + Windows**: Algunos drivers Vulkan tienen problemas de memoria con contextos muy largos. Si ves crash de llama-server, baja `ctx_size` a 2048.

---

### CPU (AVX2 / sin GPU)

```jsonc
{
  "llama": {
    "batch_size": 256,
    "ubatch_size": 256,
    "ctx_size": 2048,
    "ngl_layers": 0,
    "pooling": "mean",
  },
  "embedding": {
    "batch_size": 16,
    "dimensions": 768
  },
  "indexing": {
    "chunk_size": 50,
    "chunk_overlap": 10,
    "memory_free_interval": 0
  }
}
```

**Razonamiento:**
- `embedder.BatchSize=16`: en CPU, batches grandes no dan mucha ventaja porque el forward pass es secuencial. Pero 8 es muy conservador; 16 reduce HTTP overhead a la mitad. Probar 32 si hay varios cores.
- `memory_free_interval=0`: **desactivar**. En CPU, llama-server no usa VRAM ni tiene fugas de memoria significativas (~500MB-1GB estables). El reinicio periódico solo pierde tiempo (2-10s por carga del modelo).
- `--mlock`: importante en CPU para evitar swapping del modelo en disco.
- `maxInputLength=2048`: con `ctx_size=2048`, 1200 chars es seguro pero 2048 permite chunks sin truncar.

> **Rendimiento esperado**: ~5-15 archivos/segundo dependiendo del CPU. Para un proyecto de 5000 archivos, indexación completa en 5-15 minutos.

---

### Metal (Apple Silicon)

```jsonc
{
  "llama": {
    "batch_size": 1024,
    "ubatch_size": 1024,
    "ctx_size": 4096,
    "ngl_layers": 99,
    "pooling": "mean",
  },
  "embedding": {
    "batch_size": 48,
    "dimensions": 768
  },
  "indexing": {
    "chunk_size": 50,
    "chunk_overlap": 5,
    "memory_free_interval": 10000
  }
}
```

**Razonamiento:**
- `embedder.BatchSize=48`: Apple Neural Engine + GPU unificada. 48 chunks × ~300 tokens ≈ 14,400 tokens → ~14 pases de 1024.
- `--no-mmap`: necesario en Metal para evitar page faults con modelos grandes en RAM unificada.
- `memory_free_interval=500`: RAM unificada no se fragmenta, no necesita reinicios frecuentes.

---

## Comparativa de rendimiento estimado

| Arquitectura | Chunks/s | Indexación 5000 archivos (~25k chunks) | Embedder BatchSize ideal |
|---|---|---|---|
| CPU (AVX2) | 5-15 | 5-15 min | 16-32 |
| Vulkan (AMD) | 30-80 | 1-3 min | 32-64 |
| Metal (M1+) | 60-150 | 30-60 seg | 48-64 |
| CUDA (RTX 3060) | 100-200 | 20-40 seg | 64-128 |
| CUDA (RTX 4090) | 300-500 | 5-10 seg | 128-256 |

Estimaciones con modelo `jina-embeddings-v2-base-code-Q5_K_M` (768d, ~4.5B params cuantizado).

---

## Cómo aplicar los cambios

Editar `~/.go-mcp/indexing/config.json` con los valores de tu arquitectura:

```bash
notepad $env:USERPROFILE\.go-mcp\indexing\config.json
```

Los valores del pipeline (embedder `batch_size`, `chunk_size`, `chunk_overlap`, `memory_free_interval`) se leen en caliente al reiniciar el MCP. Los valores de llama-server (`--batch-size`, `--ctx-size`) requieren reiniciar el proceso MCP para que se reflejen en los argumentos de `llama-server --port ...`.

> El archivo `config.json` se fusiona con defaults en `config.go:289`: los campos que no existan se rellenan con valores por defecto. No es necesario copiar todo el objeto, solo los campos que quieras override.

---

## maxInputLength recomendado

| Arquitectura | `maxInputLength` | Razón |
|---|---|---|
| CPU | 2048 | Coincide con `ctx-size 2048`, evita truncamiento |
| Vulkan | 4096 | Coincide con `ctx-size 4096` |
| Metal | 4096 | Coincide con `ctx-size 4096` |
| CUDA | 4096 | Coincide con `ctx-size 4096` |

> `maxInputLength` está hardcodeado en `pkg/embedder/embedder.go:101` como constante `maxInputLength = 1200`. Para cambiarlo hay que modificar el código y recompilar.

---

## Referencias en el código

- Perfiles de hardware: `pkg/config/config.go:77-110`, `VariantProfiles`
- Auto-detección de variante: `pkg/config/config.go:114-129`, `DetectVariant()`
- Default config completo: `pkg/config/config.go:142-186`, `DefaultConfig()`
- Embedder BatchSize: `pkg/embedder/embedder.go:35-49`, `New()`
- maxInputLength: `pkg/embedder/embedder.go:101`, `const maxInputLength = 1200`
- MemoryFreeInterval: `pkg/indexer/indexer.go:148-164`, usado solo en `IndexAll()`
- ChunkSize/Overlap: `pkg/chunker/chunker.go:47-62`, `New()`
