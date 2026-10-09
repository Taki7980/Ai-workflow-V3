package brief

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/providers"
)

func item(source, text string) model.ContextItem {
	return model.ContextItem{Source: source, Text: text, Score: 1, Metadata: map[string]any{"repository_id": "r1"}}
}

func TestSufficiencyEmpty(t *testing.T) {
	if got := EvaluateSufficiency("x", nil, false, nil, 0.72); got != (Sufficiency{}) {
		t.Fatalf("got %+v", got)
	}
}

func TestSufficiencyFullScore(t *testing.T) {
	items := []model.ContextItem{item("lightweight_index", "func RetryPayment"), item("durable_memory", "retry payment backoff")}
	got := EvaluateSufficiency("retry payment", items, false, nil, 0.72)
	want := Sufficiency{Score: 1, Sufficient: true, LexicalCoverage: 1, SourceDiversity: 2, ExactMatch: true, StructuralComplete: true}
	if got != want {
		t.Fatalf("got %+v", got)
	}
}

func TestSufficiencyStructuralRequired(t *testing.T) {
	items := []model.ContextItem{item("lightweight_index", "alpha beta")}
	got := EvaluateSufficiency("alpha beta", items, true, []string{"callers"}, 0.5)
	if got.StructuralComplete || got.Sufficient || got.Score != 0.815 {
		t.Fatalf("got %+v", got)
	}
}

func TestSelectiveBranches(t *testing.T) {
	ok := Sufficiency{Score: 0.9, Sufficient: true, LexicalCoverage: 0.9, StructuralComplete: true}
	expected := map[string]bool{"r1": true}
	fresh := []model.ContextItem{item("x", "a")}
	stale := []model.ContextItem{{Source: "x", Text: "a", Stale: true, Metadata: map[string]any{"repository_id": "r1"}}}
	foreign := []model.ContextItem{{Source: "x", Text: "a", Metadata: map[string]any{"repository_id": "r9"}}}
	cases := []struct {
		name  string
		items []model.ContextItem
		s     Sufficiency
		want  Selective
	}{
		{"none", nil, ok, Selective{"no_context", false, 0, []string{"no_selected_context"}}},
		{"foreign", foreign, ok, Selective{"wrong_repository", false, 0, []string{"evidence_repository_outside_routing_plan"}}},
		{"stale", stale, ok, Selective{"irrelevant", false, 0, []string{"all_selected_evidence_is_stale"}}},
		{"floor", fresh, Sufficiency{Score: 0.1, LexicalCoverage: 0.1, StructuralComplete: true}, Selective{"irrelevant", false, 0.1, []string{"query_coverage_below_floor", "sufficiency_below_threshold"}}},
		{"partial", fresh, Sufficiency{Score: 0.5, LexicalCoverage: 0.5}, Selective{"partial", false, 0.5, []string{"required_structural_evidence_incomplete", "sufficiency_below_threshold"}}},
		{"supported", fresh, ok, Selective{"supported", true, 0.9, []string{"evidence_quality_gate_passed"}}},
	}
	for _, c := range cases {
		if got := EvaluateSelective(c.items, c.s, expected, 0.15); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s: got %+v", c.name, got)
		}
	}
}

func TestEvidenceState(t *testing.T) {
	if EvidenceState(model.LaneFull, true) != "sufficient" || EvidenceState(model.LaneAnswer, false) != "abstain" || EvidenceState(model.LaneSmall, false) != "requires_exploration" {
		t.Fatal("evidence state mapping wrong")
	}
}

func TestOrchestrationFullHighStructural(t *testing.T) {
	d := model.RouteDecision{Lane: model.LaneFull, Risk: model.RiskHigh}
	got := BuildOrchestration(d, "structural", false, true, false, []string{"a", "b", "c"}, 2, providers.Status{Superpowers: true, CodeReviewGraph: true}, config.Default().Execution.OrchestrationBudget)
	if got.ComplexityScore != 16 || got.AgentSlots != 4 || got.ReviewPasses != 2 || got.VerificationPasses != 2 || got.GraphDepth != 3 || got.NativeFallback {
		t.Fatalf("got %+v", got)
	}
	if !reflect.DeepEqual(got.CRGPlan, []string{"get_minimal_context_tool", "get_impact_radius_tool", "query_graph_tool", "get_review_context_tool"}) {
		t.Fatalf("crg plan %q", got.CRGPlan)
	}
	if !reflect.DeepEqual(got.SuperpowersSkills, []string{"writing-plans", "subagent-driven-development", "requesting-code-review", "verification-before-completion"}) {
		t.Fatalf("skills %q", got.SuperpowersSkills)
	}
}

