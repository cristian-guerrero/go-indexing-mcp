package structural

import (
	"bufio"
	"os"
	"regexp"
	"sort"
	"strings"
)

type Block struct {
	StartLine int
	EndLine   int
	NodeType  string
}

type blockEndFunc func(lines []string, startPatterns []*regexp.Regexp, start int) int

type languageDef struct {
	StartPatterns []*regexp.Regexp
	FindEnd       blockEndFunc
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
		FindEnd: findBraceEnd,
	},
	"rust": {
		StartPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:pub\s+)?(?:unsafe\s+)?fn\s`),
			regexp.MustCompile(`^\s*(?:pub\s+)?struct\s`),
			regexp.MustCompile(`^\s*(?:pub\s+)?impl\s`),
			regexp.MustCompile(`^\s*(?:pub\s+)?trait\s`),
			regexp.MustCompile(`^\s*(?:pub\s+)?enum\s`),
			regexp.MustCompile(`^\s*(?:pub\s+)?type\s`),
			regexp.MustCompile(`^\s*(?:pub\s+)?const\s`),
			regexp.MustCompile(`^\s*(?:pub\s+)?static\s`),
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

type Splitter struct{}

func New() *Splitter {
	return &Splitter{}
}

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

type ParseFile struct {
	Path     string
	Language string
}

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

		blocks = append(blocks, Block{
			StartLine: startLine + 1,
			EndLine:   endLine + 1,
		})
		i = endLine + 1
	}
	return blocks
}

func matchesAny(line string, patterns []*regexp.Regexp) bool {
	for _, p := range patterns {
		if p.MatchString(line) {
			return true
		}
	}
	return false
}

func findBraceEnd(lines []string, _ []*regexp.Regexp, start int) int {
	return findBraceEndImpl(lines, start, false)
}

func findBraceEndAny(lines []string, _ []*regexp.Regexp, start int) int {
	return findBraceEndImpl(lines, start, true)
}

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

func findSectionEnd(lines []string, startPatterns []*regexp.Regexp, start int) int {
	for i := start + 1; i < len(lines); i++ {
		if matchesAny(lines[i], startPatterns) {
			return i - 1
		}
	}
	return len(lines) - 1
}

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

func SupportedLanguages() []string {
	var langs []string
	for l := range languages {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	return langs
}

func IsLineStructuralStart(line, language string) bool {
	def, ok := languages[language]
	if !ok {
		return false
	}
	return matchesAny(line, def.StartPatterns)
}

func (s *Splitter) HasTreeSitter() bool { return false }
