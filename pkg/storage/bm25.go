// Package storage provides search algorithms: BM25 keyword ranking, grep-style
// substring/regex matching, and hybrid search via Reciprocal Rank Fusion (RRF).
// BM25 uses the standard k1=1.2, b=0.75 parameters. RRF uses k=60.
package storage

import (
	"math"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// BM25 parameters from the standard Okapi BM25 formula.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
	rrfK   = 60.0
)

// topK implements a bounded min-heap for O(n log k) top-k selection.
// Maintains at most k items; the root is always the smallest (to be evicted).
type topK[T any] struct {
	heap []T
	k    int
	less func(a, b T) bool
}

// newTopK creates a bounded min-heap that retains the top k items (by "less" order).
func newTopK[T any](k int, less func(a, b T) bool) *topK[T] {
	return &topK[T]{
		heap: make([]T, 0, k+1),
		k:    k,
		less: less,
	}
}

// Push adds an item to the min-heap. If the heap is full and the new item is
// larger than the current minimum, replaces the minimum and sifts down.
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

// Result returns the top-k items sorted in descending order (best first).
func (h *topK[T]) Result() []T {
	sort.Slice(h.heap, func(i, j int) bool {
		return !h.less(h.heap[i], h.heap[j])
	})
	return h.heap
}

// siftUp restores heap ordering after appending to the end.
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

// siftDown restores heap ordering after replacing the root.
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

// GrepOptions controls the behavior of grep-style search.
type GrepOptions struct {
	Query         string
	Limit         int
	CaseSensitive bool
	WholeWord     bool
	Language      string
}

