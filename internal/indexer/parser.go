package indexer

import (
	"path/filepath"
	"strings"
)

type symbolParser interface {
	Name() string
	Supports(extension string) bool
	Parse(source []byte, relativePath, sha256 string) ([]Symbol, error)
}

// parseSymbols extracts symbols from source using the Go AST, tree-sitter,
// and regex parsers in priority order, returning the results from the first
// parser that supports the file's extension.
func parseSymbols(source []byte, relativePath, digest string) ([]Symbol, string) {
	parsers := []symbolParser{goASTSymbolParser{}}
	parsers = append(parsers, treeSitterParsers()...)
	parsers = append(parsers, regexSymbolParser{})
	return parseWithParsers(source, relativePath, digest, parsers)
}

// parseWithParsers tries each parser in order and returns the symbols and
// parser name from the first one that supports relativePath's extension and
// parses successfully.
func parseWithParsers(source []byte, relativePath, digest string, parsers []symbolParser) ([]Symbol, string) {
	ext := strings.ToLower(filepath.Ext(relativePath))
	for _, parser := range parsers {
		if !parser.Supports(ext) {
			continue
		}
		symbols, err := parser.Parse(source, relativePath, digest)
		if err != nil {
			continue
		}
		return symbols, parser.Name()
	}
	return nil, ""
}
