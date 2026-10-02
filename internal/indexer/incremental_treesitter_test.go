//go:build treesitter

package indexer

import (
	"context"
	"reflect"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/storage"
)

func TestTreeSitterIncrementalReuseAcrossPilotLanguages(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	fixtures := map[string]string{
		"pilot.py":  "def PythonPilot():\n    return 1\n",
		"pilot.js":  "function JavaScriptPilot() {}\n",
		"pilot.ts":  "function TypeScriptPilot(): void {}\n",
		"pilot.tsx": "const TSXPilot = () => <div />\n",
	}
	for path, content := range fixtures {
		writeSource(t, root, path, content)
	}

	full, fullStats, err := BuildWithMode(context.Background(), root, repo, BuildFull)
	if err != nil {
		t.Fatal(err)
	}
	if fullStats.Reparsed != len(fixtures) {
		t.Fatalf("full reparsed=%d want=%d", fullStats.Reparsed, len(fixtures))
	}
	for name, path := range map[string]string{
		"PythonPilot":     "pilot.py",
		"JavaScriptPilot": "pilot.js",
		"TypeScriptPilot": "pilot.ts",
		"TSXPilot":        "pilot.tsx",
	} {
		if !hasSymbol(full, name, path) {
			t.Fatalf("missing %s from %s: %#v", name, path, full.Symbols)
		}
	}

	inc, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Hashed != len(fixtures) || stats.Reparsed != 0 || stats.Reused != len(fixtures) {
		t.Fatalf("unexpected tagged incremental stats: %#v", stats)
	}
	if !reflect.DeepEqual(full.Files, inc.Files) || !reflect.DeepEqual(full.Symbols, inc.Symbols) {
		t.Fatalf("tagged full/incremental mismatch:\nfull=%#v\ninc=%#v", full, inc)
	}
}

func TestTreeSitterBuildRejectsDefaultExtractorRevision(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "pilot.py", "def PythonPilot():\n    return 1\n")
	old := Index{
		Version:           IndexVersion,
		Repository:        repo.RelativePath,
		RepositoryID:      repo.RepositoryID,
		ExtractorRevision: "symbols-v1:go-ast+regex",
		Files:             map[string]FileState{},
	}
	if err := storage.WriteJSON(Path(root, repo), old); err != nil {
		t.Fatal(err)
	}
	_, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.EffectiveMode != string(BuildFull) || stats.FullReason != "extractor-revision" || stats.Reparsed != 1 {
		t.Fatalf("unexpected cross-profile fallback: %#v", stats)
	}
}
