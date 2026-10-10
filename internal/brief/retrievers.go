package brief

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/indexer"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/provider"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

const maxProviderConcurrency = 4

var (
	camelRE    = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	alnumRE    = regexp.MustCompile(`[A-Za-z0-9]+`)
	nonAlnumRE = regexp.MustCompile(`[^a-z0-9]+`)
)

func semanticTerms(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range alnumRE.FindAllString(strings.ToLower(camelRE.ReplaceAllString(s, "$1 $2")), -1) {
		if len(t) >= 2 {
			out[t] = true
		}
	}
	return out
}

func trigrams(s string) map[string]bool {
	c := strings.TrimSpace(nonAlnumRE.ReplaceAllString(strings.ToLower(s), " "))
	out := map[string]bool{}
	if len(c) < 3 {
		if c != "" {
			out[c] = true
		}
		return out
	}
	for i := 0; i+3 <= len(c); i++ {
		out[c[i:i+3]] = true
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	inter, union := 0, len(b)
	for k := range a {
		if b[k] {
			inter++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// hybridSimilarity ports V2 _hybrid_similarity: token Jaccard, trigram
// Jaccard and an exact-phrase bonus.
func hybridSimilarity(query, candidate string) float64 {
	bonus := 0.0
	if strings.Contains(strings.ToLower(candidate), strings.ToLower(strings.TrimSpace(query))) {
		bonus = .25
	}
	return min(1, .7*jaccard(semanticTerms(query), semanticTerms(candidate))+.3*jaccard(trigrams(query), trigrams(candidate))+bonus)
}

// gatherBuiltinSemantic ranks indexed symbols by hybrid similarity and emits
// at most two fresh 3-line snippets (V2 builtin-local provider).
func gatherBuiltinSemantic(g *gathered, root, query string, repos []workspace.Repository, indexes map[string]indexer.Index, limit int) {
	type cand struct {
		score float64
		repo  workspace.Repository
		sym   indexer.Symbol
	}
	ranked := []cand{}
	for _, r := range repos {
		idx, ok := indexes[r.RelativePath]
		if !ok {
			continue
		}
		for _, s := range idx.Symbols {
			if score := hybridSimilarity(query, s.Name+" "+s.Kind+" "+s.Path); score >= .08 {
				ranked = append(ranked, cand{score, r, s})
			}
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if pa, pb := prefix(a.repo.RelativePath, a.sym.Path), prefix(b.repo.RelativePath, b.sym.Path); pa != pb {
			return pa < pb
		}
		return a.sym.Line < b.sym.Line
	})
	seen, added := map[string]bool{}, 0
	for _, c := range ranked {
		if added >= min(2, max(1, limit)) {
			break
		}
		rel := prefix(c.repo.RelativePath, c.sym.Path)
		key := fmt.Sprintf("%s:%d", rel, c.sym.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		lines := freshLines(root, c.repo.RelativePath, c.sym.Path, indexes[c.repo.RelativePath].Files[c.sym.Path].SHA256)
		if lines == nil {
			continue
		}
		lo, hi := max(1, c.sym.Line-1), min(len(lines), c.sym.Line+1)
		snippet := []string{}
		for n := lo; n <= hi; n++ {
			snippet = append(snippet, fmt.Sprintf("%d: %s", n, lines[n-1]))
		}
		if len(snippet) == 0 {
			continue
		}
		g.items = append(g.items, model.ContextItem{
			Source: "semantic", Text: key + "\n" + strings.Join(snippet, "\n"), Score: c.score,
			Metadata: map[string]any{"repository": c.repo.RelativePath, "repository_id": c.repo.RepositoryID, "path": c.sym.Path,
				"line": c.sym.Line, "symbol": c.sym.Name, "kind": c.sym.Kind, "retriever": "semantic", "provider": config.BuiltinSemanticProvider},
		})
		added++
	}
}

// runProvider executes a resolved provider once per included repository.
func runProvider(ctx context.Context, g *gathered, root string, spec provider.Spec, query, intent, source string, defaults map[string]any,
	repos []workspace.Repository, changed []string, limit int, errs map[string]string, errKey string) {
	// Bounded concurrency across repositories; results merge in repository order.
	results := make([]provider.Result, len(repos))
	sem := make(chan struct{}, maxProviderConcurrency)
	var wg sync.WaitGroup
	for i, r := range repos {
		meta := map[string]any{"repository": r.RelativePath, "repository_id": r.RepositoryID}
		for k, v := range defaults {
			meta[k] = v
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = provider.Run(ctx, spec, provider.Request{Query: query, Root: filepath.Join(root, filepath.FromSlash(r.RelativePath)),
				Limit: max(1, limit), Intent: intent, ChangedFiles: changed, Defaults: meta}, source)
		}()
	}
	wg.Wait()
	for i, r := range repos {
		res := results[i]
		if !res.OK() {
			errs[errKey] = res.ErrorKind + ": " + res.Error
			g.note("%s failed for %s: %s", errKey, r.RelativePath, res.ErrorKind)
			continue
		}
		g.items = append(g.items, res.Items...)
	}
}

// gatherSemantic runs the configured semantic provider (V2 semantic_result).
func gatherSemantic(ctx context.Context, g *gathered, root, query string, repos []workspace.Repository, indexes map[string]indexer.Index,
	cfg config.Config, changed []string, skipped, errs map[string]string) {
	sc := cfg.Context.Semantic
	limit := sc.MaxResults
	if limit <= 0 {
		limit = cfg.Context.MaxResultsPerSource
	}
	id := sc.SemanticProviderID()
	switch {
	case strings.EqualFold(sc.Mode, "off"):
		skipped["semantic"] = "semantic provider disabled"
		return
	case id == config.BuiltinSemanticProvider:
		g.attempted = append(g.attempted, "semantic")
		gatherBuiltinSemantic(g, root, query, repos, indexes, limit)
		return
	case id == "" && !provider.UnsafeRepoCommandsEnabled():
		skipped["semantic"] = "repository-defined semantic command requires " + provider.UnsafeRepoCommandEnv + "=1"
		return
	}
	raw := map[string]any{"name": "semantic"}
	if id != "" {
		raw["provider_id"] = id
	} else if env := strings.TrimSpace(os.Getenv("AI_WORKFLOW_SEMANTIC_CMD")); env != "" {
		raw["command"] = env
	} else {
		raw["command"] = sc.Command
	}
	if sc.TimeoutSeconds > 0 {
		raw["timeout_seconds"] = sc.TimeoutSeconds
	}
	if sc.MaxOutputBytes > 0 {
		raw["max_output_bytes"] = sc.MaxOutputBytes
	}
	b, _ := json.Marshal(raw)
	spec, err := provider.ResolveProject(root, b, "semantic")
	if err != nil {
		errs["semantic"] = "configuration: " + err.Error()
		g.note("semantic provider unavailable: %s", err)
		return
	}
	g.attempted = append(g.attempted, "semantic")
	runProvider(ctx, g, root, spec, query, "semantic", "semantic", map[string]any{"retriever": "semantic"}, repos, changed, limit, errs, "semantic")
}

var validIntents = map[string]bool{"exact": true, "semantic": true, "structural": true, "mixed": true, "all": true}

// gatherExternal runs every enabled external retriever registered for intent
// (V2 retriever_plugins). Each failure degrades to the remaining evidence.
func gatherExternal(ctx context.Context, g *gathered, root, query, intent string, repos []workspace.Repository, cfg config.Config,
	changed []string, errs map[string]string) {
	for i, raw := range cfg.Context.ExternalRetrievers {
		var head struct {
			Name       string          `json:"name"`
			Enabled    *bool           `json:"enabled"`
			ProviderID string          `json:"provider_id"`
			Command    json.RawMessage `json:"command"`
			Intents    []string        `json:"intents"`
			MaxResults int             `json:"max_results"`
		}
		if json.Unmarshal(raw, &head) != nil || strings.TrimSpace(head.Name) == "" || (head.Enabled != nil && !*head.Enabled) {
			continue
		}
		intents := head.Intents
		if len(intents) == 0 {
			intents = []string{"all"}
		}
		match, valid := false, true
		for _, it := range intents {
			it = strings.ToLower(strings.TrimSpace(it))
			valid = valid && validIntents[it]
			match = match || it == "all" || it == intent
		}
		if !valid || !match {
			continue
		}
		name := strings.TrimSpace(head.Name)
		key := "external:" + name
		spec, err := provider.ResolveProject(root, raw, name)
		if err != nil {
			errs[key] = fmt.Sprintf("configuration (external_retrievers[%d]): %s", i, err)
			continue
		}
		g.attempted = append(g.attempted, key)
		limit := head.MaxResults
		if limit <= 0 {
			limit = cfg.Context.MaxResultsPerSource
		}
		runProvider(ctx, g, root, spec, query, intent, key, map[string]any{"retriever": name, "plugin": true}, repos, changed, limit, errs, key)
	}
}
