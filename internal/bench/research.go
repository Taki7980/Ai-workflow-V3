package bench

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/procx"
	"github.com/Taki7980/ai-workflow-v3/internal/stats"
)

// Provider-family and algorithm profiles (V2 PROFILE_ORDER / ALGORITHM_PROFILE_ORDER).
var (
	AblationProfiles  = []string{"adaptive", "base_only", "base_semantic", "base_structural", "base_scip"}
	AlgorithmProfiles = []string{"adaptive_math", "source_rank", "bm25_rank", "rrf_only", "rrf_mmr_050", "rrf_mmr_075", "rrf_mmr_090", "selector_off", "fixed_budget", "no_early_stop"}
	SeedModes         = []string{"retrieval", "random_non_gold", "oracle_gold"}
)

// AblationConfig disables provider families for one profile.
func AblationConfig(cfg config.Config, profile string) (config.Config, error) {
	switch profile {
	case "adaptive":
		return cfg, nil
	case "base_only", "base_semantic", "base_structural", "base_scip":
	default:
		return cfg, fmt.Errorf("unknown ablation profile %q; expected one of %v", profile, AblationProfiles)
	}
	cfg.Context.ExternalRetrievers = nil
	semOff := func() { cfg.Context.Semantic.Mode, cfg.Context.Semantic.Command = "off", nil }
	switch profile {
	case "base_only":
		semOff()
		cfg.Context.CRG.Mode, cfg.Context.SCIP.Mode = "off", "off"
	case "base_semantic":
		cfg.Context.CRG.Mode, cfg.Context.SCIP.Mode = "off", "off"
	case "base_structural":
		semOff()
	case "base_scip":
		semOff()
		cfg.Context.CRG.Mode = "off"
	}
	return cfg, nil
}

// AlgorithmConfig changes one ranking/selection mechanism for one profile.
func AlgorithmConfig(cfg config.Config, profile string) (config.Config, error) {
	exp := config.Experiments{}
	if cfg.Context.Experiments != nil {
		exp = *cfg.Context.Experiments
	}
	lambda := func(v float64) { exp.HybridRanker, exp.MMRLambda = "rrf_mmr", &v }
	switch profile {
	case "adaptive_math":
		return cfg, nil
	case "source_rank":
		exp.HybridRanker = "source"
	case "bm25_rank":
		exp.HybridRanker = "bm25"
	case "rrf_only":
		exp.HybridRanker = "rrf"
	case "rrf_mmr_050":
		lambda(.50)
	case "rrf_mmr_075":
		lambda(.75)
	case "rrf_mmr_090":
		lambda(.90)
	case "selector_off":
		cfg.Context.Selector.Enabled = false
	case "fixed_budget":
		cfg.Context.AdaptiveBudget.Enabled = false
	case "no_early_stop":
		exp.DisableEarlySufficiencyGate = true
	default:
		return cfg, fmt.Errorf("unknown algorithm profile %q; expected one of %v", profile, AlgorithmProfiles)
	}
	cfg.Context.Experiments = &exp
	return cfg, nil
}

func normalizeProfiles(requested, all []string) []string {
	if len(requested) == 0 {
		return append([]string{}, all...)
	}
	out := []string{}
	for _, p := range requested {
		if p = strings.ToLower(strings.TrimSpace(p)); !containsStr(out, p) {
			out = append(out, p)
		}
	}
	return out
}

func summaryDelta(base, cand map[string]any, keys []string) map[string]any {
	bs, _ := base["summary"].(map[string]any)
	cs, _ := cand["summary"].(map[string]any)
	out := map[string]any{}
	for _, k := range keys {
		l, ok1 := toFloat(bs[k])
		r, ok2 := toFloat(cs[k])
		if ok1 && ok2 {
			out[k] = round4(r - l)
		} else {
			out[k] = nil
		}
	}
	return out
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case *float64:
		if x != nil {
			return *x, true
		}
	}
	return 0, false
}

