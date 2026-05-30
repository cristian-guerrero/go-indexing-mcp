//go:build onnx

package graph

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// symbolID builds a deterministic ID for a symbol.
func symbolID(fileHash, relPath string, line int, name string) string {
	return fileHash + ":" + relPath + ":" + itoa(line) + ":" + name
}

// refID builds a deterministic ID for a reference.
func refID(sourceID, targetName string, kind RefKind, line int) string {
	h := sourceID + "->" + targetName + ":" + kind.String() + ":" + itoa(line)
	return h
}

// itoa converts an integer to string without allocation.
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

// extractNodeName gets the name/identifier from a tree-sitter node.
func extractNodeName(node *sitter.Node, source []byte) string {
	nameNode := node.ChildByFieldName("name")
	if nameNode != nil {
		return nameNode.Content(source)
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		childType := child.Type()
		if childType == "identifier" || childType == "type_identifier" || strings.HasPrefix(childType, "ident") {
			return child.Content(source)
		}
	}

	return ""
}

// extractSignatureLine returns the first few lines of a definition as its signature.
func extractSignatureLine(node *sitter.Node, source []byte) string {
	start := node.StartPoint().Row
	end := node.EndPoint().Row

	var lines []string
	for row := start; row <= end && int(row)-int(start) < 5; row++ {
		line := extractLine(source, int(row))
		lines = append(lines, line)
		if strings.Contains(line, ")") || strings.Contains(line, "{") || strings.Contains(line, ":") {
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// extractLine extracts a single line from the source bytes.
func extractLine(source []byte, row int) string {
	start := 0
	currentRow := 0
	for i := 0; i < len(source); i++ {
		if currentRow == row {
			start = i
			for i < len(source) && source[i] != '\n' && source[i] != '\r' {
				i++
			}
			return string(source[start:i])
		}
		if source[i] == '\n' {
			currentRow++
		}
	}
	return ""
}

// defaultExtractSymbol creates a Symbol using the generic name extraction.
// Override in ExtractSymbol when the name is nested (C/C++) or needs
// preprocessing (Go receiver methods).
func defaultExtractSymbol(ctx *ExtractContext, node *sitter.Node, kind SymbolKind) *Symbol {
	name := extractNodeName(node, ctx.Source)
	if name == "" {
		return nil
	}
	sig := extractSignatureLine(node, ctx.Source)
	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1
	id := symbolID(ctx.FileHash, ctx.RelPath, startLine, name)
	return &Symbol{
		ID:        id,
		Name:      name,
		Kind:      kind,
		FilePath:  ctx.FilePath,
		RelPath:   ctx.RelPath,
		StartLine: startLine,
		EndLine:   endLine,
		Signature: sig,
	}
}

// extractCalls extracts call references from call expression nodes.
func extractCalls(node *sitter.Node, source []byte, ntype, filePath, relPath, fileHash string, startLine int) []Reference {
	name := extractCallName(node, source)
	if name == "" {
		return nil
	}

	id := symbolID(fileHash, relPath, startLine, "call:"+name)
	return []Reference{{
		ID:         refID(id, name, RefCalls, startLine),
		SourceID:   id,
		TargetName: name,
		Kind:       RefCalls,
		FilePath:   filePath,
		Line:       startLine,
		Confidence: 1.0,
	}}
}

// extractCallName extracts the function/method name from a call expression.
func extractCallName(node *sitter.Node, source []byte) string {
	if int(node.ChildCount()) == 0 {
		return ""
	}

	firstChild := node.Child(0)
	if firstChild == nil {
		return ""
	}

	ftype := firstChild.Type()

	switch {
	case ftype == "identifier":
		return firstChild.Content(source)

	case ftype == "selector_expression" || ftype == "attribute" || ftype == "field_expression":
		nc := int(firstChild.ChildCount())
		if nc > 0 {
			last := firstChild.Child(nc - 1)
			if last != nil {
				return last.Content(source)
			}
		}
		return firstChild.Content(source)

	case strings.HasPrefix(ftype, "member"):
		nc := int(firstChild.ChildCount())
		if nc > 0 {
			last := firstChild.Child(nc - 1)
			if last != nil {
				return last.Content(source)
			}
		}
		return firstChild.Content(source)
	}

	return ""
}

// makeImportRef creates a single RefImports reference for a given imported name.
func makeImportRef(fileHash, relPath string, startLine int, name, filePath string) Reference {
	id := symbolID(fileHash, relPath, startLine, "import:"+name)
	return Reference{
		ID:         refID(id, name, RefImports, startLine),
		SourceID:   id,
		TargetName: name,
		Kind:       RefImports,
		FilePath:   filePath,
		Line:       startLine,
		Confidence: 1.0,
	}
}

// extractGoImportPath extracts the quoted path from a Go import line.
func extractGoImportPath(line string) string {
	line = strings.TrimSpace(line)
	if idx := strings.LastIndex(line, `"`); idx > 0 {
		line = line[strings.Index(line, `"`):]
	}
	if strings.HasPrefix(line, `"`) && strings.HasSuffix(line, `"`) {
		return strings.Trim(line, `"`)
	}
	return ""
}

// extractJSXRef extracts a call reference from a JSX element.
func extractJSXRef(node *sitter.Node, source []byte, filePath, relPath, fileHash string, startLine int) []Reference {
	name := findJSXComponentName(node, source)
	if name == "" {
		return nil
	}
	id := symbolID(fileHash, relPath, startLine, "jsx:"+name)
	return []Reference{{
		ID:         refID(id, name, RefCalls, startLine),
		SourceID:   id,
		TargetName: name,
		Kind:       RefCalls,
		FilePath:   filePath,
		Line:       startLine,
		Confidence: 1.0,
	}}
}

// findJSXComponentName extracts the component name from a JSX element node.
func findJSXComponentName(node *sitter.Node, source []byte) string {
	if nameNode := node.ChildByFieldName("name"); nameNode != nil {
		return nameNode.Content(source)
	}
	if openTag := node.ChildByFieldName("open_tag"); openTag != nil {
		if nameNode := openTag.ChildByFieldName("name"); nameNode != nil {
			return nameNode.Content(source)
		}
	}
	return ""
}

// makeTypeRef creates a RefAccessed reference for a type identifier usage.
func makeTypeRef(fileHash, relPath string, startLine, col int, typeName, filePath string) Reference {
	id := symbolID(fileHash, relPath, startLine, fmt.Sprintf("type:%s:c%d", typeName, col))
	return Reference{
		ID:         refID(id, typeName, RefAccessed, startLine),
		SourceID:   id,
		TargetName: typeName,
		Kind:       RefAccessed,
		FilePath:   filePath,
		Line:       startLine,
		Confidence: 1.0,
	}
}
