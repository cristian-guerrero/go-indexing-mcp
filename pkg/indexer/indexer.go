package indexer

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/cristian/go-indexing-mcp/pkg/chunker"
	"github.com/cristian/go-indexing-mcp/pkg/embedder"
	"github.com/cristian/go-indexing-mcp/pkg/storage"
	"github.com/cristian/go-indexing-mcp/pkg/walker"
)

type Indexer struct {
	Walker   *walker.Walker
	Chunker  *chunker.Chunker
	Embedder *embedder.Embedder
	Storage  *storage.Storage
	mu       sync.Mutex
	Running  bool
	Stats    IndexStats
}

type IndexStats struct {
	TotalChunks  int
	TotalFiles   int
	LastIndexed  string
	IsIndexing   bool
}

func New(w *walker.Walker, ch *chunker.Chunker, em *embedder.Embedder, st *storage.Storage) *Indexer {
	return &Indexer{
		Walker:   w,
		Chunker:  ch,
		Embedder: em,
		Storage:  st,
		Stats: IndexStats{
			LastIndexed: "never",
		},
	}
}

func (idx *Indexer) IndexAll() error {
	idx.mu.Lock()
	if idx.Running {
		idx.mu.Unlock()
		return fmt.Errorf("indexing already in progress")
	}
	idx.Running = true
	idx.Stats.IsIndexing = true
	idx.mu.Unlock()

	defer func() {
		idx.mu.Lock()
		idx.Running = false
		idx.Stats.IsIndexing = false
		idx.mu.Unlock()
	}()

	slog.Info("starting full index")

	files, err := idx.Walker.Walk()
	if err != nil {
		return fmt.Errorf("walk files: %w", err)
	}

	slog.Info("found files to index", "count", len(files))

	var totalChunks int
	for _, fi := range files {
		chunks, err := idx.Chunker.ChunkFile(fi)
		if err != nil {
			slog.Warn("skip chunking", "file", fi.RelPath, "error", err)
			continue
		}

		if len(chunks) == 0 {
			continue
		}

		embeddings, err := idx.Embedder.EmbedChunks(chunks)
		if err != nil {
			return fmt.Errorf("embed %s: %w", fi.RelPath, err)
		}

		if err := idx.Storage.UpsertChunks(chunks, embeddings); err != nil {
			return fmt.Errorf("store %s: %w", fi.RelPath, err)
		}

		totalChunks += len(chunks)
		slog.Debug("indexed file", "file", fi.RelPath, "chunks", len(chunks))
	}

	idx.mu.Lock()
	idx.Stats.TotalChunks = totalChunks
	idx.Stats.TotalFiles = len(files)
	idx.Stats.LastIndexed = "just now"
	idx.mu.Unlock()

	headSHA := idx.Walker.GetHeadSHA()
	if headSHA != "" {
		idx.Storage.SetCommitSHA(headSHA)
		slog.Debug("saved indexed commit", "sha", headSHA)
	}

	slog.Info("index complete", "files", len(files), "chunks", totalChunks)
	return nil
}

func (idx *Indexer) IndexPath(path string) error {
	files, err := idx.Walker.Walk()
	if err != nil {
		return err
	}

	for _, fi := range files {
		if fi.RelPath != path && fi.Path != path {
			continue
		}

		chunks, err := idx.Chunker.ChunkFile(fi)
		if err != nil {
			return err
		}

		embeddings, err := idx.Embedder.EmbedChunks(chunks)
		if err != nil {
			return err
		}

		idx.Storage.DeleteChunksByPath(fi.Path)
		return idx.Storage.UpsertChunks(chunks, embeddings)
	}

	return fmt.Errorf("file not found: %s", path)
}

func (idx *Indexer) IndexChanged() error {
	sinceSHA := idx.Storage.GetCommitSHA()
	files, err := idx.Walker.GetChangedFiles(sinceSHA)
	if err != nil {
		return fmt.Errorf("get changed files: %w", err)
	}

	if len(files) == 0 {
		return nil
	}

	slog.Info("incremental index", "files", len(files), "since", sinceSHA)

	for _, fi := range files {
		chunks, err := idx.Chunker.ChunkFile(fi)
		if err != nil {
			slog.Warn("skip chunking changed file", "file", fi.RelPath, "error", err)
			continue
		}

		embeddings, err := idx.Embedder.EmbedChunks(chunks)
		if err != nil {
			return fmt.Errorf("embed changed %s: %w", fi.RelPath, err)
		}

		idx.Storage.DeleteChunksByPath(fi.Path)
		if err := idx.Storage.UpsertChunks(chunks, embeddings); err != nil {
			return fmt.Errorf("store changed %s: %w", fi.RelPath, err)
		}

		slog.Info("re-indexed changed file", "file", fi.RelPath, "chunks", len(chunks))
	}

	headSHA := idx.Walker.GetHeadSHA()
	if headSHA != "" {
		idx.Storage.SetCommitSHA(headSHA)
		slog.Debug("updated indexed commit", "sha", headSHA)
	}

	return nil
}

func (idx *Indexer) GetStats() IndexStats {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.Storage != nil {
		chunks, files, err := idx.Storage.Stats()
		if err == nil {
			idx.Stats.TotalChunks = chunks
			idx.Stats.TotalFiles = files
		}
	}

	return idx.Stats
}

func (idx *Indexer) ListFiles() []string {
	if idx.Storage == nil {
		return nil
	}
	return idx.Storage.ListFiles()
}

func (idx *Indexer) Search(query string, pathFilter string, limit int) ([]storage.SearchResult, error) {
	queryVec, err := idx.Embedder.EmbedQuery(query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	results, err := idx.Storage.Search(queryVec, limit)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	if pathFilter != "" {
		var filtered []storage.SearchResult
		for _, r := range results {
			if matchesPath(r.RelPath, pathFilter) {
				filtered = append(filtered, r)
			}
		}
		return filtered, nil
	}

	return results, nil
}

func matchesPath(relPath, filter string) bool {
	return len(relPath) >= len(filter) && relPath[:len(filter)] == filter
}
