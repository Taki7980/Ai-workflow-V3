package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/indexer"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

func writeIndexCommandWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repo := workspace.Repository{
		RepositoryID: workspace.RepositoryID(".", ""),
		Name:         "fixture",
		RelativePath: ".",
		Included:     true,
		Reason:       "test",
	}
	if err := workspace.Save(root, []workspace.Repository{repo}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package fixture\nfunc Alpha() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func decodeIndexStats(t *testing.T, out *bytes.Buffer) map[string]indexer.BuildStats {
	t.Helper()
	var stats map[string]indexer.BuildStats
	if err := json.Unmarshal(out.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats: %v\n%s", err, out.String())
	}
	return stats
}

func TestIndexCommandDefaultsToAuto(t *testing.T) {
	root := writeIndexCommandWorkspace(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"--root", root, "index"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	stats := decodeIndexStats(t, &out)["."]
	if stats.RequestedMode != string(indexer.BuildAuto) {
		t.Fatalf("requested_mode=%q want=%q", stats.RequestedMode, indexer.BuildAuto)
	}
	if stats.EffectiveMode != string(indexer.BuildFull) {
		t.Fatalf("first auto build effective_mode=%q want=full", stats.EffectiveMode)
	}
}

func TestIndexCommandAcceptsIncrementalAndFull(t *testing.T) {
	root := writeIndexCommandWorkspace(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"--root", root, "index", "--mode", "full"}, &out, &errOut); code != 0 {
		t.Fatalf("full code=%d stderr=%s", code, errOut.String())
	}
	full := decodeIndexStats(t, &out)["."]
	if full.RequestedMode != string(indexer.BuildFull) || full.EffectiveMode != string(indexer.BuildFull) {
		t.Fatalf("full stats=%#v", full)
	}

	out.Reset()
	errOut.Reset()
	if code := Run([]string{"--root", root, "index", "--mode", "incremental"}, &out, &errOut); code != 0 {
		t.Fatalf("incremental code=%d stderr=%s", code, errOut.String())
	}
	inc := decodeIndexStats(t, &out)["."]
	if inc.RequestedMode != string(indexer.BuildIncremental) || inc.EffectiveMode != string(indexer.BuildIncremental) {
		t.Fatalf("incremental stats=%#v", inc)
	}
	if inc.Reparsed != 0 || inc.Reused != 1 || inc.Hashed != 1 {
		t.Fatalf("unexpected incremental work: %#v", inc)
	}
}

func TestIndexCommandRejectsUnknownMode(t *testing.T) {
	root := writeIndexCommandWorkspace(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"--root", root, "index", "--mode", "fast"}, &out, &errOut); code != 2 {
		t.Fatalf("code=%d want=2 stderr=%s", code, errOut.String())
	}
}

func TestIndexCommandRejectsUnexpectedPositionals(t *testing.T) {
	root := writeIndexCommandWorkspace(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"--root", root, "index", "extra"}, &out, &errOut); code != 2 {
		t.Fatalf("code=%d want=2 stderr=%s", code, errOut.String())
	}
}

func TestIndexCommandOutputsPerRepositoryBuildStats(t *testing.T) {
	root := writeIndexCommandWorkspace(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"--root", root, "index", "--mode", "full"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	stats := decodeIndexStats(t, &out)
	rootStats, ok := stats["."]
	if !ok {
		t.Fatalf("missing root stats: %#v", stats)
	}
	if rootStats.Files != 1 || rootStats.Symbols != 1 || rootStats.Hashed != 1 || rootStats.Reparsed != 1 {
		t.Fatalf("unexpected stats: %#v", rootStats)
	}
}
