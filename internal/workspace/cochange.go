package workspace

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/procx"
)

// Co-change thresholds. On held-out commits of spf13/cobra and sindresorhus/ky,
// one edited file plus these limits named a file the same commit also changed
// in 53-57% of suggestions, at one or two suggestions per edit.
const (
	coChangeCommits    = 1000 // commits touching a changed file, newest first
	coChangeMaxFiles   = 30   // larger commits are sweeps (renames, formatting), not couplings
	coChangeMinSupport = 2
	coChangeMinConf    = 0.3
)

// CoChange is a file that version history says usually changes together with
// a file already changed in the working tree (Zimmermann et al., "Mining
// Version Histories to Guide Software Changes").
type CoChange struct {
	Path string `json:"path"`
	With string `json:"changed_with"`
	// Support counts commits touching both files; Confidence is Support over
	// the commits touching With.
	Support    int     `json:"support"`
	Confidence float64 `json:"confidence"`
}

// CoChanged suggests up to limit existing files that history pairs with the
// changed files but that are not changed yet. It is advisory: a git failure
// or timeout yields no suggestions.
func CoChanged(ctx context.Context, root string, reg Registry, changed []string, limit int) []CoChange {
	groups := map[string][]string{} // repository path -> its changed files, repository-relative
	for _, p := range changed {
		if strings.HasPrefix(p, "ai-workspace/") { // this tool's own state couples with nothing; skipping it spares a git walk
			continue
		}
		repo := ""
		for _, r := range reg.Repositories {
			rel := strings.Trim(filepath.ToSlash(r.RelativePath), "/")
			if r.Included && rel != "." && len(rel) > len(repo) && strings.HasPrefix(p, rel+"/") {
				repo = rel
			}
		}
		if repo == "" {
			groups["."] = append(groups["."], p)
		} else {
			groups[repo] = append(groups[repo], strings.TrimPrefix(p, repo+"/"))
		}
	}
	out := []CoChange{}
	for repo, files := range groups {
		prefix := ""
		if repo != "." {
			prefix = repo + "/"
		}
		for _, c := range coChangedRepo(ctx, filepath.Join(root, filepath.FromSlash(repo)), files) {
			c.Path, c.With = prefix+c.Path, prefix+c.With
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return ranksAbove(out[i], out[j]) })
	return out[:min(len(out), limit)]
}

func coChangedRepo(ctx context.Context, dir string, changed []string) []CoChange {
	if len(changed) > coChangeMaxFiles { // a sweep-sized change has no single coupling to hint at
		return nil
	}
	// The pathspec walks only commits touching a changed file; --full-diff
	// still lists every file those commits changed.
	argv := []string{"git", "-c", "core.quotePath=off", "log", "--no-merges", "--full-diff", "--name-only", "--format=%x1e", "-n", strconv.Itoa(coChangeCommits), "--"}
	for _, f := range changed {
		argv = append(argv, ":(top,literal)"+f)
	}
	raw, err := procx.Output(ctx, procx.Cmd{Argv: argv, Dir: dir, Env: procx.InheritEnv(), Timeout: 3 * time.Second, MaxStdout: 32 << 20})
	if err != nil {
		return nil
	}
	isChanged := map[string]bool{}
	for _, f := range changed {
		isChanged[f] = true
	}
	seen := map[string]int{}             // changed file -> commits touching it
	pairs := map[string]map[string]int{} // changed file -> other file -> commits touching both
	for _, block := range strings.Split(string(raw), "\x1e") {
		files := []string{}
		for _, l := range strings.Split(block, "\n") {
			if l != "" {
				files = append(files, l)
			}
		}
		if len(files) > coChangeMaxFiles {
			continue
		}
		for _, s := range files {
			if !isChanged[s] {
				continue
			}
			seen[s]++
			for _, f := range files {
				if !isChanged[f] {
					if pairs[s] == nil {
						pairs[s] = map[string]int{}
					}
					pairs[s][f]++
				}
			}
		}
	}
	best := map[string]CoChange{}
	for s, others := range pairs {
		for f, n := range others {
			c := CoChange{Path: f, With: s, Support: n, Confidence: math.Round(100*float64(n)/float64(seen[s])) / 100}
			if b, ok := best[f]; n >= coChangeMinSupport && c.Confidence >= coChangeMinConf && (!ok || ranksAbove(c, b)) {
				best[f] = c
			}
		}
	}
	out := []CoChange{}
	for f, c := range best {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err == nil { // drop files deleted or renamed since
			out = append(out, c)
		}
	}
	return out
}

// ranksAbove orders by confidence, then support, then path, so output is deterministic.
func ranksAbove(a, b CoChange) bool {
	if a.Confidence != b.Confidence {
		return a.Confidence > b.Confidence
	}
	if a.Support != b.Support {
		return a.Support > b.Support
	}
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	return a.With < b.With
}
