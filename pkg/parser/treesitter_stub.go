//go:build !onnx

package parser

import "errors"

// newTreeSitterParser returns an error on builds without the onnx tag.
func newTreeSitterParser(cfg ParserConfig) (Parser, error) {
	return nil, errors.New("tree-sitter parser requires building with -tags onnx")
}
