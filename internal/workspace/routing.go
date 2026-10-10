package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Relationship priors for reviewed cross-repository edges (V2 defaults).
var relationPriors = map[string]float64{"depends_on": .85, "publishes_api": .80, "consumes_schema": .90, "deploys": .70}

// Edge is one reviewed relationship between accepted repositories.
type Edge struct {
	Source       string `json:"source_repository_id"`
	Target       string `json:"target_repository_id"`
	Relationship string `json:"relationship"`
}

// Graph is the reviewed repository graph restricted to accepted repositories.
type Graph struct {
	Nodes       []string
	Edges       []Edge
	Fingerprint string
}

// LoadGraph reads ai-workspace/config/repository-graph.json (or rel). Any
// malformed, unknown, self or duplicate edge invalidates all edges: repository
// content never creates graph authority.
func LoadGraph(root, rel string, repos []Repository) Graph {
	nodes := []string{}
	accepted := map[string]bool{}
	for _, r := range repos {
		if r.Included {
			nodes = append(nodes, r.RepositoryID)
			accepted[r.RepositoryID] = true
		}
	}
	sort.Strings(nodes)
	edges := loadEdges(root, rel, accepted)
	ids := []string{}
	for _, e := range edges {
		ids = append(ids, digest(e))
	}
	return Graph{Nodes: nodes, Edges: edges, Fingerprint: digest(map[string]any{"nodes": nodes, "edges": ids})}
}

func loadEdges(root, rel string, accepted map[string]bool) []Edge {
	if rel == "" {
		rel = "ai-workspace/config/repository-graph.json"
	}
	_, abs, err := Within(root, rel)
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil
	}
	var doc struct {
		Version        int               `json:"version"`
		ReviewRequired bool              `json:"review_required"`
		Edges          []json.RawMessage `json:"edges"`
	}
	if json.Unmarshal(b, &doc) != nil || doc.Version != 1 || !doc.ReviewRequired || doc.Edges == nil {
		return nil
	}
	seen := map[Edge]bool{}
	edges := []Edge{}
	for _, raw := range doc.Edges {
		var fields map[string]any
		var e Edge
		if json.Unmarshal(raw, &fields) != nil || len(fields) != 3 || json.Unmarshal(raw, &e) != nil {
			return nil
		}
		e.Source, e.Target, e.Relationship = strings.TrimSpace(e.Source), strings.TrimSpace(e.Target), strings.TrimSpace(e.Relationship)
		if e.Source == e.Target || !accepted[e.Source] || !accepted[e.Target] || relationPriors[e.Relationship] == 0 || seen[e] {
			return nil
		}
		seen[e] = true
		edges = append(edges, e)
	}
	return edges
}

// Routed is one repository selected for retrieval.
type Routed struct {
	Repository   Repository `json:"-"`
	RepositoryID string     `json:"repository_id"`
	Tier         string     `json:"tier"`
	Prior        float64    `json:"prior"`
	Reason       string     `json:"reason"`
	Relationship *string    `json:"relationship"`
	Anchor       *string    `json:"anchor_repository_id"`
}

// RoutePlan is the repository_first_graph_expansion decision.
type RoutePlan struct {
	Strategy         string   `json:"strategy"`
	GraphFingerprint string   `json:"graph_fingerprint"`
	Primary          []string `json:"primary_repository_ids"`
	Expanded         []string `json:"expanded_repository_ids"`
	Skipped          []string `json:"skipped_repository_ids"`
	Repositories     []Routed `json:"repositories"`
}

// RouteOptions mirrors workspace.hierarchical_retrieval plus max_roots.
type RouteOptions struct {
	Enabled       bool
	MaxPrimary    int
	MaxExpansions int
	Relationships []string
	MaxRoots      int
	GraphPath     string
}

