package bench

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
)

// The test binary doubles as an intervention runner (BENCH_RUNNER selects behaviour).
func TestMain(m *testing.M) {
	switch os.Getenv("BENCH_RUNNER") {
	case "ok":
		fmt.Print(`{"success":true,"trajectory_events":[{"kind":"explored","file":"a.go","step":1},{"kind":"utilized","file":"a.go","step":2}]}`)
		os.Exit(0)
	case "garbage":
		fmt.Print("not json")
		os.Exit(0)
	case "exit":
		os.Exit(3)
	case "sleep":
		time.Sleep(time.Hour)
	case "badevents":
		fmt.Print(`{"trajectory_events":"nope"}`)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestProfileConfigs(t *testing.T) {
	base := config.Default()
	c, _ := AlgorithmConfig(base, "rrf_mmr_050")
	if r, _, l, _ := c.Context.Experiments.Policy(); r != "rrf_mmr" || l != .5 {
		t.Fatalf("ranker=%s lambda=%v", r, l)
	}
	if c, _ := AlgorithmConfig(base, "selector_off"); c.Context.Selector.Enabled {
		t.Fatal("selector_off")
	}
	if c, _ := AlgorithmConfig(base, "no_early_stop"); !c.Context.Experiments.DisableEarlySufficiencyGate {
		t.Fatal("no_early_stop")
	}
	if base.Context.Experiments != nil {
		t.Fatal("profiles must not mutate the base config")
	}
	a, _ := AblationConfig(base, "base_only")
	if a.Context.Semantic.Mode != "off" || a.Context.CRG.Mode != "off" || a.Context.SCIP.Mode != "off" {
		t.Fatalf("%+v", a.Context)
	}
	if _, err := AlgorithmConfig(base, "nope"); err == nil {
		t.Fatal("unknown profile accepted")
	}
}

func TestCalibrate(t *testing.T) {
	rows := []any{}
	for i, s := range []float64{.9, .8, .85, .7, .95, .75} {
		rows = append(rows, map[string]any{"task": fmt.Sprint("p", i), "control_type": "positive", "retrieval_sufficiency_score": s})
	}
	for i, s := range []float64{.2, .3, .4, .1} {
		rows = append(rows, map[string]any{"task": fmt.Sprint("n", i), "control_type": "natural_no_gold", "retrieval_sufficiency_score": s})
	}
	r, err := Calibrate(map[string]any{"cases": rows}, .7, 5, 1)
	if err != nil {
		t.Fatal(err)
	}
	th := r["threshold"].(float64)
	if th <= .4 || th > .7 || r["status"] != "advisory_only" {
		t.Fatalf("threshold %v must separate controls from positives: %v", th, r)
	}
	if _, err := Calibrate(map[string]any{"cases": rows[:6]}, .7, 5, 1); err == nil {
		t.Fatal("calibration without controls must fail")
	}
}

func TestRunInterventionFailureKinds(t *testing.T) {
	root := t.TempDir()
	iv := map[string]any{"task": "t", "repository_path": ".", "seed_mode": "retrieval", "seed_files": []string{"b.go"}, "gold_files": []string{"a.go"}}
	ro := func(role string, timeout time.Duration) RunnerOptions {
		t.Setenv("BENCH_RUNNER", role)
		return RunnerOptions{Command: []string{os.Args[0]}, Timeout: timeout, MaxOutput: 1 << 20, EnvAllow: []string{"BENCH_RUNNER"}}
	}
	if r := RunIntervention(context.Background(), root, iv, ro("ok", 30*time.Second)); r["status"] != "ok" || r["success"] != true {
		t.Fatalf("%v", r)
	} else if tr := r["trajectory"].(map[string]any); tr["utilization_recall"] != 1.0 || tr["seed_gold_recall"] != 0.0 {
		t.Fatalf("trajectory=%v", tr)
	}
	for role, want := range map[string]string{"garbage": "invalid_output", "exit": "exit_error", "badevents": "invalid_output"} {
		if r := RunIntervention(context.Background(), root, iv, ro(role, 30*time.Second)); r["status"] != want {
			t.Errorf("%s: %v", role, r)
		}
	}
	if r := RunIntervention(context.Background(), root, iv, ro("sleep", 500*time.Millisecond)); r["status"] != "timeout" {
		t.Fatalf("%v", r)
	}
	bad := map[string]any{"repository_path": "../escape"}
	if r := RunIntervention(context.Background(), root, bad, ro("ok", time.Second)); !strings.Contains(fmt.Sprint(r["error"]), "inside") {
		t.Fatalf("escaping repository_path must be refused: %v", r)
	}
}
