// Package learning is V2's opt-in safe retrieval learning layer: logged
// decisions with behaviour propensities, low-risk-only bounded exploration,
// verified delayed outcomes, off-policy evaluation, contextual policies,
// signed shadow-only manifests and shadow evaluation. Nothing here changes
// runtime behaviour automatically; mode defaults to "off".
package learning

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
)

const (
	BaselineArm          = "adaptive_math"
	KillSwitchEnv        = "AI_WORKFLOW_LEARNING_KILL_SWITCH"
	FeatureSchema        = "retrieval-context-v1"
	TrustedSourcesEnv    = "AI_WORKFLOW_TRUSTED_OUTCOME_SOURCES"
	learningRelative     = "ai-workspace/generated/learning"
	explorePolicyVersion = "safe-epsilon-v1"
)

// SafeArms are ranking-only arms; safety-affecting profiles can never be explored.
var SafeArms = []string{BaselineArm, "source_rank", "bm25_rank", "rrf_only", "rrf_mmr_050", "rrf_mmr_075", "rrf_mmr_090"}

// FeatureFields is the retrieval-context-v1 categorical schema.
var FeatureFields = []string{"lane", "risk", "intent", "query_length_bucket", "changed_files_bucket", "workspace_roots_bucket"}

// DefaultPolicyFields are the default contextual policy fields.
var DefaultPolicyFields = []string{"intent", "lane", "changed_files_bucket"}

// Config is context.learning (defined in the config package).
type Config = config.Learning

// Features is the categorical context of one decision (no task text).
type Features struct {
	Lane                 string `json:"lane"`
	Risk                 string `json:"risk"`
	Intent               string `json:"intent"`
	QueryLengthBucket    string `json:"query_length_bucket"`
	ChangedFilesBucket   string `json:"changed_files_bucket"`
	WorkspaceRootsBucket string `json:"workspace_roots_bucket"`
}

// BuildFeatures buckets the decision context (V2 build_context_features).
func BuildFeatures(query string, d model.RouteDecision, intent string, changed, roots int) Features {
	words := len(strings.Fields(query))
	q := "17+"
	switch {
	case words <= 4:
		q = "1-4"
	case words <= 8:
		q = "5-8"
	case words <= 16:
		q = "9-16"
	}
	c := "4+"
	switch n := max(0, changed); {
	case n == 0:
		c = "0"
	case n == 1:
		c = "1"
	case n <= 3:
		c = "2-3"
	}
	w := "3+"
	switch n := max(1, roots); n {
	case 1:
		w = "1"
	case 2:
		w = "2"
	}
	return Features{string(d.Lane), string(d.Risk), intent, q, c, w}
}