// Suite runs the benchmark once per profile on identical cases.
func Suite(ctx context.Context, root string, cases []Case, o Options, kind string, requested []string) (map[string]any, error) {
	all, build, baseline, scope := AblationProfiles, AblationConfig, "adaptive", "retrieval-ablation-suite"
	keys := []string{"mean_file_recall_at_k", "mean_file_mrr", "mean_file_ndcg_at_k", "mean_file_f1", "mean_file_yield_per_1k_tokens",
		"selective_control_accuracy", "mean_estimated_context_tokens", "mean_elapsed_ms"}
	note := "Profiles isolate provider families. base_only disables semantic, external, and CRG/SCIP providers; base_semantic disables CRG/SCIP/external; base_structural disables semantic/external; base_scip disables semantic/CRG/external."
	if kind == "algorithms" {
		all, build, baseline, scope = AlgorithmProfiles, AlgorithmConfig, "adaptive_math", "retrieval-algorithm-ablation-suite"
		keys = []string{"mean_file_precision_at_k", "mean_file_recall_at_k", "mean_file_mrr", "mean_file_ndcg_at_k", "mean_file_f1",
			"mean_file_yield_per_1k_tokens", "selective_control_accuracy", "mean_estimated_context_tokens", "mean_budget_utilization",
			"mean_retrieved_estimated_tokens", "mean_unique_retrieved_estimated_tokens", "mean_duplicate_estimated_tokens",
			"mean_duplicate_token_fraction", "mean_ranked_candidate_estimated_tokens", "mean_selected_estimated_tokens",
			"mean_selected_vs_retrieved_token_ratio", "mean_estimated_token_reduction_vs_retrieved", "mean_selected_hard_budget_utilization",
			"mean_selected_adaptive_budget_utilization", "mean_retrieval_call_count", "mean_gold_file_token_share",
			"mean_known_distractor_token_share", "mean_gold_file_tokens_per_1k_retrieved", "mean_elapsed_ms", "sufficiency_rate", "fallback_rate"}
		note = "Profiles alter one retrieval-math or context-selection mechanism at a time while leaving normal production defaults unchanged."
	}
	profiles := normalizeProfiles(requested, all)
	if len(profiles) == 0 {
		return nil, errors.New("at least one profile is required")
	}
	base := o.Config
	if base == nil {
		c, err := config.Load(root)
		if err != nil {
			return nil, err
		}
		base = &c
	}
	results := map[string]any{}
	for _, p := range profiles {
		cfg, err := build(*base, p)
		if err != nil {
			return nil, err
		}
		po := o
		po.Config = &cfg
		r, err := Run(ctx, root, cases, po)
		if err != nil {
			return nil, err
		}
		results[p] = r
	}
	deltas := map[string]any{}
	if b, ok := results[baseline].(map[string]any); ok {
		for p, r := range results {
			if p != baseline {
				deltas[p] = summaryDelta(b, r.(map[string]any), keys)
			}
		}
	}
	return map[string]any{"scope": scope, "profiles": profiles, "results": results, "delta_vs_" + baseline: deltas, "note": note}, nil
}

func trackedFiles(repo string) ([]string, error) {
	out, ok := git(repo, "ls-files", "-z")
	if !ok {
		return nil, fmt.Errorf("unable to enumerate tracked files in %s", repo)
	}
	set := map[string]bool{}
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			set[NormalizePath(f)] = true
		}
	}
	return sortedSet(set), nil
}

func nonGoldSample(files, gold []string, key string, k int) []string {
	g := map[string]bool{}
	for _, x := range gold {
		g[NormalizePath(x)] = true
	}
	type ranked struct {
		hash [32]byte
		path string
	}
	rs := []ranked{}
	for _, f := range files {
		if !g[f] {
			rs = append(rs, ranked{sha256.Sum256([]byte(key + "\x00" + f)), f})
		}
	}
	sort.Slice(rs, func(i, j int) bool {
		if c := strings.Compare(string(rs[i].hash[:]), string(rs[j].hash[:])); c != 0 {
			return c < 0
		}
		return rs[i].path < rs[j].path
	})
	out := []string{}
	for _, r := range rs[:min(len(rs), max(1, k))] {
		out = append(out, r.path)
	}
	return out
}