// GrepMatch represents a single matching line within a chunk.
type GrepMatch struct {
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// GrepResult extends SearchResult with per-line match details.
type GrepResult struct {
	ID        string      `json:"id"`
	FilePath  string      `json:"file_path"`
	RelPath   string      `json:"rel_path"`
	Language  string      `json:"language"`
	StartLine int         `json:"start_line"`
	EndLine   int         `json:"end_line"`
	Content   string      `json:"content"`
	Score     float64     `json:"score"`
	Matches   []GrepMatch `json:"matches,omitempty"`
}

// definitionKeywords is a list of line prefixes that indicate function/type/class
// definitions. Lines matching these prefixes get a +2 score boost in grep results.
var definitionKeywords = []string{
	"func ", "function ", "def ", "class ", "interface ", "struct ",
	"type ", "var ", "const ", "let ", "var ", "pub fn ", "async fn ",
	"impl ", "trait ", "enum ", "module ", "export ", "public ",
	"private ", "protected ", "static ",
}

// posting is a (docID, term frequency) entry in the BM25 inverted index.
type posting struct {
	DocID int
	Freq  int
}

// bm25Index implements Okapi BM25 ranking with an in-memory inverted index.
// Built lazily on first hybrid search and invalidated when records change.
type bm25Index struct {
	inverted  map[string][]posting
	docLen    []int
	avgDocLen float64
	nDocs     int
}

// tokenize splits text into lowercase alphanumeric tokens (underscore included).
// Used by both BM25 indexing and query processing.
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

// buildBM25Index constructs the inverted index and precomputes document lengths
// and average document length for the BM25 scoring formula.
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

// score computes the Okapi BM25 score for a document given query terms.
// Formula: sum(idf * (freq * (k1+1)) / (freq + k1 * (1 - b + b * docLen/avgDocLen)))
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

// ensureBM25 builds the BM25 index lazily if it has been invalidated.
func (s *Storage) ensureBM25() {
	if s.bm25 != nil {
		return
	}
	s.bm25 = buildBM25Index(s.records)
}

// SearchGrep performs substring or regex matching on all stored chunks.
// Results are ranked by normalized match count [0,1], with definition lines
// receiving a +2 boost. Supports case-sensitive, whole-word, and language-filter modes.
func (s *Storage) SearchGrep(opts GrepOptions) ([]GrepResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := opts.Limit
	if limit <= 0 {
		limit = 25
	}

	query := opts.Query
	if query == "" {
		return nil, nil
	}

	flags := ""
	if !opts.CaseSensitive {
		flags = "(?i)"
	}

	pattern := query
	if opts.WholeWord {
		pattern = `\b` + query + `\b`
	}

	var (
		re  *regexp.Regexp
		err error
	)
	if hasRegexChars(query) || opts.WholeWord {
		re, err = regexp.Compile(flags + pattern)
		if err != nil {
			re = nil
		}
	}

	langFilter := strings.ToLower(opts.Language)

	// Build candidate set from trigram index for literal queries (no regex).
	// nil candidateSet = scan all docs; non-nil but empty = no results.
	var candidateSet map[int]bool
	if re == nil && len(query) >= 3 {
		s.ensureTrigrams()
		candidates := s.trigrams.candidateDocs(query)
		if candidates != nil {
			if len(candidates) == 0 {
				return nil, nil
			}
			candidateSet = make(map[int]bool, len(candidates))
			for _, idx := range candidates {
				candidateSet[idx] = true
			}
		}
	}

	type scored struct {
		idx     int
		score   float64
		matches []GrepMatch
	}

	var all []scored

	if len(s.records) == 0 {
		return nil, nil
	}

	numWorkers := runtime.GOMAXPROCS(0) / 2
	if numWorkers < 1 {
		numWorkers = 1
	}

	type batch struct {
		results []scored
		idx     int
	}
	ch := make(chan batch, numWorkers)

	batchSize := (len(s.records) + numWorkers - 1) / numWorkers
	var wg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		start := w * batchSize
		end := start + batchSize
		if end > len(s.records) {
			end = len(s.records)
		}
		if start >= end {
			break
		}

		wg.Add(1)
		go func(records []ChunkRecord, baseIdx int, candSet map[int]bool) {
			defer wg.Done()
			local := make([]scored, 0)

			for j, rec := range records {
				i := baseIdx + j
				if candSet != nil && !candSet[i] {
					continue
				}
				if langFilter != "" && !strings.EqualFold(rec.Language, langFilter) {
					continue
				}

				lines := strings.Split(rec.Content, "\n")
				var matchLines []GrepMatch
				var cnt int

				for lineIdx, line := range lines {
					var lineCnt int
					if re != nil {
						lineCnt = len(re.FindAllString(line, -1))
					} else {
						if opts.CaseSensitive {
							lineCnt = strings.Count(line, query)
						} else {
							lineCnt = strings.Count(strings.ToLower(line), strings.ToLower(query))
						}
					}
					if lineCnt > 0 {
						cnt += lineCnt
						matchLines = append(matchLines, GrepMatch{
							Line:    rec.StartLine + lineIdx,
							Content: strings.TrimRight(line, " \t\r"),
						})
					}
				}

				if cnt == 0 {
					continue
				}

				score := float64(cnt)

				for _, m := range matchLines {
					lower := strings.ToLower(m.Content)
					for _, kw := range definitionKeywords {
						if strings.HasPrefix(strings.TrimLeft(lower, " \t"), kw) {
							score += 2.0
							break
						}
					}
				}

				local = append(local, scored{idx: i, score: score, matches: matchLines})
			}

			ch <- batch{results: local, idx: baseIdx}
		}(s.records[start:end], start, candidateSet)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	for br := range ch {
		all = append(all, br.results...)
	}

	var maxScore float64
	for _, r := range all {
		if r.score > maxScore {
			maxScore = r.score
		}
	}
	for i := range all {
		if maxScore > 0 {
			all[i].score /= maxScore
		}
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].score > all[j].score
	})
	if len(all) > limit {
		all = all[:limit]
	}

	out := make([]GrepResult, len(all))
	for i, r := range all {
		rec := s.records[r.idx]
		out[i] = GrepResult{
			ID:        rec.ID,
			FilePath:  rec.FilePath,
			RelPath:   rec.RelPath,
			Language:  rec.Language,
			StartLine: rec.StartLine,
			EndLine:   rec.EndLine,
			Content:   rec.Content,
			Score:     r.score,
			Matches:   r.matches,
		}
	}

	return out, nil
}

