package compat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/brief"
	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/handoff"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/providers"
	"github.com/Taki7980/ai-workflow-v3/internal/verify"
)

// WorkflowCases are the core workflow-loop contract inputs.
type WorkflowCases struct {
	Orchestration   []OrchestrationCase   `json:"orchestration"`
	Sufficiency     []SufficiencyCase     `json:"sufficiency"`
	Selective       []SelectiveCase       `json:"selective"`
	EvidenceState   []EvidenceStateCase   `json:"evidence_state"`
	HandoffValidate []HandoffValidateCase `json:"handoff_validate"`
	HandoffRender   []HandoffRenderCase   `json:"handoff_render"`
	Compress        []CompressCase        `json:"compress"`
	BriefFormat     []BriefFormatCase     `json:"brief_format"`
	ModelTier       []ModelTierCase       `json:"model_tier"`
}

// WorkflowFixture holds frozen V2 outputs as generic JSON values.
type WorkflowFixture struct {
	Orchestration   map[string]any `json:"orchestration"`
	Sufficiency     map[string]any `json:"sufficiency"`
	Selective       map[string]any `json:"selective"`
	EvidenceState   map[string]any `json:"evidence_state"`
	HandoffValidate map[string]any `json:"handoff_validate"`
	HandoffRender   map[string]any `json:"handoff_render"`
	Compress        map[string]any `json:"compress"`
	BriefFormat     map[string]any `json:"brief_format"`
	ModelTier       map[string]any `json:"model_tier"`
}

type OrchestrationCase struct {
	Name             string              `json:"name"`
	Decision         model.RouteDecision `json:"decision"`
	Intent           string              `json:"intent"`
	Sufficient       bool                `json:"sufficient"`
	SelectiveEnabled bool                `json:"selective_enabled"`
	SelectiveAccept  bool                `json:"selective_accept"`
	ChangedFiles     []string            `json:"changed_files"`
	WorkspaceCount   int                 `json:"workspace_count"`
	Providers        providers.Status    `json:"providers"`
	Budget           json.RawMessage     `json:"budget,omitempty"`
}

type SufficiencyCase struct {
	Name               string              `json:"name"`
	Query              string              `json:"query"`
	Items              []model.ContextItem `json:"items"`
	StructuralRequired bool                `json:"structural_required"`
	StructuralPatterns []string            `json:"structural_patterns"`
	Threshold          float64             `json:"threshold"`
}

type SelectiveCase struct {
	Name            string              `json:"name"`
	Items           []model.ContextItem `json:"items"`
	Sufficiency     brief.Sufficiency   `json:"sufficiency"`
	MinimumCoverage float64             `json:"minimum_coverage"`
}

type EvidenceStateCase struct {
	Name       string     `json:"name"`
	Lane       model.Lane `json:"lane"`
	Sufficient bool       `json:"sufficient"`
}

type HandoffValidateCase struct {
	Name     string `json:"name"`
	Text     string `json:"text"`
	MaxLines int    `json:"max_lines"`
}

type HandoffRenderCase struct {
	Name     string              `json:"name"`
	Decision model.RouteDecision `json:"decision"`
	Provider string              `json:"provider"`
	Sources  []string            `json:"sources"`
	Goal     string              `json:"goal"`
}

type CompressCase struct {
	Name          string `json:"name"`
	Text          string `json:"text"`
	GenerateLines int    `json:"generate_lines"`
	RepeatChar    string `json:"repeat_char"`
	RepeatCount   int    `json:"repeat_count"`
	MaxLines      int    `json:"max_lines"`
	MaxChars      int    `json:"max_chars"`
}

// Input mirrors capture_v2.compress_input.
func (c CompressCase) Input() string {
	switch {
	case c.GenerateLines > 0:
		lines := make([]string, c.GenerateLines)
		for i := range lines {
			lines[i] = fmt.Sprintf("l%d", i)
		}
		return strings.Join(lines, "\n")
	case c.RepeatChar != "":
		return strings.Repeat(c.RepeatChar, c.RepeatCount)
	}
	return c.Text
}

