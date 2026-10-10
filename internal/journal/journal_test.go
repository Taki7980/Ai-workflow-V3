package journal

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func events() []Event {
	out := make([]Event, 0, len(EventKinds))
	for _, k := range EventKinds {
		p := map[string]any{"k": k}
		if k == "retrieval" {
			p = map[string]any{"evidence_state": "sufficient", "score": 0.75}
		}
		if k == "selection" {
			p = map[string]any{"selected_evidence": []any{map[string]any{"source": "x", "score": 1.5}}}
		}
		out = append(out, Event{Kind: k, Payload: p})
	}
	return out
}

func sample(t *testing.T) map[string]any {
	t.Helper()
	rec, err := Build(map[string]any{
		"run_id": "run-1", "policy_identity": map[string]any{"config_digest": "c"},
		"workspace_state": map[string]any{"fingerprint": "w"}, "graph_state": map[string]any{"fingerprint": "g"},
		"repository_state": map[string]any{"fingerprint": "r"}, "provider_versions": map[string]any{},
		"retrieval": map[string]any{"evidence_state": "sufficient", "score": 0.75}, "changed_files": []string{"a.go"},
		"selected_evidence": []any{map[string]any{"source": "x", "score": 1.5}},
	}, events())
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestRoundTripVerifies(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(root, sample(t)); err != nil {
		t.Fatal(err)
	}
	rec, ok := Read(root, "run-1")
	if !ok {
		t.Fatal("read failed")
	}
	if in := Verify(rec, "run-1"); !in.Valid {
		t.Fatalf("errors=%v", in.Errors)
	}
	if _, err := Write(root, sample(t)); err == nil {
		t.Fatal("existing journal must never be overwritten")
	}
}

func TestTamperDetected(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(root, sample(t)); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, filepath.FromSlash(Relative), "run-1.json")
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(p, []byte(strings.Replace(string(b), `"k": "orchestration"`, `"k": "tampered"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, _ := Read(root, "run-1")
	in := Verify(rec, "run-1")
	if in.Valid || !slices.Contains(in.Errors, "event_5_digest_mismatch") {
		t.Fatalf("tamper not detected: %+v", in)
	}
	r := Replay(root, "run-1", Current{WorkspaceFingerprint: func([]string) string { return "" }})
	if r["events_released"] != false || len(r["events"].([]any)) != 0 {
		t.Fatal("invalid journal must not release events")
	}
}

func TestBuildRejectsWrongEventContract(t *testing.T) {
	ev := events()
	ev[0], ev[1] = ev[1], ev[0]
	if _, err := Build(map[string]any{"run_id": "r"}, ev); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnsafeRunIDRejected(t *testing.T) {
	for _, id := range []string{"../x", "a/b", "", "a b"} {
		if _, ok := Read(t.TempDir(), id); ok || ValidRunID(id) {
			t.Fatalf("%q accepted", id)
		}
	}
}

func TestCompatibilityDrift(t *testing.T) {
	rec := sample(t)
	cur := Current{ConfigDigest: "c", GraphFingerprint: "g", RepositoryFingerprint: "r",
		WorkspaceFingerprint: func(ch []string) string {
			if len(ch) != 1 || ch[0] != "a.go" {
				t.Fatalf("changed=%v", ch)
			}
			return "w"
		}}
	// retrieval_policy_version was not recorded, so it compares "" with "".
	if c := Compatibility("run-1", rec, cur); c["compatible"] != true {
		t.Fatalf("%v", c)
	}
	cur.GraphFingerprint = "changed"
	c := Compatibility("run-1", rec, cur)
	if c["compatible"] != false || !slices.Contains(c["mismatches"].([]string), "graph_fingerprint") {
		t.Fatalf("%v", c)
	}
}
