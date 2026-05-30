package db

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// rrfK is the constant used in Reciprocal Rank Fusion.
const rrfK = 60.0

// Search performs a pure vector similarity search using sqlite-vec ANN.
// Returns up to `limit` results ranked by distance (ascending).
func (s *Store) Search(query []float32, limit int) ([]SearchResult, error) {
	vecBytes := float32sToBytes(query)

	// Query vec0 for chunk_id + distance, then fetch metadata from chunks
	rows, err := s.db.Query(
		`SELECT chunk_id, distance
		 FROM vec_chunks
		 WHERE vector MATCH ?
		   AND k = ?
		 ORDER BY distance`, vecBytes, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type vecHit struct {
		id       string
		distance float64
	}
	var hits []vecHit
	for rows.Next() {
		var h vecHit
		if err := rows.Scan(&h.id, &h.distance); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fetch chunk metadata for each hit
	results := make([]SearchResult, 0, len(hits))
	for _, h := range hits {
		var r SearchResult
		err := s.db.QueryRow(
			`SELECT id, file_path, rel_path, language, start_line, end_line, content
			 FROM chunks WHERE id = ?`, h.id).Scan(
			&r.ID, &r.FilePath, &r.RelPath, &r.Language,
			&r.StartLine, &r.EndLine, &r.Content)
		if err != nil {
			continue
		}
		r.Score = 1.0 - h.distance // L2 distance → similarity
		results = append(results, r)
	}

	return results, nil
}

// searchHybridVector runs the vector half of hybrid search.
func (s *Store) searchHybridVector(query []float32, limit int) (map[string]float64, error) {
	vecBytes := float32sToBytes(query)

	rows, err := s.db.Query(
		`SELECT chunk_id, distance
		 FROM vec_chunks
		 WHERE vector MATCH ?
		   AND k = ?`, vecBytes, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scores := make(map[string]float64, limit)
	for rows.Next() {
		var id string
		var dist float64
		rows.Scan(&id, &dist)
		scores[id] = 1.0 - dist
	}
	return scores, rows.Err()
}

// searchHybridBM25 runs the BM25 (FTS5) half of hybrid search.
func (s *Store) searchHybridBM25(query string, limit int) (map[string]float64, error) {
	ftsQuery := sanitizeFTS5(query)
	if ftsQuery == "" {
		return nil, nil
	}

	rows, err := s.db.Query(
		`SELECT c.id, fts.rank
		 FROM chunks_fts fts
		 JOIN chunks c ON c.rowid = fts.rowid
		 WHERE chunks_fts MATCH ?
		 ORDER BY rank
		 LIMIT ?`, ftsQuery, limit)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()

	scores := make(map[string]float64, limit)
	for rows.Next() {
		var id string
		var rank float64
		rows.Scan(&id, &rank)
		scores[id] = -rank
	}
	return scores, rows.Err()
}

// sanitizeFTS5 converts a user query string into a safe FTS5 MATCH expression.
func sanitizeFTS5(query string) string {
	if strings.HasPrefix(query, "\"") && strings.HasSuffix(query, "\"") {
		return query
	}

	q := strings.ReplaceAll(query, "\"", "\"\"")

	tokens := strings.Fields(q)
	if len(tokens) == 0 {
		return ""
	}
	var parts []string
	for _, t := range tokens {
		parts = append(parts, t+"*")
	}
	return strings.Join(parts, " ")
}

// SearchHybrid performs BM25 + vector search fused via Reciprocal Rank Fusion.
func (s *Store) SearchHybrid(queryVec []float32, query string, limit int) ([]SearchResult, error) {
	type vecResult struct {
		scores map[string]float64
	}
	type bm25Result struct {
		scores map[string]float64
	}

	vecCh := make(chan vecResult, 1)
	bm25Ch := make(chan bm25Result, 1)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		scores, _ := s.searchHybridVector(queryVec, limit*2)
		vecCh <- vecResult{scores}
	}()
	go func() {
		defer wg.Done()
		scores, _ := s.searchHybridBM25(query, limit*2)
		bm25Ch <- bm25Result{scores}
	}()
	wg.Wait()

	vr := <-vecCh
	br := <-bm25Ch

	allIDs := make(map[string]bool)
	for id := range vr.scores {
		allIDs[id] = true
	}
	for id := range br.scores {
		allIDs[id] = true
	}

	if len(allIDs) == 0 {
		return nil, nil
	}

	type idRank struct {
		id   string
		rank int
	}

	vecSorted := make([]idRank, 0, len(vr.scores))
	for id := range vr.scores {
		vecSorted = append(vecSorted, idRank{id: id})
	}
	sort.Slice(vecSorted, func(i, j int) bool {
		return vr.scores[vecSorted[i].id] > vr.scores[vecSorted[j].id]
	})
	for i := range vecSorted {
		vecSorted[i].rank = i + 1
	}
	vecRanks := make(map[string]int, len(vecSorted))
	for _, ir := range vecSorted {
		vecRanks[ir.id] = ir.rank
	}

	bm25Sorted := make([]idRank, 0, len(br.scores))
	for id := range br.scores {
		bm25Sorted = append(bm25Sorted, idRank{id: id})
	}
	sort.Slice(bm25Sorted, func(i, j int) bool {
		return br.scores[bm25Sorted[i].id] > br.scores[bm25Sorted[j].id]
	})
	for i := range bm25Sorted {
		bm25Sorted[i].rank = i + 1
	}
	bm25Ranks := make(map[string]int, len(bm25Sorted))
	for _, ir := range bm25Sorted {
		bm25Ranks[ir.id] = ir.rank
	}

	type scoredEntry struct {
		id    string
		score float64
	}
	var entries []scoredEntry
	for id := range allIDs {
		rrf := 0.0
		if vr, ok := vecRanks[id]; ok {
			rrf += 1.0 / (rrfK + float64(vr))
		}
		if br, ok := bm25Ranks[id]; ok {
			rrf += 1.0 / (rrfK + float64(br))
		}
		entries = append(entries, scoredEntry{id: id, score: rrf})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].score > entries[j].score
	})

	if len(entries) > limit {
		entries = entries[:limit]
	}

	results := make([]SearchResult, 0, len(entries))
	for _, e := range entries {
		var r SearchResult
		err := s.db.QueryRow(
			`SELECT id, file_path, rel_path, language, start_line, end_line, content
			 FROM chunks WHERE id = ?`, e.id).Scan(
			&r.ID, &r.FilePath, &r.RelPath, &r.Language,
			&r.StartLine, &r.EndLine, &r.Content)
		if err != nil {
			continue
		}
		r.Score = e.score
		results = append(results, r)
	}

	return results, nil
}

