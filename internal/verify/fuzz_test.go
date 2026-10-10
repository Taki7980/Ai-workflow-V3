package verify

import (
	"strings"
	"testing"
)

// FuzzSplitCommand checks the shell-free tokenizer never panics and never
// yields an empty argv[0] for a successful parse of non-blank input.
func FuzzSplitCommand(f *testing.F) {
	for _, s := range []string{`go test ./...`, `a "b c" 'd'`, `"unterminated`, `\`, "x\x00y"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		argv, err := SplitCommand(s)
		if err == nil && strings.TrimSpace(s) != "" && len(argv) > 0 && argv[0] == "" && !strings.Contains(s, `""`) && !strings.Contains(s, `''`) {
			t.Fatalf("empty program from %q: %q", s, argv)
		}
	})
}
