package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func repo(rel string) Repository {
	return Repository{RepositoryID: RepositoryID(rel, ""), Name: filepath.Base(rel), RelativePath: rel, Included: true}
}

func ids(rs []Routed) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, r.Repository.RelativePath+":"+r.Tier)
	}
	return out
}

func writeGraph(t *testing.T, root, body string) {
	t.Helper()
	p := filepath.Join(root, "ai-workspace", "config", "repository-graph.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRoute(t *testing.T) {
	root := t.TempDir()
	api, web, ops := repo("api"), repo("web"), repo("ops")
	off := repo("legacy")
	off.Included = false
	repos := []Repository{api, web, ops, off}
	opts := RouteOptions{Enabled: true, MaxPrimary: 2, MaxExpansions: 2, Relationships: []string{"depends_on", "deploys"}, MaxRoots: 4}

	if got := ids(Route(root, repos, "fix login", nil, opts).Repositories); len(got) != 3 || got[0] != "api:default" {
		t.Fatalf("no signal must keep every included repo: %v", got)
	}
	p := Route(root, repos, "fix login", []string{"web/src/a.ts"}, opts)
	if got := ids(p.Repositories); len(got) != 1 || got[0] != "web:primary" || len(p.Skipped) != 2 {
		t.Fatalf("changed file must pick its repo: %v skipped=%v", got, p.Skipped)
	}
	if got := ids(Route(root, repos, "update the ops pipeline", nil, opts).Repositories); len(got) != 1 || got[0] != "ops:primary" {
		t.Fatalf("query anchor: %v", got)
	}

	writeGraph(t, root, `{"version":1,"review_required":true,"edges":[`+
		`{"source_repository_id":"`+web.RepositoryID+`","target_repository_id":"`+api.RepositoryID+`","relationship":"depends_on"},`+
		`{"source_repository_id":"`+ops.RepositoryID+`","target_repository_id":"`+web.RepositoryID+`","relationship":"deploys"}]}`)
	p = Route(root, repos, "fix login", []string{"web/x"}, opts)
	if got := ids(p.Repositories); len(got) != 3 || got[1] != "api:graph_expansion" || got[2] != "ops:graph_expansion" {
		t.Fatalf("graph expansion ordered by prior: %v", got)
	}
	opts.MaxRoots = 2
	if got := ids(Route(root, repos, "fix login", []string{"web/x"}, opts).Repositories); len(got) != 2 {
		t.Fatalf("max_roots must cap selection: %v", got)
	}
	opts.MaxRoots = 4

	// One bad edge (unknown relationship / non-accepted repo) invalidates the graph.
	writeGraph(t, root, `{"version":1,"review_required":true,"edges":[`+
		`{"source_repository_id":"`+web.RepositoryID+`","target_repository_id":"`+api.RepositoryID+`","relationship":"depends_on"},`+
		`{"source_repository_id":"`+web.RepositoryID+`","target_repository_id":"`+off.RepositoryID+`","relationship":"depends_on"}]}`)
	if got := ids(Route(root, repos, "fix login", []string{"web/x"}, opts).Repositories); len(got) != 1 {
		t.Fatalf("invalid graph must grant nothing: %v", got)
	}
	g1 := LoadGraph(root, "", repos).Fingerprint
	writeGraph(t, root, `{"version":1,"review_required":true,"edges":[]}`)
	if LoadGraph(root, "", repos).Fingerprint != g1 {
		t.Fatal("an invalid graph and an empty graph have the same (edge-free) identity")
	}
	if _, _, err := Within(root, "../outside.json"); err == nil || len(LoadGraph(root, "../outside.json", repos).Edges) != 0 {
		t.Fatal("graph path must stay inside the root")
	}
}
