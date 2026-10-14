package brief

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/capability"
	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/handoff"
	"github.com/Taki7980/ai-workflow-v3/internal/indexer"
	"github.com/Taki7980/ai-workflow-v3/internal/learning"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/providers"
	"github.com/Taki7980/ai-workflow-v3/internal/retrieval"
	"github.com/Taki7980/ai-workflow-v3/internal/routing"
	"github.com/Taki7980/ai-workflow-v3/internal/storage"
	"github.com/Taki7980/ai-workflow-v3/internal/telemetry"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

// Options carries optional retrieval anchors and side effects for Build.
type Options struct {
	Symbol       string
	Endpoint     string
	ChangedFiles []string
	WriteHandoff bool
	// ReadOnly skips last-brief/handoff persistence (V2 `context`).
	ReadOnly bool
	// Trace forces a trace + run journal even for Answer lanes or read-only runs.
	Trace bool
	// OnlyRepository restricts retrieval to one registered repository
	// (benchmark case isolation, V2 _isolated_case_config).
	OnlyRepository string
	// BudgetTokens overrides the lane's hard context budget when > 0.
	BudgetTokens int
	// Config overrides the workspace configuration (benchmark ablations).
	Config *config.Config
}

// loadIndexes returns the included repositories, their loadable indexes,
// the index-file map used for the workspace fingerprint (nil when no index
// loaded), and the repositories whose index could not be loaded.
func loadIndexes(root string, reg workspace.Registry) ([]workspace.Repository, map[string]any, map[string]indexer.Index, []string) {
	included := []workspace.Repository{}
	indexes := map[string]indexer.Index{}
	missing := []string{}
	for _, repo := range reg.Repositories {
		if !repo.Included {
			continue
		}
		included = append(included, repo)
		idx, err := indexer.Load(root, repo)
		if err != nil {
			missing = append(missing, repo.RelativePath)
			continue
		}
		indexes[repo.RelativePath] = idx
	}
	var indexFiles map[string]any
	if len(indexes) > 0 {
		indexFiles = map[string]any{}
		for rel, idx := range indexes {
			indexFiles[rel] = idx.Files
		}
	}
	return included, indexFiles, indexes, missing
}

// LastBriefPath is where non-answer briefs are persisted for downstream tools.
const LastBriefPath = "ai-workspace/generated/last-brief.json"

var hints = map[string]string{
	"superpowers": "Follow the emitted Superpowers skill sequence and CRG plan; do not edit while evidence_state=requires_exploration.",
	"small":       "Use native lightweight execution.",
	"full":        "Use native Plan -> Build -> Review fallback.",
	"answer":      "Answer directly; no implementation workflow.",
}

func executionHint(provider string, lane model.Lane) string {
	if provider == "superpowers" {
		return hints["superpowers"]
	}
	return hints[string(lane)]
}

func laneBudget(cfg config.Config, lane model.Lane) config.Budget {
	switch lane {
	case model.LaneAnswer:
		return cfg.Budgets.Answer
	case model.LaneSmall:
		return cfg.Budgets.Small
	}
	return cfg.Budgets.Full
}

// adaptiveTokens ports V2 _adaptive_token_limit.
func adaptiveTokens(budget int, score float64, ab config.AdaptiveBudget) int {
	if !ab.Enabled {
		return budget
	}
	fraction := 1.0
	switch {
	case score >= 0.86:
		fraction = ab.HighSufficiencyFraction
	case score >= 0.72:
		fraction = ab.MediumFraction
	}
	return min(budget, max(1, int(float64(budget)*fraction)))
}

func estimateTokens(text string) int { return (len(text) + 3) / 4 }

