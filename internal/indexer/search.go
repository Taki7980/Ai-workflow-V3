package indexer

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/retrieval"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

type Hit struct {
	Repository string  `json:"repository"`
	Path       string  `json:"path"`
	Line       int     `json:"line"`
	Kind       string  `json:"kind"`
	Symbol     string  `json:"symbol"`
	Score      float64 `json:"score"`
}

func loadRaw(controlRoot string, repo workspace.Repository) (Index, error) {
	b, err := os.ReadFile(Path(controlRoot, repo))
	if err != nil {
		return Index{}, err
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return Index{}, err
	}
	return idx, nil
}

// Load reads and validates the on-disk index for repo within the workspace
// rooted at controlRoot. Stale or incompatible index state is rejected.
func Load(controlRoot string, repo workspace.Repository) (Index, error) {
	idx, err := loadRaw(controlRoot, repo)
	if err != nil {
		return Index{}, err
	}
	if idx.Version != IndexVersion {
		return Index{}, fmt.Errorf("unsupported index version %d (want %d)", idx.Version, IndexVersion)
	}
	if idx.Repository != repo.RelativePath {
		return Index{}, fmt.Errorf("index repository %q does not match %q", idx.Repository, repo.RelativePath)
	}
	if idx.RepositoryID != repo.RepositoryID {
		return Index{}, fmt.Errorf("index repository id mismatch for %s", repo.RelativePath)
	}
	if idx.ExtractorRevision != currentExtractorRevision() {
		return Index{}, fmt.Errorf("index extractor revision %q does not match current %q", idx.ExtractorRevision, currentExtractorRevision())
	}
	return idx, nil
}

// fileBM25B is the length normalisation for whole-file documents, below the
// symbol-level 0.75 because large central files are the ones changes touch
// most (BugLocator, Zhou et al., ICSE 2012).
const fileBM25B = 0.3

// historyWeight scales the change-history prior: the most recently and often
// changed file scores up to 1.5x. Held-out commits, prior off -> on: V2 file
// recall@5 .682->.703, ky MRR .432->.492, cobra .666->.673, click .616->.629.
const historyWeight = 0.5

// Search ranks the symbols across all indexes against query using BM25 and
// returns up to limit hits (defaulting to 6 when limit is non-positive),
// ordered by descending relevance score. A symbol's score adds its file's
// BM25 score over the file name and every symbol name in the file, so a file
// matching several query terms across symbols outranks a lone name match.
// The sum is then scaled by the file's change-history prior (fileHistory),
// on a log scale relative to the repository's most changed file; an index
// without history ranks by text alone.
// Directories stay out of the file document: the symbol text already carries
// the full path, and counting directory terms twice let a "tests" directory
// outrank the definition a query names.
func Search(query string, indexes map[string]Index, limit int) []Hit {
	texts := []string{}
	values := []Hit{}
	fileDocs := map[string][]string{} // repository + NUL + path -> file name, then symbol names
	fileKeys := []string{}
	// Sorted repositories make BM25 tie order deterministic across runs.
	for _, repo := range slices.Sorted(maps.Keys(indexes)) {
		for _, s := range indexes[repo].Symbols {
			texts = append(texts, s.Name+" "+s.Kind+" "+s.Path)
			values = append(values, Hit{Repository: repo, Path: s.Path, Line: s.Line, Kind: s.Kind, Symbol: s.Name})
			k := repo + "\x00" + s.Path
			if fileDocs[k] == nil {
				fileKeys = append(fileKeys, k)
				fileDocs[k] = []string{path.Base(s.Path)}
			}
			fileDocs[k] = append(fileDocs[k], s.Name)
		}
	}
	ranked := retrieval.NewBM25(texts, values).Rank(query)
	fileTexts := make([]string, len(fileKeys))
	for i, k := range fileKeys {
		fileTexts[i] = strings.Join(fileDocs[k], " ")
	}
	files := retrieval.NewBM25(fileTexts, fileKeys)
	files.B = fileBM25B
	fileScore := map[string]float64{}
	for _, r := range files.Rank(query) {
		fileScore[r.Value] = r.Score
	}
	hmax := map[string]float64{}
	for repo, idx := range indexes {
		for _, v := range idx.History {
			hmax[repo] = max(hmax[repo], v)
		}
	}
	for i := range ranked {
		v := ranked[i].Value
		ranked[i].Score += fileScore[v.Repository+"\x00"+v.Path]
		if m := hmax[v.Repository]; m > 0 {
			ranked[i].Score *= 1 + historyWeight*math.Log1p(indexes[v.Repository].History[v.Path])/math.Log1p(m)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Score > ranked[j].Score })
	if limit <= 0 {
		limit = 6
	}
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]Hit, 0, len(ranked))
	for _, r := range ranked {
		h := r.Value
		h.Score = r.Score
		out = append(out, h)
	}
	return out
}
