package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBenchmarkCommand(t *testing.T) {
	root := setupWorkspace(t)
	tasks := filepath.Join(t.TempDir(), "tasks.json")
	body := `[{"task":"Where is RetryPayment defined?","symbol":"RetryPayment","expected_lane":"answer","relevant_context":["RetryPayment"],"gold_files":["pay.go"]},
	{"task":"Explain the QuantumFluxTeleporter","expected_evidence_state":"abstain","control_type":"natural_no_gold"}]`
	if err := os.WriteFile(tasks, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := run(t, root, nil, "benchmark", "--tasks", tasks)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	var r struct {
		Summary map[string]any   `json:"summary"`
		Cases   []map[string]any `json:"cases"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.Summary["cases"] != 2.0 || r.Summary["lane_accuracy"] != 1.0 || r.Summary["mean_file_recall_at_k"] != 1.0 {
		t.Fatalf("summary=%v", r.Summary)
	}
	if _, ok := r.Cases[0]["context_economics"]; !ok {
		t.Fatal("token economics missing")
	}
	// The fixture repository has no commit yet, so HEAD cannot be captured.
	if code, out, _ := run(t, root, nil, "benchmark-corpus", "snapshot", "--repository", "."); code != 0 || !strings.Contains(out, `"status": "unavailable"`) {
		t.Fatalf("snapshot: %d %s", code, out)
	}
	if code, _, _ := run(t, root, nil, "benchmark-corpus", "snapshot", "--strict"); code != 1 {
		t.Fatal("--strict must fail when the snapshot is not capturable")
	}
	if code, _, _ := run(t, root, nil, "benchmark", "--tasks", tasks, "--research-protocol"); code != 1 {
		t.Fatal("research protocol must reject cases without task_type")
	}
}
