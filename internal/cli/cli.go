package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/doctor"
	"github.com/Taki7980/ai-workflow-v3/internal/indexer"
	"github.com/Taki7980/ai-workflow-v3/internal/provider"
	"github.com/Taki7980/ai-workflow-v3/internal/retrieval"
	"github.com/Taki7980/ai-workflow-v3/internal/routing"
	setuppkg "github.com/Taki7980/ai-workflow-v3/internal/setup"
	"github.com/Taki7980/ai-workflow-v3/internal/structural"
	"github.com/Taki7980/ai-workflow-v3/internal/version"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

// Run parses the CLI arguments, dispatches to the matching subcommand, and
// returns the process exit code, writing output and errors to out and errOut.
func Run(args []string, out, errOut io.Writer) int {
	return RunWithStdin(args, os.Stdin, out, errOut)
}

// RunWithStdin is Run with an explicit stdin for commands that read input.
func RunWithStdin(args []string, in io.Reader, out, errOut io.Writer) int {
	// Internal rlimit exec wrapper used inside the sandbox; its argv is the
	// provider's, so it must bypass --root extraction.
	if len(args) > 0 && args[0] == provider.ExecWrapperCommand {
		return provider.ExecWithLimits(args[1:], errOut)
	}
	root, args, err := extractRoot(args)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	if len(args) == 0 {
		usage(out)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		usage(out)
		return 0
	case "--version", "version":
		fmt.Fprintln(out, version.Version)
		return 0
	case "setup":
		return setup(root, args[1:], out, errOut)
	case "bootstrap", "init":
		return projectCmd(args[0], root, args[1:], out, errOut)
	case "search":
		return searchCmd(root, args[1:], out, errOut)
	case "route":
		return route(root, args[1:], out, errOut)
	case "repos":
		return repos(root, args[1:], out, errOut)
	case "index":
		return index(root, args[1:], out, errOut)
	case "context":
		return contextCmd(root, args[1:], out, errOut)
	case "doctor":
		return doctorCmd(root, args[1:], out, errOut)
	case "brief":
		return briefCmd(root, args[1:], out, errOut)
	case "handoff":
		return handoffCmd(root, args[1:], out, errOut)
	case "verify":
		return verifyCmd(root, args[1:], out, errOut)
	case "compress":
		return compressCmd(args[1:], in, out, errOut)
	case "memory":
		return memoryCmd(root, args[1:], out, errOut)
	case "stats":
		return statsCmd(root, args[1:], out, errOut)
	case "replay":
		return replayCmd(root, args[1:], out, errOut)
	case "run":
		return runCmd(root, args[1:], out, errOut)
	case "benchmark":
		return benchmarkCmd(root, args[1:], out, errOut)
	case "benchmark-corpus":
		return benchmarkCorpusCmd(root, args[1:], out, errOut)
	case "benchmark-ablate", "benchmark-algorithms", "benchmark-intervene", "benchmark-statistics", "benchmark-calibrate", "benchmark-policy-advisor":
		return researchCmd(args[0], root, args[1:], out, errOut)
	case "learning":
		return learningCmd(root, args[1:], out, errOut)
	case "authorize":
		return authorizeCmd(root, args[1:], in, out, errOut)
	case "graph", "scip":
		return structuralCmd(args[0], root, args[1:], out, errOut)
	default:
		fmt.Fprintf(errOut, "unknown command %q\n", args[0])
		usage(errOut)
		return 2
	}
}

// extractRoot pulls the "--root" flag and its value out of args, returning
// the resolved absolute root path along with the remaining arguments.
func extractRoot(args []string) (string, []string, error) {
	root := "."
	out := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--root" {
			if i+1 >= len(args) {
				return "", nil, errors.New("--root requires a value")
			}
			root = args[i+1]
			i++
			continue
		}
		out = append(out, args[i])
	}
	abs, err := filepath.Abs(root)
	return abs, out, err
}

