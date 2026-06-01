package db

import (
	"testing"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/chunker"
)

func TestSanitizeFTS5_QuotedString(t *testing.T) {
	got := sanitizeFTS5(`"exact phrase"`)
	if got != `"exact phrase"` {
		t.Fatalf("expected exact phrase, got %s", got)
	}
}

func TestSanitizeFTS5_EmptyString(t *testing.T) {
	if got := sanitizeFTS5(""); got != "" {
		t.Fatalf("expected empty, got %s", got)
	}
}

func TestSanitizeFTS5_NormalQuery(t *testing.T) {
	got := sanitizeFTS5("hello world")
	expected := "hello* world*"
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestSanitizeFTS5_WithDoubleQuotes(t *testing.T) {
	got := sanitizeFTS5(`say "hello"`)
	// The inner quotes get escaped: say* ""hello""*
	// Since tokens are split by whitespace, each token gets * suffix
	if got == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestHasRegexMeta_ReturnsTrueForRegexChars(t *testing.T) {
	cases := []string{".", "*", "+", "?", "|", "(", ")", "[", "]", "{", "}", "^", "$", "\\", "foo.*bar", "a|b", "^start"}
	for _, c := range cases {
		if !hasRegexMeta(c) {
			t.Errorf("expected true for %q", c)
		}
	}
}

func TestHasRegexMeta_ReturnsFalseForPlainText(t *testing.T) {
	cases := []string{"hello", "foo bar", "a_b", "123"}
	for _, c := range cases {
		if hasRegexMeta(c) {
			t.Errorf("expected false for %q", c)
		}
	}
}

func TestMatchWholeWord_MiddleOfWord(t *testing.T) {
	if matchWholeWord("getter", "get", false) {
		t.Fatal("'get' should not match inside 'getter'")
	}
}

func TestMatchWholeWord_ExactWord(t *testing.T) {
	if !matchWholeWord("get value", "get", false) {
		t.Fatal("'get' should match in 'get value'")
	}
}

func TestMatchWholeWord_EndOfString(t *testing.T) {
	if !matchWholeWord("call get", "get", false) {
		t.Fatal("'get' should match at end")
	}
}

func TestMatchWholeWord_StartOfString(t *testing.T) {
	if !matchWholeWord("get", "get", false) {
		t.Fatal("'get' should match exact string")
	}
}

func TestMatchWholeWord_CaseSensitive(t *testing.T) {
	if matchWholeWord("Get value", "get", true) {
		t.Fatal("'Get' should not match 'get' case-sensitive")
	}
}

func TestMatchWholeWord_CaseInsensitive(t *testing.T) {
	if !matchWholeWord("Get value", "get", false) {
		t.Fatal("'Get' should match 'get' case-insensitive")
	}
}

func TestExtractFirstWord(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello world", "hello"},
		{"  hello", "hello"},
		{"hello_world.foo", "hello_world"},
		{"123abc", "123abc"},
		{"!hello", "hello"},
		{"!!!", ""},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range tests {
		got := extractFirstWord(tc.input)
		if got != tc.expected {
			t.Errorf("extractFirstWord(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestSearch_EmptyDB(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	results, err := s.Search([]float32{0.5, 0.5, 0.5, 0.5}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatal("expected no results from empty db")
	}
}

func TestSearch_ReturnsResults(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		{ID: "c1", FilePath: "/p/main.go", RelPath: "main.go", Content: "hello world"},
		{ID: "c2", FilePath: "/p/other.go", RelPath: "other.go", Content: "foo bar"},
	}
	emb := map[string][]float32{
		"c1": {0.9, 0.1, 0.0, 0.0},
		"c2": {0.1, 0.9, 0.0, 0.0},
	}
	if err := s.UpsertChunks(chunks, emb); err != nil {
		t.Fatal(err)
	}

	results, err := s.Search([]float32{0.9, 0.1, 0.0, 0.0}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if results[0].ID != "c1" {
		t.Fatalf("expected c1 first, got %s", results[0].ID)
	}
}

func TestSearch_RespectsLimit(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		{ID: "c1", FilePath: "/p/a.go", RelPath: "a.go", Content: "a"},
		{ID: "c2", FilePath: "/p/b.go", RelPath: "b.go", Content: "b"},
		{ID: "c3", FilePath: "/p/c.go", RelPath: "c.go", Content: "c"},
	}
	emb := map[string][]float32{
		"c1": {0.9, 0.1}, "c2": {0.5, 0.5}, "c3": {0.1, 0.9},
	}
	s.UpsertChunks(chunks, emb)

	results, err := s.Search([]float32{0.9, 0.1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) > 2 {
		t.Fatalf("expected at most 2, got %d", len(results))
	}
}

func TestSearchHybrid_EmptyDB(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	results, err := s.SearchHybrid([]float32{1, 0, 0, 0}, "hello", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatal("expected no results")
	}
}

func TestSearchHybrid_ReturnsResults(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		{ID: "c1", FilePath: "/p/main.go", RelPath: "main.go", Content: "hello world"},
		{ID: "c2", FilePath: "/p/other.go", RelPath: "other.go", Content: "goodbye world"},
	}
	emb := map[string][]float32{
		"c1": {0.9, 0.1, 0.0, 0.0},
		"c2": {0.1, 0.9, 0.0, 0.0},
	}
	s.UpsertChunks(chunks, emb)

	results, err := s.SearchHybrid([]float32{0.9, 0.1, 0.0, 0.0}, "hello", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
}

func TestSearchGrep_BasicLiteral(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ch := chunker.Chunk{
		ID: "c1", FilePath: "/p/main.go", RelPath: "main.go",
		Language: "go", Content: "func main() {\n\tfmt.Println(\"hello\")\n}",
	}
	s.UpsertChunks([]chunker.Chunk{ch}, map[string][]float32{"c1": {1, 0, 0, 0}})

	results, err := s.SearchGrep(GrepOptions{Query: "main", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected grep results")
	}
}

func TestSearchGrep_CaseSensitive(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ch := chunker.Chunk{
		ID: "c1", FilePath: "/p/main.go", RelPath: "main.go",
		Content: "Hello World",
	}
	s.UpsertChunks([]chunker.Chunk{ch}, map[string][]float32{"c1": {1, 0, 0, 0}})

	results, err := s.SearchGrep(GrepOptions{Query: "hello", CaseSensitive: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatal("expected no results for case-sensitive mismatch")
	}

	results, err = s.SearchGrep(GrepOptions{Query: "Hello", CaseSensitive: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for exact case match")
	}
}

func TestSearchGrep_WholeWord(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ch := chunker.Chunk{
		ID: "c1", FilePath: "/p/main.go", RelPath: "main.go",
		Content: "getter value",
	}
	s.UpsertChunks([]chunker.Chunk{ch}, map[string][]float32{"c1": {1, 0, 0, 0}})

	results, err := s.SearchGrep(GrepOptions{Query: "get", WholeWord: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatal("expected no whole-word match inside 'getter'")
	}

	ch2 := chunker.Chunk{
		ID: "c2", FilePath: "/p/other.go", RelPath: "other.go",
		Content: "get value",
	}
	s.UpsertChunks([]chunker.Chunk{ch2}, map[string][]float32{"c2": {0, 1, 0, 0}})

	results, err = s.SearchGrep(GrepOptions{Query: "get", WholeWord: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected whole-word match for 'get value'")
	}
}

func TestSearchGrep_RegexQuery(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ch := chunker.Chunk{
		ID: "c1", FilePath: "/p/main.go", RelPath: "main.go",
		Content: "func foo()\nfunc bar()",
	}
	s.UpsertChunks([]chunker.Chunk{ch}, map[string][]float32{"c1": {1, 0, 0, 0}})

	results, err := s.SearchGrep(GrepOptions{Query: "func.*foo", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected regex match")
	}
	if len(results[0].Matches) == 0 {
		t.Fatal("expected at least one match line")
	}
}

func TestSearchGrep_LanguageFilter(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunks := []chunker.Chunk{
		{ID: "c1", FilePath: "/p/main.go", RelPath: "main.go", Language: "go", Content: "hello"},
		{ID: "c2", FilePath: "/p/main.py", RelPath: "main.py", Language: "python", Content: "hello"},
	}
	emb := map[string][]float32{"c1": {1, 0, 0, 0}, "c2": {0, 1, 0, 0}}
	s.UpsertChunks(chunks, emb)

	results, err := s.SearchGrep(GrepOptions{Query: "hello", Language: "go", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Language != "go" {
		t.Fatalf("expected 1 go result, got %d", len(results))
	}
}

func TestSearchGrep_EmptyQuery(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	results, err := s.SearchGrep(GrepOptions{Query: "", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatal("expected no results for empty query")
	}
}

func TestSearchGrep_DefaultLimit(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/test.sqlite", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i := 0; i < 30; i++ {
		id := string(rune('a' + i))
		ch := chunker.Chunk{ID: id, FilePath: "/p/" + id + ".go", RelPath: id + ".go", Content: "content"}
		s.UpsertChunks([]chunker.Chunk{ch}, map[string][]float32{id: {1, 0, 0, 0}})
	}

	results, err := s.SearchGrep(GrepOptions{Query: "content"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) > 25 {
		t.Fatalf("expected at most 25 (default limit), got %d", len(results))
	}
}
