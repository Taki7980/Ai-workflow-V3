package bench

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
)

func item(path string, line int, text string) model.ContextItem {
	return model.ContextItem{Source: "lightweight_index", Text: text, Metadata: map[string]any{"path": path, "line": line}}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func cases(t *testing.T, s string) []Case {
	t.Helper()
	var raw []map[string]any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		t.Fatal(err)
	}
	out := []Case{}
	for _, r := range raw {
		out = append(out, Case(r))
	}
	return out
}

func TestPatternAndFileMetrics(t *testing.T) {
	items := []model.ContextItem{item("a.go", 1, "noise"), item("b.go", 2, "has Alpha"), item("c.go", 3, "has beta and alpha")}
	m := PatternMetrics(items, []string{"alpha", "beta"}, 3)
	if m["recall_at_k"] != 1.0 || !near(m["mrr"].(float64), .5) || m["relevant_items"] != 2 {
		t.Fatalf("%v", m)
	}
	f := FileMetrics(append(items, item("b.go", 9, "dup")), []string{"./b.go", "z.go"}, 5)
	if !near(f["recall_at_k"].(float64), .5) || !near(f["precision_at_k"].(float64), .2) || !near(f["mrr"].(float64), .5) {
		t.Fatalf("%v", f)
	}
	if PatternMetrics(items, nil, 3) != nil || FileMetrics(items, nil, 3) != nil {
		t.Fatal("no gold must yield nil")
	}
}

func TestSpanMetricsWithLineBudget(t *testing.T) {
	items := []model.ContextItem{
		{Source: "x", Text: "a.go:10:20: code", Metadata: map[string]any{}},
		{Source: "x", Text: "t", Metadata: map[string]any{"path": "a.go", "start_line": 30, "end_line": 40}},
	}
	gold := []any{map[string]any{"path": "a.go", "start_line": json.Number("15"), "end_line": json.Number("35")}}
	m, err := SpanMetrics(items, gold, 5, 0)
	if err != nil || m["covered_gold_lines"] != 12 || m["total_gold_lines"] != 21 || m["span_recall_at_k"] != 1.0 {
		t.Fatalf("m=%v err=%v", m, err)
	}
	m, _ = SpanMetrics(items, gold, 5, 5) // budget truncates the first span to 10..14
	if m["covered_gold_lines"] != 0 || m["retrieved_lines"] != 5 {
		t.Fatalf("line budget not applied: %v", m)
	}
}

func TestRoleAndTrajectory(t *testing.T) {
	items := []model.ContextItem{item("edit.go", 1, "x"), item("decoy.go", 1, "y"), item("help.go", 1, "z")}
	rel := []any{map[string]any{"path": "edit.go", "role": "edit_target"}, map[string]any{"path": "help.go", "role": "supporting_context"}}
	m := RoleMetrics(items, rel, []string{"decoy.go"}, 3)
	if *m["edit_target_recall_at_k"].(*float64) != 1 || *m["known_distractor_rate_at_k"].(*float64) != 1.0/3 || *m["coverage_balance"].(*float64) != 1 {
		t.Fatalf("%v", m)
	}
	ev := []event{{"seed", "a.go", 0}, {"explored", "a.go", 1}, {"explored", "a.go", 2}, {"explored", "b.go", 3}, {"utilized", "b.go", 4}}
	tm := TrajectoryMetrics(ev, []string{"b.go"})
	if tm["exploration_recall"] != 1.0 || !near(tm["duplicate_exploration_rate"].(float64), 1.0/3) || tm["first_gold_utilization_step"] != 4 || tm["post_seed_exploration_unique_files"] != 2 {
		t.Fatalf("%v", tm)
	}
}

func TestValidateCases(t *testing.T) {
	bad := map[string]string{
		`[{"task":""}]`: "non-empty task",
		`[{"task":"x","retrieval_k":0}]`: "retrieval_k must be >= 1",
		`[{"task":"x","task_type":"code2test"}]`: "requires gold_files",
		`[{"task":"x","control_type":"wrong_repo","gold_files":["a.go"]}]`: "selective controls",
		`[{"task":"x","gold_files":["a.go"],"distractor_files":["a.go"]}]`: "disjoint",
		`[{"task":"x","repository_path":"../up"}]`: "inside the benchmark root",
		`[{"task":"x","base_commit":"abc"}]`: "full 40-64",
		`[{"task":"x","gold_spans":[{"path":"a","start_line":5,"end_line":2}]}]`: "start_line <= end_line",
		`[{"task":"x","gold_files":["a.go"],"file_relevance":[{"path":"b.go","role":"edit_target"}]}]`: "label every gold_file",
	}
	for in, want := range bad {
		if err := ValidateCases(cases(t, in), false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err=%v want %q", in, err, want)
		}
	}
	if err := ValidateCases(cases(t, `[{"task":"x","retrieval_k":"3"}]`), false); err != nil {
		t.Fatalf("numeric strings are accepted like V2: %v", err)
	}
	if err := ValidateCases(cases(t, `[{"task":"x"}]`), true); err == nil {
		t.Fatal("research protocol requires task_type")
	}
}

func corpus(id, split, repo, task string) Corpus {
	return Corpus{"schema_version": json.Number("2"), "corpus_id": id, "split": split,
		"source": map[string]any{"name": "n", "url": "u", "license": "MIT"},
		"cases": []any{map[string]any{"task": task, "task_type": "code2test", "case_id": id + "-1", "repository_id": repo, "language": "go",
			"label_source": "human", "labeler_count": json.Number("1"), "schema_version": json.Number("2"), "gold_files": []any{"a.go"},
			"gold_spans": []any{map[string]any{"path": "a.go", "start_line": json.Number("1"), "end_line": json.Number("2")}},
			"base_commit": strings.Repeat("a", 40), "content_manifest_sha256": strings.Repeat("b", 64), "source_event_time": "2026-01-02T03:04:05Z"}}}
}

func TestCorpusSummaryAndIntegrity(t *testing.T) {
	s, err := CorpusSummary(corpus("dev", "development", "org/r1", "find the retry loop in payments service"))
	if err != nil || s["publication_readiness"].(map[string]any)["ready"] != false {
		t.Fatalf("s=%v err=%v", s, err)
	}
	r, err := Integrity([]Corpus{
		corpus("dev", "development", "org/r1", "find the retry loop in payments service"),
		corpus("hold", "holdout", "org/r1", "find the retry loop in payments service"),
	})
	if err != nil || r["ready"] != false {
		t.Fatalf("r=%v err=%v", r, err)
	}
	codes := map[string]bool{}
	for _, b := range r["blockers"].([]map[string]any) {
		codes[b["code"].(string)] = true
	}
	for _, want := range []string{"cross_split_task_duplicate", "cross_split_source_instance_duplicate", "cross_split_repository_overlap"} {
		if !codes[want] {
			t.Errorf("missing blocker %s in %v", want, codes)
		}
	}
	bad := corpus("x", "dev", "r", "t")
	delete(bad["source"].(map[string]any), "license")
	if _, err := CorpusSummary(bad); err == nil {
		t.Fatal("source license is required")
	}
}