// usage writes the top-level CLI usage summary to w.
func usage(w io.Writer) {
	fmt.Fprintln(w, `AI Workflow V3

Usage: ai-workflow [--root PATH] <command>

Commands:
  setup                      connect the project: config, repos, excludes, indexes, CRG (safe to rerun)
  bootstrap --project-name N strict scaffold; refuses existing control-plane files
  init --project-name N      stamp the project name into agent rules and rebuild indexes
  brief TASK                 emit the agent brief (--format json|markdown|prompt)
  route TASK                 classify lane, risk and retrieval intent
  context TASK               gather bounded evidence without writing state (--symbol, --endpoint, --changed-file)
  search QUERY               search indexed symbols
  handoff [validate]         validate ai-workspace/handoff/HANDOFF.md
  verify --check CMD         run checks without a shell (+ handoff validation)
  compress                   bound noisy output from stdin or --file
  memory SUBCOMMAND          add|search|list|prune|export|import durable memory
  graph sync|status          build/refresh code-review-graph state (--repo, --timeout)
  scip sync|status           build/refresh SCIP indexes (--repo, --language, --timeout)
  stats [--recommend]        summarize local retrieval traces (advisory feedback only)
  replay RUN_ID [--strict]   verify and reconstruct a recorded run without executing anything
  run inspect|verify RUN_ID  print a run journal / check it against the current workspace
  benchmark --tasks FILE     score routing + context on gold-labelled cases (--research-protocol, --require-frozen-snapshot)
  benchmark-corpus validate|integrity|snapshot   corpus-v2 checks (--input, --require-ready, --fail-on-signals)
  benchmark-ablate|benchmark-algorithms --tasks F [--profile P]   compare provider families / ranking algorithms
  benchmark-intervene --tasks F [--mode M] [--runner-command EXE]  seed interventions (optionally run an agent runner)
  benchmark-statistics|benchmark-calibrate|benchmark-policy-advisor --input REPORT   advisory statistics
  learning status|record-outcome|evaluate|contextual-policy|create-manifest|verify-manifest|shadow-evaluate
                             opt-in safe retrieval learning (context.learning.mode: off|observe|explore)
  authorize RUN_ID           gate a model-proposed action (JSON on stdin) against the run's capability policy
  repos list|refresh|include|exclude  manage the repository registry
  index                      rebuild indexes (--mode auto|incremental|full, --incremental)
  doctor [--strict]          validate the environment
  version                    print the version`)
}

// printJSON marshals v as indented JSON and writes it to w, followed by a newline.
func printJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// setup implements V2 `setup`: connect the project, discover repositories,
// install Git-local excludes, build indexes and sync Code Review Graph.
func setup(root string, args []string, out, errOut io.Writer) int {
	fs := newFlags("setup", errOut)
	name := fs.String("project-name", "", "display name (default: directory name)")
	jsonOut := fs.Bool("json", false, "machine-readable result")
	create := fs.Bool("create", false, "create the project root when it does not exist")
	legacy := fs.Bool("legacy-root-files", false, "also create root AGENTS.md and .ai/PROJECT")
	noDiscover := fs.Bool("no-discover-repos", false, "skip local Git repository discovery")
	depth := fs.Int("discover-depth", 8, "maximum nested repository depth")
	noCRG := fs.Bool("no-crg-sync", false, "skip Code Review Graph build/update")
	noIndex := fs.Bool("no-index", false, "skip index build")
	fullIndex := fs.Bool("full-index", false, "force a full index rebuild")
	if pos, err := parseInterspersed(fs, args); err != nil || len(pos) != 0 {
		return 2
	}
	if *noIndex && *fullIndex {
		fmt.Fprintln(errOut, "--no-index and --full-index are mutually exclusive")
		return 2
	}
	mode := setuppkg.IndexAuto
	if *noIndex {
		mode = setuppkg.IndexNone
	} else if *fullIndex {
		mode = setuppkg.IndexFull
	}
	r, err := setuppkg.Run(context.Background(), root, setuppkg.Options{
		ProjectName: *name, Create: *create, Index: mode, LegacyRootFiles: *legacy,
		Discover: !*noDiscover, DiscoverDepth: *depth, SyncCRG: !*noCRG,
	})
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	if *jsonOut {
		printJSON(out, r)
		return 0
	}
	fmt.Fprintf(out, "AI Workflow ready: %s\nRoot: %s\n", r.Project, r.Root)
	if len(r.Created) > 0 {
		fmt.Fprintln(out, "Created: "+strings.Join(r.Created, ", "))
	}
	if len(r.Preserved) > 0 {
		fmt.Fprintln(out, "Preserved: "+strings.Join(r.Preserved, ", "))
	}
	if s := r.Registry.Summary; s != nil {
		fmt.Fprintf(out, "Repositories: %d discovered; %d active\n", s.Discovered, s.Accepted)
	}
	if crg, ok := r.CodeReview.(structural.SyncReport); ok && crg.Installed {
		fmt.Fprintf(out, "Code Review Graph: %d/%d repositories ready under ai-workspace/code-review-graph\n", crg.Ready, crg.Attempted)
	}
	fmt.Fprintln(out, "Next: "+r.Next)
	return 0
}

