package storage

import (
	"encoding/gob"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/cristian/go-indexing-mcp/pkg/chunker"
)

type ChunkRecord struct {
	ID        string
	FilePath  string
	RelPath   string
	Language  string
	StartLine int
	EndLine   int
	Content   string
	FileHash  string
	Vector    []float64
}

type SearchResult struct {
	ID        string  `json:"id"`
	FilePath  string  `json:"file_path"`
	RelPath   string  `json:"rel_path"`
	Language  string  `json:"language"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Content   string  `json:"content"`
	Score     float64 `json:"score"`
}

type StorageData struct {
	Records   []ChunkRecord
	CommitSHA string
}

type Storage struct {
	path       string
	dimensions int
	mu         sync.RWMutex
	records    []ChunkRecord
	commitSHA  string
	byID       map[string]int
	byPath     map[string][]int
	dirty      bool
}

func New(dbPath string, dimensions int) (*Storage, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}

	s := &Storage{
		path:       dbPath,
		dimensions: dimensions,
		byID:       make(map[string]int),
		byPath:     make(map[string][]int),
	}

	if err := s.load(); err != nil {
		return nil, fmt.Errorf("load storage: %w", err)
	}

	return s, nil
}

func (s *Storage) UpsertChunks(chunks []chunker.Chunk, embeddings map[string][]float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, ch := range chunks {
		emb, ok := embeddings[ch.ID]
		if !ok {
			continue
		}

		if idx, exists := s.byID[ch.ID]; exists {
			s.records[idx] = ChunkRecord{
				ID:        ch.ID,
				FilePath:  ch.FilePath,
				RelPath:   ch.RelPath,
				Language:  ch.Language,
				StartLine: ch.StartLine,
				EndLine:   ch.EndLine,
				Content:   ch.Content,
				FileHash:  ch.FileHash,
				Vector:    emb,
			}
		} else {
			idx := len(s.records)
			s.records = append(s.records, ChunkRecord{
				ID:        ch.ID,
				FilePath:  ch.FilePath,
				RelPath:   ch.RelPath,
				Language:  ch.Language,
				StartLine: ch.StartLine,
				EndLine:   ch.EndLine,
				Content:   ch.Content,
				FileHash:  ch.FileHash,
				Vector:    emb,
			})
			s.byID[ch.ID] = idx
		}
	}

	s.dirty = true
	return nil
}

func (s *Storage) DeleteChunksByPath(filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	indices, ok := s.byPath[filePath]
	if !ok {
		indices = s.findIndicesByPath(filePath)
	}
	if len(indices) == 0 {
		return nil
	}

	del := make(map[int]bool)
	for _, idx := range indices {
		del[idx] = true
	}

	var kept []ChunkRecord
	for i, rec := range s.records {
		if !del[i] {
			kept = append(kept, rec)
		}
	}

	s.rebuildIndex(kept)
	s.dirty = true
	return nil
}

func (s *Storage) Search(query []float64, limit int) ([]SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	type scored struct {
		idx   int
		score float64
	}

	var results []scored
	for i, rec := range s.records {
		score := cosineSimilarity(query, rec.Vector)
		results = append(results, scored{i, score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if len(results) > limit {
		results = results[:limit]
	}

	out := make([]SearchResult, len(results))
	for i, r := range results {
		rec := s.records[r.idx]
		out[i] = SearchResult{
			ID:        rec.ID,
			FilePath:  rec.FilePath,
			RelPath:   rec.RelPath,
			Language:  rec.Language,
			StartLine: rec.StartLine,
			EndLine:   rec.EndLine,
			Content:   rec.Content,
			Score:     r.score,
		}
	}

	return out, nil
}

func (s *Storage) Stats() (chunks, files int, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fileSet := make(map[string]bool)
	for _, rec := range s.records {
		fileSet[rec.FilePath] = true
	}

	return len(s.records), len(fileSet), nil
}

func (s *Storage) ListFiles() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]bool)
	var files []string
	for _, rec := range s.records {
		if !seen[rec.RelPath] {
			seen[rec.RelPath] = true
			files = append(files, rec.RelPath)
		}
	}
	return files
}

func (s *Storage) Close() error {
	return s.save()
}

func (s *Storage) SetCommitSHA(sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commitSHA = sha
	s.dirty = true
}

func (s *Storage) GetCommitSHA() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.commitSHA
}

func (s *Storage) load() error {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	var data StorageData
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&data); err == nil {
		s.rebuildIndex(data.Records)
		s.commitSHA = data.CommitSHA
		s.dirty = false
		return nil
	}

	f.Seek(0, 0)
	var records []ChunkRecord
	dec = gob.NewDecoder(f)
	if err := dec.Decode(&records); err != nil {
		return err
	}

	s.rebuildIndex(records)
	s.dirty = false
	return nil
}

func (s *Storage) save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.dirty {
		return nil
	}

	f, err := os.Create(s.path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := gob.NewEncoder(f)
	return enc.Encode(StorageData{
		Records:   s.records,
		CommitSHA: s.commitSHA,
	})
}

func (s *Storage) rebuildIndex(records []ChunkRecord) {
	s.records = records
	s.byID = make(map[string]int)
	s.byPath = make(map[string][]int)

	for i, rec := range records {
		s.byID[rec.ID] = i
		s.byPath[rec.FilePath] = append(s.byPath[rec.FilePath], i)
	}
}

func (s *Storage) findIndicesByPath(filePath string) []int {
	var indices []int
	for i, rec := range s.records {
		if rec.FilePath == filePath || rec.RelPath == filePath {
			indices = append(indices, i)
		}
	}
	return indices
}

func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}

	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
