package eval

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"

	"github.com/Taki7980/ai-workflow-v3/internal/indexer"
	"github.com/Taki7980/ai-workflow-v3/internal/retrieval"
)

type Fixture struct {
	SchemaVersion        int                  `json:"schema_version"`
	Name                 string               `json:"name"`
	K                    int                  `json:"k"`
	Repository           string               `json:"repository"`
	Symbols              []SymbolFixture      `json:"symbols"`
	FileTokenEstimates   map[string]int       `json:"file_token_estimates"`
	Cases                []RetrievalCase      `json:"cases"`
	Thresholds           Thresholds           `json:"thresholds"`
	Selector             *SelectorFixture     `json:"selector,omitempty"`
	ComparisonThresholds ComparisonThresholds `json:"comparison_thresholds,omitempty"`
}

type SymbolFixture struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

type RetrievalCase struct {
	Name      string   `json:"name"`
	TaskType  string   `json:"task_type"`
	Query     string   `json:"query"`
	GoldPaths []string `json:"gold_paths"`
}

type SelectorFixture struct {
	Lambda        float64 `json:"lambda"`
	MaxTokens     int     `json:"max_tokens"`
	MaxCandidates int     `json:"max_candidates"`
}

type ComparisonThresholds struct {
	MinContextYieldImprovement float64 `json:"min_context_yield_improvement"`
	MaxSelectedTokenRatio      float64 `json:"max_selected_token_ratio"`
}

type Thresholds struct {
	MinRecallAt1           float64 `json:"min_recall_at_1"`
	MinRecallAtK           float64 `json:"min_recall_at_k"`
	MinMRR                 float64 `json:"min_mrr"`
	MinFileF1AtK           float64 `json:"min_file_f1_at_k"`
	MinContextYield        float64 `json:"min_context_yield"`
	MaxNoGoldFalsePositive float64 `json:"max_no_gold_false_positive_rate"`
	MaxAvgRetrievedTokens  float64 `json:"max_avg_retrieved_tokens"`
}

type Metrics struct {
	PositiveCases           int     `json:"positive_cases"`
	NoGoldCases             int     `json:"no_gold_cases"`
	RecallAt1               float64 `json:"recall_at_1"`
	RecallAtK               float64 `json:"recall_at_k"`
	MRR                     float64 `json:"mrr"`
	FilePrecisionAtK        float64 `json:"file_precision_at_k"`
	FileF1AtK               float64 `json:"file_f1_at_k"`
	ContextYield            float64 `json:"context_yield"`
	AvgRetrievedTokens      float64 `json:"avg_retrieved_tokens"`
	NoGoldFalsePositiveRate float64 `json:"no_gold_false_positive_rate"`
}

type CaseResult struct {
	Name            string   `json:"name"`
	TaskType        string   `json:"task_type"`
	Query           string   `json:"query"`
	GoldPaths       []string `json:"gold_paths,omitempty"`
	RetrievedPaths  []string `json:"retrieved_paths,omitempty"`
	RecallAt1       float64  `json:"recall_at_1,omitempty"`
	RecallAtK       float64  `json:"recall_at_k,omitempty"`
	ReciprocalRank  float64  `json:"reciprocal_rank,omitempty"`
	FilePrecision   float64  `json:"file_precision_at_k,omitempty"`
	FileF1          float64  `json:"file_f1_at_k,omitempty"`
	ContextYield    float64  `json:"context_yield,omitempty"`
	RetrievedTokens int      `json:"retrieved_tokens"`
	FalsePositive   bool     `json:"false_positive,omitempty"`
}

type Report struct {
	SchemaVersion int          `json:"schema_version"`
	Fixture       string       `json:"fixture"`
	K             int          `json:"k"`
	Pass          bool         `json:"pass"`
	Metrics       Metrics      `json:"metrics"`
	RawMetrics    *Metrics     `json:"raw_metrics,omitempty"`
	Violations    []string     `json:"violations"`
	Cases         []CaseResult `json:"cases"`
}

// LoadFixture reads and decodes the retrieval benchmark fixture at path,
// validating it before returning.
func LoadFixture(path string) (Fixture, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Fixture{}, fmt.Errorf("read benchmark fixture: %w", err)
	}
	var f Fixture
	if err := json.Unmarshal(b, &f); err != nil {
		return Fixture{}, fmt.Errorf("decode benchmark fixture: %w", err)
	}
	if err := f.Validate(); err != nil {
		return Fixture{}, err
	}
	return f, nil
}

