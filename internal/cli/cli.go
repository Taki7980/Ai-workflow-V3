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

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/doctor"
	"github.com/Taki7980/ai-workflow-v3/internal/indexer"
	"github.com/Taki7980/ai-workflow-v3/internal/retrieval"
	"github.com/Taki7980/ai-workflow-v3/internal/routing"
	"github.com/Taki7980/ai-workflow-v3/internal/storage"
	"github.com/Taki7980/ai-workflow-v3/internal/version"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

// Run parses the CLI arguments, dispatches to the matching subcommand, and
// returns the process exit code, writing output and errors to out and errOut.
func Run(args []string, out, errOut io.Writer) int {
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
	case "--version", "version":
		fmt.Fprintln(out, version.Version)
		return 0
	case "setup":
		return setup(root, args[1:], out, errOut)
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
	fmt.Fprintln(w, "AI Workflow V3\n\nUsage: ai-workflow [--root PATH] <setup|route|repos|index|context|doctor|version>")
}

// printJSON marshals v as indented JSON and writes it to w, followed by a newline.
func printJSON(w io.Writer, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(w, string(b))
}

// setup implements the "setup" subcommand: it writes a default config if one
// does not already exist, discovers repositories under root, saves the
// workspace registry, and optionally builds indexes for the discovered repos.
func setup(root string, args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(errOut)
	depth := fs.Int("discover-depth", 8, "maximum nested repository depth")
	jsonOut := fs.Bool("json", false, "JSON output")
	noIndex := fs.Bool("no-index", false, "skip index build")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := os.MkdirAll(filepath.Join(root, "ai-workspace", "config"), 0o755); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	cfg := config.Default()
	cfg.Workspace.Discovery.MaxDepth = *depth
	if _, err := os.Stat(config.Path(root)); errors.Is(err, os.ErrNotExist) {
		if err := storage.WriteJSON(config.Path(root), config.DefaultDocument()); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
	} else if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	} else {
		loaded, err := config.Load(root)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		cfg = loaded
	}
	repos, err := workspace.Discover(context.Background(), root, *depth, cfg.Workspace.Discovery.AutoIncludeOnSetup)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	if err := workspace.Save(root, repos); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	indexes := 0
	if !*noIndex {
		reg := workspace.Registry{Version: workspace.RegistryVersion, ReviewRequired: true, Repositories: repos}
		built, err := indexer.BuildWorkspace(context.Background(), root, reg)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		indexes = len(built)
	}
	result := map[string]any{"root": root, "repositories": len(repos), "indexes": indexes, "config": config.Path(root), "registry": workspace.RegistryPath(root)}
	if *jsonOut {
		printJSON(out, result)
	} else {
		fmt.Fprintf(out, "setup complete: %d repos, %d indexes\n", len(repos), indexes)
	}
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

// repos implements the "repos" subcommand, supporting "list" to print the
// current workspace registry and "refresh" to rediscover and re-save it.
func repos(root string, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "repos requires list or refresh")
		return 2
	}
	switch args[0] {
	case "list":
		reg, err := workspace.Load(root)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		printJSON(out, reg)
		return 0
	case "refresh":
		cfg, err := config.Load(root)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		r, err := workspace.Discover(context.Background(), root, cfg.Workspace.Discovery.MaxDepth, cfg.Workspace.Discovery.AutoIncludeOnSetup)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		if err := workspace.Save(root, r); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		printJSON(out, r)
		return 0
	default:
		fmt.Fprintln(errOut, "repos requires list or refresh")
		return 2
	}
}

// index implements the "index" subcommand with explicit freshness modes and
// returns per-repository build statistics as JSON.
func index(root string, args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	fs.SetOutput(errOut)
	modeValue := fs.String("mode", string(indexer.BuildAuto), "index mode: auto, incremental, or full")
	if err := fs.Parse(args); err != nil {
		return 2
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

// contextCmd implements the "context" subcommand: it loads the indexes for
// all included repositories and prints the selected context hits for the query.
func contextCmd(root string, args []string, out, errOut io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(errOut, "context requires one query argument")
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