// Build routes the task, gathers bounded fresh evidence across included
// repositories and assembles the agent brief.
func Build(ctx context.Context, root, task string, opt Options) (Packet, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return Packet{}, err
	}
	if opt.Config != nil {
		cfg = *opt.Config
	}
	reg, err := workspace.Load(root)
	if err != nil {
		return Packet{}, err
	}
	decision := routing.Classify(task, cfg)
	plan := routing.PlanRetrievalWithAnchors(task, decision, opt.Symbol, opt.Endpoint)
	status := providers.Detect(root, cfg, reg)
	provider := providers.ExecutionProvider(decision.Lane, cfg, status)

	changed := opt.ChangedFiles
	if len(changed) == 0 {
		changed = workspace.ChangedFiles(ctx, root, reg)
	}

	g := &gathered{}
	included, indexFiles, indexes, missing := loadIndexes(root, reg)
	enabled, maxPrimary, maxExpansions, rels := cfg.Workspace.Hierarchical.Resolved()
	route := workspace.Route(root, reg.Repositories, strings.Join([]string{task, opt.Symbol, opt.Endpoint}, " "), changed, workspace.RouteOptions{
		Enabled: enabled, MaxPrimary: maxPrimary, MaxExpansions: maxExpansions, Relationships: rels,
		MaxRoots: cfg.Workspace.MaxRoots, GraphPath: cfg.Workspace.RepositoryGraph,
	})
	included = included[:0:0]
	for _, r := range route.Repositories {
		included = append(included, r.Repository)
	}
	if opt.OnlyRepository != "" {
		only := strings.Trim(filepath.ToSlash(opt.OnlyRepository), "/")
		if only == "" {
			only = "."
		}
		included = included[:0:0]
		for _, r := range reg.Repositories {
			if r.Included && r.RelativePath == only {
				included = append(included, r)
			}
		}
		if len(included) == 0 {
			return Packet{}, fmt.Errorf("repository_path %q is not an included repository; run `ai-workflow repos refresh` and `repos include`", only)
		}
	}
	expected := map[string]bool{}
	roots := []string{}
	for _, repo := range included {
		expected[repo.RepositoryID] = true
		roots = append(roots, filepath.Join(root, filepath.FromSlash(repo.RelativePath)))
	}
	for _, rel := range missing {
		g.note("index unavailable: %s", rel)
	}
	latency := map[string]float64{}
	stage := func(name string, start time.Time) {
		latency[name] = float64(time.Since(start).Microseconds()) / 1000
	}
	began := time.Now()
	// Stage 5 learning: the arm is chosen and logged before retrieval runs.
	var learned *learning.Decision
	learnPath := ""
	if cfg.Context.Learning.ModeOf() != "off" && (!opt.ReadOnly || opt.Trace) {
		dec, path := learning.Prepare(root, cfg.Context.Learning, task, decision, string(plan.Intent), len(changed), len(included), nil)
		if armCfg, err := learning.ApplyArm(cfg, dec.ChosenArm); err == nil {
			cfg = armCfg
		}
		learned, learnPath = &dec, path
	}
	query := strings.TrimSpace(strings.Join([]string{task, opt.Symbol, opt.Endpoint}, " "))
	t := time.Now()
	gatherIndex(g, root, query, included, indexes, cfg)
	stage("index", t)
	t = time.Now()
	gatherTargeted(ctx, g, root, query, included, indexes, cfg.Context.MaxResultsPerSource)
	stage("targeted_source", t)
	t = time.Now()
	gatherMemory(g, root, query, cfg)
	stage("memory", t)
	skipped, providerErrors := map[string]string{}, map[string]string{}
	// V2 early sufficiency gate: specialist providers run only when the base
	// evidence (index, targeted source, memory) is insufficient.
	base := EvaluateSufficiency(task, g.items, plan.UseStructural, plan.StructuralPatterns, cfg.Context.Sufficiency.Threshold)
	if _, _, _, noEarlyStop := cfg.Context.Experiments.Policy(); noEarlyStop {
		base.Sufficient = false
	}
	switch {
	case plan.UseSemantic && base.Sufficient:
		skipped["semantic"] = "base evidence sufficient"
	case plan.UseSemantic:
		t = time.Now()
		gatherSemantic(ctx, g, root, query, included, indexes, cfg, changed, skipped, providerErrors)
		stage("semantic", t)
	}
	if len(cfg.Context.ExternalRetrievers) > 0 && !base.Sufficient {
		t = time.Now()
		gatherExternal(ctx, g, root, query, string(plan.Intent), included, cfg, changed, providerErrors)
		stage("external", t)
	}

	threshold := cfg.Context.Sufficiency.Threshold
	pre := EvaluateSufficiency(task, g.items, plan.UseStructural, plan.StructuralPatterns, threshold)
	structuralRan := false
	if plan.UseStructural && !pre.StructuralComplete {
		t = time.Now()
		structuralRan = expandStructural(ctx, g, root, task, opt, plan, included, changed, cfg, threshold, skipped, providerErrors)
		pre = EvaluateSufficiency(task, g.items, plan.UseStructural, plan.StructuralPatterns, threshold)
		stage("structural", t)
	}
	budget := laneBudget(cfg, decision.Lane)
	if opt.BudgetTokens > 0 {
		budget.EstimatedTokens = opt.BudgetTokens
	}
	limit := adaptiveTokens(budget.EstimatedTokens, pre.Score, cfg.Context.AdaptiveBudget)
	mandatory := plan.UseStructural && cfg.Context.Selector.MandatoryStructuralEvidence
	t = time.Now()
	selected := capBySource(rankAndSelect(g, query, limit, cfg, plan.StructuralPatterns, mandatory),
		budget.EstimatedTokens, cfg.Context.SourceShares, cfg.Context.MaxResultsPerSource, plan.StructuralPatterns, mandatory)
	stage("selection", t)

	rootID := workspace.RepositoryID(".", "")
	for _, r := range reg.Repositories {
		if r.RelativePath == "." {
			rootID = r.RepositoryID
		}
	}
	for i := range selected {
		id, _ := selected[i].Metadata["repository_id"].(string)
		if id == "" {
			id = rootID
		}
		selected[i].Evidence = model.NewEvidence(selected[i], id)
	}
	final := EvaluateSufficiency(task, selected, plan.UseStructural, plan.StructuralPatterns, threshold)
	sel := cfg.Context.SelectiveRetrieval
	selective := EvaluateSelective(selected, final, expected, sel.MinimumCoverage)
	state := EvidenceState(decision.Lane, final.Sufficient && (selective.Accept || !sel.Enabled))
	if plan.UseStructural && !final.StructuralComplete {
		g.note("structural evidence incomplete; source fallback used")
	}

	if plan.UseStructural && !structuralRan {
		skipped["structural"] = "no structural provider ran"
	}
	patterns := plan.StructuralPatterns
	if patterns == nil {
		patterns = []string{}
	}
	if changed == nil {
		changed = []string{}
	}

	texts := make([]string, 0, len(selected))
	for _, it := range selected {
		texts = append(texts, it.Text)
	}
	compression := "builtin"
	if status.RTK {
		compression = "rtk"
	}
	p := Packet{
		Task:              task,
		RouteDecision:     decision,
		ExecutionProvider: provider,
		ModelTier:         providers.ModelTier(decision, cfg),
		ExecutionHint:     executionHint(provider, decision.Lane),
		Budget:            Budget{EstimatedContextTokens: budget.EstimatedTokens, MaxOutputTokens: budget.OutputTokens},
		Retrieval: RetrievalDiagnostics{
			RetrievalIntent:       string(plan.Intent),
			RetrievalReason:       plan.Reason,
			WorkspaceRoots:        roots,
			WorkspaceState:        workspace.Fingerprint(ctx, root, indexFiles, changed),
			EvidenceState:         state,
			EvidenceContract:      EvidenceContract{Schema: "evidence-v1", SelectedCount: len(selected), Authority: "evidence_only"},
			Sufficiency:           SufficiencyReport{Sufficiency: final, StructuralPatterns: patterns},
			SelectiveRetrieval:    SelectiveReport{Selective: selective, Enabled: sel.Enabled},
			AdaptiveContextTokens: limit,
			HardContextTokens:     budget.EstimatedTokens,
			ProvidersAttempted:    g.attempted,
			ProvidersSkipped:      skipped,
			ProviderErrors:        providerErrors,
			Fallbacks:             append([]string{}, g.fallbacks...),
			RepositoryRouting:     &route,
			Orchestration:         BuildOrchestration(decision, string(plan.Intent), final.Sufficient, sel.Enabled, selective.Accept, changed, len(included), status, cfg.Execution.OrchestrationBudget),
		},
		Context:                    selected,
		EstimatedContextTokensUsed: estimateTokens(strings.Join(texts, "\n")),
		OutputCompression:          compression,
		ChangedFilesDetected:       changed,
		Candidates:                 g.items,
	}
	if learned != nil {
		info := map[string]any{"decision_id": learned.DecisionID, "mode": learned.Mode, "chosen_arm": learned.ChosenArm,
			"explored": learned.Explored, "safety_reason": learned.SafetyReason, "decision_logged": learnPath != "", "observation_logged": false}
		if learnPath != "" {
			used := 0
			for _, it := range selected {
				used += len(it.Text)
			}
			if _, err := learning.Observe(root, learned.DecisionID, float64(time.Since(began).Microseconds())/1000, used, len(g.fallbacks), final.Score, state); err == nil {
				info["observation_logged"] = true
			}
		}
		p.Retrieval.Learning = info
	}
	orch := p.Retrieval.Orchestration
	policy := capability.Build(decision.Lane, decision.Risk, orch.CRGPlan, orch.VerificationPasses, orch.GraphDepth, orch.Budget.MaxGraphDepth, selected, task)
	p.Retrieval.AuthorizationPolicy = &policy
	if opt.Trace || (!opt.ReadOnly && decision.Lane != model.LaneAnswer) {
		if telemetry.Enabled(cfg, decision.Lane, opt.Trace) {
			stage("total", began)
			record(ctx, root, task, cfg, &p, g.items, selected, included, latency, g.stale)
		}
	}
	if decision.Lane == model.LaneAnswer || opt.ReadOnly {
		return p, nil
	}
	if opt.WriteHandoff {
		sources := make([]string, 0, len(selected))
		for _, it := range selected {
			sources = append(sources, it.Source)
		}
		text := handoff.Render(decision, provider, sources, task)
		if err := storage.WriteFileAtomic(handoff.Path(root), []byte(text)); err != nil {
			return Packet{}, err
		}
		p.HandoffWritten = handoff.RelativePath
	}
	if err := storage.WriteJSON(filepath.Join(root, filepath.FromSlash(LastBriefPath)), p); err != nil {
		return Packet{}, err
	}
	return p, nil
}

