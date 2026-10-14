package learning

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
)

const ManifestSchema = "retrieval-policy-manifest-v1"

// Canonical encodes v like Python json.dumps(sort_keys=True, compact,
// ensure_ascii=False). Values decoded with UseNumber keep their exact text.
func Canonical(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func canonicalSHA(v any) string {
	b, _ := Canonical(v)
	return sha256Hex(b)
}

// SigningKey reads a non-empty HMAC key from the named environment variable.
func SigningKey(env string) ([]byte, error) {
	v := os.Getenv(env)
	if v == "" {
		return nil, fmt.Errorf("policy signing key environment variable is empty: %s", env)
	}
	return []byte(v), nil
}

// KeyID is a non-secret identifier of a signing key.
func KeyID(key []byte) string { return sha256Hex(key)[:16] }

// HMAC signs canonical(v).
func HMAC(key []byte, v any) string {
	b, _ := Canonical(v)
	m := hmac.New(sha256.New, key)
	m.Write(b)
	return hex.EncodeToString(m.Sum(nil))
}

func validatePolicy(v any) (map[string]string, error) {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return nil, errors.New("contextual policy must be a non-empty object")
	}
	out := map[string]string{}
	for k, raw := range m {
		ctx, arm := strings.TrimSpace(k), strings.TrimSpace(fmt.Sprint(raw))
		if ctx == "" {
			return nil, errors.New("contextual policy contains a blank context key")
		}
		if !slices.Contains(SafeArms, arm) {
			return nil, fmt.Errorf("contextual policy contains unsafe arm: %s", arm)
		}
		out[ctx] = arm
	}
	return out, nil
}

func identity(fields []string, policy map[string]string, cutoff, reportSHA string) map[string]any {
	return map[string]any{"feature_schema_version": FeatureSchema, "context_fields": fields, "policy": policy,
		"evidence_cutoff": cutoff, "source_report_sha256": reportSHA, "fallback_arm": BaselineArm}
}

func stringList(v any) ([]string, bool) {
	l, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := []string{}
	for _, x := range l {
		out = append(out, fmt.Sprint(x))
	}
	return out, true
}

// CreateManifest signs a shadow-only manifest from an eligible Stage 6 report.
func CreateManifest(report map[string]any, key []byte) (map[string]any, error) {
	if len(key) == 0 {
		return nil, errors.New("signing key must not be empty")
	}
	if report["scope"] != "contextual-retrieval-policy-development" {
		return nil, errors.New("input is not a Stage 6 contextual policy report")
	}
	if p, _ := report["promotion"].(map[string]any); p == nil || p["eligible_for_shadow"] != true {
		return nil, errors.New("contextual policy is not eligible for shadow evaluation")
	}
	if report["feature_schema_version"] != FeatureSchema {
		return nil, errors.New("contextual policy feature schema is incompatible")
	}
	raw, ok := stringList(report["context_fields"])
	if !ok {
		return nil, errors.New("contextual policy is missing context fields")
	}
	fields, err := validateFields(raw)
	if err != nil {
		return nil, err
	}
	policy, err := validatePolicy(report["development_policy"])
	if err != nil {
		return nil, err
	}
	cutoff := strings.TrimSpace(fmt.Sprint(orNil(report["evidence_cutoff"])))
	if cutoff == "" {
		return nil, errors.New("contextual policy is missing an evidence cutoff")
	}
	reportSHA := canonicalSHA(report)
	id := canonicalSHA(identity(fields, policy, cutoff, reportSHA))[:32]
	m := map[string]any{"schema_version": ManifestSchema, "policy_id": id, "created_at": now(), "status": "shadow_only",
		"automatic_runtime_activation": false, "feature_schema_version": FeatureSchema, "context_fields": fields, "policy": policy,
		"fallback_arm": BaselineArm, "locked_risks": []string{"medium", "high"}, "evidence_cutoff": cutoff, "source_report_sha256": reportSHA,
		"source_decision_sha256": report["source_decision_sha256"], "reward_model": report["reward_model"], "cost_model": report["cost_model"],
		"holdout_evidence": report["holdout_doubly_robust_evaluation"],
		"rollback":         map[string]any{"target_arm": BaselineArm, "reason": "deterministic production baseline remains the rollback target"},
		"provenance":       map[string]any{"development_events": report["development_events"], "holdout_events": report["holdout_events"], "feature_schema_version": FeatureSchema}}
	m["signature"] = map[string]any{"algorithm": "hmac-sha256", "key_id": KeyID(key), "value": HMAC(key, m)}
	return m, nil
}

