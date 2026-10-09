package compat

import (
	"path/filepath"

	"github.com/Taki7980/ai-workflow-v3/internal/structural"
)

// StructuralCases are the structural adapter contract inputs.
type StructuralCases struct {
	CRGCompact       []CRGCompactCase       `json:"crg_compact"`
	CRGItem          []CRGItemCase          `json:"crg_item"`
	CRGVerifiedEmpty []CRGVerifiedEmptyCase `json:"crg_verified_empty"`
	ScipItems        []ScipItemsCase        `json:"scip_items"`
	RepoKey          []RepoKeyCase          `json:"repo_key"`
}

// StructuralFixture holds the frozen V2 structural outputs.
type StructuralFixture struct {
	CRGCompact       map[string]any `json:"crg_compact"`
	CRGItem          map[string]any `json:"crg_item"`
	CRGVerifiedEmpty map[string]any `json:"crg_verified_empty"`
	ScipItems        map[string]any `json:"scip_items"`
	RepoKey          map[string]any `json:"repo_key"`
}

type CRGCompactCase struct {
	Name    string         `json:"name"`
	Payload map[string]any `json:"payload"`
	Pattern string         `json:"pattern"`
	Limit   int            `json:"limit"`
}

type CRGItemCase struct {
	CRGCompactCase
	Score  float64 `json:"score"`
	Anchor string  `json:"anchor"`
}

type CRGVerifiedEmptyCase struct {
	Name    string         `json:"name"`
	Payload map[string]any `json:"payload"`
}

type ScipItemsCase struct {
	Name         string         `json:"name"`
	Payload      map[string]any `json:"payload"`
	Query        string         `json:"query"`
	Symbol       *string        `json:"symbol"`
	ChangedFiles []string       `json:"changed_files"`
	Limit        int            `json:"limit"`
	Patterns     []string       `json:"patterns"`
}

type RepoKeyCase struct {
	Name  string `json:"name"`
	Input string `json:"input"`
}

// runStructural replays the structural cases; SCIP cases resolve against
// compat/fixtures/scip-root next to the cases file.
func runStructural(cases StructuralCases, fixture StructuralFixture, casesDir string, check func(contract, name string, frozen map[string]any, actual any) error) error {
	for _, c := range cases.CRGCompact {
		if err := check("crg_compact", c.Name, fixture.CRGCompact, structural.CompactCRG(c.Payload, c.Pattern, c.Limit)); err != nil {
			return err
		}
	}
	for _, c := range cases.CRGItem {
		if err := check("crg_item", c.Name, fixture.CRGItem, structural.CRGItem(c.Payload, c.Pattern, c.Score, c.Limit, c.Anchor)); err != nil {
			return err
		}
	}
	for _, c := range cases.CRGVerifiedEmpty {
		if err := check("crg_verified_empty", c.Name, fixture.CRGVerifiedEmpty, structural.VerifiedEmptyCRG(c.Payload)); err != nil {
			return err
		}
	}
	root := filepath.Join(casesDir, "fixtures", "scip-root")
	for _, c := range cases.ScipItems {
		symbol := ""
		if c.Symbol != nil {
			symbol = *c.Symbol
		}
		q := structural.Query{Text: c.Query, Symbol: symbol, Changed: c.ChangedFiles, Limit: c.Limit, Patterns: c.Patterns}
		if err := check("scip_items", c.Name, fixture.ScipItems, structural.ItemsFromScipPayload(root, c.Payload, q)); err != nil {
			return err
		}
	}
	for _, c := range cases.RepoKey {
		if err := check("repo_key", c.Name, fixture.RepoKey, structural.RepoKey(c.Input)); err != nil {
			return err
		}
	}
	return nil
}
