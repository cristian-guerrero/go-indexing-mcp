package storage

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	bm25K1 = 1.2
	bm25B  = 0.75
	rrfK   = 60.0
)

type topK[T any] struct {
	heap []T
	k    int
	less func(a, b T) bool
}

func newTopK[T any](k int, less func(a, b T) bool) *topK[T] {
	return &topK[T]{
		heap: make([]T, 0, k+1),
		k:    k,
		less: less,
	}
}

func (h *topK[T]) Push(item T) {
	if len(h.heap) < h.k {
		h.heap = append(h.heap, item)
		h.siftUp(len(h.heap) - 1)
		return
	}
	if !h.less(item, h.heap[0]) {
		h.heap[0] = item
		h.siftDown(0)
	}
}

func (h *topK[T]) Result() []T {
	sort.Slice(h.heap, func(i, j int) bool {
		return !h.less(h.heap[i], h.heap[j])
	})
	return h.heap
}

func (h *topK[T]) siftUp(i int) {
	item := h.heap[i]
	for i > 0 {
		parent := (i - 1) / 2
		if !h.less(item, h.heap[parent]) {
			break
		}
		h.heap[i] = h.heap[parent]
		i = parent
	}
	h.heap[i] = item
}

func (h *topK[T]) siftDown(i int) {
	n := len(h.heap)
	item := h.heap[i]
	for {
		smallest := i
		left := 2*i + 1
		right := 2*i + 2
		if left < n && h.less(h.heap[left], h.heap[smallest]) {
			smallest = left
		}
		if right < n && h.less(h.heap[right], h.heap[smallest]) {
			smallest = right
		}
		if smallest == i {
			break
		}
		h.heap[i] = h.heap[smallest]
		i = smallest
	}
	h.heap[i] = item
}

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
		limit = 25
	}

	var (
		re  *regexp.Regexp
		err error
	)
	if hasRegexChars(query) {
		re, err = regexp.Compile("(?i)" + query)
		if err != nil {
			re = nil
		}
	}

	type scored struct {
		idx   int
		score float64
	}

	tk := newTopK(limit, func(a, b scored) bool {
		return a.score < b.score
	})

	for i, rec := range s.records {
		var cnt int
		if re != nil {
			matches := re.FindAllString(rec.Content, -1)
			cnt = len(matches)
		} else {
			cnt = strings.Count(strings.ToLower(rec.Content), strings.ToLower(query))
		}
		if cnt > 0 {
			tk.Push(scored{i, float64(cnt)})
		}
	}

	results := tk.Result()

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

func hasRegexChars(s string) bool {
	for _, r := range s {
		if r == '.' || r == '+' || r == '*' || r == '?' || r == '(' || r == ')' ||
			r == '|' || r == '{' || r == '}' || r == '[' || r == ']' ||
			r == '^' || r == '$' || r == '\\' {
			return true
		}
	}
	return false
}

func (s *Storage) SearchHybrid(queryVec []float64, query string, limit int) ([]SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 25
	}

	normalize(queryVec)

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
			vector: dotProduct(queryVec, rec.Vector),
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

	type rrfResult struct {
		idx   int
		score float64
	}

	rrfTK := newTopK(limit, func(a, b rrfResult) bool {
		return a.score < b.score
	})

	for i := range s.records {
		score := 1.0/(rrfK+float64(bm25Ranks[i])) + 1.0/(rrfK+float64(vecRanks[i]))
		rrfTK.Push(rrfResult{i, score})
	}

	rrfResults := rrfTK.Result()

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
		limit = 25
	}

	normalize(query)

	type scored struct {
		idx   int
		score float64
	}

	tk := newTopK(limit, func(a, b scored) bool {
		return a.score < b.score
	})

	for i, rec := range s.records {
		score := dotProduct(query, rec.Vector)
		tk.Push(scored{i, score})
	}

	results := tk.Result()

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