// projectCmd implements `bootstrap` and `init`, which both require --project-name.
func projectCmd(kind, root string, args []string, out, errOut io.Writer) int {
	fs := newFlags(kind, errOut)
	name := fs.String("project-name", "", "project name (required)")
	if pos, err := parseInterspersed(fs, args); err != nil || len(pos) != 0 {
		return 2
	}
	if strings.TrimSpace(*name) == "" {
		fmt.Fprintf(errOut, "%s requires --project-name\n", kind)
		return 2
	}
	run := setuppkg.Bootstrap
	if kind == "init" {
		run = setuppkg.Init
	}
	r, err := run(context.Background(), root, *name)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	printJSON(out, r)
	return 0
}

// route implements the "route" subcommand: it classifies the given task and
// prints the routing decision together with the resulting retrieval plan.
func route(root string, args []string, out, errOut io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(errOut, "route requires one task argument")
		return 2
	}
	cfg, err := config.Load(root)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	decision := routing.Classify(args[0], cfg)
	plan := routing.PlanRetrieval(args[0], decision)
	printJSON(out, map[string]any{"decision": decision, "retrieval": plan})
	return 0
}

// repos implements `repos list|refresh|include|exclude` (V2 parity).
func repos(root string, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "repos requires list, refresh, include, or exclude")
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(errOut, err)
		return 1
	}
	switch args[0] {
	case "list":
		reg, err := workspace.Load(root)
		if err != nil {
			return fail(err)
		}
		printJSON(out, workspace.Summarize(root, reg.Repositories))
	case "refresh":
		fs := newFlags("repos refresh", errOut)
		depth := fs.Int("discover-depth", -1, "override configured discovery depth")
		if pos, err := parseInterspersed(fs, args[1:]); err != nil || len(pos) != 0 {
			return 2
		}
		cfg, err := config.Load(root)
		if err != nil {
			return fail(err)
		}
		if *depth < 0 {
			*depth = cfg.Workspace.Discovery.MaxDepth
		}
		// refresh never auto-includes: new repositories wait for explicit review.
		r, err := workspace.Refresh(context.Background(), root, *depth, false)
		if err != nil {
			return fail(err)
		}
		printJSON(out, workspace.Summarize(root, r))
	case "include", "exclude":
		if len(args) != 2 {
			fmt.Fprintf(errOut, "repos %s requires one repository selector\n", args[0])
			return 2
		}
		r, changed, err := workspace.SetIncluded(root, args[1], args[0] == "include")
		if err != nil {
			return fail(err)
		}
		action := args[0] + "d"
		printJSON(out, struct {
			workspace.Summary
			Changed any `json:"changed"`
		}{workspace.Summarize(root, r), struct {
			workspace.Repository
			Action string `json:"action"`
		}{changed, action}})
	default:
		fmt.Fprintln(errOut, "repos requires list, refresh, include, or exclude")
		return 2
	}
	return 0
}

