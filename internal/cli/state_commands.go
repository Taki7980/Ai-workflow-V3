package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/Taki7980/ai-workflow-v3/internal/brief"
	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/journal"
	"github.com/Taki7980/ai-workflow-v3/internal/telemetry"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

// statsCmd implements `stats [--limit N] [--recommend] [--minimum-runs N]`.
func statsCmd(root string, args []string, out, errOut io.Writer) int {
	fs := newFlags("stats", errOut)
	limit := fs.Int("limit", 200, "maximum traces to read")
	recommend := fs.Bool("recommend", false, "add advisory policy feedback")
	minRuns := fs.Int("minimum-runs", 20, "minimum traces before recommending")
	if pos, err := parseInterspersed(fs, args); err != nil || len(pos) != 0 {
		return 2
	}
	r := telemetry.Summarize(root, *limit)
	if *recommend {
		r["policy_feedback"] = telemetry.Recommend(root, *limit, *minRuns)
	}
	printJSON(out, r)
	return 0
}

// currentState captures present identities for journal compatibility checks.
func currentState(root string) (journal.Current, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return journal.Current{}, err
	}
	reg, err := workspace.Load(root)
	if err != nil {
		return journal.Current{}, err
	}
	rels := []string{}
	for _, r := range reg.Repositories {
		if r.Included {
			rels = append(rels, r.RelativePath)
		}
	}
	return journal.Current{
		ConfigDigest:           config.Digest(root),
		RetrievalPolicyVersion: fmt.Sprint(cfg.Version),
		WorkspaceFingerprint: func(changed []string) string {
			return brief.CurrentWorkspaceFingerprint(context.Background(), root, changed)
		},
		GraphFingerprint:      brief.GraphFingerprint(root, rels),
		RepositoryFingerprint: workspace.Summarize(root, reg.Repositories).Fingerprint,
	}, nil
}

// replayCmd implements `replay RUN_ID [--strict]`: verified decision history,
// never re-executing models, tools, providers or network calls.
func replayCmd(root string, args []string, out, errOut io.Writer) int {
	fs := newFlags("replay", errOut)
	strict := fs.Bool("strict", false, "exit 1 on integrity failure or current-state drift")
	pos, err := parseInterspersed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprintln(errOut, "replay requires one run id")
		return 2
	}
	cur, err := currentState(root)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	r := journal.Replay(root, pos[0], cur)
	printJSON(out, r)
	integrity, _ := r["integrity"].(journal.Integrity)
	compat, _ := r["compatibility"].(map[string]any)
	if *strict && (r["found"] != true || !integrity.Valid || compat["compatible"] != true) {
		return 1
	}
	return 0
}

// runCmd implements `run inspect|verify RUN_ID` (V2 run_cli).
func runCmd(root string, args []string, out, errOut io.Writer) int {
	if len(args) != 2 || (args[0] != "inspect" && args[0] != "verify") {
		fmt.Fprintln(errOut, "run requires inspect or verify and one run id")
		return 2
	}
	id := args[1]
	rec, ok := journal.Read(root, id)
	if args[0] == "inspect" {
		if !ok {
			printJSON(out, map[string]any{"run_id": id, "error": "run journal not found"})
			return 1
		}
		printJSON(out, rec)
		return 0
	}
	if !ok {
		printJSON(out, map[string]any{"run_id": id, "compatible": false, "mismatches": []string{"missing_journal"}})
		return 0
	}
	if !journal.Verify(rec, id).Valid {
		printJSON(out, map[string]any{"run_id": id, "compatible": false, "mismatches": []string{"invalid_journal"}, "recorded": map[string]any{}, "current": map[string]any{}})
		return 0
	}
	cur, err := currentState(root)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	printJSON(out, journal.Compatibility(id, rec, cur))
	return 0
}
