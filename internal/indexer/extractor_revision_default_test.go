//go:build !treesitter

package indexer

import "testing"

func TestExtractorRevisionDiffersForTreeSitterBuild(t *testing.T) {
	const want = "symbols-v1:go-ast+regex"
	const treeSitterProfile = "symbols-v1:go-ast+tree-sitter-runtime@0.25.0+python@0.25.0+javascript@0.25.0+typescript@0.23.2+regex"
	got := currentExtractorRevision()
	if got != want {
		t.Fatalf("revision=%q want=%q", got, want)
	}
	if got == treeSitterProfile {
		t.Fatal("default and treesitter extractor revisions must differ")
	}
}
