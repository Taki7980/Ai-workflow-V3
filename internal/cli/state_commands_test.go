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

func TestAuthorizeAgainstRecordedPolicy(t *testing.T) {
	root := setupWorkspace(t)
	_, out, _ := run(t, root, nil, "brief", "fix RetryPayment retry handling")
	var p struct {
		Retrieval struct {
			RunID  string `json:"run_id"`
			Policy struct {
				Schema string `json:"schema"`
			} `json:"authorization_policy"`
		} `json:"retrieval"`
		Context []struct {
			Evidence struct {
				EvidenceID string `json:"evidence_id"`
			} `json:"evidence"`
		} `json:"context"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	if p.Retrieval.Policy.Schema != "capability-v2" || len(p.Context) == 0 || p.Context[0].Evidence.EvidenceID == "" {
		t.Fatalf("brief must carry policy and evidence ids: %s", out)
	}
	id := p.Retrieval.RunID
	deny := `{"capability":"provider_selection"}`
	if code, out, _ := run(t, root, &deny, "authorize", id); code != 1 || !strings.Contains(out, "control_plane_owned_capability") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	unknown := `{"capability":"tool_execution","tool_name":"x","bogus":1}`
	if code, _, _ := run(t, root, &unknown, "authorize", id); code != 2 {
		t.Fatal("unknown request fields must be rejected")
	}
	jp := filepath.Join(root, "ai-workspace", "generated", "run-journal", id+".json")
	b, _ := os.ReadFile(jp)
	if err := os.WriteFile(jp, []byte(strings.Replace(string(b), `"capability-v2"`, `"capability-vX"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := run(t, root, &deny, "authorize", id); code != 1 || !strings.Contains(stderr, "integrity") {
		t.Fatalf("tampered journal must be refused: %d %s", code, stderr)
	}
}