// Validate checks that the fixture's schema version, required fields, cases,
// and optional selector settings are well-formed, returning an error describing
// the first problem found.
func (f Fixture) Validate() error {
	if f.SchemaVersion != 1 {
		return fmt.Errorf("unsupported benchmark schema version %d", f.SchemaVersion)
	}
	if f.Name == "" || f.Repository == "" {
		return fmt.Errorf("benchmark name and repository are required")
	}
	if f.K <= 0 {
		return fmt.Errorf("benchmark k must be positive")
	}
	if len(f.Symbols) == 0 || len(f.Cases) == 0 {
		return fmt.Errorf("benchmark symbols and cases are required")
	}
	for _, s := range f.Symbols {
		if s.Name == "" || s.Path == "" {
			return fmt.Errorf("benchmark symbol name/path is required")
		}
		if f.FileTokenEstimates[s.Path] <= 0 {
			return fmt.Errorf("missing positive token estimate for %s", s.Path)
		}
	}
	seen := map[string]struct{}{}
	for _, c := range f.Cases {
		if c.Name == "" || c.Query == "" || c.TaskType == "" {
			return fmt.Errorf("benchmark case name/query/task_type is required")
		}
		if _, ok := seen[c.Name]; ok {
			return fmt.Errorf("duplicate benchmark case %q", c.Name)
		}
		seen[c.Name] = struct{}{}
	}
	if f.Selector != nil {
		if f.Selector.Lambda < 0 || f.Selector.Lambda > 1 {
			return fmt.Errorf("selector lambda must be 0..1")
		}
		if f.Selector.MaxTokens <= 0 || f.Selector.MaxCandidates <= 0 {
			return fmt.Errorf("selector token and candidate limits must be positive")
		}
		if f.ComparisonThresholds.MinContextYieldImprovement < 0 {
			return fmt.Errorf("minimum context-yield improvement must be non-negative")
		}
		if f.ComparisonThresholds.MaxSelectedTokenRatio <= 0 || f.ComparisonThresholds.MaxSelectedTokenRatio > 1 {
			return fmt.Errorf("max selected-token ratio must be >0 and <=1")
		}
	}
	return nil
}

// Run evaluates the retrieval fixture, computing raw retrieval metrics and,
// when a selector is configured, selector-adjusted metrics that must not
// regress recall or MRR relative to the raw results. It returns a report
// describing the metrics, any cases, and any threshold violations.
func Run(f Fixture) Report {
	symbols := make([]indexer.Symbol, 0, len(f.Symbols))
	for _, s := range f.Symbols {
		symbols = append(symbols, indexer.Symbol{Name: s.Name, Kind: s.Kind, Path: s.Path, Line: s.Line})
	}
	indexes := map[string]indexer.Index{
		f.Repository: {Version: 1, Repository: f.Repository, Symbols: symbols},
	}

	rawMetrics, rawCases, err := evaluateCases(f, indexes, nil)
	if err != nil {
		return Report{SchemaVersion: 1, Fixture: f.Name, K: f.K, Pass: false, Violations: []string{err.Error()}}
	}
	if f.Selector == nil {
		violations := f.Thresholds.Violations(rawMetrics)
		return Report{
			SchemaVersion: 1,
			Fixture:       f.Name,
			K:             f.K,
			Pass:          len(violations) == 0,
			Metrics:       rawMetrics,
			Violations:    violations,
			Cases:         rawCases,
		}
	}

	selectedMetrics, selectedCases, err := evaluateCases(f, indexes, f.Selector)
	if err != nil {
		return Report{SchemaVersion: 1, Fixture: f.Name, K: f.K, Pass: false, RawMetrics: &rawMetrics, Violations: []string{err.Error()}}
	}
	violations := f.Thresholds.Violations(selectedMetrics)
	if selectedMetrics.RecallAtK+1e-12 < rawMetrics.RecallAtK {
		violations = append(violations, fmt.Sprintf("recall_at_k %.6f regressed from raw %.6f", selectedMetrics.RecallAtK, rawMetrics.RecallAtK))
	}
	if selectedMetrics.MRR+1e-12 < rawMetrics.MRR {
		violations = append(violations, fmt.Sprintf("mrr %.6f regressed from raw %.6f", selectedMetrics.MRR, rawMetrics.MRR))
	}
	improvement := selectedMetrics.ContextYield - rawMetrics.ContextYield
	if improvement+1e-12 < f.ComparisonThresholds.MinContextYieldImprovement {
		violations = append(violations, fmt.Sprintf(
			"context_yield improvement %.6f < minimum %.6f",
			improvement,
			f.ComparisonThresholds.MinContextYieldImprovement,
		))
	}
	if rawMetrics.AvgRetrievedTokens > 0 {
		ratio := selectedMetrics.AvgRetrievedTokens / rawMetrics.AvgRetrievedTokens
		if ratio-1e-12 > f.ComparisonThresholds.MaxSelectedTokenRatio {
			violations = append(violations, fmt.Sprintf(
				"selected/raw token ratio %.6f > maximum %.6f",
				ratio,
				f.ComparisonThresholds.MaxSelectedTokenRatio,
			))
		}
	}
	sort.Strings(violations)
	return Report{
		SchemaVersion: 1,
		Fixture:       f.Name,
		K:             f.K,
		Pass:          len(violations) == 0,
		Metrics:       selectedMetrics,
		RawMetrics:    &rawMetrics,
		Violations:    violations,
		Cases:         selectedCases,
	}
}