func orNil(v any) any {
	if v == nil {
		return ""
	}
	return v
}

// VerifyManifest checks integrity, signature and the shadow-only invariants.
func VerifyManifest(m map[string]any, key []byte) map[string]any {
	errs := []string{}
	add := func(s string) { errs = append(errs, s) }
	if len(key) == 0 {
		add("signing_key_missing")
	}
	if m["schema_version"] != ManifestSchema {
		add("schema_version_mismatch")
	}
	if m["feature_schema_version"] != FeatureSchema {
		add("feature_schema_mismatch")
	}
	var fields []string
	if raw, ok := stringList(m["context_fields"]); ok {
		f, err := validateFields(raw)
		if err != nil {
			add("invalid_context_fields")
		} else {
			fields = f
		}
	}
	policy, err := validatePolicy(m["policy"])
	if err != nil {
		add("invalid_policy")
	}
	cutoff := strings.TrimSpace(fmt.Sprint(orNil(m["evidence_cutoff"])))
	reportSHA := strings.TrimSpace(fmt.Sprint(orNil(m["source_report_sha256"])))
	if cutoff == "" {
		add("missing_evidence_cutoff")
	}
	if reportSHA == "" {
		add("missing_source_report_sha256")
	}
	if len(fields) > 0 && policy != nil && cutoff != "" && reportSHA != "" {
		if m["policy_id"] != canonicalSHA(identity(fields, policy, cutoff, reportSHA))[:32] {
			add("policy_id_mismatch")
		}
	}
	switch sig, _ := m["signature"].(map[string]any); {
	case sig == nil:
		add("signature_missing")
	case sig["algorithm"] != "hmac-sha256":
		add("signature_algorithm_mismatch")
	case len(key) > 0:
		unsigned := map[string]any{}
		for k, v := range m {
			if k != "signature" {
				unsigned[k] = v
			}
		}
		supplied, _ := sig["value"].(string)
		if !hmac.Equal([]byte(HMAC(key, unsigned)), []byte(supplied)) {
			add("signature_mismatch")
		}
		if sig["key_id"] != KeyID(key) {
			add("key_id_mismatch")
		}
	}
	if m["status"] != "shadow_only" {
		add("status_mismatch")
	}
	if m["fallback_arm"] != BaselineArm {
		add("fallback_arm_mismatch")
	}
	if rb, _ := m["rollback"].(map[string]any); rb == nil || rb["target_arm"] != BaselineArm {
		add("rollback_target_mismatch")
	}
	if l, ok := stringList(m["locked_risks"]); !ok || !slices.Equal(l, []string{"medium", "high"}) {
		add("locked_risks_mismatch")
	}
	if m["automatic_runtime_activation"] == true {
		add("automatic_activation_forbidden")
	}
	return map[string]any{"valid": len(errs) == 0, "errors": errs, "policy_id": m["policy_id"], "status": m["status"],
		"automatic_runtime_activation": false, "rollback_target": BaselineArm}
}

func parseTime(v any) (time.Time, bool) {
	s, _ := v.(string)
	if strings.TrimSpace(s) == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	return t, err == nil
}