// Decision is one logged learning decision (V2 LearningDecision).
type Decision struct {
	DecisionID             string             `json:"decision_id"`
	PolicyVersion          string             `json:"policy_version"`
	Mode                   string             `json:"mode"`
	BaselineArm            string             `json:"baseline_arm"`
	EligibleArms           []string           `json:"eligible_arms"`
	ChosenArm              string             `json:"chosen_arm"`
	ChosenPropensity       float64            `json:"chosen_propensity"`
	ArmPropensities        map[string]float64 `json:"arm_propensities"`
	ExplorationProbability float64            `json:"exploration_probability"`
	Explored               bool               `json:"explored"`
	SafetyReason           string             `json:"safety_reason"`
	Lane                   string             `json:"lane"`
	Risk                   string             `json:"risk"`
	Intent                 string             `json:"intent"`
	TaskFingerprint        string             `json:"task_fingerprint"`
	FeatureSchemaVersion   string             `json:"feature_schema_version"`
	ContextFeatures        Features           `json:"context_features"`
	CreatedAt              string             `json:"created_at"`
	Deployment             map[string]any     `json:"deployment"`
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// systemFloat is a cryptographically random float in [0, 1) (SystemRandom).
func systemFloat() float64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return float64(binary.BigEndian.Uint64(b[:])>>11) / (1 << 53)
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00") }

func round12(f float64) float64 { return math.Round(f*1e12) / 1e12 }

// Assignment is a Stage 7 deployment arm assignment (nil when no deployment).
type Assignment struct {
	ChosenArm       string
	ArmPropensities map[string]float64
	Mode            string
	SafetyReason    string
	Deployment      map[string]any
}

// Choose makes the learning decision before retrieval (V2 choose_learning_decision).
func Choose(cfg Config, query string, d model.RouteDecision, intent string, changed, roots int, assign *Assignment, sample func() float64) Decision {
	mode, eps, arms := cfg.ModeOf(), cfg.Epsilon(), cfg.Arms(SafeArms, BaselineArm)
	risk := string(d.Risk)
	reason, chosen, version := "learning_disabled", BaselineArm, explorePolicyVersion
	props := map[string]float64{BaselineArm: 1}
	order := []string{BaselineArm}
	explored := false
	var deployment map[string]any
	if sample == nil {
		sample = systemFloat
	}
	switch {
	case assign != nil:
		valid := slices.Contains(SafeArms, assign.ChosenArm) && len(assign.ArmPropensities) > 0
		sum := 0.0
		for a, p := range assign.ArmPropensities {
			if !slices.Contains(SafeArms, a) || math.IsNaN(p) || p < 0 || p > 1 {
				valid = false
			}
			sum += p
		}
		if math.Abs(sum-1) > 1e-9 || assign.ArmPropensities[assign.ChosenArm] <= 0 || (assign.ChosenArm != BaselineArm && risk != "low") {
			valid = false
		}
		if valid {
			chosen, props, mode = assign.ChosenArm, assign.ArmPropensities, orDefault(assign.Mode, "canary")
			order = sortedArms(props)
			reason = orDefault(assign.SafetyReason, "deployment_canary")
			deployment = assign.Deployment
			if deployment == nil {
				deployment = map[string]any{}
			}
			explored = chosen != BaselineArm
			eps = 0
			if p, ok := deployment["candidate_probability"].(float64); ok {
				eps = math.Min(1, math.Max(0, p))
			}
			version = "deployment:fail-closed"
			if id, _ := deployment["policy_id"].(string); strings.TrimSpace(id) != "" {
				version = "deployment:" + strings.TrimSpace(id)
			}
		} else {
			mode, reason, eps = "canary", "deployment_assignment_invalid", 0
			deployment = map[string]any{"assignment": "baseline", "reason": "deployment_assignment_invalid", "source": "stage7_deployment"}
		}
	case mode == "observe":
		reason = "observe_only"
	case mode == "explore":
		switch {
		case cfg.KillSwitchOn():
			reason = "kill_switch"
		case risk != "low" || !cfg.LowAllowed():
			reason = "risk_locked"
		case len(arms) <= 1 || eps <= 0:
			reason = "no_exploration_mass"
		default:
			share := eps / float64(len(arms))
			props, order = map[string]float64{}, arms
			for _, a := range arms {
				props[a] = share
			}
			props[BaselineArm] = 1 - eps + share
			point, cum := sample(), 0.0
			chosen = arms[len(arms)-1]
			for _, a := range arms {
				cum += props[a]
				if point <= cum {
					chosen = a
					break
				}
			}
			explored = chosen != BaselineArm
			reason = "baseline_sample"
			if explored {
				reason = "bounded_exploration"
			}
		}
	}
	rounded := map[string]float64{}
	for a, p := range props {
		rounded[a] = round12(p)
	}
	exploration := 0.0
	if mode == "explore" {
		exploration = eps
	}
	sum := sha256Hex([]byte(query))
	return Decision{DecisionID: newID(), PolicyVersion: version, Mode: mode, BaselineArm: BaselineArm, EligibleArms: order,
		ChosenArm: chosen, ChosenPropensity: props[chosen], ArmPropensities: rounded, ExplorationProbability: exploration,
		Explored: explored, SafetyReason: reason, Lane: string(d.Lane), Risk: risk, Intent: intent, TaskFingerprint: sum,
		FeatureSchemaVersion: FeatureSchema, ContextFeatures: BuildFeatures(query, d, intent, changed, roots), CreatedAt: now(), Deployment: deployment}
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func sortedArms(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for a := range m {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// BaselineFallback replaces a decision that could not be logged.
func BaselineFallback(src Decision, reason string) Decision {
	d := src
	if src.Deployment != nil {
		d.Deployment = map[string]any{}
		for k, v := range src.Deployment {
			d.Deployment[k] = v
		}
		d.Deployment["assignment"], d.Deployment["reason"] = "baseline", reason
		d.Deployment["candidate_probability"], d.Deployment["chosen_probability"] = 0.0, 1.0
	}
	d.DecisionID, d.EligibleArms, d.ChosenArm, d.ChosenPropensity = newID(), []string{BaselineArm}, BaselineArm, 1
	d.ArmPropensities, d.ExplorationProbability, d.Explored, d.SafetyReason, d.CreatedAt = map[string]float64{BaselineArm: 1}, 0, false, reason, now()
	return d
}

var idRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

func recordPath(root, kind, id string) (string, error) {
	if !idRE.MatchString(id) {
		return "", errors.New("decision_id must be a 32-character lowercase hexadecimal ID")
	}
	return filepath.Join(root, filepath.FromSlash(learningRelative), kind, id+".json"), nil
}

// Mirror is called after each immutable record is written (production index).
var Mirror func(root, kind string, payload map[string]any)

// createJSON writes an immutable record exclusively (never overwritten).
func createJSON(path string, v any) error {
	b, err := sortedJSON(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rec-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, werr := f.Write(append(b, '\n'))
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		return werr
	}
	return nil
}

// sortedJSON encodes with sorted keys (maps) and indentation (V2 sort_keys=True).
func sortedJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		return nil, err
	}
	return json.MarshalIndent(generic, "", "  ")
}

func asMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func write(root, kind, id string, payload any) (string, error) {
	p, err := recordPath(root, kind, id)
	if err != nil {
		return "", err
	}
	if err := createJSON(p, payload); err != nil {
		return "", err
	}
	if Mirror != nil {
		Mirror(root, kind, asMap(payload))
	}
	return p, nil
}

// Prepare chooses and logs a decision. If an exploratory decision cannot be
// logged it fails closed to the baseline arm.
func Prepare(root string, cfg Config, query string, d model.RouteDecision, intent string, changed, roots int, assign *Assignment) (Decision, string) {
	dec := Choose(cfg, query, d, intent, changed, roots, assign, nil)
	if dec.Mode == "off" {
		return dec, ""
	}
	if p, err := write(root, "decisions", dec.DecisionID, dec); err == nil {
		return dec, p
	}
	if dec.ChosenArm == BaselineArm {
		return dec, ""
	}
	fb := BaselineFallback(dec, "decision_log_failure")
	p, _ := write(root, "decisions", fb.DecisionID, fb)
	return fb, p
}

// Observe logs post-retrieval observations for a decision.
func Observe(root, id string, elapsedMS float64, usedChars, fallbacks int, sufficiency float64, evidenceState string) (string, error) {
	return write(root, "observations", id, map[string]any{"decision_id": id, "observed_at": now(),
		"elapsed_ms": math.Round(math.Max(0, elapsedMS)*1e4) / 1e4, "used_chars": max(0, usedChars), "fallback_count": max(0, fallbacks),
		"sufficiency_score": math.Round(math.Min(1, math.Max(0, sufficiency))*1e6) / 1e6, "evidence_state": evidenceState})
}

var defaultTrusted = []string{"github-actions:test-suite", "github-actions:integration-suite", "signed-human-review", "local:test-suite"}

// TrustedSources are runtime-owned verifier IDs (never repository config).
func TrustedSources() []string {
	out := append([]string{}, defaultTrusted...)
	for _, s := range strings.Split(os.Getenv(TrustedSourcesEnv), ",") {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Outcome is a verified delayed outcome request.
type Outcome struct {
	Success          bool
	Source           string
	VerifierIdentity string
	EvidenceDigest   string
	Reward           *float64
	RealizedCost     float64
}

// RecordOutcome links a verified outcome to a logged decision.
func RecordOutcome(root, id string, o Outcome) (string, error) {
	p, err := recordPath(root, "decisions", id)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("unknown learning decision: %s", id)
	}
	var dec map[string]any
	if json.Unmarshal(b, &dec) != nil || dec == nil {
		return "", fmt.Errorf("learning decision is invalid: %s", id)
	}
	source := strings.TrimSpace(o.Source)
	if !slices.Contains(TrustedSources(), source) {
		return "", errors.New("outcome source is not trusted by runtime policy")
	}
	verifier := strings.TrimSpace(o.VerifierIdentity)
	if verifier == "" {
		return "", errors.New("verifier_identity must not be blank")
	}
	reward := 0.0
	if o.Success {
		reward = 1
	}
	if o.Reward != nil {
		reward = *o.Reward
	}
	if math.IsNaN(reward) || reward < 0 || reward > 1 {
		return "", errors.New("reward must be finite and between 0.0 and 1.0")
	}
	if math.IsNaN(o.RealizedCost) || math.IsInf(o.RealizedCost, 0) || o.RealizedCost < 0 {
		return "", errors.New("realized_cost must be finite and non-negative")
	}
	digest := strings.ToLower(strings.TrimSpace(o.EvidenceDigest))
	if !digestRE.MatchString(digest) {
		return "", errors.New("immutable verifier evidence must use sha256:<64 hex>")
	}
	recorded := now()
	created, _ := dec["created_at"].(string)
	var delay any
	if c, err := time.Parse(time.RFC3339Nano, created); err == nil {
		if r, err := time.Parse(time.RFC3339Nano, recorded); err == nil {
			delay = math.Round(math.Max(0, r.Sub(c).Seconds())*1e6) / 1e6
		}
	}
	var createdV any
	if created != "" {
		createdV = created
	}
	return write(root, "outcomes", id, map[string]any{"decision_id": id, "verified": true, "success": o.Success, "reward": reward,
		"realized_cost": o.RealizedCost, "source": source, "verifier_identity": verifier, "evidence_digest": digest, "recorded_at": recorded,
		"decision_created_at": createdV, "verification_delay_seconds": delay, "metadata": map[string]any{}})
}

func readDir(dir string) map[string]map[string]any {
	out := map[string]map[string]any{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil || m == nil {
			continue
		}
		if id, _ := m["decision_id"].(string); strings.TrimSpace(id) != "" {
			out[id] = m
		}
	}
	return out
}

// Records joins decisions with observations and outcomes, sorted by ID.
func Records(root string) []map[string]any {
	base := filepath.Join(root, filepath.FromSlash(learningRelative))
	decisions, obs, outs := readDir(filepath.Join(base, "decisions")), readDir(filepath.Join(base, "observations")), readDir(filepath.Join(base, "outcomes"))
	ids := make([]string, 0, len(decisions))
	for id := range decisions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows := []map[string]any{}
	for _, id := range ids {
		row := decisions[id]
		row["observation"], row["outcome"] = nilIfMissing(obs[id]), nilIfMissing(outs[id])
		rows = append(rows, row)
	}
	return rows
}

func nilIfMissing(m map[string]any) any {
	if m == nil {
		return nil
	}
	return m
}

// Status reports mode, safety locks and record counts.
func Status(root string, cfg Config) map[string]any {
	rows := Records(root)
	current, observed, verified := 0, 0, 0
	for _, r := range rows {
		if r["feature_schema_version"] == FeatureSchema {
			current++
		}
		if _, ok := r["observation"].(map[string]any); ok {
			observed++
		}
		if _, ok := r["outcome"].(map[string]any); ok {
			verified++
		}
	}
	return map[string]any{"mode": cfg.ModeOf(), "kill_switch": cfg.KillSwitchOn(), "kill_switch_env": KillSwitchEnv, "baseline_arm": BaselineArm,
		"safe_exploration_arms": SafeArms, "feature_schema_version": FeatureSchema, "decisions": len(rows),
		"decisions_with_current_features": current, "observations": observed, "verified_outcomes": verified}
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ApplyArm returns cfg with the ranking experiment of a safe arm applied.
func ApplyArm(cfg config.Config, arm string) (config.Config, error) {
	if !slices.Contains(SafeArms, arm) {
		return cfg, fmt.Errorf("unsafe or unknown retrieval arm: %s", arm)
	}
	if arm == BaselineArm {
		return cfg, nil
	}
	exp := config.Experiments{}
	if cfg.Context.Experiments != nil {
		exp = *cfg.Context.Experiments
	}
	switch arm {
	case "source_rank":
		exp.HybridRanker = "source"
	case "bm25_rank":
		exp.HybridRanker = "bm25"
	case "rrf_only":
		exp.HybridRanker = "rrf"
	default: // rrf_mmr_NNN
		l := 0.0
		fmt.Sscanf(strings.TrimPrefix(arm, "rrf_mmr_"), "%f", &l)
		l /= 100
		exp.HybridRanker, exp.MMRLambda = "rrf_mmr", &l
	}
	cfg.Context.Experiments = &exp
	return cfg, nil
}
