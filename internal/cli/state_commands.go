package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Taki7980/ai-workflow-v3/internal/brief"
	"github.com/Taki7980/ai-workflow-v3/internal/capability"
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

// authorizeCmd implements `authorize RUN_ID [--request FILE]`: it checks one
// model-proposed action (JSON on stdin or in FILE) against the capability
// policy recorded in a verified run journal. Exit 0 = allowed, 1 = denied.
func authorizeCmd(root string, args []string, in io.Reader, out, errOut io.Writer) int {
	fs := newFlags("authorize", errOut)
	file := fs.String("request", "", "action request JSON file (default stdin)")
	pos, err := parseInterspersed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprintln(errOut, "authorize requires one run id")
		return 2
	}
	rec, ok := journal.Read(root, pos[0])
	if !ok {
		fmt.Fprintln(errOut, "run journal not found")
		return 1
	}
	if v := journal.Verify(rec, pos[0]); !v.Valid {
		fmt.Fprintln(errOut, "run journal failed integrity verification; refusing to authorize")
		return 1
	}
	policy, known, err := recordedPolicy(rec)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	var raw []byte
	if *file != "" {
		raw, err = os.ReadFile(*file)
	} else {
		raw, err = io.ReadAll(io.LimitReader(in, 1<<20))
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	var req capability.Request
	if err := dec.Decode(&req); err != nil {
		fmt.Fprintln(errOut, "invalid action request:", err)
		return 2
	}
	d := capability.Authorize(policy, req, known)
	printJSON(out, d)
	if !d.Allowed {
		return 1
	}
	return 0
}

// recordedPolicy extracts the authorization policy and known evidence IDs
// from a verified journal.
func recordedPolicy(rec map[string]any) (capability.Policy, map[string]bool, error) {
	var policy capability.Policy
	events, _ := rec["replay_events"].([]any)
	for _, e := range events {
		ev, _ := e.(map[string]any)
		if ev["kind"] != "authorization" {
			continue
		}
		b, _ := json.Marshal(ev["payload"])
		if err := json.Unmarshal(b, &policy); err != nil || policy.Schema != capability.Schema {
			return policy, nil, errors.New("run journal has no capability-v2 policy (recorded by an older version?)")
		}
	}
	if policy.Schema == "" {
		return policy, nil, errors.New("run journal has no authorization event")
	}
	known := map[string]bool{}
	sel, _ := rec["selected_evidence"].([]any)
	for _, s := range sel {
		m, _ := s.(map[string]any)
		e, _ := m["evidence"].(map[string]any)
		if id, _ := e["evidence_id"].(string); id != "" {
			known[id] = true
		}
	}
	return policy, known, nil
}
