package retrieval

import (
	"errors"
	"reflect"
	"testing"
)

func keysOf[T any](items []Candidate[T]) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Key)
	}
	return out
}

func TestSelectMMR_DiversifiesRedundantCandidates(t *testing.T) {
	candidates := []Candidate[string]{
		{Key: "service", Text: "payment retry duplicate charge handler", Value: "service", Relevance: 1.0, EstimatedTokens: 200},
		{Key: "service-helper", Text: "payment retry duplicate charge helper", Value: "helper", Relevance: 0.95, EstimatedTokens: 200},
		{Key: "test", Text: "test process payment retry behavior", Value: "test", Relevance: 0.80, EstimatedTokens: 200},
	}
	got, err := SelectMMR(candidates, SelectorOptions{MaxTokens: 600, MaxCandidates: 10, Lambda: 0.70})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"service", "test", "service-helper"}
	if !reflect.DeepEqual(keysOf(got.Items), want) {
		t.Fatalf("keys=%v want=%v", keysOf(got.Items), want)
	}
}

func TestSelectMMR_NeverExceedsHardBudget(t *testing.T) {
	candidates := []Candidate[string]{
		{Key: "a", Text: "alpha", Value: "a", Relevance: 1.0, EstimatedTokens: 700},
		{Key: "b", Text: "beta", Value: "b", Relevance: 0.9, EstimatedTokens: 600},
		{Key: "c", Text: "gamma", Value: "c", Relevance: 0.8, EstimatedTokens: 300},
	}
	got, err := SelectMMR(candidates, SelectorOptions{MaxTokens: 1000, MaxCandidates: 10, Lambda: 0.70})
	if err != nil {
		t.Fatal(err)
	}
	if got.UsedTokens > 1000 {
		t.Fatalf("used=%d exceeds budget", got.UsedTokens)
	}
}

func TestSelectMMR_SkipsOversizedBestCandidateForSmallerFit(t *testing.T) {
	candidates := []Candidate[string]{
		{Key: "too-big", Text: "payment service", Value: "x", Relevance: 1.0, EstimatedTokens: 1200},
		{Key: "small-a", Text: "payment retry", Value: "a", Relevance: 0.8, EstimatedTokens: 400},
		{Key: "small-b", Text: "payment tests", Value: "b", Relevance: 0.7, EstimatedTokens: 450},
	}
	got, err := SelectMMR(candidates, SelectorOptions{MaxTokens: 900, MaxCandidates: 10, Lambda: 0.70})
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(keysOf(got.Items), []string{"too-big"}) {
		t.Fatal("oversized top candidate must not block smaller fitting evidence")
	}
	if got.UsedTokens > 900 {
		t.Fatalf("used=%d exceeds budget", got.UsedTokens)
	}
}

