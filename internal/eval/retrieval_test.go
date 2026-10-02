package eval

import "testing"

func TestBaselineFixture(t *testing.T) {
	fixture, err := LoadFixture("../../benchmarks/retrieval/baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	report := Run(fixture)
	if !report.Pass {
		t.Fatalf("baseline benchmark failed: %v", report.Violations)
	}
	if report.Metrics.PositiveCases != 7 || report.Metrics.NoGoldCases != 2 {
		t.Fatalf("unexpected case counts: %#v", report.Metrics)
	}
	if report.Metrics.RecallAtK != 1 {
		t.Fatalf("expected complete Recall@K baseline, got %.6f", report.Metrics.RecallAtK)
	}
	if report.Metrics.NoGoldFalsePositiveRate != 0.5 {
		t.Fatalf("expected the frozen lexical abstention weakness (0.5), got %.6f", report.Metrics.NoGoldFalsePositiveRate)
	}
}

func TestSelectorFixtureImprovesContextEfficiencyWithoutQualityRegression(t *testing.T) {
	fixture, err := LoadFixture("../../benchmarks/retrieval/selector.json")
	if err != nil {
		t.Fatal(err)
	}
	if fixture.Selector == nil {
		t.Fatal("selector fixture must enable selection")
	}
	report := Run(fixture)
	if !report.Pass {
		t.Fatalf("selector benchmark failed: %v", report.Violations)
	}
	if report.RawMetrics == nil {
		t.Fatal("selector report must include raw metrics")
	}
	if report.Metrics.RecallAtK < report.RawMetrics.RecallAtK {
		t.Fatalf("Recall@K regressed: raw=%.6f selected=%.6f", report.RawMetrics.RecallAtK, report.Metrics.RecallAtK)
	}
	if report.Metrics.MRR < report.RawMetrics.MRR {
		t.Fatalf("MRR regressed: raw=%.6f selected=%.6f", report.RawMetrics.MRR, report.Metrics.MRR)
	}
	if report.Metrics.ContextYield-report.RawMetrics.ContextYield < fixture.ComparisonThresholds.MinContextYieldImprovement {
		t.Fatalf("context yield improvement raw=%.6f selected=%.6f", report.RawMetrics.ContextYield, report.Metrics.ContextYield)
	}
	if report.RawMetrics.AvgRetrievedTokens <= 0 {
		t.Fatal("raw average tokens must be positive")
	}
	ratio := report.Metrics.AvgRetrievedTokens / report.RawMetrics.AvgRetrievedTokens
	if ratio > fixture.ComparisonThresholds.MaxSelectedTokenRatio {
		t.Fatalf("selected/raw token ratio=%.6f", ratio)
	}
	for _, c := range report.Cases {
		if c.RetrievedTokens > fixture.Selector.MaxTokens {
			t.Fatalf("case %s exceeded budget: %d > %d", c.Name, c.RetrievedTokens, fixture.Selector.MaxTokens)
		}
	}
}

func TestSelectorFixtureUsesVariedFileCosts(t *testing.T) {
	fixture, err := LoadFixture("../../benchmarks/retrieval/selector.json")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]struct{}{}
	for _, tokens := range fixture.FileTokenEstimates {
		seen[tokens] = struct{}{}
	}
	if len(seen) < 3 {
		t.Fatalf("selector benchmark needs varied file costs, got %d distinct estimates", len(seen))
	}
}
