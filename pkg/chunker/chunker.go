package chunker

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

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

type Chunker struct {
	ChunkSize    int
	ChunkOverlap int
}

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
		ChunkSize:    chunkSize,
		ChunkOverlap: chunkOverlap,
	}
}

func (c *Chunker) ChunkFile(fi walker.FileInfo) ([]Chunk, error) {
	f, err := os.Open(fi.Path)
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

	if len(lines) == 0 {
		return nil, nil
	}

	var chunks []Chunk
	step := c.ChunkSize - c.ChunkOverlap
	if step <= 0 {
		step = 1
	}

	for start := 0; start < len(lines); start += step {
		end := start + c.ChunkSize
		if end > len(lines) {
			end = len(lines)
		}

		content := strings.Join(lines[start:end], "\n")
		rel := fi.RelPath
		if rel == "" {
			rel = fi.Path
		}
		id := chunkID(fi.Hash, rel, start+1, end)
		chunks = append(chunks, Chunk{
			ID:        id,
			FilePath:  fi.Path,
			RelPath:   rel,
			Language:  fi.Language,
			StartLine: start + 1,
			EndLine:   end,
			Content:   content,
			FileHash:  fi.Hash,
		})

		if end == len(lines) {
			break
		}
	}

	return chunks, nil
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
