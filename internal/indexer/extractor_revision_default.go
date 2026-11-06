//go:build !treesitter

package indexer

func currentExtractorRevision() string {
	return "symbols-v2:go-ast+regex"
}
