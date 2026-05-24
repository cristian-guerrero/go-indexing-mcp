// Package chunker splits source files into discrete chunks for embedding and indexing.
// It uses a dual strategy: large files are split by structural blocks (functions, classes),
// while small files or files without detectable structural blocks use a sliding window approach.
package chunker

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/parser"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/structural"
	"github.com/cristian-guerrero/go-indexing-mcp/pkg/walker"
)

// Chunk represents a single text segment from a source file, tagged with metadata
// for search retrieval: file path, line range, language, content hash, and a unique ID.
type Chunk struct {
	ID        string
	FilePath  string
	RelPath   string
	Language  string
	StartLine int
	EndLine   int
	Content   string
	FileHash  string
}

// Stats tracks how many files and chunks were produced by each splitting strategy.
type Stats struct {
	TreeSitterFiles int
	SlidingWinFiles int
	TreeSitterChunks int
	SlidingWinChunks int
}

// Chunker splits source files into chunks using structural blocks or sliding windows.
type Chunker struct {
	ChunkSize      int
	ChunkOverlap   int
	MinASTLines    int             // files below this skip AST (0 = always use AST)
	MaxASTLines    int             // files above this skip AST (0 = no limit); tree-sitter is slow for large files
	structSplitter *structural.Splitter
	Parser         parser.Parser   // optional AST parser (tree-sitter); nil = regex only
	stats          Stats
}

// New creates a Chunker with the given line-based chunk size and overlap.
// If ChunkOverlap >= ChunkSize, it defaults to ChunkSize/4.
func New(chunkSize, chunkOverlap int) *Chunker {
	if chunkSize <= 0 {
		chunkSize = 50
	}
	if chunkOverlap < 0 {
		chunkOverlap = 0
	}
	if chunkOverlap >= chunkSize {
		chunkOverlap = chunkSize / 4
	}
	return &Chunker{
		ChunkSize:      chunkSize,
		ChunkOverlap:   chunkOverlap,
		MaxASTLines:    2000, // skip tree-sitter for files above 2000 lines (too slow)
		structSplitter: structural.New(),
	}
}

// Stats returns cumulative chunking statistics since the last reset.
func (c *Chunker) Stats() Stats {
	return c.stats
}

// HasStructuralSplit returns true, indicating this chunker supports structural splitting.
func (c *Chunker) HasStructuralSplit() bool {
	return true
}

// ChunkFile splits a single file into chunks. Small files (<= ChunkSize lines) use
// sliding window; larger files attempt AST parsing first (tree-sitter), then regex
// structural, falling back to sliding window.
func (c *Chunker) ChunkFile(fi walker.FileInfo) ([]Chunk, error) {
	lines, err := readFileLines(fi.Path)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, nil
	}

	totalLines := len(lines)

	if totalLines <= c.ChunkSize {
		return c.slidingWindow(lines, 0, totalLines, fi), nil
	}

	// Skip AST for small files where regex structural is fast enough
	if c.Parser != nil && c.MinASTLines > 0 && totalLines < c.MinASTLines {
		return c.doStructuralSplit(lines, fi)
	}

	// Skip AST for very large files where tree-sitter is prohibitively slow
	if c.Parser != nil && c.MaxASTLines > 0 && totalLines > c.MaxASTLines {
		return c.doStructuralSplit(lines, fi)
	}

	// Strategy 1: AST parser (tree-sitter)
	var blocks []structural.Block
	if c.Parser != nil {
		content := strings.Join(lines, "\n")
		parserBlocks, pErr := c.Parser.Parse(content, fi.Language)
		if pErr == nil && len(parserBlocks) > 0 {
			blocks = make([]structural.Block, len(parserBlocks))
			for i, b := range parserBlocks {
				blocks[i] = structural.Block{
					StartLine: b.StartLine,
					EndLine:   b.EndLine,
				}
			}
		}
	}

	// Strategy 2: Regex structural (fallback)
	if len(blocks) == 0 {
		var sErr error
		blocks, sErr = c.structSplitter.ParseBlocks(fi.Path, fi.Language)
		if sErr != nil {
			blocks = nil
		}
	}

	if len(blocks) == 0 {
		return c.slidingWindow(lines, 0, totalLines, fi), nil
	}

	return c.structuralSplit(lines, blocks, fi), nil
}

// doStructuralSplit is a shortcut for structural-only splitting (no AST).
func (c *Chunker) doStructuralSplit(lines []string, fi walker.FileInfo) ([]Chunk, error) {
	blocks, err := c.structSplitter.ParseBlocks(fi.Path, fi.Language)
	if err != nil || len(blocks) == 0 {
		chunks := c.slidingWindow(lines, 0, len(lines), fi)
		c.stats.SlidingWinFiles++
		c.stats.SlidingWinChunks += len(chunks)
		return chunks, nil
	}
	chunks := c.structuralSplit(lines, blocks, fi)
	c.stats.TreeSitterFiles++
	c.stats.TreeSitterChunks += len(chunks)
	return chunks, nil
}

