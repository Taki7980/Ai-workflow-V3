package indexer

import "testing"

func TestSearchTieOrderDeterministicAcrossRepos(t *testing.T) {
	idx := func() Index {
		return Index{Symbols: []Symbol{{Name: "RetryPayment", Kind: "function", Path: "pay.go", Line: 1}}}
	}
	indexes := map[string]Index{"zeta": idx(), "alpha": idx(), "mid": idx(), "beta": idx()}
	for i := 0; i < 50; i++ {
		hits := Search("RetryPayment", indexes, 1)
		if len(hits) != 1 || hits[0].Repository != "alpha" {
			t.Fatalf("run %d: tie broken by map order: %+v", i, hits)
		}
	}
}
