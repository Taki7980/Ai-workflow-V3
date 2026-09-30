package compat

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/retrieval"
	"github.com/Taki7980/ai-workflow-v3/internal/routing"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

type Cases struct {
	SchemaVersion   int                  `json:"schema_version"`
	Routing         []RoutingCase        `json:"routing"`
	Retrieval       []RetrievalCase      `json:"retrieval"`
	RemoteIdentity  []RemoteIdentityCase `json:"remote_identity"`
	RepositoryID    []RepositoryIDCase   `json:"repository_id"`
	MMR             []MMRCase            `json:"mmr"`
	ConfigSnapshot  bool                 `json:"config_snapshot"`
}

type RoutingCase struct {
	Name string `json:"name"`
	Task string `json:"task"`
}

type RetrievalCase struct {
	Name     string              `json:"name"`
	Query    string              `json:"query"`
	Decision model.RouteDecision `json:"decision"`
	Symbol   string              `json:"symbol,omitempty"`
	Endpoint string              `json:"endpoint,omitempty"`
}

type RemoteIdentityCase struct {
	Name  string `json:"name"`
	Input string `json:"input"`
}

type RepositoryIDCase struct {
	Name           string  `json:"name"`
	RelativePath   string  `json:"relative_path"`
	RemoteIdentity *string `json:"remote_identity"`
}

type MMRCandidateCase struct {
	Key   string  `json:"key"`
	Text  string  `json:"text"`
	Score float64 `json:"score"`
}

type MMRCase struct {
	Name       string             `json:"name"`
	Query      string             `json:"query"`
	Lambda     float64            `json:"lambda"`
	MaxItems   int                `json:"max_items"`
	Candidates []MMRCandidateCase `json:"candidates"`
}

type Fixture struct {
	SchemaVersion  int                            `json:"schema_version"`
	Source         SourceRef                      `json:"source"`
	Routing        map[string]model.RouteDecision `json:"routing"`
	Retrieval      map[string]model.RetrievalPlan `json:"retrieval"`
	RemoteIdentity map[string]*string             `json:"remote_identity"`
	RepositoryID   map[string]string              `json:"repository_id"`
	MMR            map[string][]string            `json:"mmr"`
	Config         map[string]any                 `json:"config"`
}

type SourceRef struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
}

type Mismatch struct {
	Contract string `json:"contract"`
	Case     string `json:"case"`
	Expected any    `json:"expected"`
	Actual   any    `json:"actual"`
}

type Report struct {
	OK           bool       `json:"ok"`
	SourceCommit string     `json:"source_commit"`
	Cases        int        `json:"cases"`
	Mismatches   []Mismatch `json:"mismatches"`
}

func Run(casesPath, fixturePath, lockPath string) (Report, error) {
	var cases Cases
	if err := readJSON(casesPath, &cases); err != nil {
		return Report{}, err
	}
	var fixture Fixture
	if err := readJSON(fixturePath, &fixture); err != nil {
		return Report{}, err
	}
	lockBytes, err := os.ReadFile(lockPath)
	if err != nil {
		return Report{}, fmt.Errorf("read V2 lock: %w", err)
	}
	lock := strings.TrimSpace(string(lockBytes))
	if cases.SchemaVersion != 1 || fixture.SchemaVersion != 1 {
		return Report{}, fmt.Errorf("unsupported compatibility schema")
	}
	if fixture.Source.Commit != lock {
		return Report{}, fmt.Errorf("fixture source commit %s does not match lock %s", fixture.Source.Commit, lock)
	}

	report := Report{OK: true, SourceCommit: lock, Mismatches: []Mismatch{}}
	cfg := config.Default()

	add := func(contract, name string, expected, actual any) {
		report.Cases++
		if !reflect.DeepEqual(expected, actual) {
			report.OK = false
			report.Mismatches = append(report.Mismatches, Mismatch{
				Contract: contract, Case: name, Expected: expected, Actual: actual,
			})
		}
	}

	for _, c := range cases.Routing {
		expected, ok := fixture.Routing[c.Name]
		if !ok {
			return Report{}, fmt.Errorf("missing routing fixture %q", c.Name)
		}
		add("routing", c.Name, expected, routing.Classify(c.Task, cfg))
	}

	for _, c := range cases.Retrieval {
		expected, ok := fixture.Retrieval[c.Name]
		if !ok {
			return Report{}, fmt.Errorf("missing retrieval fixture %q", c.Name)
		}
		actual := routing.PlanRetrievalWithAnchors(c.Query, c.Decision, c.Symbol, c.Endpoint)
		add("retrieval", c.Name, expected, actual)
	}

	for _, c := range cases.RemoteIdentity {
		expected, ok := fixture.RemoteIdentity[c.Name]
		if !ok {
			return Report{}, fmt.Errorf("missing remote identity fixture %q", c.Name)
		}
		actualValue := workspace.RemoteIdentity(c.Input)
		var actual *string
		if actualValue != "" {
			v := actualValue
			actual = &v
		}
		add("remote_identity", c.Name, expected, actual)
	}

	for _, c := range cases.RepositoryID {
		expected, ok := fixture.RepositoryID[c.Name]
		if !ok {
			return Report{}, fmt.Errorf("missing repository id fixture %q", c.Name)
		}
		remote := ""
		if c.RemoteIdentity != nil {
			remote = *c.RemoteIdentity
		}
		add("repository_id", c.Name, expected, workspace.RepositoryID(c.RelativePath, remote))
	}

	for _, c := range cases.MMR {
		expected, ok := fixture.MMR[c.Name]
		if !ok {
			return Report{}, fmt.Errorf("missing MMR fixture %q", c.Name)
		}
		if c.MaxItems <= 0 {
			return Report{}, fmt.Errorf("MMR case %q max_items must be positive", c.Name)
		}
		candidates := make([]retrieval.Candidate[string], 0, len(c.Candidates))
		for _, candidate := range c.Candidates {
			candidates = append(candidates, retrieval.Candidate[string]{
				Key:             candidate.Key,
				Text:            candidate.Text,
				Value:           candidate.Key,
				Relevance:       candidate.Score,
				EstimatedTokens: 1,
			})
		}
		selected, err := retrieval.SelectMMR(candidates, retrieval.SelectorOptions{
			MaxTokens:     c.MaxItems,
			MaxCandidates: len(candidates),
			Lambda:        c.Lambda,
		})
		if err != nil {
			return Report{}, fmt.Errorf("MMR case %q: %w", c.Name, err)
		}
		actual := make([]string, 0, len(selected.Items))
		for _, item := range selected.Items {
			actual = append(actual, item.Key)
		}
		add("mmr", c.Name, expected, actual)
	}

	if cases.ConfigSnapshot {
		expected, err := canonicalJSON(fixture.Config)
		if err != nil {
			return Report{}, err
		}
		actual, err := canonicalJSON(config.DefaultDocument())
		if err != nil {
			return Report{}, err
		}
		add("config", "default_config", expected, actual)
	}

	return report, nil
}

func readJSON(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func canonicalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
