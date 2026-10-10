package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/brief"
	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/handoff"
	"github.com/Taki7980/ai-workflow-v3/internal/memory"
	"github.com/Taki7980/ai-workflow-v3/internal/verify"
)

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// parseInterspersed parses flags that may appear before or after positional
// arguments (e.g. `brief "task" --format prompt`) and returns the positionals.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func newFlags(name string, errOut io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(errOut)
	return fs
}

// briefCmd implements `brief TASK [--format json|markdown|prompt] ...`.
func briefCmd(root string, args []string, out, errOut io.Writer) int {
	fs := newFlags("brief", errOut)
	format := fs.String("format", "json", "output format: json, markdown, or prompt")
	symbol := fs.String("symbol", "", "exact symbol anchor")
	endpoint := fs.String("endpoint", "", "endpoint anchor")
	writeHandoff := fs.Bool("write-handoff", false, "write ai-workspace/handoff/HANDOFF.md for non-answer lanes")
	trace := fs.Bool("trace", false, "write a trace and run journal even for Answer lanes")
	var changed stringList
	fs.Var(&changed, "changed-file", "changed file (repeatable); default: detected from git")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprintln(errOut, "brief requires one task argument")
		return 2
	}
	switch *format {
	case "json", "markdown", "prompt":
	default:
		fmt.Fprintf(errOut, "invalid format %q (want json, markdown, or prompt)\n", *format)
		return 2
	}
	p, err := brief.Build(context.Background(), root, pos[0], brief.Options{Symbol: *symbol, Endpoint: *endpoint, ChangedFiles: changed, WriteHandoff: *writeHandoff, Trace: *trace})
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	text, err := brief.Format(p, *format)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	fmt.Fprintln(out, text)
	return 0
}

// contextCmd implements V2 `context TASK`: the brief retrieval pipeline
// without persistence, emitted as lane/risk/retrieval/items.
func contextCmd(root string, args []string, out, errOut io.Writer) int {
	fs := newFlags("context", errOut)
	trace := fs.Bool("trace", false, "write a trace and run journal")
	symbol := fs.String("symbol", "", "exact symbol anchor")
	endpoint := fs.String("endpoint", "", "endpoint anchor")
	var changed stringList
	fs.Var(&changed, "changed-file", "changed file (repeatable); default: detected from git")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprintln(errOut, "context requires one task argument")
		return 2
	}
	p, err := brief.Build(context.Background(), root, pos[0], brief.Options{Symbol: *symbol, Endpoint: *endpoint, ChangedFiles: changed, ReadOnly: true, Trace: *trace})
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	printJSON(out, map[string]any{
		"lane": p.Lane, "risk": p.Risk, "confidence": p.Confidence,
		"budget": p.Budget.EstimatedContextTokens, "retrieval": p.Retrieval,
		"items": p.Context, "estimated_tokens": p.EstimatedContextTokensUsed,
	})
	return 0
}

// handoffCmd validates the active handoff; exit 1 when invalid.
func handoffCmd(root string, args []string, out, errOut io.Writer) int {
	if len(args) > 0 && args[0] == "validate" { // V2 spelling: `handoff validate`
		args = args[1:]
	}
	if len(args) != 0 {
		fmt.Fprintln(errOut, "handoff takes no arguments")
		return 2
	}
	cfg, err := config.Load(root)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	errs := handoff.Validate(root, cfg.Handoff.MaxLines)
	printJSON(out, map[string]any{"valid": len(errs) == 0, "errors": errs})
	if len(errs) > 0 {
		return 1
	}
	return 0
}

// verifyCmd runs focused checks plus handoff validation.
func verifyCmd(root string, args []string, out, errOut io.Writer) int {
	fs := newFlags("verify", errOut)
	var checks stringList
	fs.Var(&checks, "check", "check command, run without a shell (repeatable)")
	strict := fs.Bool("strict", false, "exit 1 unless every check passes and the handoff is valid")
	if pos, err := parseInterspersed(fs, args); err != nil || len(pos) != 0 {
		return 2
	}
	cfg, err := config.Load(root)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	res := verify.Verify(context.Background(), root, checks, cfg.Handoff.MaxLines)
	printJSON(out, res)
	if *strict && !res.OK {
		return 1
	}
	return 0
}

