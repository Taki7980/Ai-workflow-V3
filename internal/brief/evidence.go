// Package brief assembles the V2-compatible agent brief: bounded context,
// evidence state, workspace fingerprint and orchestration contract.
package brief

import (
	"strconv"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/retrieval"
)

// Sufficiency is the V2 retrieval_policy.Sufficiency contract.
type Sufficiency struct {
	Score              float64 `json:"score"`
	Sufficient         bool    `json:"sufficient"`
	LexicalCoverage    float64 `json:"lexical_coverage"`
	SourceDiversity    int     `json:"source_diversity"`
	ExactMatch         bool    `json:"exact_match"`
	StructuralComplete bool    `json:"structural_complete"`
}

// Selective is the V2 SelectiveRetrievalDecision contract.
type Selective struct {
	Condition string   `json:"condition"`
	Accept    bool     `json:"accept"`
	Score     float64  `json:"score"`
	Reasons   []string `json:"reasons"`
}

// round4 matches CPython round(x, 4) (correctly rounded decimal).
func round4(x float64) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 4, 64), 64)
	return v
}

func squash(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), "") }

// EvaluateSufficiency ports V2 evaluate_sufficiency.
func EvaluateSufficiency(query string, items []model.ContextItem, structuralRequired bool, patterns []string, threshold float64) Sufficiency {
	if len(items) == 0 {
		return Sufficiency{}
	}
	q := map[string]bool{}
	for _, t := range retrieval.Tokenize(query) {
		q[t] = true
	}
	matched, sources, structural := map[string]bool{}, map[string]bool{}, map[string]bool{}
	exact := false
	// ponytail: ToLower approximates casefold (ß/ς differ); swap for x/text/cases if non-ASCII queries matter.
	nq := squash(query)
	for _, it := range items {
		sources[it.Source] = true
		for _, t := range retrieval.Tokenize(it.Text) {
			if q[t] {
				matched[t] = true
			}
		}
		if nq != "" && strings.Contains(squash(it.Text), nq) {
			exact = true
		}
		if valid, _ := it.Metadata["structural_valid"].(bool); valid {
			if p, _ := it.Metadata["pattern"].(string); strings.TrimSpace(p) != "" {
				structural[strings.TrimSpace(p)] = true
			}
		}
	}
	complete := true
	if structuralRequired {
		if len(patterns) > 0 {
			for _, p := range patterns {
				complete = complete && structural[p]
			}
		} else {
			complete = len(structural) > 0
		}
	}
	coverage := float64(len(matched)) / float64(max(1, len(q)))
	diversity := len(sources)
	score := 0.58*coverage + 0.17*min(1.0, float64(diversity)/2) + 0.15*b2f(exact) + 0.10*b2f(complete)
	score = max(0.0, min(1.0, score))
	return Sufficiency{
		Score:              round4(score),
		Sufficient:         score >= threshold && (complete || !structuralRequired),
		LexicalCoverage:    round4(coverage),
		SourceDiversity:    diversity,
		ExactMatch:         exact,
		StructuralComplete: complete,
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// EvaluateSelective ports V2 evaluate_selective_retrieval. Repository identity
// is read from item.Metadata["repository_id"].
// ponytail: verified structural-conflict check omitted; no structural items exist until the CRG/SCIP adapter lands.
func EvaluateSelective(items []model.ContextItem, s Sufficiency, expectedRepoIDs map[string]bool, minCoverage float64) Selective {
	if len(items) == 0 {
		return Selective{"no_context", false, 0, []string{"no_selected_context"}}
	}
	if len(expectedRepoIDs) > 0 {
		for _, it := range items {
			if id, _ := it.Metadata["repository_id"].(string); id != "" && !expectedRepoIDs[id] {
				return Selective{"wrong_repository", false, 0, []string{"evidence_repository_outside_routing_plan"}}
			}
		}
	}
	fresh := false
	for _, it := range items {
		fresh = fresh || !it.Stale
	}
	if !fresh {
		return Selective{"irrelevant", false, 0, []string{"all_selected_evidence_is_stale"}}
	}
	reasons := []string{}
	if !s.StructuralComplete {
		reasons = append(reasons, "required_structural_evidence_incomplete")
	}
	if s.LexicalCoverage < minCoverage {
		reasons = append(reasons, "query_coverage_below_floor")
	}
	if !s.Sufficient {
		reasons = append(reasons, "sufficiency_below_threshold")
	}
	if len(reasons) > 0 {
		cond := "partial"
		if s.LexicalCoverage < minCoverage {
			cond = "irrelevant"
		}
		return Selective{cond, false, s.Score, reasons}
	}
	return Selective{"supported", true, s.Score, []string{"evidence_quality_gate_passed"}}
}

// EvidenceState ports V2 workflow_engine._evidence_state.
func EvidenceState(lane model.Lane, sufficient bool) string {
	switch {
	case sufficient:
		return "sufficient"
	case lane == model.LaneAnswer:
		return "abstain"
	}
	return "requires_exploration"
}
