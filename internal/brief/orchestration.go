package brief

import (
	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/providers"
)

type ComplexityVector struct {
	LaneWeight         int  `json:"lane_weight"`
	RiskWeight         int  `json:"risk_weight"`
	ChangedFileCount   int  `json:"changed_file_count"`
	WorkspaceRootCount int  `json:"workspace_root_count"`
	StructuralIntent   bool `json:"structural_intent"`
	EvidenceGap        bool `json:"evidence_gap"`
}

// Orchestration is the V2 OrchestrationContract.
type Orchestration struct {
	ComplexityVector   ComplexityVector           `json:"complexity_vector"`
	ComplexityScore    int                        `json:"complexity_score"`
	SuperpowersSkills  []string                   `json:"superpowers_skills"`
	CRGPlan            []string                   `json:"crg_plan"`
	AgentSlots         int                        `json:"agent_slots"`
	ReviewPasses       int                        `json:"review_passes"`
	VerificationPasses int                        `json:"verification_passes"`
	GraphDepth         int                        `json:"graph_depth"`
	Budget             config.OrchestrationBudget `json:"budget"`
	NativeFallback     bool                       `json:"native_fallback"`
}

var laneWeight = map[model.Lane]int{model.LaneAnswer: 0, model.LaneSmall: 1, model.LaneFull: 2}
var riskWeight = map[model.Risk]int{model.RiskLow: 0, model.RiskMedium: 1, model.RiskHigh: 2}

func i2(b bool) int {
	if b {
		return 1
	}
	return 0
}

// BuildOrchestration ports V2 build_orchestration_contract.
func BuildOrchestration(d model.RouteDecision, intent string, sufficient, selectiveEnabled, selectiveAccept bool, changed []string, rootCount int, p providers.Status, raw config.OrchestrationBudget) Orchestration {
	ok := sufficient && (selectiveAccept || !selectiveEnabled)
	cv := ComplexityVector{
		LaneWeight:         laneWeight[d.Lane],
		RiskWeight:         riskWeight[d.Risk],
		ChangedFileCount:   len(changed),
		WorkspaceRootCount: max(1, rootCount),
		StructuralIntent:   intent == "structural" || d.StructuralContext,
		EvidenceGap:        !ok,
	}
	score := cv.LaneWeight*2 + cv.RiskWeight*2 + min(4, cv.ChangedFileCount) + min(2, cv.WorkspaceRootCount-1) + 2*i2(cv.StructuralIntent) + 2*i2(cv.EvidenceGap)
	b := config.OrchestrationBudget{
		MaxAgentSlots:      max(1, raw.MaxAgentSlots),
		MaxCRGCalls:        max(0, raw.MaxCRGCalls),
		MaxGraphDepth:      max(1, raw.MaxGraphDepth),
		ReviewPasses:       max(1, raw.ReviewPasses),
		VerificationPasses: max(1, raw.VerificationPasses),
	}
	slots, review, verification := 1, 1, 1
	skills := []string{}
	switch d.Lane {
	case model.LaneSmall:
		if p.Superpowers {
			skills = []string{"test-driven-development", "verification-before-completion"}
		}
	case model.LaneFull:
		slots = min(b.MaxAgentSlots, 2+i2(score >= 8)+i2(score >= 12))
		if p.Superpowers {
			skills = []string{"writing-plans", "subagent-driven-development", "requesting-code-review", "verification-before-completion"}
		}
		review = min(b.ReviewPasses, 1+i2(d.Risk == model.RiskHigh || score >= 10))
		verification = min(b.VerificationPasses, 1+i2(d.Risk == model.RiskHigh || cv.EvidenceGap))
	}
	plan := []string{}
	if p.CodeReviewGraph && d.Lane != model.LaneAnswer {
		plan = append(plan, "get_minimal_context_tool")
		if cv.StructuralIntent || len(changed) >= 2 {
			plan = append(plan, "get_impact_radius_tool")
		}
		if cv.StructuralIntent {
			plan = append(plan, "query_graph_tool")
		}
		plan = append(plan, "get_review_context_tool")
		plan = plan[:min(len(plan), b.MaxCRGCalls)]
	}
	return Orchestration{
		ComplexityVector:   cv,
		ComplexityScore:    score,
		SuperpowersSkills:  skills,
		CRGPlan:            plan,
		AgentSlots:         min(slots, b.MaxAgentSlots),
		ReviewPasses:       min(review, b.ReviewPasses),
		VerificationPasses: min(verification, b.VerificationPasses),
		GraphDepth:         min(1+i2(score >= 8)+i2(score >= 12), b.MaxGraphDepth),
		Budget:             b,
		NativeFallback:     d.Lane != model.LaneAnswer && !p.Superpowers,
	}
}
