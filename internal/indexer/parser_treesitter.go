//go:build treesitter

package indexer

import (
	"fmt"
	"sort"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

type treeSitterSymbolParser struct {
	name       string
	extensions map[string]bool
	language   func() *tree_sitter.Language
	family     string
}

func (p treeSitterSymbolParser) Name() string { return p.name }

func (p treeSitterSymbolParser) Supports(extension string) bool {
	return p.extensions[extension]
}

func (p treeSitterSymbolParser) Parse(source []byte, relativePath, digest string) ([]Symbol, error) {
	parser := tree_sitter.NewParser()
	defer parser.Close()

	language := p.language()
	if language == nil {
		return nil, fmt.Errorf("%s language is nil", p.name)
	}
	if err := parser.SetLanguage(language); err != nil {
		return nil, fmt.Errorf("%s language: %w", p.name, err)
	}

	tree := parser.Parse(source, nil)
	if tree == nil {
		return nil, fmt.Errorf("%s returned nil tree", p.name)
	}
	defer tree.Close()

	out := make([]Symbol, 0)
	walkTreeSitter(tree.RootNode(), source, relativePath, digest, p.family, &out)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		if out[i].EndLine != out[j].EndLine {
			return out[i].EndLine < out[j].EndLine
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

func treeSitterParsers() []symbolParser {
	return []symbolParser{
		treeSitterSymbolParser{
			name: "tree-sitter-python",
			extensions: map[string]bool{".py": true},
			language: func() *tree_sitter.Language {
				return tree_sitter.NewLanguage(tree_sitter_python.Language())
			},
			family: "python",
		},
		treeSitterSymbolParser{
			name: "tree-sitter-javascript",
			extensions: map[string]bool{".js": true, ".jsx": true},
			language: func() *tree_sitter.Language {
				return tree_sitter.NewLanguage(tree_sitter_javascript.Language())
			},
			family: "javascript",
		},
		treeSitterSymbolParser{
			name: "tree-sitter-typescript",
			extensions: map[string]bool{".ts": true},
			language: func() *tree_sitter.Language {
				return tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript())
			},
			family: "typescript",
		},
		treeSitterSymbolParser{
			name: "tree-sitter-tsx",
			extensions: map[string]bool{".tsx": true},
			language: func() *tree_sitter.Language {
				return tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTSX())
			},
			family: "typescript",
		},
	}
}

func walkTreeSitter(node *tree_sitter.Node, source []byte, relativePath, digest, family string, out *[]Symbol) {
	if node == nil {
		return
	}
	if symbol, ok := treeSitterSymbol(node, source, relativePath, digest, family); ok {
		*out = append(*out, symbol)
	}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		walkTreeSitter(node.NamedChild(i), source, relativePath, digest, family, out)
	}
}

func treeSitterSymbol(node *tree_sitter.Node, source []byte, relativePath, digest, family string) (Symbol, bool) {
	kind := node.Kind()
	symbolKind := ""

	switch family {
	case "python":
		switch kind {
		case "function_definition":
			symbolKind = "function"
		case "class_definition":
			symbolKind = "type"
		default:
			return Symbol{}, false
		}
	case "javascript", "typescript":
		switch kind {
		case "function_declaration":
			symbolKind = "function"
		case "class_declaration", "abstract_class_declaration", "interface_declaration", "type_alias_declaration", "enum_declaration":
			symbolKind = "type"
		case "variable_declarator":
			value := node.ChildByFieldName("value")
			if value == nil || (value.Kind() != "arrow_function" && value.Kind() != "function_expression") {
				return Symbol{}, false
			}
			symbolKind = "function"
		default:
			return Symbol{}, false
		}
	default:
		return Symbol{}, false
	}

	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return Symbol{}, false
	}
	name := nameNode.Utf8Text(source)
	if name == "" {
		return Symbol{}, false
	}

	return Symbol{
		Name:    name,
		Kind:    symbolKind,
		Path:    relativePath,
		Line:    int(node.StartPosition().Row) + 1,
		EndLine: int(node.EndPosition().Row) + 1,
		SHA256:  digest,
	}, true
}
