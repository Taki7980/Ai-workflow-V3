package brief

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

type Budget struct {
	EstimatedContextTokens int `json:"estimated_context_tokens"`
	MaxOutputTokens        int `json:"max_output_tokens"`
}

type EvidenceContract struct {
	Schema        string `json:"schema"`
	SelectedCount int    `json:"selected_count"`
	Authority     string `json:"authority"`
}

type SufficiencyReport struct {
	Sufficiency
	StructuralPatterns []string `json:"structural_patterns"`
}

type SelectiveReport struct {
	Selective
	Enabled bool `json:"enabled"`
}

// RetrievalDiagnostics is the agent-facing subset of V2's retrieval packet.
type RetrievalDiagnostics struct {
	RetrievalIntent       string            `json:"retrieval_intent"`
	RetrievalReason       string            `json:"retrieval_reason"`
	WorkspaceRoots        []string          `json:"workspace_roots"`
	WorkspaceState        workspace.State   `json:"workspace_state"`
	EvidenceState         string            `json:"evidence_state"`
	EvidenceContract      EvidenceContract  `json:"evidence_contract"`
	Sufficiency           SufficiencyReport `json:"sufficiency"`
	SelectiveRetrieval    SelectiveReport   `json:"selective_retrieval"`
	AdaptiveContextTokens int               `json:"adaptive_context_tokens"`
	HardContextTokens     int               `json:"hard_context_tokens"`
	ProvidersAttempted    []string          `json:"providers_attempted"`
	ProvidersSkipped      map[string]string `json:"providers_skipped"`
	ProviderErrors        map[string]string `json:"provider_errors"`
	Fallbacks             []string          `json:"fallbacks"`
	Orchestration         Orchestration     `json:"orchestration"`
}

// Packet is the brief emitted to agents (spec §4.8).
type Packet struct {
	Task string `json:"task"`
	model.RouteDecision
	ExecutionProvider          string               `json:"execution_provider"`
	ModelTier                  string               `json:"model_tier"`
	ExecutionHint              string               `json:"execution_hint"`
	Budget                     Budget               `json:"budget"`
	Retrieval                  RetrievalDiagnostics `json:"retrieval"`
	Context                    []model.ContextItem  `json:"context"`
	EstimatedContextTokensUsed int                  `json:"estimated_context_tokens_used"`
	OutputCompression          string               `json:"output_compression"`
	ChangedFilesDetected       []string             `json:"changed_files_detected"`
	HandoffWritten             string               `json:"handoff_written,omitempty"`
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// Format renders p as json, markdown or prompt, line-for-line with V2 _format_brief.
func Format(p Packet, format string) (string, error) {
	r := p.Retrieval
	o := r.Orchestration
	texts := []string{}
	for _, it := range p.Context {
		if it.Text != "" {
			texts = append(texts, it.Text)
		}
	}
	ctx := strings.Join(texts, "\n")
	fp := orUnknown(r.WorkspaceState.Fingerprint)
	switch format {
	case "json":
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(p); err != nil {
			return "", err
		}
		return strings.TrimSuffix(buf.String(), "\n"), nil
	case "markdown":
		body := ctx
		if body == "" {
			body = "(no context gathered)"
		}
		changed := "none detected"
		if len(p.ChangedFilesDetected) > 0 {
			changed = strings.Join(p.ChangedFilesDetected, ", ")
		}
		return strings.Join([]string{
			"# Brief: " + p.Task,
			fmt.Sprintf("**Lane**: %s | **Risk**: %s | **Confidence**: %.2f | **Model tier**: %s", p.Lane, p.Risk, p.Confidence, p.ModelTier),
			fmt.Sprintf("**Retrieval**: %s | **Evidence**: %s", orUnknown(r.RetrievalIntent), orUnknown(r.EvidenceState)),
			fmt.Sprintf("**Execution**: %s — %s", p.ExecutionProvider, p.ExecutionHint),
			"**Superpowers**: " + orNone(o.SuperpowersSkills),
			"**CRG**: " + orNone(o.CRGPlan),
			"**Workspace state**: " + fp,
			fmt.Sprintf("**Budget**: %d est. tokens | %d output", p.Budget.EstimatedContextTokens, p.Budget.MaxOutputTokens),
			"**Changed files**: " + changed,
			"",
			"## Context",
			"",
			body,
		}, "\n"), nil
	case "prompt":
		lines := []string{
			"[TASK] " + p.Task,
			fmt.Sprintf("[LANE] %s [RISK] %s [CONFIDENCE] %.2f [MODEL_TIER] %s", p.Lane, p.Risk, p.Confidence, p.ModelTier),
			"[RETRIEVAL_INTENT] " + orUnknown(r.RetrievalIntent),
			"[EVIDENCE_STATE] " + orUnknown(r.EvidenceState),
			"[WORKSPACE_FINGERPRINT] " + fp,
			"[EXECUTION] " + p.ExecutionProvider,
			"[SUPERPOWERS_SKILLS] " + orNone(o.SuperpowersSkills),
			"[CRG_PLAN] " + orNone(o.CRGPlan),
			fmt.Sprintf("[AGENT_SLOTS] %d", o.AgentSlots),
			"[HINT] " + p.ExecutionHint,
			fmt.Sprintf("[BUDGET] context=%dtok output=%dtok", p.Budget.EstimatedContextTokens, p.Budget.MaxOutputTokens),
			"[CHANGED_FILES] " + orNone(p.ChangedFilesDetected),
		}
		if len(p.Context) > 0 {
			lines = append(lines, "[CONTEXT_START]", ctx, "[CONTEXT_END]")
		}
		lines = append(lines, "[INVARIANTS] preserve existing contracts unless task explicitly changes them")
		return strings.Join(lines, "\n"), nil
	}
	return "", fmt.Errorf("unknown brief format %q (want json, markdown, or prompt)", format)
}