// hasRegexChars returns true if the string contains regex metacharacters.
// Used to decide whether to compile query as regex or use plain string search.
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

// SearchHybrid combines BM25 keyword scores and vector similarity via
// Reciprocal Rank Fusion (RRF). Both systems score all documents independently,
// rank them, then fuse the ranks: rrf_score = 1/(k + bm25Rank) + 1/(k + vecRank).
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

// trigramIndex maps lowercase trigrams to sorted doc IDs for candidate pre-filtering
// in grep searches. Allows O(1) lookup of which chunks could contain a literal query,
// drastically reducing the number of chunks that need full line-by-line scanning.
type trigramIndex struct {
	index map[string][]int
}

// TrigramData is the on-disk format for the persisted trigram index.
type TrigramData struct {
	Index    map[string][]int
	DocCount int
}

// buildTrigramIndex constructs the inverted trigram index from all chunk records.
// For each record, extracts all 3-char sliding window substrings (lowercased)
// and maps them to the record's index. Duplicate trigrams per doc are deduped.
// Posting lists are sorted for O(n+m) intersection.
func buildTrigramIndex(records []ChunkRecord) *trigramIndex {
	idx := &trigramIndex{index: make(map[string][]int)}
	for i, rec := range records {
		seen := make(map[string]bool)
		content := strings.ToLower(rec.Content)
		for j := 0; j <= len(content)-3; j++ {
			tg := content[j : j+3]
			if !seen[tg] {
				seen[tg] = true
				idx.index[tg] = append(idx.index[tg], i)
			}
		}
	}
	for _, postings := range idx.index {
		sort.Ints(postings)
	}
	return idx
}

// candidateDocs returns doc IDs that contain ALL trigrams of the query.
// Returns nil if the query is too short (<3 chars), meaning "no filter — all docs"
// must be scanned. Returns empty slice when no docs can possibly match.
func (idx *trigramIndex) candidateDocs(query string) []int {
	q := strings.ToLower(query)
	if len(q) < 3 {
		return nil
	}

	trigrams := make([]string, 0, len(q)-2)
	for j := 0; j <= len(q)-3; j++ {
		trigrams = append(trigrams, q[j:j+3])
	}

	// Sort by posting list length (smallest first) for efficient intersection
	sort.Slice(trigrams, func(i, j int) bool {
		return len(idx.index[trigrams[i]]) < len(idx.index[trigrams[j]])
	})

	first, ok := idx.index[trigrams[0]]
	if !ok {
		return []int{}
	}
	result := make([]int, len(first))
	copy(result, first)

	for _, tg := range trigrams[1:] {
		postings, ok := idx.index[tg]
		if !ok {
			return []int{}
		}
		result = intersectSorted(result, postings)
		if len(result) == 0 {
			return []int{}
		}
	}
	return result
}

// intersectSorted returns the intersection of two sorted int slices.
func intersectSorted(a, b []int) []int {
	var result []int
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			result = append(result, a[i])
			i++
			j++
		} else if a[i] < b[j] {
			i++
		} else {
			j++
		}
	}
	return result
}

// ensureTrigrams builds the trigram index lazily if it hasn't been built yet.
func (s *Storage) ensureTrigrams() {
	if s.trigrams != nil {
		return
	}
	s.trigrams = buildTrigramIndex(s.records)
}

// searchLocked performs a pure vector similarity search (caller must hold RLock).
// Normalizes the query vector, computes dot product against all stored vectors,
// and returns the top-k results via the bounded min-heap.
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
