package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/structural"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

type repoStatus struct {
	RelativePath string `json:"relative_path"`
	structural.Status
}

// includedRepos returns the requested repositories, or every included one.
func includedRepos(root string, requested []string) ([]string, error) {
	if len(requested) > 0 {
		return requested, nil
	}
	reg, err := workspace.Load(root)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, r := range reg.Repositories {
		if r.Included {
			out = append(out, r.RelativePath)
		}
	}
	return out, nil
}

// structuralCmd handles `graph sync|status` and `scip sync|status`.
func structuralCmd(kind, root string, args []string, out, errOut io.Writer) int {
	if len(args) == 0 || (args[0] != "sync" && args[0] != "status") {
		fmt.Fprintf(errOut, "%s requires sync or status\n", kind)
		return 2
	}
	sub := args[0]
	fs := newFlags(kind+" "+sub, errOut)
	var repos stringList
	fs.Var(&repos, "repo", "repository relative path (repeatable; default all included)")
	timeout := fs.Int("timeout", 180, "per-repository timeout in seconds")
	language := fs.String("language", "", "SCIP indexer language (scip sync only)")
	if pos, err := parseInterspersed(fs, args[1:]); err != nil || len(pos) != 0 || (kind == "graph" && *language != "") {
		return 2
	}
	rels, err := includedRepos(root, repos)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	ctx := context.Background()
	if sub == "status" {
		statuses := []repoStatus{}
		for _, rel := range rels {
			st := structural.GraphStatus(ctx, root, rel)
			if kind == "scip" {
				st = structural.ScipStatus(ctx, root, rel)
			}
			statuses = append(statuses, repoStatus{rel, st})
		}
		printJSON(out, map[string]any{"repositories": statuses})
		return 0
	}
	limit := time.Duration(max(1, *timeout)) * time.Second
	var report structural.SyncReport
	if kind == "graph" {
		report = structural.SyncGraphs(ctx, root, rels, limit)
	} else {
		report = structural.SyncScip(ctx, root, rels, *language, limit)
	}
	printJSON(out, report)
	if !report.Installed || report.Ready != report.Attempted {
		return 1
	}
	return 0
}