type BriefFormatCase struct {
	Name   string       `json:"name"`
	Format string       `json:"format"`
	Packet brief.Packet `json:"packet"`
}

type ModelTierCase struct {
	Name     string              `json:"name"`
	Decision model.RouteDecision `json:"decision"`
}

// normalize round-trips v through JSON so it compares equal to decoded fixtures.
func normalize(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	return out, json.Unmarshal(b, &out)
}

// checker compares a normalized V3 value against its frozen V2 fixture.
func checker(add func(contract, name string, expected, actual any)) func(contract, name string, frozen map[string]any, actual any) error {
	return func(contract, name string, frozen map[string]any, actual any) error {
		expected, ok := frozen[name]
		if !ok {
			return fmt.Errorf("missing %s fixture %q", contract, name)
		}
		norm, err := normalize(actual)
		if err != nil {
			return err
		}
		add(contract, name, expected, norm)
		return nil
	}
}

// runWorkflow replays the workflow-loop cases through V3 and reports each via add.
func runWorkflow(cases WorkflowCases, fixture WorkflowFixture, cfg config.Config, add func(contract, name string, expected, actual any)) error {
	check := checker(add)
	for _, c := range cases.Orchestration {
		budget := cfg.Execution.OrchestrationBudget
		if len(c.Budget) > 0 {
			if err := json.Unmarshal(c.Budget, &budget); err != nil {
				return fmt.Errorf("orchestration case %q budget: %w", c.Name, err)
			}
		}
		got := brief.BuildOrchestration(c.Decision, c.Intent, c.Sufficient, c.SelectiveEnabled, c.SelectiveAccept, c.ChangedFiles, c.WorkspaceCount, c.Providers, budget)
		if err := check("orchestration", c.Name, fixture.Orchestration, got); err != nil {
			return err
		}
	}
	for _, c := range cases.Sufficiency {
		got := brief.EvaluateSufficiency(c.Query, c.Items, c.StructuralRequired, c.StructuralPatterns, c.Threshold)
		if err := check("sufficiency", c.Name, fixture.Sufficiency, got); err != nil {
			return err
		}
	}
	for _, c := range cases.Selective {
		got := brief.EvaluateSelective(c.Items, c.Sufficiency, nil, c.MinimumCoverage)
		if err := check("selective", c.Name, fixture.Selective, got); err != nil {
			return err
		}
	}
	for _, c := range cases.EvidenceState {
		if err := check("evidence_state", c.Name, fixture.EvidenceState, brief.EvidenceState(c.Lane, c.Sufficient)); err != nil {
			return err
		}
	}
	for _, c := range cases.HandoffValidate {
		dir, err := os.MkdirTemp("", "compat-handoff-*")
		if err != nil {
			return err
		}
		p := handoff.Path(dir)
		err = os.MkdirAll(filepath.Dir(p), 0o755)
		if err == nil {
			err = os.WriteFile(p, []byte(c.Text), 0o644)
		}
		got := handoff.Validate(dir, c.MaxLines)
		os.RemoveAll(dir)
		if err != nil {
			return err
		}
		if err := check("handoff_validate", c.Name, fixture.HandoffValidate, got); err != nil {
			return err
		}
	}
	for _, c := range cases.HandoffRender {
		if err := check("handoff_render", c.Name, fixture.HandoffRender, handoff.Render(c.Decision, c.Provider, c.Sources, c.Goal)); err != nil {
			return err
		}
	}
	for _, c := range cases.Compress {
		if err := check("compress", c.Name, fixture.Compress, verify.Compress(c.Input(), c.MaxLines, c.MaxChars)); err != nil {
			return err
		}
	}
	for _, c := range cases.BriefFormat {
		got, err := brief.Format(c.Packet, c.Format)
		if err != nil {
			return fmt.Errorf("brief_format case %q: %w", c.Name, err)
		}
		if err := check("brief_format", c.Name, fixture.BriefFormat, got); err != nil {
			return err
		}
	}
	for _, c := range cases.ModelTier {
		if err := check("model_tier", c.Name, fixture.ModelTier, providers.ModelTier(c.Decision, cfg)); err != nil {
			return err
		}
	}
	return nil
}
