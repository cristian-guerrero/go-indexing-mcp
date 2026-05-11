package treeparse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

type Block struct {
	StartLine int
	EndLine   int
	NodeType  string
}

type ParseFile struct {
	Path     string
	Language string
}

type Parser struct {
	BinPath string
}

func New(binPath string) *Parser {
	return &Parser{BinPath: binPath}
}

var structuralTypes = map[string]map[string]bool{
	"go": {
		"function_declaration": true,
		"method_declaration":   true,
		"type_declaration":     true,
	},
	"python": {
		"function_definition": true,
		"class_definition":    true,
	},
	"javascript": {
		"function_declaration":   true,
		"class_declaration":      true,
		"method_definition":      true,
		"arrow_function":         true,
		"export_statement":       true,
		"interface_declaration":  true,
		"type_alias_declaration": true,
		"enum_declaration":       true,
	},
	"rust": {
		"function_item": true,
		"struct_item":   true,
		"impl_item":     true,
		"trait_item":    true,
		"enum_item":     true,
		"type_item":     true,
		"const_item":    true,
		"static_item":   true,
	},
	"java": {
		"class_declaration":         true,
		"interface_declaration":     true,
		"method_declaration":        true,
		"enum_declaration":          true,
		"record_declaration":        true,
		"annotation_type_declaration": true,
	},
	"c": {
		"function_definition": true,
		"struct_specifier":    true,
		"union_specifier":     true,
		"enum_specifier":      true,
	},
	"cpp": {
		"function_definition": true,
		"class_specifier":     true,
		"struct_specifier":    true,
		"namespace_definition": true,
		"enum_specifier":      true,
		"template_declaration": true,
	},
	"csharp": {
		"class_declaration":     true,
		"struct_declaration":    true,
		"interface_declaration": true,
		"method_declaration":    true,
		"namespace_declaration": true,
		"enum_declaration":      true,
		"record_declaration":    true,
	},
	"ruby": {
		"method":           true,
		"singleton_method": true,
		"class":            true,
		"module":           true,
	},
	"php": {
		"function_definition":  true,
		"class_declaration":    true,
		"interface_declaration": true,
		"trait_declaration":    true,
		"enum_declaration":     true,
		"method_declaration":   true,
	},
	"swift": {
		"function_declaration":  true,
		"class_declaration":     true,
		"struct_declaration":    true,
		"enum_declaration":      true,
		"protocol_declaration":  true,
		"extension_declaration": true,
	},
	"kotlin": {
		"function_declaration":  true,
		"class_declaration":     true,
		"interface_declaration": true,
		"object_declaration":    true,
	},
	"scala": {
		"function_definition": true,
		"class_definition":    true,
		"trait_definition":    true,
		"object_definition":   true,
	},
	"bash": {
		"function_definition": true,
	},
}

func (p *Parser) ParseBlocks(filePath, language string) ([]Block, error) {
	types, ok := structuralTypes[language]
	if !ok {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.BinPath, "parse", "--json", "--quiet", filePath)
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tree-sitter parse: %w", err)
	}

	root, err := parseTreeJSON(stdout)
	if err != nil {
		return nil, fmt.Errorf("parse tree json: %w", err)
	}

	return extractBlocks(root, types), nil
}

func (p *Parser) ParseBlocksBatch(files []ParseFile) (map[string][]Block, error) {
	var filtered []ParseFile
	for _, f := range files {
		if _, ok := structuralTypes[f.Language]; ok {
			filtered = append(filtered, f)
		}
	}

	if len(filtered) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := []string{"parse", "--json", "--quiet"}
	for _, f := range filtered {
		args = append(args, f.Path)
	}

	cmd := exec.CommandContext(ctx, p.BinPath, args...)
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tree-sitter parse batch: %w", err)
	}

	return parseBatchOutput(stdout, filtered)
}

func extractBlocks(root jsonNode, types map[string]bool) []Block {
	var blocks []Block
	collectBlocks(root, types, &blocks)

	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].StartLine < blocks[j].StartLine
	})
	return blocks
}

func parseBatchOutput(data []byte, files []ParseFile) (map[string][]Block, error) {
	langByPath := make(map[string]string, len(files))
	pathSet := make(map[string]bool, len(files))
	for _, f := range files {
		abs, err := filepath.Abs(f.Path)
		if err == nil {
			langByPath[abs] = f.Language
			pathSet[abs] = true
		}
		langByPath[f.Path] = f.Language
		pathSet[f.Path] = true
	}

	results := make(map[string][]Block)

	var rawResults []rawFileResult
	if err := json.Unmarshal(data, &rawResults); err == nil {
		for _, r := range rawResults {
			fp := resolvePath(r.FilePath, pathSet)
			types, ok := structuralTypes[langByPath[fp]]
			if !ok || r.Tree.Type == "" {
				continue
			}
			results[fp] = extractBlocks(r.Tree, types)
		}
		if len(results) > 0 {
			return results, nil
		}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var r rawFileResult
		if err := dec.Decode(&r); err != nil {
			break
		}
		if r.Tree.Type == "" {
			continue
		}
		fp := resolvePath(r.FilePath, pathSet)
		types, ok := structuralTypes[langByPath[fp]]
		if !ok {
			continue
		}
		results[fp] = extractBlocks(r.Tree, types)
	}

	return results, nil
}

type rawFileResult struct {
	FilePath string   `json:"file_path"`
	Tree     jsonNode `json:"tree"`
}

func resolvePath(p string, pathSet map[string]bool) string {
	if pathSet[p] {
		return p
	}
	abs, err := filepath.Abs(p)
	if err == nil && pathSet[abs] {
		return abs
	}
	return p
}

type jsonNode struct {
	Type          string        `json:"type"`
	StartPosition jsonPosition  `json:"start_position"`
	EndPosition   jsonPosition  `json:"end_position"`
	Children      []jsonNode    `json:"children,omitempty"`
}

type jsonPosition struct {
	Row    int `json:"row"`
	Column int `json:"column"`
}

func parseTreeJSON(data []byte) (jsonNode, error) {
	var root jsonNode
	if err := json.Unmarshal(data, &root); err == nil && root.Type != "" {
		return root, nil
	}

	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(data, &arr); err == nil && len(arr) > 0 {
		if raw, ok := arr[0]["tree"]; ok {
			var n jsonNode
			if err := json.Unmarshal(raw, &n); err == nil {
				return n, nil
			}
		}
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err == nil {
		if raw, ok := m["tree"]; ok {
			var n jsonNode
			if err := json.Unmarshal(raw, &n); err == nil {
				return n, nil
			}
		}
	}

	return root, fmt.Errorf("unrecognized json format")
}

func collectBlocks(n jsonNode, types map[string]bool, blocks *[]Block) {
	for _, child := range n.Children {
		if types[child.Type] {
			*blocks = append(*blocks, Block{
				StartLine: child.StartPosition.Row + 1,
				EndLine:   child.EndPosition.Row,
				NodeType:  child.Type,
			})
		} else {
			collectBlocks(child, types, blocks)
		}
	}
}
