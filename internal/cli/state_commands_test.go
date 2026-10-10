package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBriefTraceJournalReplayAndStats(t *testing.T) {
	root := setupWorkspace(t)
	code, out, stderr := run(t, root, nil, "brief", "fix RetryPayment retry handling")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	var p struct {
		Retrieval struct {
			RunID   string `json:"run_id"`
			Trace   string `json:"trace"`
			Journal string `json:"journal"`
		} `json:"retrieval"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	if p.Retrieval.RunID == "" || p.Retrieval.Trace == "" || p.Retrieval.Journal == "" {
		t.Fatalf("mutation brief must trace and journal: %+v", p.Retrieval)
	}
	trace, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(p.Retrieval.Trace)))
	if strings.Contains(string(trace), "fix RetryPayment retry handling") {
		t.Fatal("trace must not store task text")
	}
	id := p.Retrieval.RunID
	if code, out, _ := run(t, root, nil, "replay", id, "--strict"); code != 0 || !strings.Contains(out, `"events_released": true`) {
		t.Fatalf("replay code=%d out=%s", code, out)
	}
	if code, out, _ := run(t, root, nil, "run", "verify", id); code != 0 || !strings.Contains(out, `"compatible": true`) {
		t.Fatalf("run verify: %d %s", code, out)
	}
	if code, out, _ := run(t, root, nil, "stats", "--recommend"); code != 0 || !strings.Contains(out, `"runs": 1`) || !strings.Contains(out, "policy_feedback") {
		t.Fatalf("stats: %s", out)
	}

	// Drift: a config change fails --strict.
	cfgPath := filepath.Join(root, "ai-workspace", "config", "control-plane.json")
	b, _ := os.ReadFile(cfgPath)
	changed := strings.Replace(string(b), `"max_results_per_source": 6`, `"max_results_per_source": 7`, 1)
	if changed == string(b) {
		t.Fatal("fixture config did not change")
	}
	if err := os.WriteFile(cfgPath, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run(t, root, nil, "replay", id, "--strict"); code != 1 || !strings.Contains(out, `"config_digest"`) {
		t.Fatalf("drift must fail strict: %d %s", code, out)
	}

	// Tamper: integrity failure withholds events.
	jp := filepath.Join(root, filepath.FromSlash(p.Retrieval.Journal))
	jb, _ := os.ReadFile(jp)
	tampered := strings.Replace(string(jb), `"risk": "`, `"risk": "x`, 1)
	if tampered == string(jb) {
		t.Fatal("journal fixture did not change")
	}
	if err := os.WriteFile(jp, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run(t, root, nil, "replay", id, "--strict"); code != 1 || !strings.Contains(out, `"events_released": false`) {
		t.Fatalf("tamper: %d %s", code, out)
	}
	if code, out, _ := run(t, root, nil, "replay", "../etc"); code != 0 || !strings.Contains(out, `"found": false`) {
		t.Fatalf("unsafe id must read as not found: %d %s", code, out)
	}
}

func TestAnswerBriefDoesNotTraceUnlessAsked(t *testing.T) {
	root := setupWorkspace(t)
	if _, out, _ := run(t, root, nil, "brief", "explain RetryPayment"); strings.Contains(out, `"journal"`) {
		t.Fatal("answer lane must not journal by default")
	}
	if _, out, _ := run(t, root, nil, "context", "explain RetryPayment", "--trace"); !strings.Contains(out, `"journal"`) {
		t.Fatalf("--trace must journal: %s", out)
	}
}