func TestOrchestrationAnswerNoProviders(t *testing.T) {
	got := BuildOrchestration(model.RouteDecision{Lane: model.LaneAnswer, Risk: model.RiskLow}, "exact", true, true, true, nil, 1, providers.Status{}, config.OrchestrationBudget{})
	if got.AgentSlots != 1 || got.SuperpowersSkills == nil || len(got.CRGPlan) != 0 || got.CRGPlan == nil || got.NativeFallback || got.Budget.MaxAgentSlots != 1 || got.Budget.MaxCRGCalls != 0 {
		t.Fatalf("got %+v", got)
	}
}

func samplePacket() Packet {
	return Packet{
		Task:              `fix "quoted" ñ`,
		RouteDecision:     model.RouteDecision{Lane: model.LaneFull, Risk: model.RiskHigh, Confidence: 0.5},
		ExecutionProvider: "native", ModelTier: "capable", ExecutionHint: "Use native Plan -> Build -> Review fallback.",
		Budget: Budget{EstimatedContextTokens: 6000, MaxOutputTokens: 1200},
		Retrieval: RetrievalDiagnostics{
			RetrievalIntent: "mixed", EvidenceState: "requires_exploration",
			Orchestration: Orchestration{AgentSlots: 2, SuperpowersSkills: []string{}, CRGPlan: []string{}},
		},
		Context:              []model.ContextItem{{Source: "x", Text: "ctx line"}},
		ChangedFilesDetected: []string{},
	}
}

func TestFormatPrompt(t *testing.T) {
	out, err := Format(samplePacket(), "prompt")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	for _, want := range []string{`[TASK] fix "quoted" ñ`, "[LANE] full [RISK] high [CONFIDENCE] 0.50 [MODEL_TIER] capable", "[EVIDENCE_STATE] requires_exploration", "[SUPERPOWERS_SKILLS] none", "[CONTEXT_START]", "ctx line", "[CONTEXT_END]"} {
		if !strings.Contains(out, want+"\n") {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if lines[len(lines)-1] != "[INVARIANTS] preserve existing contracts unless task explicitly changes them" {
		t.Fatalf("last line %q", lines[len(lines)-1])
	}
}

func TestFormatMarkdownAndUnknown(t *testing.T) {
	out, err := Format(samplePacket(), "markdown")
	if err != nil || !strings.Contains(out, "**Execution**: native — Use native") || !strings.HasSuffix(out, "ctx line") {
		t.Fatalf("markdown:\n%s %v", out, err)
	}
	if _, err := Format(samplePacket(), "xml"); err == nil {
		t.Fatal("unknown format must error")
	}
}

func TestEvaluateSelectiveStructuralConflict(t *testing.T) {
	item := func(confidence string, empty bool) model.ContextItem {
		return model.ContextItem{Source: "code_review_graph", Text: confidence, Score: 9, Metadata: map[string]any{
			"structural_valid": true, "evidence_confidence": confidence, "pattern": "callers_of", "symbol": "Foo", "empty_verified": empty}}
	}
	s := Sufficiency{Score: 0.9, Sufficient: true, LexicalCoverage: 1, StructuralComplete: true}
	got := EvaluateSelective([]model.ContextItem{item("verified", false), item("corroborated", true)}, s, nil, 0.15)
	if got.Condition != "conflicting" || got.Accept || got.Score != 0 || len(got.Reasons) != 1 || got.Reasons[0] != "verified_structural_evidence_conflicts" {
		t.Fatalf("got %+v", got)
	}
	if got := EvaluateSelective([]model.ContextItem{item("verified", false), item("candidate", true)}, s, nil, 0.15); got.Condition == "conflicting" {
		t.Fatalf("candidate must not conflict: %+v", got)
	}
}
