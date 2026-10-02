package indexer

import (
	"regexp"
	"strings"
)

var symbolRE = regexp.MustCompile(`(?m)^\s*(?:(?:export\s+)?(?:default\s+)?(?:async\s+)?(?:def|fn|function|class|struct|interface|type|enum|pub\s+fn|pub\s+struct|pub\(crate\)\s+fn))\s+([A-Za-z_][A-Za-z0-9_]*)`)
var goFuncRE = regexp.MustCompile(`(?m)^\s*func\s+(?:\([^)]+\)\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

type regexSymbolParser struct{}

// Name returns the identifier for the regex-based fallback symbol parser.
func (regexSymbolParser) Name() string { return "regex" }

// Supports reports that this fallback parser handles every file extension.
func (regexSymbolParser) Supports(string) bool { return true }

// Parse extracts symbols from source using regexSymbols.
func (regexSymbolParser) Parse(source []byte, relativePath, digest string) ([]Symbol, error) {
	return regexSymbols(string(source), relativePath, digest), nil
}

// regexSymbols scans text line by line, matching common function/type/class
// declaration patterns across languages and returning a Symbol for each match.
// Lines are split without a length limit: a bufio.Scanner would stop at the
// first line over 64 KiB (common in minified or generated code) and silently
// drop every symbol after it.
func regexSymbols(text, rel, digest string) []Symbol {
	out := []Symbol{}
	line := 0
	for s := range strings.Lines(text) {
		line++
		s = strings.TrimRight(s, "\r\n")
		m := symbolRE.FindStringSubmatch(s)
		if len(m) < 2 {
			m = goFuncRE.FindStringSubmatch(s)
		}
		if len(m) >= 2 {
			out = append(out, Symbol{Name: m[1], Kind: "symbol", Path: rel, Line: line, SHA256: digest})
		}
	}
	return out
}