func TestSelectMMR_DeduplicatesByKeyDeterministically(t *testing.T) {
	candidates := []Candidate[string]{
		{Key: "same", Text: "payment service", Value: "high", Relevance: 1.0, EstimatedTokens: 100},
		{Key: "same", Text: "payment helper", Value: "required", Relevance: 0.8, EstimatedTokens: 220, Required: true},
		{Key: "other", Text: "auth service", Value: "other", Relevance: 0.7, EstimatedTokens: 100},
	}
	got, err := SelectMMR(candidates, SelectorOptions{MaxTokens: 500, MaxCandidates: 10, Lambda: 0.70, MandatoryRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items=%d want=2", len(got.Items))
	}
	if got.Items[0].Key != "same" || !got.Items[0].Required || got.Items[0].Relevance != 1.0 || got.Items[0].EstimatedTokens != 220 {
		t.Fatalf("deduped candidate=%+v", got.Items[0])
	}
	if got.UsedTokens != 320 {
		t.Fatalf("used=%d want=320", got.UsedTokens)
	}
}

func TestSelectMMR_PreservesRequiredEvidence(t *testing.T) {
	candidates := []Candidate[string]{
		{Key: "optional", Text: "payment optional", Value: "optional", Relevance: 1.0, EstimatedTokens: 300},
		{Key: "required", Text: "structural caller evidence", Value: "required", Relevance: 0.2, EstimatedTokens: 400, Required: true},
	}
	got, err := SelectMMR(candidates, SelectorOptions{MaxTokens: 500, MaxCandidates: 10, Lambda: 0.70, MandatoryRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Key != "required" {
		t.Fatalf("required evidence not preserved: %v", keysOf(got.Items))
	}
}

func TestSelectMMR_RequiredEvidenceOverBudgetFails(t *testing.T) {
	_, err := SelectMMR([]Candidate[string]{
		{Key: "required", Text: "structural", Value: "x", Relevance: 1, EstimatedTokens: 501, Required: true},
	}, SelectorOptions{MaxTokens: 500, MaxCandidates: 10, Lambda: 0.70, MandatoryRequired: true})
	if !errors.Is(err, ErrRequiredEvidenceOverBudget) {
		t.Fatalf("err=%v want ErrRequiredEvidenceOverBudget", err)
	}
}

func TestSelectMMR_RequiredEvidenceOverCandidateLimitFails(t *testing.T) {
	_, err := SelectMMR([]Candidate[string]{
		{Key: "a", Text: "a", Value: "a", Relevance: 1, EstimatedTokens: 1, Required: true},
		{Key: "b", Text: "b", Value: "b", Relevance: 1, EstimatedTokens: 1, Required: true},
	}, SelectorOptions{MaxTokens: 10, MaxCandidates: 1, Lambda: 0.70, MandatoryRequired: true})
	if !errors.Is(err, ErrRequiredEvidenceOverCandidateLimit) {
		t.Fatalf("err=%v want ErrRequiredEvidenceOverCandidateLimit", err)
	}
}

func TestSelectMMR_StableTieBreakByInputOrder(t *testing.T) {
	candidates := []Candidate[string]{
		{Key: "first", Text: "alpha", Value: "first", Relevance: 1, EstimatedTokens: 1},
		{Key: "second", Text: "beta", Value: "second", Relevance: 1, EstimatedTokens: 1},
	}
	got, err := SelectMMR(candidates, SelectorOptions{MaxTokens: 2, MaxCandidates: 2, Lambda: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keysOf(got.Items), []string{"first", "second"}) {
		t.Fatalf("keys=%v", keysOf(got.Items))
	}
}

func TestSelectMMR_RejectsInvalidOptionsAndCandidates(t *testing.T) {
	cases := []struct {
		name       string
		candidates []Candidate[string]
		options    SelectorOptions
	}{
		{"zero budget", []Candidate[string]{{Key: "a", Text: "a", EstimatedTokens: 1}}, SelectorOptions{MaxCandidates: 1, Lambda: .7}},
		{"zero cap", []Candidate[string]{{Key: "a", Text: "a", EstimatedTokens: 1}}, SelectorOptions{MaxTokens: 1, Lambda: .7}},
		{"lambda low", []Candidate[string]{{Key: "a", Text: "a", EstimatedTokens: 1}}, SelectorOptions{MaxTokens: 1, MaxCandidates: 1, Lambda: -.1}},
		{"lambda high", []Candidate[string]{{Key: "a", Text: "a", EstimatedTokens: 1}}, SelectorOptions{MaxTokens: 1, MaxCandidates: 1, Lambda: 1.1}},
		{"empty key", []Candidate[string]{{Text: "a", EstimatedTokens: 1}}, SelectorOptions{MaxTokens: 1, MaxCandidates: 1, Lambda: .7}},
		{"zero token estimate", []Candidate[string]{{Key: "a", Text: "a"}}, SelectorOptions{MaxTokens: 1, MaxCandidates: 1, Lambda: .7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := SelectMMR(tc.candidates, tc.options); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
