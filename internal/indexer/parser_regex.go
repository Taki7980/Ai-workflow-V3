package indexer

import (
	"bufio"
	"path/filepath"
	"regexp"
	"strings"
)

// Release binaries build without cgo, so these patterns are the only symbol
// extractor for every language except Go: they cover declarations in the
// languages sourceExts indexes, with modifiers and attributes allowed first.
var (
	attrs     = `(?:@[\w.]+(?:\([^)]*\))?\s+)*`
	modifiers = `(?:(?:export|default|async|public|private|protected|internal|fileprivate|static|final|abstract|sealed|open|override|virtual|data|inline|partial|readonly|unsafe|extern|const|case|enum|annotation|suspend|mutating|pub(?:\([^)]*\))?)\s+)*`

	// declRE: keyword declarations (def, fn, class, interface, trait, ...).
	declRE = regexp.MustCompile(`^\s*` + attrs + modifiers +
		`(?:def|fn|fun|func|function\*?|class|struct|interface|trait|protocol|type|typealias|enum|module|object|record|namespace|extension|union|mod)\s+` +
		`(?:<[^>]*>\s*)?(?:[A-Za-z_]\w*\.)?([A-Za-z_$][\w$]*)`)
	// methodRE: modifier-led methods (Java, C#, Kotlin, PHP, Swift, TS classes).
	methodRE = regexp.MustCompile(`^\s*` + attrs + `(?:(?:public|private|protected|internal|static|final|abstract|override|virtual|async|synchronized|native|open|suspend|operator|readonly|unsafe|extern|get|set)\s+)+` +
		`(?:[\w<>\[\],.?:]+\s+)?([A-Za-z_$][\w$]*)\s*(?:<[^>]*>)?\s*\(`)
	// bindingRE: JS/TS functions bound to names (const f = async () => ...),
	// optionally inside one undotted wrapper (useCallback, memo); dotted calls
	// such as xs.filter((x) => ...) bind results, not functions.
	bindingRE = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::[^=]+)?=\s*(?:async\s+)?` +
		`(?:function\b|(?:[A-Za-z_$][\w$]*\(\s*)?(?:async\s+)?(?:<[^>]*>)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*(?::[^=]+)?=>|\(\s*\{?\s*$)`)
	// shorthandRE: unmodified JS/TS class and object methods: name(args) {
	shorthandRE = regexp.MustCompile(`^\s+(?:\*\s*)?([A-Za-z_$][\w$]*)\s*\([^)]*\)\s*(?::\s*[^{;]+)?\{\s*$`)
	// cFuncRE: column-0 C/C++ function definitions (return type, then name).
	cFuncRE  = regexp.MustCompile(`^(?:[\w*&:<>,]+\s+)+\**([A-Za-z_][\w:~]*)\s*\([^;]*$`)
	goFuncRE = regexp.MustCompile(`^\s*func\s+(?:\([^)]+\)\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

	cFamily  = map[string]bool{".c": true, ".h": true, ".cc": true, ".cpp": true, ".cxx": true, ".hpp": true}
	jsFamily = map[string]bool{".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".vue": true, ".svelte": true}
	// noMethods: languages whose methods use a keyword, where methodRE would
	// misread statements such as Python's `async with lock(`.
	noMethods = map[string]bool{".py": true, ".rb": true, ".rs": true, ".sql": true}
	notSymbol = map[string]bool{"if": true, "for": true, "while": true, "switch": true, "catch": true, "with": true, "return": true,
		"else": true, "function": true, "sizeof": true, "typeof": true, "new": true, "await": true, "elif": true, "foreach": true, "do": true}
)

type regexSymbolParser struct{}

// Name returns the identifier for the regex-based fallback symbol parser.
func (regexSymbolParser) Name() string { return "regex" }

// Supports reports that this fallback parser handles every file extension.
func (regexSymbolParser) Supports(string) bool { return true }

// Parse extracts symbols from source using regexSymbols.
func (regexSymbolParser) Parse(source []byte, relativePath, digest string) ([]Symbol, error) {
	return regexSymbols(string(source), relativePath, digest), nil
}

// regexSymbols scans text line by line, matching the declaration patterns for
// the file's language and returning a Symbol for each match.
func regexSymbols(text, rel, digest string) []Symbol {
	ext := strings.ToLower(filepath.Ext(rel))
	res := []*regexp.Regexp{declRE, goFuncRE}
	if !noMethods[ext] {
		res = append(res, methodRE)
	}
	if jsFamily[ext] {
		res = append(res, bindingRE, shorthandRE)
	}
	if cFamily[ext] {
		res = append(res, cFuncRE)
	}
	out := []Symbol{}
	scan := bufio.NewScanner(strings.NewReader(text))
	scan.Buffer(make([]byte, 0, 64<<10), 4<<20) // minified bundles have megabyte lines
	line := 0
	for scan.Scan() {
		line++
		s := scan.Text()
		for _, re := range res {
			if m := re.FindStringSubmatch(s); len(m) >= 2 && !notSymbol[m[1]] {
				out = append(out, Symbol{Name: m[1], Kind: "symbol", Path: rel, Line: line, SHA256: digest})
				break
			}
		}
	}
	return out
}
