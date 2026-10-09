package brief

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/structural"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

// structuralDeadline bounds all structural expansion steps of one brief.
var structuralDeadline = 10 * time.Second

// structuralAnchor ports V2 _structural_anchor over index items: the
// best-scoring fresh item gives the repository, symbol and file. Unlike V2,
// an item whose symbol is named in the task wins over higher-scoring
// partial matches ("who calls Build" anchors Build, not crgCalls). A single
// included repository is the fallback anchor.
func structuralAnchor(task string, items []model.ContextItem, included []workspace.Repository) (repo workspace.Repository, symbol, file string, ok bool) {
	named := map[string]bool{}
	for _, w := range identRE.FindAllString(task, -1) {
		named[strings.ToLower(w)] = true
	}
	type cand struct {
		repo workspace.Repository
		h    indexHeader
		it   model.ContextItem
	}
	var cands []cand
	for _, it := range items {
		if it.Source != "lightweight_index" {
			continue
		}
		rel, _ := it.Metadata["repository"].(string)
		for _, r := range included {
			if r.RelativePath == rel {
				var h indexHeader
				first, _, _ := strings.Cut(it.Text, "\n")
				_ = json.Unmarshal([]byte(first), &h)
				cands = append(cands, cand{r, h, it})
			}
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.it.Stale != b.it.Stale {
			return !a.it.Stale
		}
		if na, nb := named[strings.ToLower(a.h.Symbol)], named[strings.ToLower(b.h.Symbol)]; na != nb {
			return na
		}
		return a.it.Score > b.it.Score
	})
	if len(cands) > 0 {
		return cands[0].repo, cands[0].h.Symbol, cands[0].h.File, true
	}
	if len(included) == 1 {
		return included[0], "", "", true
	}
	return workspace.Repository{}, "", "", false
}

// changedUnder returns changed paths (control-root relative) that belong to
// repository rel, made relative to it. Paths inside other included
// repositories nested under rel are excluded.
func changedUnder(changed []string, rel string, included []workspace.Repository) []string {
	out := []string{}
	for _, c := range changed {
		owner := ""
		for _, r := range included {
			p := strings.TrimSuffix(r.RelativePath, "/")
			if p != "." && p != "" && strings.HasPrefix(c, p+"/") && len(p) > len(owner) {
				owner = p
			}
		}
		if owner == "" {
			owner = "."
		}
		if owner != strings.TrimSuffix(rel, "/") && !(owner == "." && (rel == "" || rel == ".")) {
			continue
		}
		if owner == "." {
			out = append(out, c)
		} else {
			out = append(out, strings.TrimPrefix(c, owner+"/"))
		}
	}
	return out
}

type structuralStep struct {
	label        string
	relevant     bool
	status       func(context.Context, string, string) structural.Status
	enabled      bool
	sync         string
	quietMissing string // reason that skips without a note (provider simply not set up)
	run          func(context.Context, string, string, structural.Query) []model.ContextItem
}

// expandStructural runs CRG then SCIP for the anchor repository until the
// requested structural evidence is complete (V2 workflow_engine order). It
// reports whether any step ran.
func expandStructural(ctx context.Context, g *gathered, root, task string, opt Options, plan model.RetrievalPlan,
	included []workspace.Repository, changed []string, cfg config.Config, threshold float64,
	skipped, errs map[string]string) bool {
	refs := slices.Contains(plan.StructuralPatterns, "references_to")
	repo, symbol, file, ok := structuralAnchor(task, g.items, included)
	if opt.Symbol != "" {
		symbol = opt.Symbol
	}
	files := changedUnder(changed, repo.RelativePath, included)
	if len(files) == 0 && file != "" {
		files = []string{file}
	}
	canExpand := ok && (symbol != "" || len(files) > 0 || slices.Contains(plan.StructuralPatterns, "architecture") || refs)
	if !canExpand {
		skipped["structural-expansion"] = "no unambiguous repository anchor"
		if refs {
			skipped["scip-structural-expansion"] = "no unambiguous repository anchor"
		}
		return false
	}
	_, crgErr := exec.LookPath("code-review-graph")
	steps := []structuralStep{
		{label: "structural-expansion", relevant: true, status: structural.GraphStatus, run: structural.CRGContext,
			enabled: crgErr == nil && cfg.Context.CRG.Mode != "off", sync: "code-review-graph stale for %s: %s; run ai-workflow graph sync"},
		{label: "scip-structural-expansion", relevant: refs, status: structural.ScipStatus, run: structural.ScipContext,
			enabled: cfg.Context.SCIP.Mode != "off", sync: "scip index stale for %s: %s; run ai-workflow scip sync", quietMissing: "index.scip is missing"},
	}
	dctx, cancel := context.WithTimeout(ctx, structuralDeadline)
	defer cancel()
	q := structural.Query{Text: task, Symbol: symbol, Changed: files, Limit: max(1, cfg.Context.MaxResultsPerSource),
		Patterns: plan.StructuralPatterns, MaxCalls: cfg.Execution.OrchestrationBudget.MaxCRGCalls, File: file}
	ran := false
	for _, s := range steps {
		if EvaluateSufficiency(task, g.items, true, plan.StructuralPatterns, threshold).StructuralComplete {
			break
		}
		if !s.relevant {
			continue
		}
		if !s.enabled {
			skipped[s.label] = "provider not configured"
			continue
		}
		if st := s.status(dctx, root, repo.RelativePath); !st.Ready {
			skipped[s.label] = st.Reason
			if st.Reason != s.quietMissing {
				g.note(s.sync, repo.RelativePath, st.Reason)
			}
			continue
		}
		ran = true
		g.attempted = append(g.attempted, s.label)
		items := s.run(dctx, root, repo.RelativePath, q)
		switch {
		case dctx.Err() != nil:
			errs[s.label] = fmt.Sprintf("global retrieval deadline exceeded after %g seconds", structuralDeadline.Seconds())
			g.note("%s failed: deadline", s.label)
		case len(items) == 0:
			errs[s.label] = "no structural results"
			g.note("%s returned no evidence", s.label)
		}
		for _, it := range items {
			meta := map[string]any{"repository": repo.RelativePath, "repository_id": repo.RepositoryID}
			for k, v := range it.Metadata {
				meta[k] = v
			}
			it.Metadata = meta
			g.items = append(g.items, it)
		}
	}
	return ran
}

// requiredStructural marks valid structural evidence for a requested pattern
// as mandatory in selection.
func requiredStructural(it model.ContextItem, patterns []string) bool {
	if it.Source != "code_review_graph" && it.Source != "scip" {
		return false
	}
	valid, _ := it.Metadata["structural_valid"].(bool)
	pattern, _ := it.Metadata["pattern"].(string)
	return valid && (len(patterns) == 0 || slices.Contains(patterns, pattern))
}
