//go:build !treesitter

package indexer

func currentExtractorRevision() string {
	return "symbols-v1:go-ast+regex"
}