// ChunkFiles processes a batch of files, splitting each into chunks.
// Tracks statistics (structural vs sliding window) on the Chunker.
func (c *Chunker) ChunkFiles(files []walker.FileInfo) (map[string][]Chunk, error) {
	c.stats = Stats{}
	results := make(map[string][]Chunk, len(files))

	for _, fi := range files {
		lines, err := readFileLines(fi.Path)
		if err != nil {
			continue
		}
		if len(lines) == 0 {
			continue
		}

		if len(lines) <= c.ChunkSize {
			chunks := c.slidingWindow(lines, 0, len(lines), fi)
			results[fi.Path] = chunks
			c.stats.SlidingWinFiles++
			c.stats.SlidingWinChunks += len(chunks)
			continue
		}

		// Skip AST for small files where regex structural is fast enough
		if c.Parser != nil && c.MinASTLines > 0 && len(lines) < c.MinASTLines {
			chunks, _ := c.doStructuralSplit(lines, fi)
			results[fi.Path] = chunks
			continue
		}

		// Skip AST for very large files where tree-sitter is prohibitively slow
		if c.Parser != nil && c.MaxASTLines > 0 && len(lines) > c.MaxASTLines {
			chunks, _ := c.doStructuralSplit(lines, fi)
			results[fi.Path] = chunks
			continue
		}

		// Strategy 1: AST parser (tree-sitter)
		var blocks []structural.Block
		if c.Parser != nil {
			content := strings.Join(lines, "\n")
			parserBlocks, pErr := c.Parser.Parse(content, fi.Language)
			if pErr == nil && len(parserBlocks) > 0 {
				blocks = make([]structural.Block, len(parserBlocks))
				for i, b := range parserBlocks {
					blocks[i] = structural.Block{
						StartLine: b.StartLine,
						EndLine:   b.EndLine,
					}
				}
			}
		}

		// Strategy 2: Regex structural (fallback)
		if len(blocks) == 0 {
			var sErr error
			blocks, sErr = c.structSplitter.ParseBlocks(fi.Path, fi.Language)
			if sErr != nil {
				blocks = nil
			}
		}

		if len(blocks) == 0 {
			chunks := c.slidingWindow(lines, 0, len(lines), fi)
			results[fi.Path] = chunks
			c.stats.SlidingWinFiles++
			c.stats.SlidingWinChunks += len(chunks)
			continue
		}

		chunks := c.structuralSplit(lines, blocks, fi)
		results[fi.Path] = chunks
		c.stats.TreeSitterFiles++
		c.stats.TreeSitterChunks += len(chunks)
	}

	return results, nil
}

// readFileLines reads a text file and returns its lines as a string slice.
func readFileLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer for long lines (e.g. base64 data)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// structuralSplit iterates through structural blocks, emitting each block as a chunk
// (if it fits within ChunkSize) or splitting it further via sliding window.
// Lines between blocks are chunked with sliding window as interstitial segments.
func (c *Chunker) structuralSplit(lines []string, blocks []structural.Block, fi walker.FileInfo) []Chunk {
	totalLines := len(lines)
	var chunks []Chunk
	currentLine := 0

	for _, block := range blocks {
		if block.StartLine < 1 {
			continue
		}

		blockStart := block.StartLine - 1
		blockEnd := block.EndLine
		if blockEnd > totalLines {
			blockEnd = totalLines
		}

		if blockStart < currentLine {
			continue
		}

		if blockStart > currentLine {
			sub := c.slidingWindow(lines, currentLine, blockStart, fi)
			chunks = append(chunks, sub...)
		}

		blockSize := blockEnd - blockStart
		if blockSize <= c.ChunkSize {
			rel := relPath(fi)
			chunks = append(chunks, Chunk{
				ID:        chunkID(fi.Hash, rel, blockStart+1, blockEnd),
				FilePath:  fi.Path,
				RelPath:   rel,
				Language:  fi.Language,
				StartLine: blockStart + 1,
				EndLine:   blockEnd,
				Content:   strings.Join(lines[blockStart:blockEnd], "\n"),
				FileHash:  fi.Hash,
			})
		} else {
			sub := c.slidingWindow(lines, blockStart, blockEnd, fi)
			chunks = append(chunks, sub...)
		}

		currentLine = blockEnd
	}

	if currentLine < totalLines {
		sub := c.slidingWindow(lines, currentLine, totalLines, fi)
		chunks = append(chunks, sub...)
	}

	return chunks
}

// slidingWindow splits line range [start, end) into fixed-size chunks with overlap.
// If the range fits within ChunkSize, a single chunk is emitted.
func (c *Chunker) slidingWindow(lines []string, start, end int, fi walker.FileInfo) []Chunk {
	if end-start <= c.ChunkSize {
		rel := relPath(fi)
		return []Chunk{{
			ID:        chunkID(fi.Hash, rel, start+1, end),
			FilePath:  fi.Path,
			RelPath:   rel,
			Language:  fi.Language,
			StartLine: start + 1,
			EndLine:   end,
			Content:   strings.Join(lines[start:end], "\n"),
			FileHash:  fi.Hash,
		}}
	}

	var chunks []Chunk
	step := c.ChunkSize - c.ChunkOverlap
	if step <= 0 {
		step = 1
	}

	for pos := start; pos < end; pos += step {
		chunkEnd := pos + c.ChunkSize
		if chunkEnd > end {
			chunkEnd = end
		}

		rel := relPath(fi)
		chunks = append(chunks, Chunk{
			ID:        chunkID(fi.Hash, rel, pos+1, chunkEnd),
			FilePath:  fi.Path,
			RelPath:   rel,
			Language:  fi.Language,
			StartLine: pos + 1,
			EndLine:   chunkEnd,
			Content:   strings.Join(lines[pos:chunkEnd], "\n"),
			FileHash:  fi.Hash,
		})

		if chunkEnd == end {
			break
		}
	}

	return chunks
}

// relPath returns RelPath if set, falling back to the absolute Path.
func relPath(fi walker.FileInfo) string {
	if fi.RelPath != "" {
		return fi.RelPath
	}
	return fi.Path
}

// chunkID builds a deterministic unique chunk identifier from file hash,
// relative path, and line range. Format: hash:path:start-end.
func chunkID(fileHash, relPath string, start, end int) string {
	base := filepath.ToSlash(relPath)
	return fileHash + ":" + base + ":" + itoa(start) + "-" + itoa(end)
}

// itoa converts an integer to its decimal string representation without allocation.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
