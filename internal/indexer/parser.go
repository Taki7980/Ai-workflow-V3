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

func parseSymbols(source []byte, relativePath, digest string) ([]Symbol, string) {
	parsers := []symbolParser{goASTSymbolParser{}}
	parsers = append(parsers, treeSitterParsers()...)
	parsers = append(parsers, regexSymbolParser{})
	return parseWithParsers(source, relativePath, digest, parsers)
}

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
