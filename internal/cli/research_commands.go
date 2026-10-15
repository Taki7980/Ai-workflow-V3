package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/bench"
)

func loadReport(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return nil, fmt.Errorf("%s: expected a JSON object report", path)
	}
	return m, nil
}

// benchFlags are the flags shared by commands that run the benchmark.
type benchFlags struct {
	tasks, output    *string
	research, frozen *bool
	profiles         stringList
}

func newBenchFlags(fs *flag.FlagSet) *benchFlags {
	b := &benchFlags{
		tasks:    fs.String("tasks", "", "benchmark task file (required)"),
		output:   fs.String("output", "", "write the report to this file"),
		research: fs.Bool("research-protocol", false, "require research protocol fields"),
		frozen:   fs.Bool("require-frozen-snapshot", false, "require frozen-clean snapshots"),
	}
	return b
}

// researchCmd dispatches benchmark-ablate|algorithms|intervene|statistics|calibrate|policy-advisor.
func researchCmd(name, root string, args []string, out, errOut io.Writer) int {
	fs := newFlags(name, errOut)
	fail := func(err error) int {
		fmt.Fprintln(errOut, err)
		return 1
	}
	ctx := context.Background()
	switch name {
	case "benchmark-ablate", "benchmark-algorithms", "benchmark-intervene":
		b := newBenchFlags(fs)
		fs.Var(&b.profiles, "profile", "profile to run (repeatable; default all)")
		var modes, runnerArgs, runnerEnv stringList
		fs.Var(&modes, "mode", "seed mode (repeatable; default all)")
		seedK := fs.Int("seed-k", 5, "seed files per intervention")
		runner := fs.String("runner-command", "", "optional executable implementing the JSON intervention runner protocol")
		fs.Var(&runnerArgs, "runner-arg", "argument passed to the runner (repeatable)")
		runnerTimeout := fs.Float64("runner-timeout", 120, "per-intervention runner timeout in seconds")
		runnerMax := fs.Int64("runner-max-output-bytes", 4<<20, "maximum accepted runner stdout bytes")
		fs.Var(&runnerEnv, "runner-env", "environment variable name exposed to the runner (repeatable)")
		if pos, err := parseInterspersed(fs, args); err != nil || len(pos) != 0 || *b.tasks == "" {
			fmt.Fprintf(errOut, "%s requires --tasks FILE\n", name)
			return 2
		}
		cases, err := bench.LoadTasks(*b.tasks, *b.research)
		if err != nil {
			return fail(err)
		}
		o := bench.Options{Research: *b.research, RequireFrozen: *b.frozen}
		var r map[string]any
		switch name {
		case "benchmark-ablate":
			r, err = bench.Suite(ctx, root, cases, o, "ablate", b.profiles)
		case "benchmark-algorithms":
			r, err = bench.Suite(ctx, root, cases, o, "algorithms", b.profiles)
		default:
			if *runnerTimeout <= 0 || *runnerMax <= 0 {
				fmt.Fprintln(errOut, "--runner-timeout and --runner-max-output-bytes must be positive")
				return 2
			}
			r, err = bench.InterventionManifest(ctx, root, cases, o, modes, *seedK)
			if err == nil && *runner != "" {
				r = bench.RunInterventions(ctx, root, r, bench.RunnerOptions{Command: append([]string{*runner}, runnerArgs...),
					Timeout: time.Duration(*runnerTimeout * float64(time.Second)), MaxOutput: *runnerMax, EnvAllow: runnerEnv})
			}
		}
		if err != nil {
			return fail(err)
		}
		if err := emit(out, *b.output, r); err != nil {
			return fail(err)
		}
		return 0
	}

	input := fs.String("input", "", "input report JSON (required)")
	output := fs.String("output", "", "write the result to this file")
	confidence := fs.Float64("confidence", .95, "confidence level")
	resamples := fs.Int("resamples", 5000, "bootstrap resamples")
	seed := fs.Int64("seed", 20260911, "bootstrap random seed")
	var stratify stringList
	fs.Var(&stratify, "stratify", "result field to analyze separately (repeatable)")
	fraction := fs.Float64("calibration-fraction", .7, "calibration split fraction")
	fa := fs.Float64("false-accept-cost", 5, "cost of accepting insufficient evidence")
	fr := fs.Float64("false-reject-cost", 1, "cost of rejecting sufficient evidence")
	contextField := fs.String("context-field", "task_type", "case field defining the policy context")
	minSamples := fs.Int("minimum-samples", 10, "minimum paired samples per context")
	margin := fs.Float64("safety-margin", 0, "required lower-bound improvement")
	tokenPenalty := fs.Float64("token-penalty", .05, "utility penalty per unit of budget utilization")
	latencyPenalty := fs.Float64("latency-penalty", .01, "utility penalty per second of latency")
	if pos, err := parseInterspersed(fs, args); err != nil || len(pos) != 0 || *input == "" {
		fmt.Fprintf(errOut, "%s requires --input FILE\n", name)
		return 2
	}
	report, err := loadReport(*input)
	if err != nil {
		return fail(err)
	}
	var r map[string]any
	switch name {
	case "benchmark-statistics":
		r, err = bench.SeedStatistics(report, *confidence, *resamples, *seed, stratify)
	case "benchmark-calibrate":
		r, err = bench.Calibrate(report, *fraction, *fa, *fr)
	case "benchmark-policy-advisor":
		r, err = bench.PolicyAdvisor(report, bench.AdvisorOptions{ContextField: *contextField, MinimumSamples: *minSamples, Confidence: *confidence,
			Resamples: *resamples, Seed: *seed, SafetyMargin: *margin, TokenPenalty: *tokenPenalty, LatencyPenalty: *latencyPenalty})
	}
	if err != nil {
		return fail(err)
	}
	if err := emit(out, *output, r); err != nil {
		return fail(err)
	}
	return 0
}
