// Package handoff renders and validates the compact active-task handoff file.
package handoff

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
)

const (
	RelativePath       = "ai-workspace/handoff/HANDOFF.md"
	LegacyRelativePath = ".ai/HANDOFF.md"
)

var required = []string{"Lane / risk", "Goal / state", "Exact paths+symbols", "Context sources", "Ordered edits", "Invariants", "Changed files", "Checks", "Blockers", "Exact next step"}

// placeholderRE is copied verbatim from V2 handoff.PLACEHOLDER_RE.
var placeholderRE = regexp.MustCompile(`(?i)\[(?:answer\||small\||full\||low\||medium\||high\||goal and|bounded edit|cache/index|max \d+|contracts that|files changed|exact verification|one action|TODO|YOUR_)[^\]]*\]|\{\{[^}]+\}\}|<(?:insert|replace|TODO)[^>]*>`)

// Path returns the clean-layout handoff path inside ai-workspace.
func Path(root string) string { return filepath.Join(root, filepath.FromSlash(RelativePath)) }

func existingPath(root string) string {
	clean := Path(root)
	if _, err := os.Stat(clean); err == nil {
		return clean
	}
	legacy := filepath.Join(root, filepath.FromSlash(LegacyRelativePath))
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return clean
}

// Validate returns V2-compatible validation errors; an empty slice means valid.
func Validate(root string, maxLines int) []string {
	b, err := os.ReadFile(existingPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return []string{"missing " + RelativePath}
	}
	if err != nil {
		return []string{err.Error()}
	}
	text := normalizeNewlines(string(b))
	errs := []string{}
	if n := countLines(text); n > maxLines {
		errs = append(errs, fmt.Sprintf("handoff has %d lines; cap is %d", n, maxLines))
	}
	lower := strings.ToLower(text)
	for _, field := range required {
		if !strings.Contains(lower, strings.ToLower(field)) {
			errs = append(errs, "missing field: "+field)
		}
	}
	if placeholderRE.MatchString(text) {
		errs = append(errs, "handoff still contains template placeholders")
	}
	return errs
}

// normalizeNewlines mirrors Python universal-newline reads.
func normalizeNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// countLines mirrors Python str.splitlines() length for \n-normalized text.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSuffix(s, "\n"), "\n"))
}

// Render returns the V2 handoff template for a routed task.
func Render(d model.RouteDecision, provider string, sources []string, goal string) string {
	seen := map[string]bool{}
	uniq := []string{}
	for _, s := range sources {
		if !seen[s] {
			seen[s] = true
			uniq = append(uniq, s)
		}
	}
	joined := strings.Join(uniq, ", ")
	if joined == "" {
		joined = "none"
	}
	return strings.Join([]string{
		"# Handoff",
		fmt.Sprintf("- **Lane / risk**: %s / %s", d.Lane, d.Risk),
		fmt.Sprintf("- **Goal / state**: %s / routed", strings.TrimSpace(goal)),
		"- **Exact paths+symbols**: none yet",
		"- **Context sources**: " + joined,
		"- **Ordered edits**: execution provider = " + provider,
		"- **Invariants**: preserve existing contracts unless task explicitly changes them",
		"- **Changed files**: none",
		"- **Checks**: define focused checks before build",
		"- **Blockers**: none",
		"- **Exact next step**: inspect bounded context and finalize exact edit sites",
		"",
	}, "\n")
}
