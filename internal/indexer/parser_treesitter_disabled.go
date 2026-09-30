//go:build !treesitter

package indexer

func treeSitterParsers() []symbolParser { return nil }
