package learning

import (
	"math"
	"strings"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
)

func route(risk model.Risk) model.RouteDecision {
	return model.RouteDecision{Lane: model.LaneSmall, Risk: risk}
}

func TestChooseSafetyRules(t *testing.T) {
	cfg := config.Learning{Mode: "explore"}
	always := func(v float64) func() float64 { return func() float64 { return v } }

	d := Choose(cfg, "q", route(model.RiskLow), "exact", 0, 1, nil, always(.999))
	if d.ChosenArm != "rrf_mmr_090" || !d.Explored || d.SafetyReason != "bounded_exploration" {
		t.Fatalf("high sample must explore the last arm: %+v", d)
	}
	sum := 0.0
	for _, p := range d.ArmPropensities {
		sum += p
	}
	if math.Abs(sum-1) > 1e-9 || math.Abs(d.ArmPropensities[BaselineArm]-(1-.05+.05/7)) > 1e-12 {
		t.Fatalf("propensities=%v", d.ArmPropensities)
	}
	if d := Choose(cfg, "q", route(model.RiskLow), "exact", 0, 1, nil, always(0)); d.ChosenArm != BaselineArm || d.Explored {
		t.Fatalf("low sample keeps baseline: %+v", d)
	}
	if d := Choose(cfg, "q", route(model.RiskMedium), "exact", 0, 1, nil, always(.999)); d.ChosenArm != BaselineArm || d.SafetyReason != "risk_locked" {
		t.Fatalf("medium risk must be locked: %+v", d)
	}
	t.Setenv(config.LearningKillSwitchEnv, "1")
	if d := Choose(cfg, "q", route(model.RiskLow), "exact", 0, 1, nil, always(.999)); d.ChosenArm != BaselineArm || d.SafetyReason != "kill_switch" {
		t.Fatalf("kill switch: %+v", d)
	}
	if d := Choose(config.Learning{}, "q", route(model.RiskLow), "exact", 0, 1, nil, nil); d.Mode != "off" || d.ChosenArm != BaselineArm {
		t.Fatalf("default mode is off: %+v", d)
	}
	bad := &Assignment{ChosenArm: "bm25_rank", ArmPropensities: map[string]float64{"bm25_rank": .5, BaselineArm: .4}}
	if d := Choose(cfg, "q", route(model.RiskLow), "exact", 0, 1, bad, nil); d.ChosenArm != BaselineArm || d.SafetyReason != "deployment_assignment_invalid" {
		t.Fatalf("propensities not summing to 1 must fail closed: %+v", d)
	}
	if f := BuildFeatures(strings.Repeat("w ", 20), route(model.RiskLow), "exact", 5, 3); f.QueryLengthBucket != "17+" || f.ChangedFilesBucket != "4+" || f.WorkspaceRootsBucket != "3+" {
		t.Fatalf("%+v", f)
	}
}

func TestRecordsAndOutcomes(t *testing.T) {
	root := t.TempDir()
	cfg := config.Learning{Mode: "observe"}
	d, path := Prepare(root, cfg, "fix retry", route(model.RiskLow), "exact", 1, 1, nil)
	if path == "" || d.SafetyReason != "observe_only" {
		t.Fatalf("observe mode must log: %+v %q", d, path)
	}
	if _, err := Observe(root, d.DecisionID, 12.5, 400, 0, .8, "sufficient"); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	for name, o := range map[string]Outcome{
		"untrusted": {Success: true, Source: "my-laptop", VerifierIdentity: "me", EvidenceDigest: digest},
		"blank id":  {Success: true, Source: "local:test-suite", EvidenceDigest: digest},
		"digest":    {Success: true, Source: "local:test-suite", VerifierIdentity: "ci", EvidenceDigest: "md5:abc"},
	} {
		if _, err := RecordOutcome(root, d.DecisionID, o); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := RecordOutcome(root, d.DecisionID, Outcome{Success: true, Source: "local:test-suite", VerifierIdentity: "ci", EvidenceDigest: digest}); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordOutcome(root, d.DecisionID, Outcome{Success: true, Source: "local:test-suite", VerifierIdentity: "ci", EvidenceDigest: digest}); err == nil {
		t.Fatal("outcomes are immutable")
	}
	if _, err := RecordOutcome(root, "../escape", Outcome{}); err == nil {
		t.Fatal("unsafe decision id accepted")
	}
	s := Status(root, cfg)
	if s["decisions"] != 1 || s["observations"] != 1 || s["verified_outcomes"] != 1 || s["mode"] != "observe" {
		t.Fatalf("%v", s)
	}
	c, err := ApplyArm(config.Default(), "rrf_mmr_050")
	if r, _, l, _ := c.Context.Experiments.Policy(); err != nil || r != "rrf_mmr" || l != .5 {
		t.Fatalf("apply arm: %v %s %v", err, r, l)
	}
	if _, err := ApplyArm(config.Default(), "selector_off"); err == nil {
		t.Fatal("safety-affecting profiles are not learning arms")
	}
}

func TestHoeffdingWidthShrinks(t *testing.T) {
	vals := make([]float64, 64)
	for i := range vals {
		vals[i] = .5
	}
	s, err := Hoeffding(vals, .95, 1)
	if err != nil {
		t.Fatal(err)
	}
	cps := s["checkpoints"].([]map[string]any)
	if len(cps) != 7 || cps[0]["n"] != 1 || cps[6]["n"] != 64 {
		t.Fatalf("power-of-two checkpoints: %v", cps)
	}
	if w0, w6 := cps[0]["upper"].(float64)-cps[0]["lower"].(float64), cps[6]["upper"].(float64)-cps[6]["lower"].(float64); w6 >= w0 {
		t.Fatal("interval must shrink")
	}
}