func seedMetrics(seed, gold []string) map[string]any {
	s := uniq(seed)
	g := map[string]bool{}
	for _, x := range gold {
		g[NormalizePath(x)] = true
	}
	matched := []string{}
	for _, x := range s {
		if g[x] {
			matched = append(matched, x)
		}
	}
	precision := 0.0
	if len(s) > 0 {
		precision = float64(len(matched)) / float64(len(s))
	}
	var recall any
	f1 := 0.0
	if len(g) > 0 {
		r := float64(len(matched)) / float64(len(g))
		recall = r
		if precision+r > 0 {
			f1 = 2 * precision * r / (precision + r)
		}
	}
	return map[string]any{"seed_count": len(s), "matched_gold_files": matched, "precision": precision, "recall": recall, "f1": f1}
}

// InterventionManifest builds deterministic retrieval / random-non-gold /
// oracle-gold seed sets per positive case (V2 build_seed_intervention_manifest).
func InterventionManifest(ctx context.Context, root string, cases []Case, o Options, modes []string, seedK int) (map[string]any, error) {
	limit := max(1, seedK)
	modes = normalizeProfiles(modes, SeedModes)
	for _, m := range modes {
		if !containsStr(SeedModes, m) {
			return nil, fmt.Errorf("unknown seed mode %q; expected one of %v", m, SeedModes)
		}
	}
	copied := make([]Case, len(cases))
	for i, c := range cases {
		cc := Case{}
		for k, v := range c {
			cc[k] = v
		}
		k, _ := c.Int("retrieval_k", limit)
		cc["retrieval_k"] = max(limit, k)
		copied[i] = cc
	}
	report, err := Run(ctx, root, copied, o)
	if err != nil {
		return nil, err
	}
	rows := report["cases"].([]map[string]any)
	out := []map[string]any{}
	for i, c := range copied {
		gold, _ := c.Strings("gold_files")
		for j := range gold {
			gold[j] = NormalizePath(gold[j])
		}
		if len(gold) == 0 {
			continue
		}
		repo, err := ResolveCaseRoot(root, c)
		if err != nil {
			return nil, err
		}
		tracked, err := trackedFiles(repo)
		if err != nil {
			return nil, err
		}
		retrieved := []string{}
		if fr, ok := rows[i]["file_retrieval"].(map[string]any); ok {
			retrieved = fr["retrieved_files"].([]string)
		}
		key := strings.Join([]string{strconv.Itoa(i + 1), c.RepositoryPath(), c.Str("base_commit"), c.Str("task")}, "|")
		seeds := map[string][]string{"retrieval": retrieved[:min(len(retrieved), limit)], "oracle_gold": uniq(gold)[:min(len(uniq(gold)), limit)],
			"random_non_gold": nonGoldSample(tracked, gold, key, limit)}
		for _, m := range modes {
			out = append(out, map[string]any{"case_index": i + 1, "task": c.Str("task"), "task_type": c["task_type"],
				"repository_path": c.RepositoryPath(), "base_commit": c["base_commit"], "snapshot": rows[i]["snapshot"], "seed_mode": m,
				"seed_files": seeds[m], "gold_files": gold, "seed_metrics": seedMetrics(seeds[m], gold)})
		}
	}
	return map[string]any{"scope": "seed-intervention-manifest", "seed_k": limit, "modes": modes, "interventions": out,
		"benchmark_summary": report["summary"],
		"note":              "random_non_gold is deterministic, excludes exact gold files, and is keyed by case identity so repeated runs are reproducible."}, nil
}

// RunnerOptions bounds one intervention runner invocation.
type RunnerOptions struct {
	Command   []string
	Timeout   time.Duration
	MaxOutput int64
	EnvAllow  []string
}

func runnerFailure(status, msg string) map[string]any {
	return map[string]any{"status": status, "success": nil, "trajectory": nil, "error": msg}
}

