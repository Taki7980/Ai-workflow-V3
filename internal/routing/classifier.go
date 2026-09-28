package routing

import (
	"regexp"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
)

var (
	changeWords   = regexp.MustCompile(`(?i)\b(add|alter|build|create|change|edit|fix|implement|modify|patch|remove|rename|replace|rewrite|update|refactor|migrate|deploy)\b`)
	questionWords = regexp.MustCompile(`(?i)\b(what|why|how|where|which|who|whom|whose|when|explain|describe|compare|difference|understand|show|find|list|locate|tell)\b`)
	fileHint      = regexp.MustCompile(`(?i)[\w./\\-]+\.(py|go|rs|js|ts|tsx|java|cs|cpp|h|rb|php|md|json|ya?ml)`)
)

func containsAny(text string, items []string) []string {
	lower := strings.ToLower(text)
	out := make([]string, 0, 3)
	for _, item := range items {
		cleaned := strings.TrimSpace(strings.ToLower(item))
		if cleaned == "" {
			continue
		}
		rx := regexp.MustCompile(`\b` + regexp.QuoteMeta(cleaned) + `\b`)
		if rx.FindStringIndex(lower) != nil {
			out = append(out, item)
		}
	}
	return out
}

func Classify(task string, cfg config.Config) model.RouteDecision {
	text := strings.Join(strings.Fields(task), " ")
	high := containsAny(text, cfg.Classifier.HighRiskKeywords)
	full := containsAny(text, cfg.Classifier.FullKeywords)
	answer := containsAny(text, cfg.Classifier.AnswerKeywords)
	small := containsAny(text, cfg.Classifier.SmallKeywords)
	structuralMatches := containsAny(text, cfg.Context.CRG.StructuralKeywords)
	structural := len(structuralMatches) > 0
	fileHints := fileHint.FindAllString(text, -1)
	hasChange := changeWords.FindStringIndex(text) != nil
	looksQuestion := questionWords.FindStringIndex(text) != nil || strings.HasSuffix(text, "?") || len(answer) > 0

	if looksQuestion && !hasChange && !structural {
		confidence := .88
		if len(answer) > 0 {
			confidence = .96
		}
		return model.RouteDecision{Lane: model.LaneAnswer, Risk: model.RiskLow, Reasons: []string{"read-only question/explanation"}, Confidence: confidence}
	}
	if len(high) > 0 {
		return model.RouteDecision{Lane: model.LaneFull, Risk: model.RiskHigh, Reasons: []string{"high-risk change keyword: " + strings.Join(high[:min(3, len(high))], ", ")}, StructuralContext: structural, Confidence: .99}
	}
	if len(full) > 0 || structural {
		reasons := []string{}
		if len(full) > 0 {
			reasons = append(reasons, "full-lane signal: "+strings.Join(full[:min(3, len(full))], ", "))
		}
		if structural {
			reasons = append(reasons, "multi-hop/structural context requested")
		}
		confidence := .90
		if structural {
			confidence = .94
		}
		return model.RouteDecision{Lane: model.LaneFull, Risk: model.RiskMedium, Reasons: reasons, StructuralContext: structural, Confidence: confidence}
	}
	if hasChange {
		if len(fileHints) >= 1 && len(fileHints) <= 2 && (len(small) > 0 || len(strings.Fields(text)) <= 22) {
			return model.RouteDecision{Lane: model.LaneSmall, Risk: model.RiskLow, Reasons: []string{"bounded low-complexity edit", "explicit file scope: " + itoa(len(fileHints)) + " file(s)"}, Confidence: map[bool]float64{true: .90, false: .82}[len(small) > 0]}
		}
		return model.RouteDecision{Lane: model.LaneFull, Risk: model.RiskMedium, Reasons: []string{"implementation scope not safely bounded"}, Confidence: .78}
	}
	return model.RouteDecision{Lane: model.LaneFull, Risk: model.RiskMedium, Reasons: []string{"ambiguous task defaults to safe full-lane planning"}, Confidence: .62}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 20)
	for n > 0 {
		b = append(b, byte('0'+n%10))
		n /= 10
	}
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}