// index implements the "index" subcommand with explicit freshness modes and
// returns per-repository build statistics as JSON.
func index(root string, args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	fs.SetOutput(errOut)
	modeValue := fs.String("mode", string(indexer.BuildAuto), "index mode: auto, incremental, or full")
	incremental := fs.Bool("incremental", false, "alias for --mode incremental (V2)")
	// V3 always hashes every source file, so V2's strict-hash flag is satisfied by default.
	fs.Bool("strict-hash", false, "accepted for V2 compatibility; hashing is always strict")
	fs.Bool("verify-hashes", false, "alias of --strict-hash")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *incremental {
		*modeValue = string(indexer.BuildIncremental)
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(errOut, "index does not accept positional arguments")
		return 2
	}
	mode := indexer.BuildMode(*modeValue)
	if mode != indexer.BuildAuto && mode != indexer.BuildIncremental && mode != indexer.BuildFull {
		fmt.Fprintf(errOut, "invalid index mode %q (want auto, incremental, or full)\n", *modeValue)
		return 2
	}
	reg, err := workspace.Load(root)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	built, err := indexer.BuildWorkspaceWithMode(context.Background(), root, reg, mode)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	summary := make(map[string]indexer.BuildStats, len(built))
	for rel, result := range built {
		summary[rel] = result.Stats
	}
	printJSON(out, summary)
	return 0
}

// searchCmd implements the "search" subcommand: it loads the indexes for
// all included repositories and prints the selected symbol hits for the query.
func searchCmd(root string, args []string, out, errOut io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(errOut, "search requires one query argument")
		return 2
	}
	cfg, err := config.Load(root)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	reg, err := workspace.Load(root)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	indexes := map[string]indexer.Index{}
	for _, repo := range reg.Repositories {
		if !repo.Included {
			continue
		}
		idx, e := indexer.Load(root, repo)
		if e == nil {
			indexes[repo.RelativePath] = idx
		}
	}
	hits, err := selectContextHits(args[0], indexes, cfg)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	printJSON(out, hits)
	return 0
}

// estimateFileTokens estimates the token count for a file based on its byte
// size, returning at least 1 for empty or unknown-size files.
func estimateFileTokens(state indexer.FileState) int {
	if state.Size <= 0 {
		return 1
	}
	return int((state.Size + 3) / 4)
}

// selectContextHits searches the given indexes for query and, when the
// selector is enabled, applies MMR-based selection to fit within the
// configured token budget; otherwise it returns the raw search hits.
func selectContextHits(query string, indexes map[string]indexer.Index, cfg config.Config) ([]indexer.Hit, error) {
	if !cfg.Context.Selector.Enabled {
		return indexer.Search(query, indexes, cfg.Context.MaxResultsPerSource), nil
	}

	limit := cfg.Context.Selector.MaxSelectorCandidates
	hits := indexer.Search(query, indexes, limit)
	candidates := make([]retrieval.Candidate[indexer.Hit], 0, len(hits))
	for _, hit := range hits {
		idx, ok := indexes[hit.Repository]
		if !ok {
			continue
		}
		state := idx.Files[hit.Path]
		candidates = append(candidates, retrieval.Candidate[indexer.Hit]{
			Key:             hit.Repository + "\x00" + hit.Path,
			Text:            hit.Symbol + " " + hit.Kind,
			Value:           hit,
			Relevance:       hit.Score,
			EstimatedTokens: estimateFileTokens(state),
		})
	}
	selected, err := retrieval.SelectMMR(candidates, retrieval.SelectorOptions{
		MaxTokens:         cfg.Budgets.Answer.EstimatedTokens,
		MaxCandidates:     limit,
		Lambda:            0.70,
		MandatoryRequired: cfg.Context.Selector.MandatoryStructuralEvidence,
	})
	if err != nil {
		return nil, err
	}
	out := make([]indexer.Hit, 0, len(selected.Items))
	for _, item := range selected.Items {
		out = append(out, item.Value)
	}
	return out, nil
}

// doctorCmd implements the "doctor" subcommand: it runs workspace health
// checks (optionally in strict mode) and prints the resulting report.
func doctorCmd(root string, args []string, out, errOut io.Writer) int {
	strict := false
	for _, a := range args {
		if a == "--strict" {
			strict = true
		} else {
			fmt.Fprintf(errOut, "unknown doctor option %s\n", a)
			return 2
		}
	}
	r := doctor.Run(context.Background(), root, strict)
	printJSON(out, r)
	if !r.OK {
		return 1
	}
	return 0
}
