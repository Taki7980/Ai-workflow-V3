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
