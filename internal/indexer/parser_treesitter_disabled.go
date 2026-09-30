//go:build !treesitter

package indexer

// treeSitterParsers returns no parsers when the treesitter build tag is disabled.
func treeSitterParsers() []symbolParser { return nil }
