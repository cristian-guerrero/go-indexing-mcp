// Package structural detects structural code blocks (functions, classes, methods)
// per language using regex pattern matching and brace/indent counting.
// Supports 17 languages with decorator/annotation backward scanning.
// No external parser dependencies — pure regex-based structural analysis.
package structural

import (
	"bufio"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Block represents a detected structural code block with its line range.
// Lines are 1-indexed. HasDecorators indicates preceding decorators were found.
type Block struct {
	StartLine    int
	EndLine      int
	NodeType     string
	HasDecorators bool
}

// blockEndFunc is a function that finds the end line of a structural block
// given the lines array, start patterns, and the starting line index.
type blockEndFunc func(lines []string, startPatterns []*regexp.Regexp, start int) int

// languageDef defines how to detect blocks for a specific language:
// start patterns match the opening line, decorator patterns capture annotations,
// and FindEnd locates the closing line via brace depth or indentation.
type languageDef struct {
	StartPatterns     []*regexp.Regexp
	DecoratorPatterns []*regexp.Regexp
	FindEnd           blockEndFunc
}

var (
	reJSONKey   = regexp.MustCompile(`^\s*"\w+"\s*:\s*[{[]`)
	reTOMLSect  = regexp.MustCompile(`^\s*\[{1,2}[\w."]+\]{1,2}\s*$`)
	reMDHeading = regexp.MustCompile(`^#{1,6}\s+`)
)

var languages = map[string]languageDef{
	"go": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*func\s`),
			regexp.MustCompile(`^\s*type\s`),
		},
		FindEnd: findBraceEnd,
	},
	"python": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:async\s+)?def\s`),
			regexp.MustCompile(`^\s*class\s`),
		},
		DecoratorPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*@`),
		},
		FindEnd: findIndentEnd,
	},
	"javascript": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?function\s`),
			regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?class\s`),
			regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+\w+\s*=\s*(?:async\s*)?\(`),
			regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+\w+\s*=\s*\w+\s*=>`),
			regexp.MustCompile(`^\s*(?:export\s+)?interface\s`),
			regexp.MustCompile(`^\s*(?:export\s+)?type\s`),
		},
		DecoratorPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*@`),
		},
		FindEnd: findBraceEnd,
	},
	"rust": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:pub\s+)?(?:async\s+)?(?:unsafe\s+)?fn\s`),
			regexp.MustCompile(`^\s*(?:pub\s+)?struct\s`),
			regexp.MustCompile(`^\s*(?:pub\s+)?impl\s`),
			regexp.MustCompile(`^\s*(?:pub\s+)?trait\s`),
			regexp.MustCompile(`^\s*(?:pub\s+)?enum\s`),
			regexp.MustCompile(`^\s*(?:pub\s+)?type\s`),
			regexp.MustCompile(`^\s*(?:pub\s+)?const\s`),
			regexp.MustCompile(`^\s*(?:pub\s+)?static\s`),
		},
		DecoratorPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*#\[`),
		},
		FindEnd: findBraceEnd,
	},
	"java": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|final|abstract|synchronized)\s+)*class\s`),
			regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|final|abstract|synchronized)\s+)*interface\s`),
			regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|final|abstract|synchronized)\s+)*enum\s`),
			regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|final|abstract|synchronized)\s+)*record\s`),
			regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|final|abstract|synchronized)\s+)*(?:<\w+>)?\s*\w+\s*\(`),
			regexp.MustCompile(`^\s*(?:public|private|protected)\s+.*\{$`),
		},
		DecoratorPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*@`),
		},
		FindEnd: findBraceEnd,
	},
	"c": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:static\s+)?(?:inline\s+)?(?:\w+\s+)+\w+\s*\(`),
			regexp.MustCompile(`^\s*(?:typedef\s+)?(?:struct|union|enum)\s`),
		},
		FindEnd: findBraceEnd,
	},
	"cpp": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:virtual\s+)?(?:static\s+)?(?:inline\s+)?(?:\w+\s+)+\w+\s*\(`),
			regexp.MustCompile(`^\s*(?:class|struct|union|namespace|enum)\s`),
			regexp.MustCompile(`^\s*template\s*<`),
		},
		FindEnd: findBraceEnd,
	},
	"csharp": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|virtual|override|abstract|sealed|async)\s+)*(?:class|struct|interface|enum|record)\s`),
			regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|virtual|override|abstract|sealed|async)\s+)*(?:void|\w+)\s+\w+\s*\(`),
			regexp.MustCompile(`^\s*namespace\s`),
		},
		DecoratorPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*\[`),
		},
		FindEnd: findBraceEnd,
	},
	"ruby": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:def|class|module)\s`),
		},
		FindEnd: findIndentEnd,
	},
	"php": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:public|private|protected|static|final|abstract)?\s*function\s`),
			regexp.MustCompile(`^\s*(?:abstract\s+)?(?:class|interface|trait|enum)\s`),
		},
		DecoratorPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*#\[`),
		},
		FindEnd: findBraceEnd,
	},
	"swift": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:public|private|internal|fileprivate|static|class|override|mutating|nonmutating)?\s*func\s`),
			regexp.MustCompile(`^\s*(?:public|private|internal|fileprivate)?\s*(?:class|struct|enum|protocol|extension)\s`),
		},
		FindEnd: findBraceEnd,
	},
	"kotlin": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:public|private|internal|protected)?\s*(?:fun|class|interface|object)\s`),
		},
		DecoratorPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*@`),
		},
		FindEnd: findBraceEnd,
	},
	"scala": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:def|class|trait|object|enum)\s`),
		},
		FindEnd: findBraceEnd,
	},
	"bash": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*function\s+\w+`),
			regexp.MustCompile(`^\s*\w+\s*\(\s*\)\s*\{`),
		},
		FindEnd: findBraceEnd,
	},
	"json": {
		StartPatterns: []*regexp.Regexp{reJSONKey},
		FindEnd:       findBraceEndAny,
	},
	"yaml": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*\w[\w./-]*\s*:\s*(?:#.*)?$`),
		},
		FindEnd: findIndentEnd,
	},
	"toml": {
		StartPatterns: []*regexp.Regexp{reTOMLSect},
		FindEnd:       findSectionEnd,
	},
	"markdown": {
		StartPatterns: []*regexp.Regexp{reMDHeading},
		FindEnd:       findSectionEnd,
	},
}

