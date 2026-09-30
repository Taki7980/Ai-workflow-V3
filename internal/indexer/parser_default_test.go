//go:build !treesitter

package indexer

import "testing"

func TestDefaultPythonUsesRegexWithoutTreeSitterTag(t *testing.T) {
	source := []byte("def payment_retry():\n    return True\n")
	got, parserName := parseSymbols(source, "payment.py", "digest")
	if parserName != "regex" {
		t.Fatalf("parser=%q want regex", parserName)
	}
	if len(got) != 1 || got[0].Name != "payment_retry" {
		t.Fatalf("symbols=%#v", got)
	}
}
