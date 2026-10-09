package indexer

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"

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

// Search ranks the symbols across all indexes against query using BM25 and
// returns up to limit hits (defaulting to 6 when limit is non-positive),
// ordered by descending relevance score.
func Search(query string, indexes map[string]Index, limit int) []Hit {
	texts := []string{}
	values := []Hit{}
	// Sorted repositories make BM25 tie order deterministic across runs.
	for _, repo := range slices.Sorted(maps.Keys(indexes)) {
		for _, s := range indexes[repo].Symbols {
			texts = append(texts, s.Name+" "+s.Kind+" "+s.Path)
			values = append(values, Hit{Repository: repo, Path: s.Path, Line: s.Line, Kind: s.Kind, Symbol: s.Name})
		}
	}
	b := retrieval.NewBM25(texts, values)
	ranked := b.Rank(query)
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
