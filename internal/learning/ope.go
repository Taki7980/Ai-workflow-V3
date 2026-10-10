package learning

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/stats"
)

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case json.Number:
		f, err := x.Float64()
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

func outcomeOf(r map[string]any) map[string]any {
	o, _ := r["outcome"].(map[string]any)
	return o
}

func propensity(r map[string]any, arm string) (float64, bool) {
	p, _ := r["arm_propensities"].(map[string]any)
	if p == nil {
		return 0, false
	}
	return num(p[arm])
}

func chosenArm(r map[string]any) string {
	s, _ := r["chosen_arm"].(string)
	return strings.TrimSpace(s)
}

func verifiedRows(rows []map[string]any) []map[string]any {
	out := []map[string]any{}
	for _, r := range rows {
		o := outcomeOf(r)
		if o == nil || o["verified"] != true {
			continue
		}
		_, ok1 := num(o["reward"])
		c, ok2 := num(o["realized_cost"])
		if !ok1 || !ok2 || c < 0 {
			continue
		}
		if p, ok := propensity(r, chosenArm(r)); !ok || p <= 0 {
			continue
		}
		out = append(out, r)
	}
	return out
}

func safeTarget(r map[string]any, target string) string {
	if p, ok := propensity(r, target); ok && p > 0 {
		return target
	}
	return BaselineArm
}

func r6(f float64) float64 { return stats.Round6(f) }

func r6p(f float64, ok bool) any {
	if !ok {
		return nil
	}
	return r6(f)
}

func estimate(rows []map[string]any, target string) map[string]any {
	var wr, wc, ws, costs []float64
	matched, direct := 0, 0
	for _, r := range rows {
		ch := chosenArm(r)
		if ch != safeTarget(r, target) {
			continue
		}
		p, ok1 := propensity(r, ch)
		o := outcomeOf(r)
		rew, ok2 := num(o["reward"])
		cost, ok3 := num(o["realized_cost"])
		if !ok1 || p <= 0 || !ok2 || !ok3 {
			continue
		}
		w := 1 / p
		matched++
		ws, wr, wc = append(ws, w), append(wr, w*rew), append(wc, w*cost)
		if ch == target && target != BaselineArm {
			direct++
			costs = append(costs, cost)
		}
	}
	n := len(rows)
	sum := stats.PySum // V2 uses builtin sum() (Neumaier) here
	squares := make([]float64, len(ws))
	for i, w := range ws {
		squares[i] = w * w
	}
	tw, sq := sum(ws), sum(squares)
	ess := 0.0
	if sq > 0 {
		ess = tw * tw / sq
	}
	coverage := 0.0
	if n > 0 {
		coverage = r6(float64(matched) / float64(n))
	}
	maxOf := func(xs []float64) any {
		if len(xs) == 0 {
			return nil
		}
		return r6(slices.Max(xs))
	}
	return map[string]any{"target_arm": target, "logged_events": n, "matched_events": matched, "direct_target_exposures": direct,
		"coverage": coverage, "ips_reward": r6p(sum(wr)/float64(n), n > 0), "snips_reward": r6p(sum(wr)/tw, tw > 0),
		"ips_cost": r6p(sum(wc)/float64(n), n > 0), "snips_cost": r6p(sum(wc)/tw, tw > 0), "effective_sample_size": r6(ess),
		"max_importance_weight": maxOf(ws), "max_direct_realized_cost": maxOf(costs)}
}

func snips(rows []map[string]any, target, field string) (float64, bool) {
	weighted, total := 0.0, 0.0
	for _, r := range rows {
		ch := chosenArm(r)
		if ch != safeTarget(r, target) {
			continue
		}
		p, ok1 := propensity(r, ch)
		v, ok2 := num(outcomeOf(r)[field])
		if !ok1 || p <= 0 || !ok2 {
			continue
		}
		w := 1 / p // V2 order: weight = 1/p, then weight * value
		weighted += w * v
		total += w
	}
	if total <= 0 {
		return 0, false
	}
	return weighted / total, true
}