// selectItems applies MMR within the token limit, falling back to
// relevance-order truncation if the selector rejects the candidate set.
// shareKey maps an evidence source to its context.source_shares bucket.
var shareKey = map[string]string{"lightweight_index": "lightweight", "targeted_source": "source_fallback",
	"code_review_graph": "crg", "scip": "crg", "durable_memory": "hot_cache"}

// capBySource enforces V2 per-source budget shares (budget ceilings, not
// targets): each bucket may use at most max(40, share * hard budget) tokens
// and maxItems items (V2 max_results_per_source). Mandatory structural
// evidence and unbucketed sources are not capped.
func capBySource(items []model.ContextItem, hardTokens int, shares map[string]float64, maxItems int, patterns []string, mandatory bool) []model.ContextItem {
	if len(shares) == 0 {
		return items
	}
	used, count := map[string]int{}, map[string]int{}
	out := items[:0:0]
	for _, it := range items {
		key, ok := shareKey[it.Source]
		share, hasShare := shares[key]
		if !ok || !hasShare || (mandatory && requiredStructural(it, patterns)) {
			out = append(out, it)
			continue
		}
		limit := max(40, int(float64(hardTokens)*share))
		cost := estimateTokens(it.Text)
		if (used[key]+cost > limit && used[key] > 0) || (maxItems > 0 && count[key] >= maxItems) {
			continue
		}
		used[key] += cost
		count[key]++
		out = append(out, it)
	}
	return out
}

