package indexer

import (
	"bufio"
	"regexp"
	"strings"
)

var symbolRE = regexp.MustCompile(`(?m)^\\s*(?:(?:export\\s+)?(?:default\\s+)?(?:async\\s+)?(?:def|fn|function|class|struct|interface|type|enum|pub\\s+fn|pub\\s+struct|pub\\(crate\\)\\s+fn))\\s+([A-Za-z_][A-Za-z0-9_]*)`)
var goFuncRE = regexp.MustCompile(`(?m)^\\s*func\\s+(?:\\([^)]+\\)\\s+)?([A-Za-z_][A-Za-z0-9_]*)\\s*\\(`)

type regexSymbolParser struct{}

func (regexSymbolParser) Name() string { return "regex" }
func (regexSymbolParser) Supports(string) bool { return true }
func (regexSymbolParser) Parse(source []byte, relativePath, digest string) ([]Symbol, error) {
	return regexSymbols(string(source), relativePath, digest), nil
}

func regexSymbols(text, rel, digest string) []Symbol {
	out := []Symbol{}
	scan := bufio.NewScanner(strings.NewReader(text))
	line := 0
	for scan.Scan() {
		line++
		s := scan.Text()
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
