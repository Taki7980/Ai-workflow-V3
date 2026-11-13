package indexer

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/procx"
)

// Change-history prior: files modified recently and often are the ones the
// next change most likely touches (Sisman & Kak, "Incorporating Version
// Histories in Information Retrieval Based Bug Localization", MSR 2012; the
// time decay gave most of their gain). Tuned on held-out commits of the V2
// codebase, sindresorhus/ky, spf13/cobra and pallets/click.
const (
	historyCommits  = 300 // at a 30-commit half-life older commits weigh under 0.1%
	historyHalfLife = 30.0
	historyMaxFiles = 30 // larger commits are sweeps (renames, formatting), not maintenance signal
)

// fileHistory returns a time-decayed modification count per file of
// repoRoot's recent commits, keeping only files in keep. Each commit adds
// 0.5^(age/historyHalfLife), age counted in commits from HEAD. The prior is
// advisory: a missing git, a non-Git directory or a timeout yields nil.
func fileHistory(ctx context.Context, repoRoot string, keep map[string]FileState) map[string]float64 {
	// --relative keeps paths repository-relative even when repoRoot is a
	// subdirectory of an outer Git repository, and drops changes outside it.
	argv := []string{"git", "-c", "core.quotePath=off", "log", "--no-merges", "--relative", "--name-only", "--format=%x1e", "-n", strconv.Itoa(historyCommits)}
	raw, err := procx.Output(ctx, procx.Cmd{Argv: argv, Dir: repoRoot, Env: procx.InheritEnv(), Timeout: 5 * time.Second, MaxStdout: 16 << 20})
	if err != nil {
		return nil
	}
	out := map[string]float64{}
	blocks := strings.Split(string(raw), "\x1e")
	if len(blocks) > 0 {
		blocks = blocks[1:] // text before the first commit marker
	}
	for age, block := range blocks {
		files := []string{}
		for _, l := range strings.Split(block, "\n") {
			if l != "" {
				files = append(files, l)
			}
		}
		if len(files) > historyMaxFiles {
			continue
		}
		w := math.Pow(0.5, float64(age)/historyHalfLife)
		for _, f := range files {
			if _, ok := keep[f]; ok {
				out[f] += w
			}
		}
	}
	for f, v := range out {
		if out[f] = math.Round(v*1000) / 1000; out[f] == 0 {
			delete(out, f)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