// RunIntervention executes the operator-supplied runner (no shell, safe env,
// timeout, bounded stdout) and scores the returned trajectory.
func RunIntervention(ctx context.Context, root string, iv map[string]any, ro RunnerOptions) map[string]any {
	rp, _ := iv["repository_path"].(string)
	repo, err := ResolveCaseRoot(root, Case{"repository_path": rp})
	if err != nil {
		return runnerFailure("launch_error", err.Error())
	}
	payload, _ := json.Marshal(map[string]any{"task": iv["task"], "task_type": iv["task_type"], "repository_root": repo,
		"repository_path": rp, "base_commit": iv["base_commit"], "seed_mode": iv["seed_mode"], "seed_files": iv["seed_files"], "gold_files": iv["gold_files"]})
	r, err := procx.Run(ctx, procx.Cmd{Argv: ro.Command, Dir: repo, Env: procx.SafeEnv(ro.EnvAllow...), Stdin: append(payload, '\n'),
		Timeout: ro.Timeout, MaxStdout: ro.MaxOutput})
	switch {
	case err != nil:
		return runnerFailure("launch_error", "runner could not start")
	case r.StdoutExceeded:
		return runnerFailure("output_limit", fmt.Sprintf("runner output exceeded %d bytes", ro.MaxOutput))
	case r.TimedOut:
		return runnerFailure("timeout", fmt.Sprintf("runner timed out after %s", ro.Timeout))
	case r.ExitCode != 0:
		return runnerFailure("exit_error", fmt.Sprintf("runner exited with status %d", r.ExitCode))
	}
	var decoded map[string]any
	if json.Unmarshal(r.Stdout, &decoded) != nil || decoded == nil {
		return runnerFailure("invalid_output", "runner must return one JSON object")
	}
	rawEvents, ok := decoded["trajectory_events"].([]any)
	if _, present := decoded["trajectory_events"]; present && !ok {
		return runnerFailure("invalid_output", "runner trajectory_events must be an array")
	}
	all := []any{}
	seedFiles, _ := iv["seed_files"].([]string)
	for _, f := range seedFiles {
		all = append(all, map[string]any{"kind": "seed", "file": f, "step": json.Number("0")})
	}
	events, err := LoadTrajectory(root, Case{"trajectory_events": append(all, rawEvents...)})
	if err != nil {
		return runnerFailure("invalid_output", err.Error())
	}
	success, _ := decoded["success"].(bool)
	var successV any
	if _, isBool := decoded["success"].(bool); isBool {
		successV = success
	}
	meta, _ := decoded["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	gold, _ := iv["gold_files"].([]string)
	return map[string]any{"status": "ok", "success": successV, "trajectory": TrajectoryMetrics(events, gold), "metadata": meta}
}

var trajectoryKeys = []string{"exploration_recall", "utilization_recall", "context_utilization_rate", "duplicate_exploration_rate", "post_seed_exploration_unique_files"}

// RunInterventions executes every manifest intervention and summarizes by mode.
func RunInterventions(ctx context.Context, root string, manifest map[string]any, ro RunnerOptions) map[string]any {
	rows := []map[string]any{}
	ivs, _ := manifest["interventions"].([]map[string]any)
	for _, iv := range ivs {
		row := map[string]any{}
		for k, v := range iv {
			row[k] = v
		}
		row["runner"] = RunIntervention(ctx, root, iv, ro)
		rows = append(rows, row)
	}
	byMode := map[string][]map[string]any{}
	for _, r := range rows {
		m := fmt.Sprint(r["seed_mode"])
		byMode[m] = append(byMode[m], r)
	}
	summaries := map[string]any{}
	for m, group := range byMode {
		ok, traj, seeds, succ := 0, []map[string]any{}, []map[string]any{}, []bool{}
		for _, r := range group {
			seeds = append(seeds, r["seed_metrics"].(map[string]any))
			run := r["runner"].(map[string]any)
			if run["status"] != "ok" {
				continue
			}
			ok++
			if t, isMap := run["trajectory"].(map[string]any); isMap {
				traj = append(traj, t)
			}
			if b, isBool := run["success"].(bool); isBool {
				succ = append(succ, b)
			}
		}
		var rate any
		if len(succ) > 0 {
			n := 0
			for _, b := range succ {
				if b {
					n++
				}
			}
			rate = round4(float64(n) / float64(len(succ)))
		}
		summaries[m] = map[string]any{"runs": len(group), "completed_runs": ok, "success_rate": rate,
			"mean_seed_precision": mean(seeds, "precision"), "mean_seed_recall": mean(seeds, "recall"), "mean_seed_f1": mean(seeds, "f1"),
			"mean_seed_gold_recall": mean(traj, "seed_gold_recall"), "mean_exploration_recall": mean(traj, "exploration_recall"),
			"mean_utilization_recall": mean(traj, "utilization_recall"), "mean_context_utilization_rate": mean(traj, "context_utilization_rate"),
			"mean_duplicate_exploration_rate":         mean(traj, "duplicate_exploration_rate"),
			"mean_post_seed_exploration_unique_files": mean(traj, "post_seed_exploration_unique_files")}
	}
	paired := map[string]any{}
	for mode, deltas := range pairedDeltas(rows, "random_non_gold") {
		entry := map[string]any{"paired_cases": deltas.cases}
		for _, k := range trajectoryKeys {
			entry["mean_delta_"+k] = meanOf(deltas.values[k])
		}
		paired[mode] = entry
	}
	return map[string]any{"scope": "seed-intervention-run", "seed_k": manifest["seed_k"], "results": rows, "by_seed_mode": summaries,
		"paired_delta_vs_random_non_gold": paired,
		"note":                            "Runner commands execute only when explicitly supplied to the benchmark-intervene CLI and receive a restricted environment."}
}

func meanOf(v []float64) any {
	if len(v) == 0 {
		return nil
	}
	return round4(stats.Mean(v))
}

type modeDeltas struct {
	cases  int
	values map[string][]float64
}

// pairedDeltas pairs each mode with the baseline mode of the same case.
func pairedDeltas(rows []map[string]any, baseline string) map[string]*modeDeltas {
	byCase := map[int]map[string]map[string]any{}
	for _, r := range rows {
		idx, ok := toFloat(r["case_index"])
		if !ok {
			continue
		}
		if byCase[int(idx)] == nil {
			byCase[int(idx)] = map[string]map[string]any{}
		}
		byCase[int(idx)][fmt.Sprint(r["seed_mode"])] = r
	}
	traj := func(r map[string]any) map[string]any {
		run, _ := r["runner"].(map[string]any)
		if run == nil || run["status"] != "ok" {
			return nil
		}
		t, _ := run["trajectory"].(map[string]any)
		return t
	}
	out := map[string]*modeDeltas{}
	idxs := make([]int, 0, len(byCase))
	for i := range byCase {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		modes := byCase[i]
		b := traj(modes[baseline])
		if b == nil {
			continue
		}
		names := sortedKeysOf(modes)
		for _, m := range names {
			if m == baseline {
				continue
			}
			t := traj(modes[m])
			if t == nil {
				continue
			}
			if out[m] == nil {
				out[m] = &modeDeltas{values: map[string][]float64{}}
			}
			out[m].cases++
			for _, k := range trajectoryKeys {
				l, ok1 := toFloat(b[k])
				r, ok2 := toFloat(t[k])
				if ok1 && ok2 {
					out[m].values[k] = append(out[m].values[k], r-l)
				}
			}
		}
	}
	return out
}

func fieldValue(row map[string]any, field string) string {
	var cur any = row
	for _, part := range strings.Split(field, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return "unknown"
		}
		if cur, ok = m[part]; !ok {
			return "unknown"
		}
	}
	if cur == nil || cur == "" {
		return "unknown"
	}
	if f, ok := cur.(float64); ok && f == math.Trunc(f) {
		return strconv.FormatInt(int64(f), 10)
	}
	return fmt.Sprint(cur)
}

