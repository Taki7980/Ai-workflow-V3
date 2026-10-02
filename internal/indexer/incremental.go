package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/storage"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

type BuildMode string

const (
	BuildAuto        BuildMode = "auto"
	BuildIncremental BuildMode = "incremental"
	BuildFull        BuildMode = "full"
)

type BuildStats struct {
	RequestedMode string `json:"requested_mode"`
	EffectiveMode string `json:"effective_mode"`
	FullReason    string `json:"full_reason,omitempty"`
	Files         int    `json:"files"`
	Symbols       int    `json:"symbols"`
	Hashed        int    `json:"hashed"`
	Reparsed      int    `json:"reparsed"`
	Reused        int    `json:"reused"`
	Added         int    `json:"added"`
	Changed       int    `json:"changed"`
	Removed       int    `json:"removed"`
}

type fileBuildResult struct {
	rel      string
	state    FileState
	symbols  []Symbol
	reused   bool
	reparsed bool
	added    bool
	changed  bool
	err      error
}

// BuildWithMode builds a complete current index while optionally reusing
// symbol rows whose file content and extraction semantics are unchanged.
func BuildWithMode(ctx context.Context, controlRoot string, repo workspace.Repository, mode BuildMode) (Index, BuildStats, error) {
	stats := BuildStats{RequestedMode: string(mode)}
	if mode != BuildAuto && mode != BuildIncremental && mode != BuildFull {
		return Index{}, stats, fmt.Errorf("unsupported index build mode %q", mode)
	}
	if err := ctx.Err(); err != nil {
		return Index{}, stats, err
	}

	repoRoot := controlRoot
	if repo.RelativePath != "." {
		repoRoot = filepath.Join(controlRoot, filepath.FromSlash(repo.RelativePath))
	}
	paths, err := sourcePaths(repoRoot)
	if err != nil {
		return Index{}, stats, err
	}
	stats.Files = len(paths)

	var old Index
	reuseOld := false
	if mode == BuildFull {
		stats.EffectiveMode = string(BuildFull)
	} else {
		old, stats.FullReason = reusablePriorIndex(controlRoot, repo)
		if stats.FullReason == "" {
			reuseOld = true
			stats.EffectiveMode = string(BuildIncremental)
		} else {
			stats.EffectiveMode = string(BuildFull)
		}
	}

	oldSymbols := map[string][]Symbol{}
	if reuseOld {
		for _, symbol := range old.Symbols {
			oldSymbols[symbol.Path] = append(oldSymbols[symbol.Path], symbol)
		}
	}

	currentPaths := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return Index{}, stats, relErr
		}
		currentPaths[filepath.ToSlash(rel)] = struct{}{}
	}
	if reuseOld {
		for rel := range old.Files {
			if _, ok := currentPaths[rel]; !ok {
				stats.Removed++
			}
		}
	}

	jobs := make(chan string)
	results := make(chan fileBuildResult, len(paths))
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				results <- buildSourceFile(ctx, repoRoot, path, old, oldSymbols, reuseOld)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, path := range paths {
			select {
			case <-ctx.Done():
				return
			case jobs <- path:
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	idx := Index{
		Version:           IndexVersion,
		Repository:        repo.RelativePath,
		RepositoryID:      repo.RepositoryID,
		ExtractorRevision: currentExtractorRevision(),
		BuiltAt:           time.Now().UTC().Format(time.RFC3339),
		Files:             map[string]FileState{},
		Symbols:           []Symbol{},
	}
	var firstErr error
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		stats.Hashed++
		if result.reused {
			stats.Reused++
		}
		if result.reparsed {
			stats.Reparsed++
		}
		if result.added {
			stats.Added++
		}
		if result.changed {
			stats.Changed++
		}
		idx.Files[result.rel] = result.state
		idx.Symbols = append(idx.Symbols, result.symbols...)
	}
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	if firstErr != nil {
		return Index{}, stats, firstErr
	}

	sortSymbols(idx.Symbols)
	stats.Symbols = len(idx.Symbols)
	if err := storage.WriteJSON(Path(controlRoot, repo), idx); err != nil {
		return Index{}, stats, err
	}
	return idx, stats, nil
}

func sourcePaths(repoRoot string) ([]string, error) {
	paths := []string{}
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == repoRoot {
				return walkErr
			}
			return nil
		}
		if d.IsDir() {
			if path != repoRoot && excludes[d.Name()] {
				return filepath.SkipDir
			}
			if path != repoRoot && isNestedGitRoot(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if sourceExts[strings.ToLower(filepath.Ext(path))] {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func reusablePriorIndex(controlRoot string, repo workspace.Repository) (Index, string) {
	idx, err := loadRaw(controlRoot, repo)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Index{}, "missing-index"
		}
		return Index{}, "invalid-index"
	}
	switch {
	case idx.Version != IndexVersion:
		return Index{}, "index-version"
	case idx.Repository != repo.RelativePath:
		return Index{}, "repository-path"
	case idx.RepositoryID != repo.RepositoryID:
		return Index{}, "repository-id"
	case idx.ExtractorRevision != currentExtractorRevision():
		return Index{}, "extractor-revision"
	default:
		return idx, ""
	}
}

func buildSourceFile(ctx context.Context, repoRoot, path string, old Index, oldSymbols map[string][]Symbol, reuseOld bool) fileBuildResult {
	if err := ctx.Err(); err != nil {
		return fileBuildResult{err: err}
	}
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return fileBuildResult{err: err}
	}
	rel = filepath.ToSlash(rel)
	data, err := os.ReadFile(path)
	if err != nil {
		return fileBuildResult{rel: rel, err: err}
	}
	if err := ctx.Err(); err != nil {
		return fileBuildResult{rel: rel, err: err}
	}
	st, err := os.Stat(path)
	if err != nil {
		return fileBuildResult{rel: rel, err: err}
	}
	h := sha256.Sum256(data)
	digest := hex.EncodeToString(h[:])
	state := FileState{SHA256: digest, Size: st.Size(), MTimeNS: st.ModTime().UnixNano()}

	if reuseOld {
		oldState, existed := old.Files[rel]
		if existed && oldState.SHA256 == digest && reusableSymbols(oldSymbols[rel], digest) {
			return fileBuildResult{rel: rel, state: state, symbols: append([]Symbol(nil), oldSymbols[rel]...), reused: true}
		}
		symbols, _ := parseSymbols(data, rel, digest)
		result := fileBuildResult{rel: rel, state: state, symbols: symbols, reparsed: true}
		if !existed {
			result.added = true
		} else if oldState.SHA256 != digest {
			result.changed = true
		}
		return result
	}

	symbols, _ := parseSymbols(data, rel, digest)
	return fileBuildResult{rel: rel, state: state, symbols: symbols, reparsed: true, added: true}
}

func reusableSymbols(symbols []Symbol, digest string) bool {
	for _, symbol := range symbols {
		if symbol.SHA256 != digest {
			return false
		}
	}
	return true
}

func sortSymbols(symbols []Symbol) {
	sort.SliceStable(symbols, func(i, j int) bool {
		if symbols[i].Path != symbols[j].Path {
			return symbols[i].Path < symbols[j].Path
		}
		if symbols[i].Line != symbols[j].Line {
			return symbols[i].Line < symbols[j].Line
		}
		if symbols[i].EndLine != symbols[j].EndLine {
			return symbols[i].EndLine < symbols[j].EndLine
		}
		if symbols[i].Name != symbols[j].Name {
			return symbols[i].Name < symbols[j].Name
		}
		return symbols[i].Kind < symbols[j].Kind
	})
}
