// Package providers detects optional execution and retrieval providers and
// maps a routing decision to an execution provider and model tier.
package providers

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

// Status reports which optional providers are available.
type Status struct {
	Superpowers     bool `json:"superpowers"`
	CodeReviewGraph bool `json:"code_review_graph"`
	RTK             bool `json:"rtk"`
	Ripgrep         bool `json:"ripgrep"`
	Semantic        bool `json:"semantic"`
	SCIP            bool `json:"scip"`
}

var (
	lookPath = exec.LookPath
	homeDir  = os.UserHomeDir
)

const pluginWalkDepth = 6

// Detect resolves provider availability, honoring config modes ("on", "off",
// "auto") and the AI_WORKFLOW_SUPERPOWERS environment override.
func Detect(root string, cfg config.Config, reg workspace.Registry) Status {
	return Status{
		Superpowers:     modeOr(cfg.Execution.Superpowers.Mode, func() bool { return hasSuperpowers(root) }),
		CodeReviewGraph: modeOr(cfg.Context.CRG.Mode, func() bool { return hasCRG(root, reg) }),
		RTK:             found("rtk"),
		Ripgrep:         found("rg"),
	}
}

// modeOr returns true for "on", false for "off", and detect() otherwise.
func modeOr(mode string, detect func() bool) bool {
	switch mode {
	case "on":
		return true
	case "off":
		return false
	}
	return detect()
}

func found(name string) bool {
	_, err := lookPath(name)
	return err == nil
}

func hasSuperpowers(root string) bool {
	if v, ok := os.LookupEnv("AI_WORKFLOW_SUPERPOWERS"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		}
		return false
	}
	candidates := []string{filepath.Join(root, ".agents", "skills", "superpowers")}
	home, err := homeDir()
	if err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".agents", "skills", "superpowers"),
			filepath.Join(home, ".codex", "superpowers"),
			filepath.Join(home, ".codex", "skills", "superpowers"),
			filepath.Join(home, ".gemini", "skills", "superpowers"),
			filepath.Join(home, ".config", "opencode", "skills", "superpowers"),
		)
	}
	for _, c := range candidates {
		if _, statErr := os.Stat(c); statErr == nil {
			return true
		}
	}
	if err != nil {
		return false
	}
	for _, base := range []string{
		filepath.Join(home, ".claude", "plugins"),
		filepath.Join(home, ".codex", "plugins"),
		filepath.Join(home, ".gemini", "extensions"),
	} {
		if containsSuperpowers(base) {
			return true
		}
	}
	return false
}

// containsSuperpowers walks base up to pluginWalkDepth levels looking for an
// entry whose name contains "superpowers".
func containsSuperpowers(base string) bool {
	hit := false
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(base, path)
		if rel == "." {
			return nil
		}
		if strings.Contains(strings.ToLower(d.Name()), "superpowers") {
			hit = true
			return fs.SkipAll
		}
		if d.IsDir() && strings.Count(filepath.ToSlash(rel), "/")+1 >= pluginWalkDepth {
			return fs.SkipDir
		}
		return nil
	})
	return hit
}

// hasCRG reports whether the code-review-graph CLI is installed and at least
// one included repository has a managed graph database.
// ponytail: presence check only; graph schema/freshness validation lands with the structural adapter.
func hasCRG(root string, reg workspace.Registry) bool {
	if !found("code-review-graph") {
		return false
	}
	for _, repo := range reg.Repositories {
		if !repo.Included {
			continue
		}
		db := filepath.Join(root, "ai-workspace", "code-review-graph", filepath.FromSlash(repo.RelativePath), "graph.db")
		if info, err := os.Stat(db); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// ExecutionProvider returns "superpowers" for full-lane work when preferred
// and detected, otherwise "native".
func ExecutionProvider(lane model.Lane, cfg config.Config, s Status) string {
	if lane == model.LaneFull && cfg.Execution.PreferSuperpowersForFull && s.Superpowers {
		return "superpowers"
	}
	return "native"
}

// ModelTier maps a routing decision to the configured model tier.
func ModelTier(d model.RouteDecision, cfg config.Config) string {
	switch {
	case d.Lane == model.LaneAnswer:
		return orDefault(cfg.Models.Answer, "fast")
	case d.Lane == model.LaneSmall:
		return orDefault(cfg.Models.Small, "fast")
	case d.Risk == model.RiskHigh:
		return orDefault(cfg.Models.FullHigh, "capable")
	}
	return orDefault(cfg.Models.FullMedium, "standard")
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