// definitionRE matches common definition line patterns.
var definitionRE = regexp.MustCompile(`(?i)^\s*(func|type|class|struct|interface|enum|trait|impl|def|fn|pub|export|async|function|const|var|let|val)\b`)

// SearchGrep performs substring/regex matching on chunk content.
func (s *Store) SearchGrep(opts GrepOptions) ([]GrepResult, error) {
	if opts.Limit <= 0 || opts.Limit > 50 {
		opts.Limit = 25
	}
	query := opts.Query
	if query == "" {
		return nil, nil
	}

	isRegex := hasRegexMeta(query)

	var sqlQuery string
	var sqlArgs []interface{}

	if !isRegex && !opts.CaseSensitive && len(query) >= 3 {
		ftsQ := sanitizeFTS5(query)
		sqlQuery = `
			SELECT c.id, c.file_path, c.rel_path, c.language,
			       c.start_line, c.end_line, c.content
			FROM chunks c
			WHERE c.rowid IN (
				SELECT rowid FROM chunks_fts WHERE chunks_fts MATCH ?
			)`
		sqlArgs = append(sqlArgs, ftsQ)
	} else if !isRegex {
		if opts.CaseSensitive {
			sqlQuery = `SELECT id, file_path, rel_path, language, start_line, end_line, content
				FROM chunks WHERE content LIKE ?`
			sqlArgs = append(sqlArgs, "%"+query+"%")
		} else {
			sqlQuery = `SELECT id, file_path, rel_path, language, start_line, end_line, content
				FROM chunks WHERE content LIKE ? COLLATE NOCASE`
			sqlArgs = append(sqlArgs, "%"+query+"%")
		}
	} else {
		firstWord := extractFirstWord(query)
		if len(firstWord) >= 3 {
			sqlQuery = `SELECT id, file_path, rel_path, language, start_line, end_line, content
				FROM chunks WHERE content LIKE ? COLLATE NOCASE`
			sqlArgs = append(sqlArgs, "%"+firstWord+"%")
		} else {
			sqlQuery = `SELECT id, file_path, rel_path, language, start_line, end_line, content
				FROM chunks`
		}
	}

	if opts.Language != "" {
		if strings.Contains(sqlQuery, "WHERE") {
			sqlQuery += " AND language = ?"
		} else {
			sqlQuery = strings.Replace(sqlQuery, "FROM chunks",
				"FROM chunks WHERE language = ?", 1)
		}
		sqlArgs = append(sqlArgs, opts.Language)
	}

	rows, err := s.db.Query(sqlQuery, sqlArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type candidate struct {
		id        string
		filePath  string
		relPath   string
		language  string
		startLine int
		endLine   int
		content   string
	}

	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.filePath, &c.relPath, &c.language,
			&c.startLine, &c.endLine, &c.content); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var results []GrepResult
	var re *regexp.Regexp
	if isRegex {
		var compileErr error
		if opts.CaseSensitive {
			re, compileErr = regexp.Compile(query)
		} else {
			re, compileErr = regexp.Compile("(?i)" + query)
		}
		if compileErr != nil {
			return nil, compileErr
		}
	}

	for _, c := range candidates {
		var matches []GrepMatch
		contentLines := strings.Split(c.content, "\n")

		for lineIdx, line := range contentLines {
			matched := false
			if isRegex && re != nil {
				if re.MatchString(line) {
					matched = true
				}
			} else if opts.CaseSensitive {
				if strings.Contains(line, query) {
					matched = true
				}
			} else {
				if strings.Contains(strings.ToLower(line), strings.ToLower(query)) {
					matched = true
				}
			}

			if opts.WholeWord && matched {
				matched = matchWholeWord(line, query, opts.CaseSensitive)
			}

			if matched {
				matches = append(matches, GrepMatch{
					Line:    c.startLine + lineIdx,
					Content: line,
				})
			}
		}

		if len(matches) > 0 {
			score := float64(len(matches))
			for _, m := range matches {
				if definitionRE.MatchString(m.Content) {
					score *= 2.0
					break
				}
			}

			results = append(results, GrepResult{
				ID:        c.id,
				FilePath:  c.filePath,
				RelPath:   c.relPath,
				Language:  c.language,
				StartLine: c.startLine,
				EndLine:   c.endLine,
				Content:   c.content,
				Score:     score,
				Matches:   matches,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	if len(results) > opts.Limit {
		results = results[:opts.Limit]
	}

	if len(results) > 0 && results[0].Score > 0 {
		maxScore := results[0].Score
		for i := range results {
			results[i].Score = results[i].Score / maxScore
		}
	}

	return results, nil
}

func hasRegexMeta(s string) bool {
	for _, c := range s {
		switch c {
		case '.', '*', '+', '?', '|', '(', ')', '[', ']', '{', '}', '^', '$', '\\':
			return true
		}
	}
	return false
}

func extractFirstWord(s string) string {
	var word strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			word.WriteRune(c)
		} else if word.Len() > 0 {
			break
		}
	}
	return word.String()
}

func matchWholeWord(line, query string, caseSensitive bool) bool {
	if !caseSensitive {
		line = strings.ToLower(line)
		query = strings.ToLower(query)
	}
	idx := strings.Index(line, query)
	if idx < 0 {
		return false
	}
	if idx > 0 {
		c := line[idx-1]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			return false
		}
	}
	end := idx + len(query)
	if end < len(line) {
		c := line[end]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			return false
		}
	}
	return true
}

// Ensure regexp is used (for tests)
var _ = regexp.MatchString