func selectItems(g *gathered, limit, maxCandidates int, patterns []string, mandatory bool) []model.ContextItem {
	candidates := make([]retrieval.Candidate[model.ContextItem], 0, len(g.items))
	for _, it := range g.items {
		candidates = append(candidates, retrieval.Candidate[model.ContextItem]{
			Key: it.Source + "\x00" + it.Text, Text: it.Text, Value: it,
			Relevance: it.Score, EstimatedTokens: estimateTokens(it.Text),
			Required: mandatory && requiredStructural(it, patterns),
		})
	}
	out := []model.ContextItem{}
	sel, err := retrieval.SelectMMR(candidates, retrieval.SelectorOptions{MaxTokens: limit, MaxCandidates: maxCandidates, Lambda: 0.70, MandatoryRequired: mandatory})
	if err == nil {
		for _, c := range sel.Items {
			out = append(out, c.Value)
		}
		return out
	}
	g.note("selector failed: %v; relevance truncation used", err)
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Relevance > candidates[j].Relevance })
	used := 0
	for _, c := range candidates {
		if used+c.EstimatedTokens <= limit {
			used += c.EstimatedTokens
			out = append(out, c.Value)
		}
	}
	return out
}

// rankAndSelect applies the configured algorithm policy. The default
// ("adaptive") is V3's deterministic MMR selector; the other rankers port
// V2 _hybrid_rank for benchmark ablations and fill the budget greedily.
func rankAndSelect(g *gathered, query string, limit int, cfg config.Config, patterns []string, mandatory bool) []model.ContextItem {
	ranker, rrfK, lambda, _ := cfg.Context.Experiments.Policy()
	if ranker == "adaptive" && cfg.Context.Selector.Enabled {
		return selectItems(g, limit, cfg.Context.Selector.MaxSelectorCandidates, patterns, mandatory)
	}
	unique, seen := []model.ContextItem{}, map[[32]byte]bool{}
	for _, it := range g.items {
		if k := it.DedupeKey(); !seen[k] {
			seen[k] = true
			unique = append(unique, it)
		}
	}
	source := append([]model.ContextItem{}, unique...)
	sort.SliceStable(source, func(i, j int) bool { return source[i].Score > source[j].Score })
	keyOf := func(it model.ContextItem) string { k := it.DedupeKey(); return string(k[:]) }
	texts := make([]string, len(unique))
	for i, it := range unique {
		texts[i] = it.Text
	}
	lexical := []model.ContextItem{}
	inLex := map[string]bool{}
	for _, s := range retrieval.NewBM25(texts, unique).Rank(query) {
		lexical = append(lexical, s.Value)
		inLex[keyOf(s.Value)] = true
	}
	for _, it := range source {
		if !inLex[keyOf(it)] {
			lexical = append(lexical, it)
		}
	}
	greedy := func(order []model.ContextItem) []model.ContextItem {
		out, used := []model.ContextItem{}, 0
		for _, it := range order {
			if c := estimateTokens(it.Text); used+c <= limit {
				used += c
				out = append(out, it)
			}
		}
		return out
	}
	switch {
	case ranker == "adaptive" || ranker == "source": // adaptive here means selector_off
		return greedy(source)
	case ranker == "bm25":
		return greedy(lexical)
	}
	specialist := []model.ContextItem{}
	for _, it := range source {
		switch it.Source {
		case "lightweight_index", "targeted_source", "durable_memory":
		default:
			specialist = append(specialist, it)
		}
	}
	toRanked := func(xs []model.ContextItem) []retrieval.Ranked[model.ContextItem] {
		out := make([]retrieval.Ranked[model.ContextItem], len(xs))
		for i, it := range xs {
			out[i] = retrieval.Ranked[model.ContextItem]{Value: it, Key: keyOf(it)}
		}
		return out
	}
	fused := retrieval.RRF([][]retrieval.Ranked[model.ContextItem]{toRanked(source), toRanked(lexical), toRanked(specialist)}, float64(rrfK))
	order := make([]model.ContextItem, len(fused))
	for i, f := range fused {
		order[i] = f.Value
	}
	if ranker == "rrf" || len(order) <= 1 || !cfg.Context.Selector.Enabled {
		return greedy(order)
	}
	cands := make([]retrieval.Candidate[model.ContextItem], len(fused))
	for i, f := range fused {
		cands[i] = retrieval.Candidate[model.ContextItem]{Key: keyOf(f.Value), Text: f.Value.Text, Value: f.Value, Relevance: f.Score,
			EstimatedTokens: estimateTokens(f.Value.Text), Required: mandatory && requiredStructural(f.Value, patterns)}
	}
	sel, err := retrieval.SelectMMR(cands, retrieval.SelectorOptions{MaxTokens: limit, MaxCandidates: max(1, len(cands)), Lambda: lambda, MandatoryRequired: mandatory})
	if err != nil {
		return greedy(order)
	}
	out := make([]model.ContextItem, 0, len(sel.Items))
	for _, c := range sel.Items {
		out = append(out, c.Value)
	}
	return out
}