// Hoeffding is the conservative union-bound anytime confidence sequence.
func Hoeffding(values []float64, confidence, bound float64) (map[string]any, error) {
	if !(confidence > 0 && confidence < 1) {
		return nil, errors.New("confidence must be between 0 and 1")
	}
	if bound <= 0 || math.IsInf(bound, 0) || math.IsNaN(bound) {
		return nil, errors.New("absolute_bound must be finite and positive")
	}
	alpha, running := 1-confidence, 0.0
	checkpoints := []map[string]any{}
	var final map[string]any
	for i, v := range values {
		n := float64(i + 1)
		running += v
		mean := running / n
		radius := bound * math.Sqrt(2*math.Log(2/(alpha*6/(math.Pi*math.Pi*n*n)))/n)
		final = map[string]any{"n": i + 1, "mean": r6(mean), "lower": r6(mean - radius), "upper": r6(mean + radius)}
		if (i+1)&i == 0 {
			checkpoints = append(checkpoints, final)
		}
	}
	if final != nil && (len(checkpoints) == 0 || checkpoints[len(checkpoints)-1]["n"] != final["n"]) {
		checkpoints = append(checkpoints, final)
	}
	var f any
	if final != nil {
		f = final
	}
	return map[string]any{"confidence": confidence, "method": "union_bound_hoeffding_anytime_sequence", "anytime_valid": true,
		"exact_off_policy_confidence_sequence_paper": false, "absolute_bound": bound, "final": f, "checkpoints": checkpoints}, nil
}

// ShadowOptions configures shadow evaluation.
type ShadowOptions struct {
	Confidence, RewardMin, RewardMax, MaxWeight, SafetyMargin float64
	MinimumNewEvents, Resamples                               int
	Seed                                                      int64
	MaxRealizedCost                                           *float64
}

func modelFrom(v any) (Model, bool) {
	b, err := json.Marshal(v)
	if err != nil {
		return Model{}, false
	}
	var m Model
	return m, json.Unmarshal(b, &m) == nil && v != nil
}

