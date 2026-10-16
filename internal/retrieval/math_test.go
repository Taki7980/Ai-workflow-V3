package retrieval

import "testing"

func TestBM25RanksRelevantDocumentFirst(t *testing.T) {
	texts := []string{"payment duplicate charge guard", "css button spacing"}
	b := NewBM25(texts, []int{1, 2})
	got := b.Rank("duplicate payment")
	if len(got) == 0 || got[0].Value != 1 {
		t.Fatalf("unexpected ranking: %#v", got)
	}
}

func TestTokenizeCamelAndSnake(t *testing.T) {
	got := Tokenize("payment_guard DuplicateCharge")
	want := map[string]bool{"payment": true, "guard": true, "duplicatecharge": true, "duplicate": true, "charge": true}
	for _, x := range got {
		delete(want, x)
	}
	if len(want) > 0 {
		t.Fatalf("missing tokens: %#v; got %#v", want, got)
	}
}

func TestBM25MatchesInflectedTaskWords(t *testing.T) {
	b := NewBM25([]string{"gatherContext function brief/gather.go", "Tier type providers/tier.go", "css button spacing"}, []int{1, 2, 3})
	for query, want := range map[string]int{"repository context gathering changes": 1, "model tiers": 2} {
		if got := b.Rank(query); len(got) == 0 || got[0].Value != want {
			t.Errorf("Rank(%q) = %#v, want %d first", query, got, want)
		}
	}
}