// Splitter parses source files into structural blocks per language.
type Splitter struct{}

// New creates a new structural Splitter.
func New() *Splitter {
	return &Splitter{}
}

// ParseBlocks detects structural blocks in a single file by language.
// Returns nil if the language is not supported or no blocks are found.
func (s *Splitter) ParseBlocks(filePath, language string) ([]Block, error) {
	def, ok := languages[language]
	if !ok {
		return nil, nil
	}

	lines, err := readLines(filePath)
	if err != nil {
		return nil, err
	}

	return findBlocks(lines, def), nil
}

// ParseBlocksBatch detects structural blocks for multiple files in one call.
func (s *Splitter) ParseBlocksBatch(files []ParseFile) (map[string][]Block, error) {
	results := make(map[string][]Block, len(files))
	for _, f := range files {
		def, ok := languages[f.Language]
		if !ok {
			continue
		}
		lines, err := readLines(f.Path)
		if err != nil {
			continue
		}
		blocks := findBlocks(lines, def)
		if len(blocks) > 0 {
			results[f.Path] = blocks
		}
	}
	return results, nil
}

// ParseFile represents a file to be parsed with its detected language.
type ParseFile struct {
	Path     string
	Language string
}

// readLines reads all lines from a file into a string slice.
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// findBlocks scans lines for structural start patterns, finds each block's end,
// and collects decorators preceding each block. Returns all detected blocks.
func findBlocks(lines []string, def languageDef) []Block {
	var blocks []Block
	i := 0
	for i < len(lines) {
		line := lines[i]
		if !matchesAny(line, def.StartPatterns) {
			i++
			continue
		}

		startLine := i
		endLine := def.FindEnd(lines, def.StartPatterns, i)

		origStart := startLine
		startLine = collectDecorators(lines, startLine, def.DecoratorPatterns)

		blocks = append(blocks, Block{
			StartLine:     startLine + 1,
			EndLine:       endLine + 1,
			HasDecorators: startLine < origStart,
		})
		i = endLine + 1
	}
	return blocks
}

