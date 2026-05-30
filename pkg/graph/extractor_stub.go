//go:build !onnx

package graph

// Extractor is not available without the onnx build tag.
type Extractor struct{}

// NewExtractor returns nil on builds without the onnx build tag.
func NewExtractor() *Extractor {
	return nil
}

// Extract is a no-op on builds without the onnx build tag.
func (e *Extractor) Extract(content, language, filePath, relPath, fileHash string) ([]Symbol, []Reference, error) {
	return nil, nil, nil
}
