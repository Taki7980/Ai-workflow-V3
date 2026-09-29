package eval

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"

	"github.com/Taki7980/ai-workflow-v3/internal/indexer"
)

type Fixture struct {
	SchemaVersion      int               `json:"schema_version"`
	Name               string            `json:"name"`
	K                  int               `json:"k"`
	Repository         string            `json:"repository"`
	Symbols            []SymbolFixture   `json:"symbols"`
	FileTokenEstimates map[string]int    `json:"file_token_estimates"`
	Cases              []RetrievalCase   `json:"cases"`
	Thresholds         Thresholds        `json:"thresholds"`
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

type Thresholds struct {
	MinRecallAt1          float64 `json:"min_recall_at_1"`
	MinRecallAtK          float64 `json:"min_recall_at_k"`
	MinMRR                float64 `json:"min_mrr"`
	MinFileF1AtK          float64 `json:"min_file_f1_at_k"`
	MinContextYield       float64 `json:"min_context_yield"`
	MaxNoGoldFalsePositive float64 `json:"max_no_gold_false_positive_rate"`
	MaxAvgRetrievedTokens float64 `json:"max_avg_retrieved_tokens"`
}

type Metrics struct {
	PositiveCases            int     `json:"positive_cases"`
	NoGoldCases              int     `json:"no_gold_cases"`
	RecallAt1                float64 `json:"recall_at_1"`
	RecallAtK                float64 `json:"recall_at_k"`
	MRR                      float64 `json:"mrr"`
	FilePrecisionAtK         float64 `json:"file_precision_at_k"`
	FileF1AtK                float64 `json:"file_f1_at_k"`
	ContextYield             float64 `json:"context_yield"`
	AvgRetrievedTokens       float64 `json:"avg_retrieved_tokens"`
	NoGoldFalsePositiveRate  float64 `json:"no_gold_false_positive_rate"`
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
	Violations    []string     `json:"violations"`
	Cases         []CaseResult `json:"cases"`
}

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
	return nil
}

func Run(f Fixture) Report {
	symbols := make([]indexer.Symbol, 0, len(f.Symbols))
	for _, s := range f.Symbols {
		symbols = append(symbols, indexer.Symbol{Name: s.Name, Kind: s.Kind, Path: s.Path, Line: s.Line})
	}
	indexes := map[string]indexer.Index{
		f.Repository: {Version: 1, Repository: f.Repository, Symbols: symbols},
	}

	report := Report{
		SchemaVersion: 1,
		Fixture:       f.Name,
		K:             f.K,
		Pass:          true,
		Violations:    []string{},
		Cases:         []CaseResult{},
	}

	var sumRecall1, sumRecallK, sumRR, sumPrecision, sumF1, sumYield, sumTokens float64
	var noGoldFalsePositives int

	for _, c := range f.Cases {
		hits := indexer.Search(c.Query, indexes, f.K)
		paths := uniquePaths(hits, f.K)
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
			report.Metrics.NoGoldCases++
			result.FalsePositive = len(paths) > 0
			if result.FalsePositive {
				noGoldFalsePositives++
			}
			report.Cases = append(report.Cases, result)
			continue
		}

		report.Metrics.PositiveCases++
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
		report.Cases = append(report.Cases, result)
	}

	if n := float64(report.Metrics.PositiveCases); n > 0 {
		report.Metrics.RecallAt1 = sumRecall1 / n
		report.Metrics.RecallAtK = sumRecallK / n
		report.Metrics.MRR = sumRR / n
		report.Metrics.FilePrecisionAtK = sumPrecision / n
		report.Metrics.FileF1AtK = sumF1 / n
		report.Metrics.ContextYield = sumYield / n
		report.Metrics.AvgRetrievedTokens = sumTokens / n
	}
	if n := float64(report.Metrics.NoGoldCases); n > 0 {
		report.Metrics.NoGoldFalsePositiveRate = float64(noGoldFalsePositives) / n
	}

	report.Violations = f.Thresholds.Violations(report.Metrics)
	report.Pass = len(report.Violations) == 0
	return report
}

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

func stringSet(items []string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, item := range items {
		out[item] = struct{}{}
	}
	return out
}

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

func reciprocalRank(paths []string, gold map[string]struct{}) float64 {
	for i, path := range paths {
		if _, ok := gold[path]; ok {
			return 1 / float64(i+1)
		}
	}
	return 0
}

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

func f1(precision, recall float64) float64 {
	if precision+recall == 0 {
		return 0
	}
	return 2 * precision * recall / (precision + recall)
}

func retrievedTokenEstimate(paths []string, estimates map[string]int) int {
	total := 0
	for _, path := range paths {
		total += estimates[path]
	}
	return total
}

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

func closeEnough(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

var _ = closeEnough