// collectDecorators scans backward from start to find decorator/annotation lines
// (e.g., @Decorator, [Attribute], #[Attribute]). Blank lines between decorators
// are skipped. Non-decorator lines stop the backward scan to avoid false positives.
func collectDecorators(lines []string, start int, patterns []*regexp.Regexp) int {
	if len(patterns) == 0 || start <= 0 {
		return start
	}

	for i := start - 1; i >= 0; i-- {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			continue
		}

		if matchesAny(line, patterns) {
			start = i
			continue
		}

		break
	}

	return start
}

// matchesAny checks if line matches any of the given regex patterns.
func matchesAny(line string, patterns []*regexp.Regexp) bool {
	for _, p := range patterns {
		if p.MatchString(line) {
			return true
		}
	}
	return false
}

// findBraceEnd finds the matching closing brace for a block opening at start.
// Tracks string literals, line comments (//), and block comments (/* */) to avoid false matches.
func findBraceEnd(lines []string, _ []*regexp.Regexp, start int) int {
	return findBraceEndImpl(lines, start, false)
}

// findBraceEndAny is like findBraceEnd but also tracks '[' and ']' brackets (used for JSON).
func findBraceEndAny(lines []string, _ []*regexp.Regexp, start int) int {
	return findBraceEndImpl(lines, start, true)
}

// findBraceEndImpl implements brace depth counting with string literal and comment awareness.
// Returns the line index (0-based) of the closing brace that brings depth back to 0.
func findBraceEndImpl(lines []string, start int, countBrackets bool) int {
	depth := 0
	inString := false
	stringChar := byte(0)
	inLineComment := false

	for i := start; i < len(lines); i++ {
		line := lines[i]
		inLineComment = false

		for j := 0; j < len(line); j++ {
			ch := line[j]

			if inLineComment {
				break
			}

			if inString {
				if ch == '\\' && j+1 < len(line) {
					j++
					continue
				}
				if ch == stringChar {
					inString = false
				}
				continue
			}

			if ch == '"' || ch == '\'' || ch == '`' {
				inString = true
				stringChar = ch
				continue
			}

			if ch == '/' && j+1 < len(line) {
				if line[j+1] == '/' {
					break
				}
				if line[j+1] == '*' {
					j += 2
					for j < len(line) {
						if line[j] == '*' && j+1 < len(line) && line[j+1] == '/' {
							j++
							break
						}
						j++
					}
					continue
				}
			}

			if ch == '{' {
				depth++
			} else if ch == '}' {
				depth--
				if depth == 0 {
					return i
				}
			} else if countBrackets && ch == '[' {
				depth++
			} else if countBrackets && ch == ']' {
				depth--
				if depth == 0 {
					return i
				}
			}
		}
	}

	return len(lines) - 1
}

// findIndentEnd finds the end of an indentation-based block (Python, Ruby, YAML).
// Returns the last line whose indentation is greater than the block's start line.
func findIndentEnd(lines []string, _ []*regexp.Regexp, start int) int {
	indent := countIndent(lines[start])
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if countIndent(line) <= indent {
			return i - 1
		}
	}
	return len(lines) - 1
}

// findSectionEnd finds the end of a section-based block (TOML, Markdown).
// The block ends when a line matches one of the start patterns (next section heading).
func findSectionEnd(lines []string, startPatterns []*regexp.Regexp, start int) int {
	for i := start + 1; i < len(lines); i++ {
		if matchesAny(lines[i], startPatterns) {
			return i - 1
		}
	}
	return len(lines) - 1
}

// countIndent measures the indentation level of a line (spaces or tabs).
// Each tab counts as 4 spaces for indentation comparison.
func countIndent(line string) int {
	count := 0
	for _, ch := range line {
		if ch == ' ' {
			count++
		} else if ch == '\t' {
			count += 4
		} else {
			break
		}
	}
	return count
}

// SupportedLanguages returns the list of languages with structural block detection.
func SupportedLanguages() []string {
	var langs []string
	for l := range languages {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	return langs
}

// IsLineStructuralStart checks if a given line is a structural start pattern for a language.
func IsLineStructuralStart(line, language string) bool {
	def, ok := languages[language]
	if !ok {
		return false
	}
	return matchesAny(line, def.StartPatterns)
}

// HasTreeSitter returns false — this package uses regex-based parsing, not Tree-sitter.
func (s *Splitter) HasTreeSitter() bool { return false }
