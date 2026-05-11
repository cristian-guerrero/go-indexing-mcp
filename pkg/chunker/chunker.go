package chunker

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/cristian/go-indexing-mcp/pkg/treeparse"
	"github.com/cristian/go-indexing-mcp/pkg/walker"
)

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

type Stats struct {
	TreeSitterFiles int
	SlidingWinFiles int
	TreeSitterChunks int
	SlidingWinChunks int
}

type Chunker struct {
	ChunkSize    int
	ChunkOverlap int
	treeParser   *treeparse.Parser
	stats        Stats
}

func New(chunkSize, chunkOverlap int, treeBinPath string) *Chunker {
	if chunkSize <= 0 {
		chunkSize = 50
	}
	if chunkOverlap < 0 {
		chunkOverlap = 0
	}
	if chunkOverlap >= chunkSize {
		chunkOverlap = chunkSize / 4
	}
	var tp *treeparse.Parser
	if treeBinPath != "" {
		tp = treeparse.New(treeBinPath)
	}
	return &Chunker{
		ChunkSize:    chunkSize,
		ChunkOverlap: chunkOverlap,
		treeParser:   tp,
	}
}

func (c *Chunker) Stats() Stats {
	return c.stats
}

func (c *Chunker) ChunkFile(fi walker.FileInfo) ([]Chunk, error) {
	lines, err := readFileLines(fi.Path)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, nil
	}

	totalLines := len(lines)

	if totalLines <= c.ChunkSize || c.treeParser == nil {
		return c.slidingWindow(lines, 0, totalLines, fi), nil
	}

	blocks, err := c.treeParser.ParseBlocks(fi.Path, fi.Language)
	if err != nil || len(blocks) == 0 {
		return c.slidingWindow(lines, 0, totalLines, fi), nil
	}

	return c.structuralSplit(lines, blocks, fi), nil
}

func (c *Chunker) ChunkFiles(files []walker.FileInfo) (map[string][]Chunk, error) {
	c.stats = Stats{}
	results := make(map[string][]Chunk, len(files))

	var batchFiles []walker.FileInfo
	var batchLines [][]string

	for _, fi := range files {
		lines, err := readFileLines(fi.Path)
		if err != nil {
			continue
		}
		if len(lines) == 0 {
			continue
		}

		if len(lines) <= c.ChunkSize || c.treeParser == nil {
			chunks := c.slidingWindow(lines, 0, len(lines), fi)
			results[fi.Path] = chunks
			c.stats.SlidingWinFiles++
			c.stats.SlidingWinChunks += len(chunks)
		} else {
			batchFiles = append(batchFiles, fi)
			batchLines = append(batchLines, lines)
		}
	}

	if len(batchFiles) > 0 && c.treeParser != nil {
		parseFiles := make([]treeparse.ParseFile, len(batchFiles))
		for i, fi := range batchFiles {
			parseFiles[i] = treeparse.ParseFile{Path: fi.Path, Language: fi.Language}
		}

		blocksMap, err := c.treeParser.ParseBlocksBatch(parseFiles)
		if err != nil {
			for i, fi := range batchFiles {
				chunks := c.slidingWindow(batchLines[i], 0, len(batchLines[i]), fi)
				results[fi.Path] = chunks
				c.stats.SlidingWinFiles++
				c.stats.SlidingWinChunks += len(chunks)
			}
		} else {
			for i, fi := range batchFiles {
				blocks := blocksMap[fi.Path]
				if len(blocks) == 0 {
					chunks := c.slidingWindow(batchLines[i], 0, len(batchLines[i]), fi)
					results[fi.Path] = chunks
					c.stats.SlidingWinFiles++
					c.stats.SlidingWinChunks += len(chunks)
				} else {
					chunks := c.structuralSplit(batchLines[i], blocks, fi)
					results[fi.Path] = chunks
					c.stats.TreeSitterFiles++
					c.stats.TreeSitterChunks += len(chunks)
				}
			}
		}
	}

	return results, nil
}

func readFileLines(path string) ([]string, error) {
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
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func (c *Chunker) structuralSplit(lines []string, blocks []treeparse.Block, fi walker.FileInfo) []Chunk {
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

func relPath(fi walker.FileInfo) string {
	if fi.RelPath != "" {
		return fi.RelPath
	}
	return fi.Path
}

func chunkID(fileHash, relPath string, start, end int) string {
	base := filepath.ToSlash(relPath)
	return fileHash + ":" + base + ":" + itoa(start) + "-" + itoa(end)
}

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
