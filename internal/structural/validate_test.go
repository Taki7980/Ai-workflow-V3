package structural

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
)

func crgRow(path, name string) map[string]any {
	return map[string]any{"name": name, "qualified_name": path + "::" + name, "file_path": path}
}

func crgItemRows(rows ...map[string]any) model.ContextItem {
	results := []any{}
	for _, r := range rows {
		results = append(results, r)
	}
	return CRGItem(map[string]any{"status": "ok", "results": results}, "callers_of", 9, 6, "Foo")
}

func basis(it model.ContextItem) []string {
	b, _ := it.Metadata["confidence_basis"].([]string)
	return b
}

func TestValidateCandidate(t *testing.T) {
	ws := gitRepo(t)
	it := Validate(context.Background(), ws, ".", []model.ContextItem{crgItemRows(crgRow("pkg/a.go", "Missing"))}, Query{Limit: 6})[0]
	if it.Metadata["evidence_confidence"] != "candidate" || it.Metadata["structural_valid"] != false || it.Metadata["high_risk_eligible"] != false {
		t.Fatalf("got %+v", it.Metadata)
	}
}

func TestValidateCorroboratedSource(t *testing.T) {
	ws := gitRepo(t)
	it := Validate(context.Background(), ws, ".", []model.ContextItem{crgItemRows(crgRow("pkg/a.go", "Bar"))}, Query{Limit: 6})[0]
	if it.Metadata["evidence_confidence"] != "corroborated" || !slices.Equal(basis(it), []string{"source"}) ||
		it.Metadata["source_confirmed_results"] != 1 || it.Metadata["structural_valid"] != true || it.Metadata["high_risk_eligible"] != true {
		t.Fatalf("got %+v", it.Metadata)
	}
}

func TestValidateOutsideRowNotConfirmed(t *testing.T) {
	ws := gitRepo(t)
	it := Validate(context.Background(), ws, ".", []model.ContextItem{crgItemRows(crgRow("../a.go", "Bar"))}, Query{Limit: 6})[0]
	if it.Metadata["evidence_confidence"] != "candidate" {
		t.Fatalf("got %+v", it.Metadata)
	}
}

func readyScip(t *testing.T, ws string) {
	t.Helper()
	dir, _ := ScipDir(ws, ".")
	writeFile(t, filepath.Join(dir, "index.scip"), "bin")
	b, _ := os.ReadFile(filepath.Join("testdata", "scip", "index.json"))
	writeFile(t, filepath.Join(dir, "index.json"), string(b))
	if _, err := WriteScipManifest(context.Background(), ws, ".", "go", "scip-go"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateVerified(t *testing.T) {
	ws := gitRepo(t)
	readyScip(t, ws)
	it := Validate(context.Background(), ws, ".", []model.ContextItem{crgItemRows(crgRow("pkg/a.go", "Foo"))}, Query{Symbol: "Foo", Limit: 6})[0]
	if it.Metadata["evidence_confidence"] != "verified" || !slices.Equal(basis(it), []string{"source", "scip"}) ||
		it.Metadata["verified_results"] != 1 || it.Metadata["scip_confirmed_results"] != 1 {
		t.Fatalf("got %+v", it.Metadata)
	}
}

func TestValidateEmptyVerified(t *testing.T) {
	ws := gitRepo(t)
	in := CRGItem(map[string]any{"status": "ok", "results": []any{}, "confidence": "real absence; graph current"}, "callers_of", 9, 6, "Foo")
	it := Validate(context.Background(), ws, ".", []model.ContextItem{in}, Query{Limit: 6})[0]
	if it.Metadata["evidence_confidence"] != "corroborated" || !slices.Equal(basis(it), []string{"crg_verified_empty"}) ||
		it.Metadata["structural_valid"] != true || it.Metadata["high_risk_eligible"] != false {
		t.Fatalf("got %+v", it.Metadata)
	}
}

func TestValidatePassesNonCRG(t *testing.T) {
	in := model.ContextItem{Source: "scip", Text: "x", Metadata: map[string]any{"a": 1}}
	out := Validate(context.Background(), t.TempDir(), ".", []model.ContextItem{in}, Query{})
	if len(out) != 1 || out[0].Source != "scip" || len(out[0].Metadata) != 1 {
		t.Fatalf("got %+v", out)
	}
}
