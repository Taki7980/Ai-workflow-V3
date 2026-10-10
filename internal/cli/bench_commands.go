package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/Taki7980/ai-workflow-v3/internal/bench"
	"github.com/Taki7980/ai-workflow-v3/internal/storage"
)

// emit prints v or writes it atomically to output.
func emit(out io.Writer, output string, v any) error {
	if output == "" {
		printJSON(out, v)
		return nil
	}
	if err := storage.WriteJSON(output, v); err != nil {
		return err
	}
	printJSON(out, map[string]any{"output": output})
	return nil
}

// benchmarkCmd implements `benchmark --tasks FILE [--output F] [--research-protocol] [--require-frozen-snapshot]`.
func benchmarkCmd(root string, args []string, out, errOut io.Writer) int {
	fs := newFlags("benchmark", errOut)
	tasks := fs.String("tasks", "", "benchmark task JSON array or corpus-v2 document (required)")
	output := fs.String("output", "", "write the report to this file")
	research := fs.Bool("research-protocol", false, "require research task types, gold labels, frozen contents and control provenance")
	frozen := fs.Bool("require-frozen-snapshot", false, "fail unless base commit, clean worktree and content manifest all match")
	if pos, err := parseInterspersed(fs, args); err != nil || len(pos) != 0 || *tasks == "" {
		fmt.Fprintln(errOut, "benchmark requires --tasks FILE")
		return 2
	}
	cases, err := bench.LoadTasks(*tasks, *research)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	r, err := bench.Run(context.Background(), root, cases, bench.Options{Research: *research, RequireFrozen: *frozen})
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	if err := emit(out, *output, r); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

// benchmarkCorpusCmd implements `benchmark-corpus validate|integrity|snapshot`.
func benchmarkCorpusCmd(root string, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "benchmark-corpus requires validate, integrity, or snapshot")
		return 2
	}
	sub, args := args[0], args[1:]
	fs := newFlags("benchmark-corpus "+sub, errOut)
	var inputs stringList
	fs.Var(&inputs, "input", "corpus-v2 JSON file (repeatable for integrity)")
	requireReady := fs.Bool("require-ready", false, "exit 1 unless the corpus clears the research-scale floor")
	failOnSignals := fs.Bool("fail-on-signals", false, "also exit 1 for non-fatal leakage signals")
	repository := fs.String("repository", ".", "repository to snapshot")
	strict := fs.Bool("strict", false, "exit 1 unless the repository is clean and capturable")
	if pos, err := parseInterspersed(fs, args); err != nil || len(pos) != 0 {
		return 2
	}
	load := func(p string) (bench.Corpus, error) {
		v, err := bench.LoadJSON(p)
		if err != nil {
			return nil, err
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: benchmark corpus v2 must be a JSON object", p)
		}
		return bench.Corpus(m), nil
	}
	switch sub {
	case "validate":
		if len(inputs) != 1 {
			fmt.Fprintln(errOut, "validate requires exactly one --input")
			return 2
		}
		c, err := load(inputs[0])
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		s, err := bench.CorpusSummary(c)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		printJSON(out, s)
		if ready, _ := s["publication_readiness"].(map[string]any)["ready"].(bool); *requireReady && !ready {
			return 1
		}
	case "integrity":
		if len(inputs) == 0 {
			fmt.Fprintln(errOut, "integrity requires at least one --input")
			return 2
		}
		docs := []bench.Corpus{}
		for _, p := range inputs {
			c, err := load(p)
			if err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
			docs = append(docs, c)
		}
		r, err := bench.Integrity(docs)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		printJSON(out, r)
		signals, _ := r["leakage_signals"].([]map[string]any)
		if r["ready"] != true || (*failOnSignals && len(signals) > 0) {
			return 1
		}
	case "snapshot":
		repo := *repository
		if !filepath.IsAbs(repo) {
			repo = filepath.Join(root, repo)
		}
		s := bench.CaptureSnapshot(repo)
		s["repository"] = *repository
		printJSON(out, s)
		if *strict && (s["status"] != "captured" || s["worktree_clean"] != true) {
			return 1
		}
	default:
		fmt.Fprintf(errOut, "unknown benchmark-corpus command %q\n", sub)
		return 2
	}
	return 0
}
