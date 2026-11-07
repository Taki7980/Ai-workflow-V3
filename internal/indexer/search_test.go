package indexer

import "testing"

func TestSearchFavoursFilesMatchingSeveralTerms(t *testing.T) {
	sym := func(path string, names ...string) []Symbol {
		out := []Symbol{}
		for i, n := range names {
			out = append(out, Symbol{Name: n, Kind: "function", Path: path, Line: i + 1})
		}
		return out
	}
	symbols := append(sym("lexer.go", "parseInput", "readHeader", "nextToken"), sym("types.go", "Token")...)
	// Common terms elsewhere make "token" the rarest query term, so the short
	// lone Token symbol scores highest on its own.
	for _, f := range []string{"a.go", "b.go"} {
		symbols = append(symbols, sym(f, "parseConfig", "headerName")...)
	}
	hits := Search("parse header token", map[string]Index{".": {Symbols: symbols}}, 1)
	if len(hits) != 1 || hits[0].Path != "lexer.go" {
		t.Fatalf("want lexer.go first, got %+v", hits)
	}
}
