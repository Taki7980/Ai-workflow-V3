package cli

import (
	"encoding/json"
	"testing"
)

func TestGraphStatusCmd(t *testing.T) {
	root := setupWorkspace(t)
	code, out, stderr := run(t, root, nil, "graph", "status")
	if code != 0 {
		t.Fatalf("exit %d %s", code, stderr)
	}
	var got struct {
		Repositories []struct {
			RelativePath string `json:"relative_path"`
			Ready        bool   `json:"ready"`
			Reason       string `json:"reason"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Repositories) != 1 || got.Repositories[0].Reason != "graph.db is missing" {
		t.Fatalf("got %s %v", out, err)
	}
	if code, _, _ := run(t, root, nil, "graph", "bogus"); code != 2 {
		t.Fatalf("bogus exit %d", code)
	}
	if code, _, _ := run(t, root, nil, "graph"); code != 2 {
		t.Fatalf("bare exit %d", code)
	}
	t.Setenv("PATH", t.TempDir())
	if code, out, _ := run(t, root, nil, "graph", "sync"); code != 1 || !json.Valid([]byte(out)) {
		t.Fatalf("sync without CRG exit %d %s", code, out)
	}
}

func TestScipStatusCmd(t *testing.T) {
	root := setupWorkspace(t)
	code, out, _ := run(t, root, nil, "scip", "status")
	if code != 0 || !json.Valid([]byte(out)) {
		t.Fatalf("exit %d %s", code, out)
	}
	if code, _, _ := run(t, root, nil, "scip", "sync", "--repo", "../outside"); code != 1 {
		t.Fatalf("outside repo exit %d", code)
	}
}
