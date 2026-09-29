package routing

import (
	"regexp"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/retrieval"
)

var (
	identifier       = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_./-]+)?\b`)
	pathSignal       = regexp.MustCompile(`(?:[\w.-]+[/\\])+[\w.-]+`)
	structuralSignal = regexp.MustCompile(`(?i)\b(caller|callee|call graph|dependency|dependents|impact|blast radius|what breaks|affected|tests for|execution flow|architecture)\b`)
	semanticSignal   = regexp.MustCompile(`(?i)\b(where do we|how do we|how does|responsible for|handles?|prevents?|ensures?|implements?|logic for|flow for|behavior|behaviour|concept|meaning)\b`)
)

func PlanRetrieval(query string, decision model.RouteDecision) model.RetrievalPlan {
	return PlanRetrievalWithAnchors(query, decision, "", "")
}

// PlanRetrievalWithAnchors preserves the V2 retrieval contract for callers that
// already resolved an exact symbol or endpoint before policy classification.
func PlanRetrievalWithAnchors(query string, decision model.RouteDecision, symbol, endpoint string) model.RetrievalPlan {
	text := strings.Join(strings.Fields(query), " ")
	exact := symbol != "" || endpoint != "" || pathSignal.FindStringIndex(text) != nil
	if !exact {
		for _, tok := range identifier.FindAllString(text, -1) {
			if strings.Contains(tok, "_") || hasInteriorUpper(tok) {
				exact = true
				break
			}
		}
	}
	semantic := semanticSignal.FindStringIndex(text) != nil || (len(retrieval.Tokenize(text)) >= 7 && !exact)
	patterns := structuralRequirements(text)
	structural := decision.StructuralContext || len(patterns) > 0 || structuralSignal.FindStringIndex(text) != nil
	if structural {
		if len(patterns) == 0 {
			patterns = []string{"architecture"}
		}
		if !exact {
			return model.RetrievalPlan{Intent: model.RetrievalMixed, UseLexical: true, UseSemantic: true, UseStructural: true, Reason: "structural relationship needs entry-point discovery", StructuralPatterns: patterns}
		}
		return model.RetrievalPlan{Intent: model.RetrievalStructural, UseLexical: true, UseStructural: true, Reason: "structural relationship requested", StructuralPatterns: patterns}
	}
	if exact && semantic {
		return model.RetrievalPlan{Intent: model.RetrievalMixed, UseLexical: true, UseSemantic: true, Reason: "identifier and semantic intent both present"}
	}
	if exact {
		return model.RetrievalPlan{Intent: model.RetrievalExact, UseLexical: true, Reason: "exact identifier/path/endpoint signal"}
	}
	if semantic {
		return model.RetrievalPlan{Intent: model.RetrievalSemantic, UseLexical: true, UseSemantic: true, Reason: "natural-language semantic intent"}
	}
	if decision.Lane == model.LaneAnswer {
		return model.RetrievalPlan{Intent: model.RetrievalMixed, UseLexical: true, UseSemantic: true, Reason: "read-only query with ambiguous retrieval intent"}
	}
	return model.RetrievalPlan{Intent: model.RetrievalExact, UseLexical: true, Reason: "bounded deterministic default"}
}

func hasInteriorUpper(s string) bool {
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

func structuralRequirements(s string) []string {
	checks := []struct{ name, rx string }{
		{"impact", `(?i)\b(impact|blast\s+radius|what\s+breaks|affected)\b`},
		{"tests_for", `(?i)\b(tests?\s+(?:for|cover|covering)|which\s+tests?|test\s+coverage)\b`},
		{"references_to", `(?i)\b(references?\s+to|find\s+references?|usages?\s+of)\b`},
		{"callers_of", `(?i)\b(who\s+calls?|callers?|called\s+by|dependents?)\b`},
		{"callees_of", `(?i)\b(callees?|what\s+does\b.*\bcall|calls?\s+into|dependencies?|imports?\s+of)\b`},
		{"architecture", `(?i)\b(architecture|execution\s+flow|call\s+graph)\b`},
	}
	out := []string{}
	for _, c := range checks {
		if regexp.MustCompile(c.rx).FindStringIndex(s) != nil {
			out = append(out, c.name)
		}
	}
	return out
}