// evaluateCases runs every case in the fixture against indexes, optionally
// applying MMR-based selection, and returns the aggregated metrics along with
// per-case results.
func evaluateCases(f Fixture, indexes map[string]indexer.Index, selector *SelectorFixture) (Metrics, []CaseResult, error) {
	var metrics Metrics
	results := make([]CaseResult, 0, len(f.Cases))
	var sumRecall1, sumRecallK, sumRR, sumPrecision, sumF1, sumYield, sumTokens float64
	var noGoldFalsePositives int

	for _, c := range f.Cases {
		var paths []string
		if selector == nil {
			hits := indexer.Search(c.Query, indexes, f.K)
			paths = uniquePaths(hits, f.K)
		} else {
			hits := indexer.Search(c.Query, indexes, selector.MaxCandidates)
			candidates := make([]retrieval.Candidate[indexer.Hit], 0, len(hits))
			for _, hit := range hits {
				candidates = append(candidates, retrieval.Candidate[indexer.Hit]{
					Key:             hit.Repository + "\x00" + hit.Path,
					Text:            hit.Symbol + " " + hit.Kind,
					Value:           hit,
					Relevance:       hit.Score,
					EstimatedTokens: f.FileTokenEstimates[hit.Path],
				})
			}
			selected, err := retrieval.SelectMMR(candidates, retrieval.SelectorOptions{
				MaxTokens:     selector.MaxTokens,
				MaxCandidates: selector.MaxCandidates,
				Lambda:        selector.Lambda,
			})
			if err != nil {
				return Metrics{}, nil, fmt.Errorf("case %s selector: %w", c.Name, err)
			}
			if selected.UsedTokens > selector.MaxTokens {
				return Metrics{}, nil, fmt.Errorf("case %s exceeded selector budget", c.Name)
			}
			paths = make([]string, 0, len(selected.Items))
			for _, item := range selected.Items {
				paths = append(paths, item.Value.Path)
			}
		}

		tokens := retrievedTokenEstimate(paths, f.FileTokenEstimates)
		result := CaseResult{
			Name:            c.Name,
			TaskType:        c.TaskType,
			Query:           c.Query,
			GoldPaths:       append([]string(nil), c.GoldPaths...),
			RetrievedPaths:  paths,
			RetrievedTokens: tokens,
		}

		if len(c.GoldPaths) == 0 {
			metrics.NoGoldCases++
			result.FalsePositive = len(paths) > 0
			if result.FalsePositive {
				noGoldFalsePositives++
			}
			results = append(results, result)
			continue
		}

		metrics.PositiveCases++
		gold := stringSet(c.GoldPaths)
		result.RecallAt1 = recall(paths, gold, 1)
		result.RecallAtK = recall(paths, gold, f.K)
		result.ReciprocalRank = reciprocalRank(paths, gold)
		result.FilePrecision = precision(paths, gold)
		result.FileF1 = f1(result.FilePrecision, result.RecallAtK)
		result.ContextYield = contextYield(paths, gold, f.FileTokenEstimates)

		sumRecall1 += result.RecallAt1
		sumRecallK += result.RecallAtK
		sumRR += result.ReciprocalRank
		sumPrecision += result.FilePrecision
		sumF1 += result.FileF1
		sumYield += result.ContextYield
		sumTokens += float64(tokens)
		results = append(results, result)
	}

	if n := float64(metrics.PositiveCases); n > 0 {
		metrics.RecallAt1 = sumRecall1 / n
		metrics.RecallAtK = sumRecallK / n
		metrics.MRR = sumRR / n
		metrics.FilePrecisionAtK = sumPrecision / n
		metrics.FileF1AtK = sumF1 / n
		metrics.ContextYield = sumYield / n
		metrics.AvgRetrievedTokens = sumTokens / n
	}
	if n := float64(metrics.NoGoldCases); n > 0 {
		metrics.NoGoldFalsePositiveRate = float64(noGoldFalsePositives) / n
	}
	return metrics, results, nil
}

