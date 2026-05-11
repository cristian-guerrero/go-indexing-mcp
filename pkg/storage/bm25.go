package storage

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

const (
	bm25K1 = 1.2
	bm25B  = 0.75
	rrfK   = 60.0
)

type posting struct {
	DocID int
	Freq  int
}

type bm25Index struct {
	inverted  map[string][]posting
	docLen    []int
	avgDocLen float64
	nDocs     int
}

func tokenize(s string) []string {
	var tokens []string
	var buf []rune
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			buf = append(buf, r)
		} else {
			if len(buf) > 0 {
				tokens = append(tokens, string(buf))
				buf = buf[:0]
			}
		}
	}
	if len(buf) > 0 {
		tokens = append(tokens, string(buf))
	}
	return tokens
}

func buildBM25Index(records []ChunkRecord) *bm25Index {
	idx := &bm25Index{
		inverted: make(map[string][]posting),
		docLen:   make([]int, len(records)),
		nDocs:    len(records),
	}

	for i, rec := range records {
		terms := tokenize(rec.Content)
		idx.docLen[i] = len(terms)

		seen := make(map[string]int)
		for _, t := range terms {
			seen[t]++
		}
		for term, freq := range seen {
			idx.inverted[term] = append(idx.inverted[term], posting{DocID: i, Freq: freq})
		}
	}

	if idx.nDocs > 0 {
		var total int
		for _, l := range idx.docLen {
			total += l
		}
		idx.avgDocLen = float64(total) / float64(idx.nDocs)
	}

	return idx
}

func (idx *bm25Index) score(queryTerms []string, docID int) float64 {
	var score float64
	docLen := idx.docLen[docID]

	for _, term := range queryTerms {
		postings, ok := idx.inverted[term]
		if !ok {
			continue
		}

		var freq int
		for _, p := range postings {
			if p.DocID == docID {
				freq = p.Freq
				break
			}
		}
		if freq == 0 {
			continue
		}

		nq := len(postings)
		idf := math.Log(1 + (float64(idx.nDocs)-float64(nq)+0.5)/(float64(nq)+0.5))

		numerator := float64(freq) * (bm25K1 + 1)
		denominator := float64(freq) + bm25K1*(1-bm25B+bm25B*float64(docLen)/idx.avgDocLen)
		score += idf * numerator / denominator
	}

	return score
}

func (s *Storage) ensureBM25() {
	if s.bm25 != nil {
		return
	}
	s.bm25 = buildBM25Index(s.records)
}

func (s *Storage) SearchGrep(query string, limit int) ([]SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	q := strings.ToLower(query)

	type scored struct {
		idx   int
		score float64
	}

	var results []scored
	for i, rec := range s.records {
		cnt := strings.Count(strings.ToLower(rec.Content), q)
		if cnt > 0 {
			results = append(results, scored{i, float64(cnt)})
		}
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

func (s *Storage) SearchHybrid(queryVec []float64, query string, limit int) ([]SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	s.ensureBM25()
	queryTerms := tokenize(query)

	if len(queryTerms) == 0 {
		return s.searchLocked(queryVec, limit)
	}

	type scored struct {
		idx    int
		bm25   float64
		vector float64
	}

	all := make([]scored, len(s.records))
	for i, rec := range s.records {
		all[i] = scored{
			idx:    i,
			bm25:   s.bm25.score(queryTerms, i),
			vector: cosineSimilarity(queryVec, rec.Vector),
		}
	}

	bm25Ranks := make([]int, len(s.records))
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].bm25 > all[j].bm25
	})
	for rank, r := range all {
		bm25Ranks[r.idx] = rank + 1
	}

	vecRanks := make([]int, len(s.records))
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].vector > all[j].vector
	})
	for rank, r := range all {
		vecRanks[r.idx] = rank + 1
	}

	rrfResults := make([]struct {
		idx   int
		score float64
	}, len(s.records))
	for i := range s.records {
		rrfResults[i] = struct {
			idx   int
			score float64
		}{
			idx:   i,
			score: 1.0/(rrfK+float64(bm25Ranks[i])) + 1.0/(rrfK+float64(vecRanks[i])),
		}
	}

	sort.Slice(rrfResults, func(i, j int) bool {
		return rrfResults[i].score > rrfResults[j].score
	})

	if len(rrfResults) > limit {
		rrfResults = rrfResults[:limit]
	}

	out := make([]SearchResult, len(rrfResults))
	for i, r := range rrfResults {
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

func (s *Storage) searchLocked(query []float64, limit int) ([]SearchResult, error) {
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
