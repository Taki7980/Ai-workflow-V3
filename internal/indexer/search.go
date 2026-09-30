package indexer

import (
	"encoding/json"
	"os"

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

// Load reads and decodes the on-disk index for repo within the workspace
// rooted at controlRoot.
func Load(controlRoot string, repo workspace.Repository) (Index, error) {
	b, err := os.ReadFile(Path(controlRoot, repo))
	if err != nil {
		return Index{}, err
	}
	var idx Index
	err = json.Unmarshal(b, &idx)
	return idx, err
}

// Search ranks the symbols across all indexes against query using BM25 and
// returns up to limit hits (defaulting to 6 when limit is non-positive),
// ordered by descending relevance score.
func Search(query string, indexes map[string]Index, limit int) []Hit {
	texts := []string{}
	values := []Hit{}
	for repo, idx := range indexes {
		for _, s := range idx.Symbols {
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