func effects(rows []map[string]any, confidence float64, resamples int, seed int64) map[string]any {
	out := map[string]any{}
	for mode, d := range pairedDeltas(rows, "random_non_gold") {
		metrics := map[string]any{}
		for k, v := range d.values {
			metrics[k] = stats.PairedEffect(v, confidence, resamples, seed)
		}
		out[mode] = metrics
	}
	return out
}

// SeedStatistics adds paired bootstrap intervals to an intervention run.
func SeedStatistics(report map[string]any, confidence float64, resamples int, seed int64, stratify []string) (map[string]any, error) {
	if !(confidence > 0 && confidence < 1) {
		return nil, errors.New("confidence must be between 0 and 1")
	}
	rows := []map[string]any{}
	if l, ok := report["results"].([]any); ok {
		for _, r := range l {
			if m, ok := r.(map[string]any); ok {
				rows = append(rows, m)
			}
		}
	}
	strat := map[string]any{}
	for _, field := range stratify {
		groups := map[string][]map[string]any{}
		for _, r := range rows {
			v := fieldValue(r, field)
			groups[v] = append(groups[v], r)
		}
		f := map[string]any{}
		for v, g := range groups {
			f[v] = effects(g, confidence, resamples, seed)
		}
		strat[field] = f
	}
	return map[string]any{"scope": "seed-intervention-statistics", "confidence": confidence, "bootstrap_resamples": max(1, resamples),
		"random_seed": seed, "paired_against": "random_non_gold", "comparisons": effects(rows, confidence, resamples, seed), "stratified": strat,
		"interpretation": "A confidence interval entirely above zero supports improvement over the paired random-non-gold control. Intervals crossing zero are inconclusive and must not be treated as evidence of a win."}, nil
}