func mentions(query, name, rel string) bool {
	lq := strings.ToLower(query)
	for _, c := range []string{strings.ToLower(strings.TrimSpace(name)), strings.Trim(strings.ToLower(rel), "./")} {
		if c == "" || c == "." {
			continue
		}
		if strings.ContainsAny(c, `/\`) {
			if strings.Contains(lq, c) {
				return true
			}
			continue
		}
		if regexp.MustCompile(`(^|[^a-z0-9_.-])` + regexp.QuoteMeta(c) + `($|[^a-z0-9_.-])`).MatchString(lq) {
			return true
		}
	}
	return false
}

// Route selects repositories: changed-file owners and repositories named in
// the query first, then bounded reviewed-graph neighbours. With no signal it
// keeps every included repository up to MaxRoots (V2 kept only the first).
func Route(root string, repos []Repository, query string, changed []string, o RouteOptions) RoutePlan {
	included := []Repository{}
	for _, r := range repos {
		if r.Included {
			included = append(included, r)
		}
	}
	g := LoadGraph(root, o.GraphPath, repos)
	plan := RoutePlan{Strategy: "repository_first_graph_expansion", GraphFingerprint: g.Fingerprint, Primary: []string{}, Expanded: []string{}, Skipped: []string{}, Repositories: []Routed{}}
	maxRoots := o.MaxRoots
	if maxRoots <= 0 {
		maxRoots = len(included)
	}
	nested := []string{}
	for _, r := range included {
		if r.RelativePath != "." {
			nested = append(nested, strings.TrimSuffix(r.RelativePath, "/")+"/")
		}
	}
	owns := func(r Repository) bool {
		for _, c := range changed {
			c = filepath.ToSlash(c)
			if r.RelativePath == "." {
				inNested := false
				for _, n := range nested {
					inNested = inNested || strings.HasPrefix(c, n)
				}
				if !inNested {
					return true
				}
			} else if c == r.RelativePath || strings.HasPrefix(c, strings.TrimSuffix(r.RelativePath, "/")+"/") {
				return true
			}
		}
		return false
	}
	selected := map[string]bool{}
	add := func(r Routed) {
		selected[r.RepositoryID] = true
		plan.Repositories = append(plan.Repositories, r)
	}
	for _, r := range included {
		if len(plan.Primary) >= max(1, o.MaxPrimary) {
			break
		}
		reason := ""
		if owns(r) {
			reason = "changed_files"
		} else if mentions(query, r.Name, r.RelativePath) {
			reason = "explicit_query_anchor"
		}
		if reason != "" {
			add(Routed{Repository: r, RepositoryID: r.RepositoryID, Tier: "primary", Prior: 1, Reason: reason})
			plan.Primary = append(plan.Primary, r.RepositoryID)
		}
	}
	if len(plan.Primary) == 0 {
		for _, r := range included {
			add(Routed{Repository: r, RepositoryID: r.RepositoryID, Tier: "default", Prior: 1, Reason: "no_repository_signal"})
		}
	} else if o.Enabled && o.MaxExpansions > 0 {
		allowed := map[string]bool{}
		for _, rel := range o.Relationships {
			allowed[rel] = true
		}
		byID := map[string]Repository{}
		for _, r := range included {
			byID[r.RepositoryID] = r
		}
		type cand struct {
			prior            float64
			edge, id, anchor string
			rel              string
		}
		cands := []cand{}
		for _, e := range g.Edges {
			if !allowed[e.Relationship] {
				continue
			}
			neighbor, anchor := "", ""
			switch {
			case selected[e.Source]:
				neighbor, anchor = e.Target, e.Source
			case selected[e.Target]:
				neighbor, anchor = e.Source, e.Target
			default:
				continue
			}
			if _, ok := byID[neighbor]; ok && !selected[neighbor] {
				cands = append(cands, cand{relationPriors[e.Relationship], digest(e), neighbor, anchor, e.Relationship})
			}
		}
		sort.Slice(cands, func(i, j int) bool {
			if cands[i].prior != cands[j].prior {
				return cands[i].prior > cands[j].prior
			}
			return cands[i].edge < cands[j].edge
		})
		for _, c := range cands {
			if len(plan.Expanded) >= o.MaxExpansions || selected[c.id] {
				continue
			}
			rel, anchor := c.rel, c.anchor
			add(Routed{Repository: byID[c.id], RepositoryID: c.id, Tier: "graph_expansion", Prior: c.prior, Reason: "reviewed_graph_neighbor", Relationship: &rel, Anchor: &anchor})
			plan.Expanded = append(plan.Expanded, c.id)
		}
	}
	if len(plan.Repositories) > maxRoots {
		plan.Repositories = plan.Repositories[:maxRoots]
	}
	kept := map[string]bool{}
	for _, r := range plan.Repositories {
		kept[r.RepositoryID] = true
	}
	plan.Primary, plan.Expanded = filterKept(plan.Primary, kept), filterKept(plan.Expanded, kept)
	for _, r := range included {
		if !kept[r.RepositoryID] {
			plan.Skipped = append(plan.Skipped, r.RepositoryID)
		}
	}
	return plan
}

func filterKept(ids []string, kept map[string]bool) []string {
	out := []string{}
	for _, id := range ids {
		if kept[id] {
			out = append(out, id)
		}
	}
	return out
}