// Violations compares metrics m against the thresholds and returns a sorted
// list of human-readable descriptions for every threshold that is violated.
func (t Thresholds) Violations(m Metrics) []string {
	var out []string
	minCheck := func(name string, got, want float64) {
		if got+1e-12 < want {
			out = append(out, fmt.Sprintf("%s %.6f < minimum %.6f", name, got, want))
		}
	}
	maxCheck := func(name string, got, want float64) {
		if got-1e-12 > want {
			out = append(out, fmt.Sprintf("%s %.6f > maximum %.6f", name, got, want))
		}
	}
	minCheck("recall_at_1", m.RecallAt1, t.MinRecallAt1)
	minCheck("recall_at_k", m.RecallAtK, t.MinRecallAtK)
	minCheck("mrr", m.MRR, t.MinMRR)
	minCheck("file_f1_at_k", m.FileF1AtK, t.MinFileF1AtK)
	minCheck("context_yield", m.ContextYield, t.MinContextYield)
	maxCheck("no_gold_false_positive_rate", m.NoGoldFalsePositiveRate, t.MaxNoGoldFalsePositive)
	maxCheck("avg_retrieved_tokens", m.AvgRetrievedTokens, t.MaxAvgRetrievedTokens)
	sort.Strings(out)
	return out
}

// uniquePaths returns up to limit distinct file paths from hits, preserving
// their relative order and skipping duplicates.
func uniquePaths(hits []indexer.Hit, limit int) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, limit)
	for _, hit := range hits {
		if _, ok := seen[hit.Path]; ok {
			continue
		}
		seen[hit.Path] = struct{}{}
		out = append(out, hit.Path)
		if len(out) == limit {
			break
		}
	}
	return out
}

// stringSet builds a set from the given items for O(1) membership checks.
func stringSet(items []string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, item := range items {
		out[item] = struct{}{}
	}
	return out
}

// recall returns the fraction of gold paths found among the first k
// (deduplicated) retrieved paths, or 0 if gold is empty.
func recall(paths []string, gold map[string]struct{}, k int) float64 {
	if len(gold) == 0 {
		return 0
	}
	if k > len(paths) {
		k = len(paths)
	}
	found := 0
	seen := map[string]struct{}{}
	for _, path := range paths[:k] {
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		if _, ok := gold[path]; ok {
			found++
		}
	}
	return float64(found) / float64(len(gold))
}

// reciprocalRank returns 1 divided by the rank of the first path in paths
// that belongs to gold, or 0 if no such path exists.
func reciprocalRank(paths []string, gold map[string]struct{}) float64 {
	for i, path := range paths {
		if _, ok := gold[path]; ok {
			return 1 / float64(i+1)
		}
	}
	return 0
}

// precision returns the fraction of paths that belong to gold, or 0 if paths is empty.
func precision(paths []string, gold map[string]struct{}) float64 {
	if len(paths) == 0 {
		return 0
	}
	found := 0
	for _, path := range paths {
		if _, ok := gold[path]; ok {
			found++
		}
	}
	return float64(found) / float64(len(paths))
}

// f1 returns the harmonic mean of precision and recall, or 0 if both are zero.
func f1(precision, recall float64) float64 {
	if precision+recall == 0 {
		return 0
	}
	return 2 * precision * recall / (precision + recall)
}

// retrievedTokenEstimate sums the per-file token estimates for the given paths.
func retrievedTokenEstimate(paths []string, estimates map[string]int) int {
	total := 0
	for _, path := range paths {
		total += estimates[path]
	}
	return total
}

// contextYield returns the fraction of retrieved tokens that come from gold
// paths, or 0 if no tokens were retrieved.
func contextYield(paths []string, gold map[string]struct{}, estimates map[string]int) float64 {
	total := 0
	relevant := 0
	for _, path := range paths {
		tokens := estimates[path]
		total += tokens
		if _, ok := gold[path]; ok {
			relevant += tokens
		}
	}
	if total == 0 {
		return 0
	}
	return float64(relevant) / float64(total)
}

// closeEnough reports whether a and b are equal within a small epsilon,
// guarding against floating-point rounding error.
func closeEnough(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

var _ = closeEnough