func calibrationLabel(r map[string]any) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(r["control_type"]))) {
	case "positive":
		return 1, true
	case "natural_no_gold", "wrong_repo":
		return 0, true
	}
	return 0, false
}

func calibrationScore(r map[string]any) (float64, bool) {
	if _, isBool := r["retrieval_sufficiency_score"].(bool); isBool {
		return 0, false
	}
	return toFloat(r["retrieval_sufficiency_score"])
}

func calibrationKey(r map[string]any, i int) string {
	snap, _ := r["snapshot"].(map[string]any)
	exp := ""
	if snap != nil && snap["expected_head"] != nil {
		exp = fmt.Sprint(snap["expected_head"])
	}
	rp := "."
	if r["repository_path"] != nil {
		rp = fmt.Sprint(r["repository_path"])
	}
	return strings.Join([]string{strconv.Itoa(i), fmt.Sprint(orEmpty(r["task"])), fmt.Sprint(orEmpty(r["task_type"])), rp, exp}, "|")
}

func bucket(key string) float64 {
	sum := sha256.Sum256([]byte(key))
	return float64(binary.BigEndian.Uint64(sum[:8])) / math.Pow(2, 64)
}

func thresholdMetrics(rows []map[string]any, t, fa, fr float64) map[string]any {
	var tp, tn, fp, fn int
	for _, r := range rows {
		l, ok1 := calibrationLabel(r)
		s, ok2 := calibrationScore(r)
		if !ok1 || !ok2 {
			continue
		}
		pred := s >= t
		switch {
		case pred && l == 1:
			tp++
		case !pred && l == 0:
			tn++
		case pred && l == 0:
			fp++
		default:
			fn++
		}
	}
	pos, neg, n := tp+fn, tn+fp, tp+tn+fp+fn
	r6 := func(num, den int) any {
		if den == 0 {
			return nil
		}
		return stats.Round6(float64(num) / float64(den))
	}
	cost := float64(fp)*fa + float64(fn)*fr
	var bal, per any
	if pos > 0 && neg > 0 {
		bal = stats.Round6((float64(tp)/float64(pos) + float64(tn)/float64(neg)) / 2)
	}
	if n > 0 {
		per = stats.Round6(cost / float64(n))
	}
	return map[string]any{"threshold": stats.Round6(t), "n": n, "tp": tp, "tn": tn, "false_accepts": fp, "false_rejects": fn,
		"true_positive_rate": r6(tp, pos), "true_negative_rate": r6(tn, neg), "false_accept_rate": r6(fp, neg), "false_reject_rate": r6(fn, pos),
		"balanced_accuracy": bal, "expected_cost_per_case": per, "total_cost": stats.Round6(cost)}
}

