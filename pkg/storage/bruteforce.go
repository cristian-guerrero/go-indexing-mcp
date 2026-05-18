package storage

// bruteForceIndex implements VectorIndex via exhaustive linear scan.
// Exact results, O(n*d) per query. Optimal for small datasets (<4000 docs).
type bruteForceIndex struct {
	records []ChunkRecord
}

// NewBruteForceIndex creates a brute-force vector index.
func NewBruteForceIndex() VectorIndex {
	return &bruteForceIndex{}
}

func (idx *bruteForceIndex) Build(records []ChunkRecord) error {
	idx.records = records
	return nil
}

func (idx *bruteForceIndex) Query(query []float32, k int) ([]SearchResult, error) {
	if k <= 0 {
		k = 25
	}

	normalize32(query)

	type scored struct {
		idx   int
		score float64
	}

	tk := newTopK(k, func(a, b scored) bool {
		return a.score < b.score
	})

	for i, rec := range idx.records {
		score := float64(dotProduct32(query, rec.Vector))
		tk.Push(scored{i, score})
	}

	results := tk.Result()
	out := make([]SearchResult, len(results))
	for i, r := range results {
		rec := idx.records[r.idx]
		out[i] = SearchResult{
			ID: rec.ID, FilePath: rec.FilePath, RelPath: rec.RelPath,
			Language: rec.Language, StartLine: rec.StartLine, EndLine: rec.EndLine,
			Content: rec.Content, Score: r.score,
		}
	}
	return out, nil
}

func (idx *bruteForceIndex) Reset()       { idx.records = nil }
func (idx *bruteForceIndex) Name() string { return "bruteforce" }
