package bench

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/brief"
	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/providers"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

// LoadTasks reads a JSON array of cases or a corpus-v2 document.
func LoadTasks(p string, research bool) ([]Case, error) {
	v, err := LoadJSON(p)
	if err != nil {
		return nil, err
	}
	var cases []Case
	switch x := v.(type) {
	case map[string]any:
		c := Corpus(x)
		if err := ValidateCorpus(c); err != nil {
			return nil, err
		}
		cases = c.cases()
	case []any:
		for _, raw := range x {
			if m, ok := raw.(map[string]any); ok {
				cases = append(cases, Case(m))
			}
		}
	default:
		return nil, errors.New("benchmark task file must be a JSON array or corpus-v2 object")
	}
	return cases, ValidateCases(cases, research)
}

func round4(f float64) float64 { return math.Round(f*1e4) / 1e4 }

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case *float64:
		if x != nil {
			return *x, true
		}
	}
	return 0, false
}

// mean over rows[key], ignoring missing/null values (V2 _mean).
func mean(rows []map[string]any, key string) any {
	sum, n := 0.0, 0
	for _, r := range rows {
		if f, ok := num(r[key]); ok {
			sum += f
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return round4(sum / float64(n))
}

func accuracy(rows []map[string]any, key string) any {
	hit, n := 0, 0
	for _, r := range rows {
		if b, ok := r[key].(bool); ok {
			n++
			if b {
				hit++
			}
		}
	}
	if n == 0 {
		return nil
	}
	return round4(float64(hit) / float64(n))
}

func sub(rows []map[string]any, key string) []map[string]any {
	out := []map[string]any{}
	for _, r := range rows {
		if m, ok := r[key].(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

var metricMeans = []struct{ out, group, key string }{
	{"mean_recall_at_k", "retrieval", "recall_at_k"}, {"mean_mrr", "retrieval", "mrr"},
	{"mean_relevant_item_density", "retrieval", "relevant_item_density"}, {"mean_pattern_yield_per_1k_tokens", "retrieval", "matched_patterns_per_1k_tokens"},
	{"mean_file_precision_at_k", "file_retrieval", "precision_at_k"}, {"mean_file_recall_at_k", "file_retrieval", "recall_at_k"},
	{"mean_file_mrr", "file_retrieval", "mrr"}, {"mean_file_ndcg_at_k", "file_retrieval", "ndcg_at_k"}, {"mean_file_f1", "file_retrieval", "file_f1"},
	{"mean_file_yield_per_1k_tokens", "file_retrieval", "matched_gold_files_per_1k_tokens"},
	{"mean_span_precision_at_k", "span_retrieval", "span_precision_at_k"}, {"mean_span_recall_at_k", "span_retrieval", "span_recall_at_k"},
	{"mean_span_f1", "span_retrieval", "span_f1"}, {"mean_line_precision", "span_retrieval", "line_precision"},
	{"mean_line_recall", "span_retrieval", "line_recall"}, {"mean_gold_line_yield_per_1k_tokens", "span_retrieval", "covered_gold_lines_per_1k_tokens"},
	{"mean_exploration_precision", "trajectory", "exploration_precision"}, {"mean_exploration_recall", "trajectory", "exploration_recall"},
	{"mean_utilization_precision", "trajectory", "utilization_precision"}, {"mean_utilization_recall", "trajectory", "utilization_recall"},
	{"mean_context_utilization_rate", "trajectory", "context_utilization_rate"}, {"mean_duplicate_exploration_rate", "trajectory", "duplicate_exploration_rate"},
	{"mean_edit_target_recall_at_k", "role_aware_retrieval", "edit_target_recall_at_k"},
	{"mean_supporting_context_recall_at_k", "role_aware_retrieval", "supporting_context_recall_at_k"},
	{"mean_edit_target_mrr", "role_aware_retrieval", "edit_target_mrr"}, {"mean_weighted_recall_at_k", "role_aware_retrieval", "weighted_recall_at_k"},
	{"mean_graded_ndcg_at_k", "role_aware_retrieval", "graded_ndcg_at_k"}, {"mean_known_distractor_rate_at_k", "role_aware_retrieval", "known_distractor_rate_at_k"},
	{"mean_coverage_balance", "role_aware_retrieval", "coverage_balance"},
	{"mean_retrieved_estimated_tokens", "context_economics", "retrieved_estimated_tokens"},
	{"mean_unique_retrieved_estimated_tokens", "context_economics", "unique_retrieved_estimated_tokens"},
	{"mean_duplicate_estimated_tokens", "context_economics", "duplicate_estimated_tokens"},
	{"mean_duplicate_token_fraction", "context_economics", "duplicate_token_fraction"},
	{"mean_ranked_candidate_estimated_tokens", "context_economics", "ranked_candidate_estimated_tokens"},
	{"mean_selected_estimated_tokens", "context_economics", "selected_estimated_tokens"},
	{"mean_selected_vs_retrieved_token_ratio", "context_economics", "selected_vs_retrieved_token_ratio"},
	{"mean_estimated_token_reduction_vs_retrieved", "context_economics", "estimated_token_reduction_vs_retrieved"},
	{"mean_selected_hard_budget_utilization", "context_economics", "selected_hard_budget_utilization"},
	{"mean_selected_adaptive_budget_utilization", "context_economics", "selected_adaptive_budget_utilization"},
	{"mean_retrieval_call_count", "context_economics", "retrieval_call_count"},
	{"mean_gold_file_token_share", "context_economics", "gold_file_token_share"},
	{"mean_known_distractor_token_share", "context_economics", "known_distractor_token_share"},
	{"mean_gold_file_tokens_per_1k_retrieved", "context_economics", "gold_file_tokens_per_1k_retrieved"},
}

func metricSummary(rows []map[string]any, out map[string]any) {
	for _, m := range metricMeans {
		out[m.out] = mean(sub(rows, m.group), m.key)
	}
}

func groupSummary(rows []map[string]any) map[string]any {
	out := map[string]any{"cases": len(rows), "lane_accuracy": accuracy(rows, "lane_correct"), "intent_accuracy": accuracy(rows, "intent_correct"),
		"evidence_state_accuracy": accuracy(rows, "evidence_state_correct"), "selective_control_accuracy": accuracy(rows, "selective_control_correct")}
	metricSummary(rows, out)
	return out
}

// Options controls one benchmark run.
type Options struct {
	Research      bool
	RequireFrozen bool
	// Config overrides the workspace configuration (ablation profiles).
	Config *config.Config
}

// Run executes every case through the real brief pipeline (no persistence,
// one repository per case) and scores routing and context quality.
func Run(ctx context.Context, root string, cases []Case, o Options) (map[string]any, error) {
	if err := ValidateCases(cases, o.Research); err != nil {
		return nil, err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	if o.Config != nil {
		cfg = *o.Config
	}
	reg, err := workspace.Load(root)
	if err != nil {
		return nil, err
	}
	status := providers.Detect(root, cfg, reg)
	rows := []map[string]any{}
	for _, c := range cases {
		task := c.Str("task")
		if _, err := ResolveCaseRoot(root, c); err != nil {
			return nil, err
		}
		snapshot := SnapshotStatus(root, c)
		if o.RequireFrozen && snapshot["status"] != "match" {
			return nil, fmt.Errorf("benchmark snapshot is not frozen-clean: %s status=%v expected=%v actual=%v worktree_clean=%v content_manifest_match=%v",
				snapshot["repository_path"], snapshot["status"], snapshot["expected_head"], snapshot["actual_head"], snapshot["worktree_clean"], snapshot["content_manifest_match"])
		}
		budgetOverride, _ := c.Int("budget_tokens", 0)
		changed, _ := c.Strings("changed_files")
		start := time.Now()
		p, err := brief.Build(ctx, root, task, brief.Options{Symbol: c.Str("symbol"), Endpoint: c.Str("endpoint"), ChangedFiles: changed,
			ReadOnly: true, OnlyRepository: c.RepositoryPath(), BudgetTokens: budgetOverride, Config: &cfg})
		if err != nil {
			return nil, fmt.Errorf("case %q: %w", task, err)
		}
		elapsed := math.Round(float64(time.Since(start).Microseconds())/10) / 100
		texts := make([]string, 0, len(p.Context))
		for _, it := range p.Context {
			texts = append(texts, it.Text)
		}
		used := estimateTokens(strings.Join(texts, "\n"))
		k, _ := c.Int("retrieval_k", 5)
		control := orDefault(c.Str("control_type"), "positive")
		queryType := orDefault(c.Str("query_type"), "unclassified")
		r := p.Retrieval
		sources := []string{}
		for _, it := range p.Context {
			if !containsStr(sources, it.Source) {
				sources = append(sources, it.Source)
			}
		}
		util := 0.0
		if p.Budget.EstimatedContextTokens > 0 {
			util = round4(float64(used) / float64(p.Budget.EstimatedContextTokens))
		}
		row := map[string]any{"task": task, "task_type": orDefault(c.Str("task_type"), queryType), "query_type": queryType,
			"control_type": control, "repository_path": c.RepositoryPath(), "repository_id": c["repository_id"], "case_id": c["case_id"],
			"language": c["language"], "label_source": c["label_source"], "snapshot": snapshot, "lane": p.Lane, "risk": p.Risk,
			"routing_confidence": p.Confidence, "execution_provider": p.ExecutionProvider, "model_tier": p.ModelTier, "providers": status,
			"retrieval_intent": r.RetrievalIntent, "algorithm_policy": algorithmPolicy(cfg),
			"retrieval_sufficient": r.Sufficiency.Sufficient, "retrieval_sufficiency_score": r.Sufficiency.Score,
			"evidence_state": r.EvidenceState, "selector_mode": "mmr", "workspace_fingerprint": r.WorkspaceState.Fingerprint,
			"orchestration_complexity_score": r.Orchestration.ComplexityScore, "fallbacks": r.Fallbacks, "context_sources": sources,
			"estimated_context_tokens": used, "budget_tokens": p.Budget.EstimatedContextTokens, "budget_utilization": util, "elapsed_ms": elapsed}
		if v := c.Str("expected_lane"); v != "" {
			row["lane_correct"] = string(p.Lane) == v
		}
		if v := c.Str("expected_intent"); v != "" {
			row["intent_correct"] = r.RetrievalIntent == v
		}
		if v := c.Str("expected_evidence_state"); v != "" {
			row["evidence_state_correct"] = r.EvidenceState == v
			if noGold, _ := c["no_gold"].(bool); noGold && v == "abstain" {
				row["abstention_correct"] = r.EvidenceState == "abstain"
			}
		}
		if control == "natural_no_gold" || control == "wrong_repo" {
			row["selective_control_correct"] = r.EvidenceState == orDefault(c.Str("expected_evidence_state"), "abstain")
		}
		patterns, _ := c.Strings("relevant_context")
		if m := PatternMetrics(p.Context, patterns, k); m != nil {
			m["matched_patterns_per_1k_tokens"] = round4(float64(m["matched_patterns"].(int)) * 1000 / float64(max(1, used)))
			row["retrieval"] = m
		}
		gold, _ := c.Strings("gold_files")
		if m := FileMetrics(p.Context, gold, k); m != nil {
			m["matched_gold_files_per_1k_tokens"] = round4(float64(len(m["matched_gold_files"].([]string))) * 1000 / float64(max(1, used)))
			row["file_retrieval"] = m
		}
		distractors, _ := c.Strings("distractor_files")
		if m := RoleMetrics(p.Context, c.list("file_relevance"), distractors, k); m != nil {
			row["role_aware_retrieval"] = m
		}
		row["context_economics"] = TokenEconomics(p.Candidates, p.Context, c, r.HardContextTokens, r.AdaptiveContextTokens, len(r.ProvidersAttempted), elapsed)
		budgetLines, _ := c.Int("budget_lines", 0)
		sm, err := SpanMetrics(p.Context, c.list("gold_spans"), k, budgetLines)
		if err != nil {
			return nil, err
		}
		if sm != nil {
			sm["covered_gold_lines_per_1k_tokens"] = round4(float64(sm["covered_gold_lines"].(int)) * 1000 / float64(max(1, used)))
			row["span_retrieval"] = sm
		}
		forbidden, _ := c.Strings("forbidden_repositories")
		if m := RepositoryControlMetrics(p.Context, forbidden, k); m != nil {
			row["repository_control"] = m
		}
		events, err := LoadTrajectory(root, c)
		if err != nil {
			return nil, err
		}
		if m := TrajectoryMetrics(events, gold); m != nil {
			row["trajectory"] = m
		}
		rows = append(rows, row)
	}
	return report(rows, status, o.Research), nil
}

func algorithmPolicy(cfg config.Config) map[string]any {
	ranker, rrfK, lambda, noEarly := cfg.Context.Experiments.Policy()
	return map[string]any{"hybrid_ranker": ranker, "rrf_k": rrfK, "mmr_lambda": lambda, "disable_early_sufficiency_gate": noEarly,
		"selector_enabled": cfg.Context.Selector.Enabled, "adaptive_budget_enabled": cfg.Context.AdaptiveBudget.Enabled}
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func report(rows []map[string]any, status providers.Status, research bool) map[string]any {
	group := func(key string) map[string]any {
		groups := map[string][]map[string]any{}
		for _, r := range rows {
			k := fmt.Sprint(r[key])
			groups[k] = append(groups[k], r)
		}
		out := map[string]any{}
		names := make([]string, 0, len(groups))
		for k := range groups {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			out[k] = groupSummary(groups[k])
		}
		return out
	}
	summary := map[string]any{"cases": len(rows), "mean_estimated_context_tokens": 0.0, "mean_budget_utilization": 0.0, "mean_elapsed_ms": 0.0,
		"lane_accuracy": accuracy(rows, "lane_correct"), "intent_accuracy": accuracy(rows, "intent_correct"),
		"evidence_state_accuracy": accuracy(rows, "evidence_state_correct"), "abstention_accuracy": accuracy(rows, "abstention_correct"),
		"selective_control_accuracy": accuracy(rows, "selective_control_correct"),
		"mean_precision_at_k":        mean(sub(rows, "retrieval"), "precision_at_k"), "mean_ndcg_at_k": mean(sub(rows, "retrieval"), "ndcg_at_k"),
		"frozen_snapshot_match_rate": nil, "sufficiency_rate": 0.0, "fallback_rate": 0.0}
	metricSummary(rows, summary)
	if len(rows) > 0 {
		var tok, util, el, suff, fb, frozen, frozenHit float64
		for _, r := range rows {
			tok += float64(r["estimated_context_tokens"].(int))
			util += r["budget_utilization"].(float64)
			el += r["elapsed_ms"].(float64)
			if r["retrieval_sufficient"] == true {
				suff++
			}
			if fbs, _ := r["fallbacks"].([]string); len(fbs) > 0 {
				fb++
			}
			if snap := r["snapshot"].(map[string]any); snap["match"] != nil {
				frozen++
				if snap["match"] == true {
					frozenHit++
				}
			}
		}
		n := float64(len(rows))
		summary["mean_estimated_context_tokens"] = math.Round(tok/n*100) / 100
		summary["mean_budget_utilization"], summary["mean_elapsed_ms"] = round4(util/n), math.Round(el/n*100)/100
		summary["sufficiency_rate"], summary["fallback_rate"] = round4(suff/n), round4(fb/n)
		if frozen > 0 {
			summary["frozen_snapshot_match_rate"] = round4(frozenHit / frozen)
		}
	}
	return map[string]any{
		"scope": "routing-and-context-only",
		"research_protocol": map[string]any{"file_level_gold_supported": true, "span_level_gold_supported": true,
			"fixed_line_budget_scoring_supported": true, "clean_content_snapshot_check_supported": true, "frozen_snapshot_check_supported": true,
			"selective_controls_supported": true, "multi_repo_case_isolation_supported": true, "persistent_project_knowledge_isolated": research,
			"trajectory_utilization_metrics_supported": true, "role_aware_file_relevance_supported": true, "known_distractor_scoring_supported": true,
			"context_token_economics_supported": true, "task_types": []string{"code2test", "comment2context", "trace2code", "edit2ripple", "no_gold"}},
		"warning":   "Estimated context tokens are not provider-billed tokens. Retrieval metrics measure supplied gold patterns/files/spans, optional edit/support roles, labelled distractors, and provider-neutral token estimates; they do not prove downstream task correctness or provider-billed cost.",
		"providers": status, "cases": rows, "by_query_type": group("query_type"), "by_task_type": group("task_type"), "summary": summary,
	}
}