// Calibrate chooses an advisory sufficiency threshold on a deterministic
// stratified split and reports it on the held-out cases (never applied).
func Calibrate(report map[string]any, fraction, fa, fr float64) (map[string]any, error) {
	if !(fraction > 0 && fraction < 1) {
		return nil, errors.New("calibration_fraction must be between 0 and 1")
	}
	if fa < 0 || fr < 0 {
		return nil, errors.New("misclassification costs must be non-negative")
	}
	rows := []map[string]any{}
	if l, ok := report["cases"].([]any); ok {
		for _, r := range l {
			if m, ok := r.(map[string]any); ok {
				rows = append(rows, m)
			}
		}
	}
	type ent struct {
		idx int
		row map[string]any
	}
	byLabel := map[int][]ent{}
	for i, r := range rows {
		l, ok1 := calibrationLabel(r)
		_, ok2 := calibrationScore(r)
		if ok1 && ok2 {
			byLabel[l] = append(byLabel[l], ent{i + 1, r})
		}
	}
	var cal, hold []map[string]any
	for _, l := range []int{0, 1} {
		g := byLabel[l]
		sort.SliceStable(g, func(i, j int) bool {
			ki, kj := calibrationKey(g[i].row, g[i].idx), calibrationKey(g[j].row, g[j].idx)
			if bi, bj := bucket(ki), bucket(kj); bi != bj {
				return bi < bj
			}
			return ki < kj
		})
		if len(g) == 1 {
			cal = append(cal, g[0].row)
			continue
		}
		cut := min(len(g)-1, max(1, int(math.RoundToEven(float64(len(g))*fraction))))
		for i, e := range g {
			if i < cut {
				cal = append(cal, e.row)
			} else {
				hold = append(hold, e.row)
			}
		}
	}
	labels := map[int]bool{}
	for _, r := range cal {
		l, _ := calibrationLabel(r)
		labels[l] = true
	}
	if !labels[0] || !labels[1] {
		return nil, errors.New("calibration split requires both positive and selective-control cases")
	}
	scores := map[float64]bool{0: true, 1: true}
	uniqScores := []float64{}
	for _, r := range cal {
		s, _ := calibrationScore(r)
		if !containsF(uniqScores, s) {
			uniqScores = append(uniqScores, s)
		}
		scores[s] = true
	}
	sort.Float64s(uniqScores)
	for i := 0; i+1 < len(uniqScores); i++ {
		scores[(uniqScores[i]+uniqScores[i+1])/2] = true
	}
	thresholds := []float64{}
	for s := range scores {
		thresholds = append(thresholds, s)
	}
	sort.Float64s(thresholds)
	var best map[string]any
	for _, t := range thresholds {
		m := thresholdMetrics(cal, t, fa, fr)
		if best == nil {
			best = m
			continue
		}
		c, bc := m["total_cost"].(float64), best["total_cost"].(float64)
		if c < bc || c == bc && (m["false_accepts"].(int) < best["false_accepts"].(int) ||
			m["false_accepts"].(int) == best["false_accepts"].(int) && m["threshold"].(float64) > best["threshold"].(float64)) {
			best = m
		}
	}
	t := best["threshold"].(float64)
	return map[string]any{"scope": "sufficiency-threshold-calibration", "status": "advisory_only", "threshold": t,
		"split": map[string]any{"method": "deterministic_sha256_stratified", "calibration_fraction": fraction, "calibration_cases": len(cal), "holdout_cases": len(hold)},
		"costs": map[string]any{"false_accept": fa, "false_reject": fr}, "calibration": thresholdMetrics(cal, t, fa, fr),
		"holdout": thresholdMetrics(hold, t, fa, fr), "candidates_evaluated": len(thresholds),
		"warning": "This calibration is not applied to runtime configuration. Agent Retrieval Bench shows that thresholds calibrated on counterfactual controls may fail to generalize to natural no-gold cases, so deployment requires representative held-out controls."}, nil
}

func containsF(xs []float64, x float64) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func caseUtility(r map[string]any, tokenPenalty, latencyPenalty float64) (float64, bool) {
	var quality float64
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(r["control_type"]))) {
	case "natural_no_gold", "wrong_repo":
		b, ok := r["selective_control_correct"].(bool)
		if !ok {
			return 0, false
		}
		if b {
			quality = 1
		}
	default:
		fm, ok := r["file_retrieval"].(map[string]any)
		if !ok {
			return 0, false
		}
		f1, ok := toFloat(fm["file_f1"])
		if !ok {
			return 0, false
		}
		quality = f1
	}
	bu, _ := toFloat(r["budget_utilization"])
	el, _ := toFloat(r["elapsed_ms"])
	return quality - math.Max(0, tokenPenalty)*math.Min(1, math.Max(0, bu)) - math.Max(0, latencyPenalty)*math.Min(1, math.Max(0, el/1000)), true
}

// AdvisorOptions configures the safe policy advisor.
type AdvisorOptions struct {
	ContextField   string
	MinimumSamples int
	Confidence     float64
	Resamples      int
	Seed           int64
	SafetyMargin   float64
	TokenPenalty   float64
	LatencyPenalty float64
}