// Shadow evaluates a fixed signed policy only on verified outcomes recorded
// after its evidence cutoff; it never receives runtime traffic.
func Shadow(root string, manifest map[string]any, key []byte, o ShadowOptions) (map[string]any, error) {
	verification := VerifyManifest(manifest, key)
	if o.RewardMax <= o.RewardMin {
		return nil, errors.New("reward_max must be greater than reward_min")
	}
	if o.MaxWeight <= 0 {
		return nil, errors.New("max_importance_weight must be positive")
	}
	cutoffS := strings.TrimSpace(fmt.Sprint(orNil(manifest["evidence_cutoff"])))
	cutoff, ok := parseTime(cutoffS)
	if !ok {
		return nil, errors.New("manifest evidence cutoff is not a valid timestamp")
	}
	excluded := map[string]int{"not_after_cutoff": 0, "unverified": 0, "invalid_outcome": 0, "feature_schema_mismatch": 0, "invalid_propensity": 0, "invalid_timestamp": 0}
	rows := []map[string]any{}
	for _, r := range Records(root) {
		out := outcomeOf(r)
		if out == nil || out["verified"] != true {
			excluded["unverified"]++
			continue
		}
		rec, ok := parseTime(out["recorded_at"])
		if !ok {
			excluded["invalid_timestamp"]++
			continue
		}
		if !rec.After(cutoff) {
			excluded["not_after_cutoff"]++
			continue
		}
		_, ok1 := num(out["reward"])
		c, ok2 := num(out["realized_cost"])
		if !ok1 || !ok2 || c < 0 {
			excluded["invalid_outcome"]++
			continue
		}
		feats := normalizeFeatures(r["context_features"])
		if r["feature_schema_version"] != FeatureSchema || feats == nil {
			excluded["feature_schema_mismatch"]++
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
		rows = append(rows, cp)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return fmt.Sprint(outcomeOf(rows[i])["recorded_at"]) < fmt.Sprint(outcomeOf(rows[j])["recorded_at"])
	})
	fields, ok1 := stringList(manifest["context_fields"])
	policyRaw, ok2 := manifest["policy"].(map[string]any)
	rm, ok3 := modelFrom(manifest["reward_model"])
	cm, ok4 := modelFrom(manifest["cost_model"])
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil, errors.New("manifest is missing contextual policy evidence")
	}
	policy := map[string]string{}
	for k, v := range policyRaw {
		policy[k] = fmt.Sprint(v)
	}
	violations := map[string]int{"reward_range_violation": 0, "importance_weight_violation": 0, "invalid_outcome": 0, "invalid_propensity": 0}
	var supported []map[string]any
	var scores, costs []float64
	unsupported := 0
	for _, r := range rows {
		target := BaselineArm
		if isLow(r) {
			if a, ok := policy[rowContext(r, fields)]; ok && slices.Contains(SafeArms, a) {
				target = a
			}
		}
		if p, ok := propensity(r, target); !ok || p <= 0 {
			unsupported++
			continue
		}
		reward, ok := num(outcomeOf(r)["reward"])
		if !ok || reward < o.RewardMin || reward > o.RewardMax {
			violations["reward_range_violation"]++
			continue
		}
		ch := chosenArm(r)
		p, _ := propensity(r, ch)
		score, bad := 0.0, false
		for _, arm := range []string{target, BaselineArm} {
			if ch != arm {
				continue
			}
			if 1/p > o.MaxWeight {
				bad = true
				break
			}
		}
		if bad {
			violations["importance_weight_violation"]++
			continue
		}
		tTerm, bTerm := 0.0, 0.0 // V2: target_term - baseline_term with weight = 1/p
		if ch == target {
			tTerm = (1 / p) * reward
		}
		if ch == BaselineArm {
			bTerm = (1 / p) * reward
		}
		score = tTerm - bTerm
		supported, scores = append(supported, r), append(scores, score)
		if target != BaselineArm && ch == target {
			if c, ok := num(outcomeOf(r)["realized_cost"]); ok {
				costs = append(costs, c)
			}
		}
	}
	seq, err := Hoeffding(scores, o.Confidence, math.Max(o.MaxWeight*math.Max(math.Abs(o.RewardMin), math.Abs(o.RewardMax)), 1e-12))
	if err != nil {
		return nil, err
	}
	dr := evaluateDR(supported, policy, rm, cm, fields, o.Confidence, o.Resamples, o.Seed)
	blockers := []string{}
	if verification["valid"] != true {
		blockers = append(blockers, "manifest_verification_failed")
	}
	if len(rows) < max(1, o.MinimumNewEvents) {
		blockers = append(blockers, "insufficient_post_cutoff_events")
	}
	if unsupported > 0 {
		blockers = append(blockers, "target_policy_support_violation")
	}
	for _, v := range violations {
		if v > 0 {
			blockers = append(blockers, "sequential_monitoring_input_violation")
			break
		}
	}
	final, _ := seq["final"].(map[string]any)
	if low, ok := final["lower"].(float64); !ok || low <= o.SafetyMargin {
		blockers = append(blockers, "anytime_lower_bound_does_not_clear_margin")
	}
	var maxCost, maxLimit any
	if len(costs) > 0 {
		maxCost = r6(slices.Max(costs))
	}
	if o.MaxRealizedCost != nil {
		maxLimit = *o.MaxRealizedCost
		if len(costs) == 0 || slices.Max(costs) > *o.MaxRealizedCost {
			blockers = append(blockers, "realized_cost_constraint_not_met")
		}
	}
	return map[string]any{"scope": "contextual-retrieval-shadow-evaluation", "policy_id": manifest["policy_id"], "manifest_verification": verification,
		"evidence_cutoff": cutoffS, "post_cutoff_events": len(rows), "supported_events": len(supported), "excluded_events": excluded,
		"unsupported_target_events": unsupported, "monitoring_violations": violations, "doubly_robust_shadow_evaluation": dr,
		"sequential_reward_confidence": seq, "max_direct_realized_cost": maxCost,
		"shadow_gate": map[string]any{"eligible_for_manual_promotion_review": len(blockers) == 0, "blockers": blockers, "minimum_new_events": o.MinimumNewEvents,
			"safety_margin": o.SafetyMargin, "max_realized_cost": maxLimit, "automatic_runtime_activation": false},
		"execution":   map[string]any{"policy_received_runtime_traffic": false, "mode": "counterfactual_shadow_only", "rollback_target": BaselineArm},
		"methodology": map[string]any{"sequential_gate": "conservative union-bound Hoeffding confidence sequence", "paper_equivalence": "does not implement the exact betting/martingale construction from Off-policy Confidence Sequences"}}, nil
}