// compressCmd bounds noisy text from --file or stdin.
func compressCmd(args []string, in io.Reader, out, errOut io.Writer) int {
	fs := newFlags("compress", errOut)
	file := fs.String("file", "", "input file (default stdin)")
	maxLines := fs.Int("max-lines", 80, "maximum output lines")
	maxChars := fs.Int("max-chars", 12000, "maximum output characters")
	preferRTK := fs.Bool("prefer-rtk", false, "pipe through rtk when installed")
	filter := fs.String("filter", "", "rtk filter name")
	if pos, err := parseInterspersed(fs, args); err != nil || len(pos) != 0 {
		return 2
	}
	if *maxLines < 1 || *maxChars < 1 {
		fmt.Fprintln(errOut, "--max-lines and --max-chars must be positive")
		return 2
	}
	var b []byte
	var err error
	if *file != "" {
		b, err = os.ReadFile(*file)
	} else {
		b, err = io.ReadAll(in)
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	text := strings.ToValidUTF8(string(b), "�")
	if *preferRTK {
		fmt.Fprint(out, verify.CompressPreferRTK(text, *maxLines, *maxChars, *filter))
	} else {
		fmt.Fprint(out, verify.Compress(text, *maxLines, *maxChars))
	}
	return 0
}

// memoryCmd dispatches `memory add|search|list|prune|export|import`.
func memoryCmd(root string, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "memory requires add, search, list, prune, export, or import")
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(errOut, err)
		return 1
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "add":
		fs := newFlags("memory add", errOut)
		typ := fs.String("type", "", "decision|incident|verified-fix|architecture|pattern|optimization|constraint")
		keywords := fs.String("keywords", "", "search keywords")
		summary := fs.String("summary", "", "reusable fact")
		evidence := fs.String("evidence", "", "supporting evidence")
		confidence := fs.Float64("confidence", 0.8, "0..1")
		var files stringList
		fs.Var(&files, "file", "related workspace file (repeatable)")
		if pos, err := parseInterspersed(fs, args); err != nil || len(pos) != 0 {
			return 2
		}
		if *typ == "" || *keywords == "" || *summary == "" {
			fmt.Fprintln(errOut, "memory add requires --type, --keywords, and --summary")
			return 2
		}
		rec, err := memory.Add(root, *typ, *keywords, *summary, *evidence, files, *confidence)
		if err != nil {
			return fail(err)
		}
		printJSON(out, rec)
	case "search":
		fs := newFlags("memory search", errOut)
		limit := fs.Int("limit", 0, "maximum results (default memory.max_results)")
		pos, err := parseInterspersed(fs, args)
		if err != nil || len(pos) != 1 {
			fmt.Fprintln(errOut, "memory search requires one query argument")
			return 2
		}
		cfg, err := config.Load(root)
		if err != nil {
			return fail(err)
		}
		if *limit <= 0 {
			*limit = cfg.Memory.MaxResults
		}
		hits, err := memory.Search(root, pos[0], *limit, cfg.Memory.MinimumConfidence, false)
		if err != nil {
			return fail(err)
		}
		printJSON(out, hits)
	case "list":
		entries, err := memory.List(root)
		if err != nil {
			return fail(err)
		}
		printJSON(out, entries)
	case "prune":
		kept, pruned, err := memory.Prune(root)
		if err != nil {
			return fail(err)
		}
		printJSON(out, map[string]int{"kept": kept, "pruned": pruned})
	case "export", "import":
		if len(args) != 1 {
			fmt.Fprintf(errOut, "memory %s requires one path argument\n", sub)
			return 2
		}
		if sub == "export" {
			n, err := memory.Export(root, args[0])
			if err != nil {
				return fail(err)
			}
			printJSON(out, map[string]any{"format": "jsonl", "output": args[0], "records": n})
			return 0
		}
		imported, skipped, invalid, err := memory.Import(root, args[0])
		if err != nil {
			return fail(err)
		}
		printJSON(out, map[string]int{"imported": imported, "skipped": skipped, "invalid": invalid})
	default:
		fmt.Fprintf(errOut, "unknown memory command %q\n", sub)
		return 2
	}
	return 0
}