func bootstrapDelta(rows []map[string]any, target, field string, confidence float64, resamples int, seed int64) (map[string]any, error) {
	if !(confidence > 0 && confidence < 1) {
		return nil, errors.New("confidence must be between 0 and 1")
	}
	if len(rows) == 0 {
		return map[string]any{"mean_delta": nil, "ci_low": nil, "ci_high": nil, "resamples_used": 0}, nil
	}
	rng := stats.NewPyRandom(seed)
	n := len(rows)
	deltas := []float64{}
	for range max(1, resamples) {
		sample := make([]map[string]any, n)
		for i := range sample {
			sample[i] = rows[rng.Randrange(n)]
		}
		c, ok1 := snips(sample, target, field)
		b, ok2 := snips(sample, BaselineArm, field)
		if ok1 && ok2 {
			deltas = append(deltas, c-b)
		}
	}
	oc, ok1 := snips(rows, target, field)
	ob, ok2 := snips(rows, BaselineArm, field)
	var observed any
	if ok1 && ok2 {
		observed = r6(oc - ob)
	}
	if len(deltas) == 0 {
		return map[string]any{"mean_delta": observed, "ci_low": nil, "ci_high": nil, "resamples_used": 0}, nil
	}
	if observed == nil {
		observed = r6(stats.Mean(deltas))
	}
	alpha := (1 - confidence) / 2
	return map[string]any{"mean_delta": observed, "ci_low": r6(stats.Percentile(deltas, alpha)), "ci_high": r6(stats.Percentile(deltas, 1-alpha)),
		"resamples_used": len(deltas)}, nil
}

// EvaluateOptions configures propensity-aware off-policy evaluation.
type EvaluateOptions struct {
	Arms            []string
	Confidence      float64
	Resamples       int
	Seed            int64
	MinimumESS      float64
	MinimumDirect   int
	SafetyMargin    float64
	MaxRealizedCost *float64
}

// Evaluate runs IPS/SNIPS with paired bootstrap deltas and conservative,
// advisory-only promotion gates (V2 evaluate_learning_policies).
func Evaluate(root string, o EvaluateOptions) (map[string]any, error) {
	arms := o.Arms
	if len(arms) == 0 {
		arms = SafeArms
	}
	norm := []string{}
	for _, a := range arms {
		a = strings.TrimSpace(a)
		if !slices.Contains(SafeArms, a) {
			return nil, fmt.Errorf("unsupported learning arm: %s", a)
		}
		if !slices.Contains(norm, a) {
			norm = append(norm, a)
		}
	}
	rows := verifiedRows(Records(root))
	evals := map[string]any{}
	bestArm, bestLow, bestSnips := BaselineArm, math.Inf(-1), math.Inf(-1)
	for _, a := range norm {
		est := estimate(rows, a)
		rd, err := bootstrapDelta(rows, a, "reward", o.Confidence, o.Resamples, o.Seed)
		if err != nil {
			return nil, err
		}
		cd, _ := bootstrapDelta(rows, a, "realized_cost", o.Confidence, o.Resamples, o.Seed+1)
		reasons := []string{}
		eligible := a != BaselineArm
		if est["effective_sample_size"].(float64) < o.MinimumESS {
			eligible = false
			reasons = append(reasons, "insufficient_effective_sample_size")
		}
		if est["direct_target_exposures"].(int) < o.MinimumDirect {
			eligible = false
			reasons = append(reasons, "insufficient_direct_target_exposures")
		}
		low, hasLow := rd["ci_low"].(float64)
		if !hasLow || low <= o.SafetyMargin {
			eligible = false
			reasons = append(reasons, "reward_lower_bound_does_not_clear_margin")
		}
		if o.MaxRealizedCost != nil {
			if c, ok := est["max_direct_realized_cost"].(float64); !ok || c > *o.MaxRealizedCost {
				eligible = false
				reasons = append(reasons, "realized_cost_constraint_not_met")
			}
		}
		est["reward_delta_vs_baseline"], est["cost_delta_vs_baseline"] = rd, cd
		est["promotion_eligible"], est["promotion_blockers"] = eligible, reasons
		evals[a] = est
		if eligible {
			s, _ := est["snips_reward"].(float64)
			if low > bestLow || low == bestLow && (s > bestSnips || s == bestSnips && a > bestArm) {
				bestArm, bestLow, bestSnips = a, low, s
			}
		}
	}
	var maxCost any
	if o.MaxRealizedCost != nil {
		maxCost = *o.MaxRealizedCost
	}
	return map[string]any{"scope": "retrieval-learning-off-policy-evaluation",
		"estimator":              map[string]any{"reward": "ips_and_snips", "cost": "ips_and_snips", "confidence_interval": "paired_nonparametric_bootstrap", "exact_paper_bound": false},
		"logged_verified_events": len(rows), "baseline": estimate(rows, BaselineArm), "evaluations": evals,
		"promotion": map[string]any{"recommended_arm": bestArm, "automatic_runtime_promotion": false, "minimum_effective_sample_size": o.MinimumESS,
			"minimum_direct_exposures": o.MinimumDirect, "safety_margin": o.SafetyMargin, "max_realized_cost": maxCost},
		"warning": "SNIPS is only trustworthy when the logged behavior policy records valid propensities and provides adequate overlap. The confidence interval here is bootstrap-based and is not the exact Efron-Stein lower bound from Kuzborskij et al."}, nil
}