// PolicyAdvisor recommends an algorithm per context only when its paired
// bootstrap lower bound clears the margin versus adaptive_math (advisory).
func PolicyAdvisor(report map[string]any, o AdvisorOptions) (map[string]any, error) {
	if o.MinimumSamples < 1 {
		return nil, errors.New("minimum_samples must be positive")
	}
	results, ok := report["results"].(map[string]any)
	if !ok || results["adaptive_math"] == nil {
		return nil, errors.New("algorithm report must contain baseline profile 'adaptive_math'")
	}
	casesOf := func(p string) []map[string]any {
		r, _ := results[p].(map[string]any)
		out := []map[string]any{}
		l, _ := r["cases"].([]any)
		for _, c := range l {
			m, _ := c.(map[string]any)
			out = append(out, m)
		}
		return out
	}
	base := casesOf("adaptive_math")
	contexts := map[string]map[string]any{}
	locked := 0
	for _, p := range sortedKeysOf(results) {
		if p == "adaptive_math" {
			continue
		}
		cand := casesOf(p)
		if len(cand) != len(base) {
			return nil, errors.New("algorithm profiles must contain the same case count")
		}
		grouped := map[string][]float64{}
		locked = 0
		for i := range base {
			l, r := base[i], cand[i]
			if l == nil || r == nil {
				continue
			}
			if risk := strings.ToLower(fmt.Sprint(l["risk"])); risk == "high" || risk == "critical" {
				locked++
				continue
			}
			bu, ok1 := caseUtility(l, o.TokenPenalty, o.LatencyPenalty)
			cu, ok2 := caseUtility(r, o.TokenPenalty, o.LatencyPenalty)
			if ok1 && ok2 {
				ctx := fieldValue(l, o.ContextField)
				grouped[ctx] = append(grouped[ctx], cu-bu)
			}
		}
		for ctx, deltas := range grouped {
			if contexts[ctx] == nil {
				contexts[ctx] = map[string]any{"arms": map[string]any{}, "locked_high_risk_cases": 0}
			}
			s := stats.BootstrapMeanCI(deltas, o.Confidence, o.Resamples, o.Seed)
			low, hasLow := s["ci_low"].(float64)
			s["eligible"] = s["n"].(int) >= o.MinimumSamples && hasLow && low > o.SafetyMargin
			contexts[ctx]["arms"].(map[string]any)[p] = s
		}
		for _, c := range contexts {
			c["locked_high_risk_cases"] = max(c["locked_high_risk_cases"].(int), locked)
		}
	}
	recs := map[string]any{}
	for ctx, c := range contexts {
		var bestP string
		var best map[string]any
		for p, raw := range c["arms"].(map[string]any) {
			a := raw.(map[string]any)
			if a["eligible"] != true {
				continue
			}
			if best == nil || a["ci_low"].(float64) > best["ci_low"].(float64) ||
				a["ci_low"] == best["ci_low"] && (a["mean"].(float64) > best["mean"].(float64) || a["mean"] == best["mean"] && p > bestP) {
				bestP, best = p, a
			}
		}
		if best != nil {
			recs[ctx] = map[string]any{"recommended_profile": bestP, "status": "advisory_candidate", "mean_utility_delta": best["mean"],
				"lower_confidence_bound": best["ci_low"], "confidence": o.Confidence, "samples": best["n"]}
		} else {
			recs[ctx] = map[string]any{"recommended_profile": "adaptive_math", "status": "baseline_retained",
				"reason": "no candidate cleared minimum samples and the high-confidence safety margin"}
		}
	}
	return map[string]any{"scope": "safe-retrieval-policy-advisor", "status": "advisory_only", "baseline_profile": "adaptive_math",
		"context_field": o.ContextField, "minimum_samples": o.MinimumSamples, "confidence": o.Confidence, "bootstrap_resamples": o.Resamples,
		"safety_margin": o.SafetyMargin,
		"reward":        map[string]any{"quality": "file_f1_or_selective_control_correctness", "token_penalty": o.TokenPenalty, "latency_penalty": o.LatencyPenalty},
		"safety":        map[string]any{"locked_risks": []string{"critical", "high"}, "runtime_override_enabled": false, "requires_randomized_logging_for_bandit_ope": true},
		"contexts":      contexts, "recommendations": recs,
		"warning": "This is high-confidence offline policy advice, not an online contextual bandit. Do not use it for off-policy claims until logging propensities or randomized exploration data exist."}, nil
}
