package learning

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"testing"
)

// fixture was produced by V2's own Python learning stack (see testdata):
// seeded records, then V2's evaluate / contextual-policy / manifest / shadow
// outputs. V3 must reproduce them exactly.
type fixture struct {
	Records      []map[string]any `json:"records"`
	PreCutoffIDs []string         `json:"pre_cutoff_ids"`
	Evaluate     map[string]any   `json:"evaluate"`
	Contextual   map[string]any   `json:"contextual"`
	Manifest     json.RawMessage  `json:"manifest"`
	Shadow       map[string]any   `json:"shadow"`
	SigningKey   string           `json:"signing_key"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "v2_parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func writeRecords(t *testing.T, root string, recs []map[string]any) {
	t.Helper()
	for _, r := range recs {
		id := r["decision_id"].(string)
		dec := map[string]any{}
		for k, v := range r {
			if k != "observation" && k != "outcome" {
				dec[k] = v
			}
		}
		for kind, v := range map[string]any{"decisions": dec, "outcomes": r["outcome"]} {
			if v == nil {
				continue
			}
			p := filepath.Join(root, filepath.FromSlash(learningRelative), kind, id+".json")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(v)
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// roundTrip normalizes a Go result to the shape json.Unmarshal gives V2's output.
func roundTrip(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func diffs(path string, a, b any, out *[]string) {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: %v != %v", path, a, b))
			return
		}
		keys := []string{}
		for k := range x {
			keys = append(keys, k)
		}
		for k := range y {
			if _, ok := x[k]; !ok {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range slices.Compact(keys) {
			diffs(path+"/"+k, x[k], y[k], out)
		}
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			*out = append(*out, fmt.Sprintf("%s: %v != %v", path, a, b))
			return
		}
		for i := range x {
			diffs(fmt.Sprintf("%s[%d]", path, i), x[i], y[i], out)
		}
	default:
		if !reflect.DeepEqual(a, b) {
			*out = append(*out, fmt.Sprintf("%s: V2 %v != V3 %v", path, a, b))
		}
	}
}

func assertSame(t *testing.T, name string, want map[string]any, got any) {
	t.Helper()
	var d []string
	diffs("", want, roundTrip(t, got), &d)
	if len(d) > 0 {
		t.Fatalf("%s differs from V2 (%d diffs), first: %v", name, len(d), d[:min(5, len(d))])
	}
}

func TestV2Parity(t *testing.T) {
	f := loadFixture(t)
	if len(f.Manifest) == 0 || string(f.Manifest) == "null" || f.Shadow == nil {
		t.Fatal("fixture must include an eligible manifest and its shadow evaluation")
	}
	root := t.TempDir()
	pre := []map[string]any{}
	for _, r := range f.Records {
		if slices.Contains(f.PreCutoffIDs, r["decision_id"].(string)) {
			pre = append(pre, r)
		}
	}
	writeRecords(t, root, pre)

	ev, err := Evaluate(root, EvaluateOptions{Confidence: .95, Resamples: 200, Seed: 9, MinimumESS: 10, MinimumDirect: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertSame(t, "evaluate", f.Evaluate, ev)

	ctx, err := ContextualPolicy(root, ContextualOptions{Fields: []string{"intent"}, DevelopmentFraction: .7, PriorWeight: 5, Folds: 5,
		MinContext: 3, MinDirect: 3, MinHoldout: 3, MinESS: 1, MinValidation: 3, Confidence: .95, Resamples: 200, Seed: 4})
	if err != nil {
		t.Fatal(err)
	}
	assertSame(t, "contextual-policy", f.Contextual, ctx)

	// Verify V2's signed manifest with exact number text preserved.
	dec := json.NewDecoder(bytes.NewReader(f.Manifest))
	dec.UseNumber()
	var manifest map[string]any
	if err := dec.Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	key := []byte(f.SigningKey)
	if v := VerifyManifest(manifest, key); v["valid"] != true {
		t.Fatalf("V3 must verify V2's manifest: %v", v)
	}

	writeRecords(t, root, f.Records) // add the post-cutoff records
	sh, err := Shadow(root, manifest, key, ShadowOptions{Confidence: .95, RewardMin: 0, RewardMax: 1, MaxWeight: 20,
		MinimumNewEvents: 5, Resamples: 200, Seed: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertSame(t, "shadow-evaluate", f.Shadow, sh)

	// A V3-created manifest round-trips through V3 verification and detects tampering.
	m3, err := CreateManifest(manifest2report(t, f), key)
	if err != nil {
		t.Fatal(err)
	}
	m3 = roundTrip(t, m3)
	if v := VerifyManifest(m3, key); v["valid"] != true {
		t.Fatalf("%v", v)
	}
	m3["status"] = "active"
	if v := VerifyManifest(m3, key); v["valid"] == true {
		t.Fatal("tampered manifest verified")
	}
}

func manifest2report(t *testing.T, f fixture) map[string]any {
	t.Helper()
	return roundTrip(t, f.Contextual)
}