// --- contextual policy (V2 contextual_policy.py) ---

func validateFields(fields []string) ([]string, error) {
	out := []string{}
	for _, f := range fields {
		if f = strings.TrimSpace(f); !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("at least one context field is required")
	}
	bad := []string{}
	for _, f := range out {
		if !slices.Contains(FeatureFields, f) {
			bad = append(bad, f)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, errors.New("unsupported context fields: " + strings.Join(bad, ", "))
	}
	return out, nil
}

func normalizeFeatures(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, f := range FeatureFields {
		raw, present := m[f]
		if !present || raw == nil {
			return nil
		}
		s := strings.TrimSpace(fmt.Sprint(raw))
		if s == "" {
			return nil
		}
		out[f] = s
	}
	return out
}

// contextKey is V2 context_key: sorted-key compact JSON of the selected fields.
func contextKey(features map[string]string, fields []string) string {
	payload := map[string]string{}
	for _, f := range fields {
		payload[f] = features[f]
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
	return strings.TrimSuffix(buf.String(), "\n")
}

func rowContext(r map[string]any, fields []string) string {
	f, _ := r["context_features"].(map[string]string)
	return contextKey(f, fields)
}

func verifiedFeatureRows(root string) ([]map[string]any, map[string]int) {
	excluded := map[string]int{"unverified": 0, "invalid_outcome": 0, "missing_current_features": 0, "invalid_propensity": 0}
	out := []map[string]any{}
	for _, r := range Records(root) {
		o := outcomeOf(r)
		if o == nil || o["verified"] != true {
			excluded["unverified"]++
			continue
		}
		_, ok1 := num(o["reward"])
		c, ok2 := num(o["realized_cost"])
		if !ok1 || !ok2 || c < 0 {
			excluded["invalid_outcome"]++
			continue
		}
		feats := normalizeFeatures(r["context_features"])
		if r["feature_schema_version"] != FeatureSchema || feats == nil {
			excluded["missing_current_features"]++
			continue
		}
		if p, ok := propensity(r, chosenArm(r)); !ok || p <= 0 {
			excluded["invalid_propensity"]++
			continue
		}
		cp := map[string]any{}
		for k, v := range r {
			cp[k] = v
		}
		cp["context_features"] = feats
		out = append(out, cp)
	}
	return out, excluded
}

// Model is a smoothed categorical direct reward/cost model.
type Model struct {
	OutcomeField    string                        `json:"outcome_field"`
	ContextFields   []string                      `json:"context_fields"`
	PriorWeight     float64                       `json:"prior_weight"`
	GlobalMean      float64                       `json:"global_mean"`
	ArmMeans        map[string]float64            `json:"arm_means"`
	ContextArmMeans map[string]map[string]float64 `json:"context_arm_means"`
	ContextCounts   map[string]map[string]int     `json:"context_arm_counts"`
}

func fitModel(rows []map[string]any, fields []string, field string, prior float64) Model {
	global := []float64{}
	arms := map[string][]float64{}
	ctx := map[string]map[string][]float64{}
	for _, r := range rows {
		v, ok := num(outcomeOf(r)[field])
		a := chosenArm(r)
		if !ok || a == "" {
			continue
		}
		k := rowContext(r, fields)
		global = append(global, v)
		arms[a] = append(arms[a], v)
		if ctx[k] == nil {
			ctx[k] = map[string][]float64{}
		}
		ctx[k][a] = append(ctx[k][a], v)
	}
	m := Model{OutcomeField: field, ContextFields: fields, PriorWeight: prior, ArmMeans: map[string]float64{},
		ContextArmMeans: map[string]map[string]float64{}, ContextCounts: map[string]map[string]int{}}
	if len(global) > 0 {
		m.GlobalMean = stats.Mean(global)
	}
	for a, vs := range arms {
		m.ArmMeans[a] = stats.Mean(vs)
	}
	for k, per := range ctx {
		m.ContextArmMeans[k], m.ContextCounts[k] = map[string]float64{}, map[string]int{}
		for a, vs := range per {
			p, ok := m.ArmMeans[a]
			if !ok {
				p = m.GlobalMean
			}
			m.ContextArmMeans[k][a] = (stats.PySum(vs) + prior*p) / math.Max(1, float64(len(vs))+prior)
			m.ContextCounts[k][a] = len(vs)
		}
	}
	return m
}

func (m Model) predict(ctx, arm string) float64 {
	if v, ok := m.ContextArmMeans[ctx][arm]; ok {
		return v
	}
	if v, ok := m.ArmMeans[arm]; ok {
		return v
	}
	return m.GlobalMean
}

func digestUint(id string, n int) uint64 {
	s := sha256.Sum256([]byte(id))
	if n == 4 {
		return uint64(binary.BigEndian.Uint32(s[:4]))
	}
	return binary.BigEndian.Uint64(s[:8])
}

func crossValidate(rows []map[string]any, fields []string, field string, folds int, prior float64) map[string]any {
	k := max(2, folds)
	var abs, sq []float64
	for fold := range k {
		var train, valid []map[string]any
		for _, r := range rows {
			id, _ := r["decision_id"].(string)
			if int(digestUint(id, 4)%uint64(k)) == fold {
				valid = append(valid, r)
			} else {
				train = append(train, r)
			}
		}
		if len(train) == 0 || len(valid) == 0 {
			continue
		}
		m := fitModel(train, fields, field, prior)
		for _, r := range valid {
			obs, ok := num(outcomeOf(r)[field])
			if !ok {
				continue
			}
			e := m.predict(rowContext(r, fields), chosenArm(r)) - obs
			abs, sq = append(abs, math.Abs(e)), append(sq, e*e)
		}
	}
	var mae, rmse any
	if len(abs) > 0 {
		mae, rmse = r6(stats.Mean(abs)), r6(math.Sqrt(stats.Mean(sq)))
	}
	return map[string]any{"folds": k, "n": len(abs), "mae": mae, "rmse": rmse}
}

func isLow(r map[string]any) bool { return strings.ToLower(fmt.Sprint(r["risk"])) == "low" }

func policyMap(rows []map[string]any, m Model, fields []string, minContext, minDirect int, minGain float64) (map[string]string, map[string]any) {
	groups := map[string][]map[string]any{}
	exposures := map[string]map[string]int{}
	for _, r := range rows {
		if !isLow(r) {
			continue
		}
		k := rowContext(r, fields)
		groups[k] = append(groups[k], r)
		if a := chosenArm(r); a != "" {
			if exposures[k] == nil {
				exposures[k] = map[string]int{}
			}
			exposures[k][a]++
		}
	}
	policy, diag := map[string]string{}, map[string]any{}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		base := m.predict(k, BaselineArm)
		chosen, value := BaselineArm, base
		enough := len(groups[k]) >= max(1, minContext)
		cands := map[string]any{}
		for _, a := range SafeArms {
			direct := exposures[k][a]
			est := m.predict(k, a)
			supported := a == BaselineArm || direct >= max(1, minDirect)
			cands[a] = map[string]any{"direct_exposures": direct, "direct_model_reward": r6(est), "supported": supported}
			if !enough || a == BaselineArm || !supported || est <= base+minGain {
				continue
			}
			if est > value {
				chosen, value = a, est
			}
		}
		policy[k] = chosen
		diag[k] = map[string]any{"development_events": len(groups[k]), "baseline_estimated_reward": r6(base), "chosen_arm": chosen,
			"chosen_estimated_reward": r6(value), "candidates": cands}
	}
	return policy, diag
}

func targetFor(r map[string]any, policy map[string]string, fields []string) string {
	if !isLow(r) {
		return BaselineArm
	}
	want, ok := policy[rowContext(r, fields)]
	if !ok {
		want = BaselineArm
	}
	if p, ok := propensity(r, want); ok && p > 0 {
		return want
	}
	return BaselineArm
}

func drValue(r map[string]any, target string, m Model, fields []string, field string) (float64, bool) {
	obs, ok := num(outcomeOf(r)[field])
	ch := chosenArm(r)
	p, okP := propensity(r, ch)
	if !ok || !okP || p <= 0 {
		return 0, false
	}
	k := rowContext(r, fields)
	v := m.predict(k, target)
	if ch == target {
		v += (obs - m.predict(k, ch)) / p
	}
	return v, true
}

// evaluateDR is V2 evaluate_context_policy_dr on held-out rows.
func evaluateDR(rows []map[string]any, policy map[string]string, reward, cost Model, fields []string, confidence float64, resamples int, seed int64) map[string]any {
	var rd, cd, weights, costs []float64
	direct, nonBase := 0, 0
	for _, r := range rows {
		t := targetFor(r, policy, fields)
		if t != BaselineArm {
			nonBase++
		}
		if a, ok := drValue(r, t, reward, fields, "reward"); ok {
			if b, ok := drValue(r, BaselineArm, reward, fields, "reward"); ok {
				rd = append(rd, a-b)
			}
		}
		if a, ok := drValue(r, t, cost, fields, "realized_cost"); ok {
			if b, ok := drValue(r, BaselineArm, cost, fields, "realized_cost"); ok {
				cd = append(cd, a-b)
			}
		}
		ch := chosenArm(r)
		if p, ok := propensity(r, ch); ch == t && ok && p > 0 {
			weights = append(weights, 1/p)
			if t != BaselineArm {
				direct++
				if c, ok := num(outcomeOf(r)["realized_cost"]); ok {
					costs = append(costs, c)
				}
			}
		}
	}
	squares := make([]float64, len(weights))
	for i, w := range weights {
		squares[i] = w * w
	}
	tw, sq := stats.PySum(weights), stats.PySum(squares)
	ess := 0.0
	if sq > 0 {
		ess = tw * tw / sq
	}
	var maxW, maxC any
	if len(weights) > 0 {
		maxW = r6(slices.Max(weights))
	}
	if len(costs) > 0 {
		maxC = r6(slices.Max(costs))
	}
	return map[string]any{"holdout_events": len(rows), "nonbaseline_target_events": nonBase, "direct_target_exposures": direct,
		"effective_sample_size": r6(ess), "max_importance_weight": maxW, "max_direct_realized_cost": maxC,
		"reward_delta_vs_baseline": stats.BootstrapMeanCI(rd, confidence, resamples, seed),
		"cost_delta_vs_baseline":   stats.BootstrapMeanCI(cd, confidence, resamples, seed+1)}
}

// ContextualOptions configures contextual policy development.
type ContextualOptions struct {
	Fields                                   []string
	DevelopmentFraction, PriorWeight         float64
	Folds, MinContext, MinDirect, MinHoldout int
	MinESS                                   float64
	MinValidation                            int
	MinGain, SafetyMargin                    float64
	MaxRealizedCost                          *float64
	Confidence                               float64
	Resamples                                int
	Seed                                     int64
}

// ContextualPolicy develops a contextual policy on a deterministic split and
// evaluates it doubly-robustly on the untouched holdout (advisory only).
func ContextualPolicy(root string, o ContextualOptions) (map[string]any, error) {
	fields := o.Fields
	if len(fields) == 0 {
		fields = DefaultPolicyFields
	}
	fields, err := validateFields(fields)
	if err != nil {
		return nil, err
	}
	if !(o.DevelopmentFraction > 0 && o.DevelopmentFraction < 1) {
		return nil, errors.New("development_fraction must be between 0 and 1")
	}
	if o.PriorWeight < 0 {
		return nil, errors.New("prior_weight must be non-negative")
	}
	rows, excluded := verifiedFeatureRows(root)
	limit := uint64(o.DevelopmentFraction * float64(math.MaxUint64))
	var dev, hold []map[string]any
	for _, r := range rows {
		id, _ := r["decision_id"].(string)
		if digestUint(id, 8) <= limit {
			dev = append(dev, r)
		} else {
			hold = append(hold, r)
		}
	}
	rm, cm := fitModel(dev, fields, "reward", o.PriorWeight), fitModel(dev, fields, "realized_cost", o.PriorWeight)
	rv, cv := crossValidate(dev, fields, "reward", o.Folds, o.PriorWeight), crossValidate(dev, fields, "realized_cost", o.Folds, o.PriorWeight)
	policy, diag := policyMap(dev, rm, fields, o.MinContext, o.MinDirect, o.MinGain)
	eval := evaluateDR(hold, policy, rm, cm, fields, o.Confidence, o.Resamples, o.Seed)
	nonBase := 0
	for _, a := range policy {
		if a != BaselineArm {
			nonBase++
		}
	}
	blockers := []string{}
	if nonBase == 0 {
		blockers = append(blockers, "no_contextual_candidate")
	}
	if len(hold) < max(1, o.MinHoldout) {
		blockers = append(blockers, "insufficient_holdout_events")
	}
	if eval["effective_sample_size"].(float64) < o.MinESS {
		blockers = append(blockers, "insufficient_effective_sample_size")
	}
	if eval["direct_target_exposures"].(int) < o.MinDirect {
		blockers = append(blockers, "insufficient_direct_target_exposures")
	}
	if rv["n"].(int) < o.MinValidation {
		blockers = append(blockers, "insufficient_reward_model_validation")
	}
	if low, ok := eval["reward_delta_vs_baseline"].(map[string]any)["ci_low"].(float64); !ok || low <= o.SafetyMargin {
		blockers = append(blockers, "reward_lower_bound_does_not_clear_margin")
	}
	if o.MaxRealizedCost != nil {
		if c, ok := eval["max_direct_realized_cost"].(float64); !ok || c > *o.MaxRealizedCost {
			blockers = append(blockers, "realized_cost_constraint_not_met")
		}
	}
	ids := []string{}
	cutoff := ""
	for _, r := range rows {
		id, _ := r["decision_id"].(string)
		ids = append(ids, id)
		if rec, _ := outcomeOf(r)["recorded_at"].(string); rec > cutoff {
			cutoff = rec
		}
	}
	sort.Strings(ids)
	var cutoffV, maxCost any
	if cutoff != "" {
		cutoffV = cutoff
	}
	if o.MaxRealizedCost != nil {
		maxCost = *o.MaxRealizedCost
	}
	return map[string]any{"scope": "contextual-retrieval-policy-development", "feature_schema_version": FeatureSchema, "context_fields": fields,
		"baseline_arm": BaselineArm, "development_fraction": o.DevelopmentFraction, "development_events": len(dev), "holdout_events": len(hold),
		"excluded_events": excluded, "source_decision_sha256": sha256Hex([]byte(strings.Join(ids, "\n"))), "evidence_cutoff": cutoffV,
		"reward_model": rm, "cost_model": cm, "reward_model_cross_validation": rv, "cost_model_cross_validation": cv,
		"development_policy": policy, "development_diagnostics": diag, "holdout_doubly_robust_evaluation": eval,
		"promotion": map[string]any{"eligible_for_shadow": len(blockers) == 0, "automatic_runtime_promotion": false, "nonbaseline_contexts": nonBase,
			"blockers": blockers, "minimum_context_events": o.MinContext, "minimum_direct_exposures": o.MinDirect, "minimum_holdout_events": o.MinHoldout,
			"minimum_effective_sample_size": o.MinESS, "minimum_model_validation_events": o.MinValidation, "minimum_estimated_gain": o.MinGain,
			"safety_margin": o.SafetyMargin, "max_realized_cost": maxCost},
		"methodology": map[string]any{"policy_learning": "development split with smoothed categorical direct model",
			"reward_model_validation": "deterministic cross-validation on development rows",
			"final_evaluation":        "doubly robust evaluation on an untouched deterministic holdout",
			"high_risk_override":      "non-low-risk rows are always mapped to adaptive_math"}}, nil
}
