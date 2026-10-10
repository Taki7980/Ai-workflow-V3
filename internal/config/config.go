package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	CurrentVersion = 2
	RelativePath   = "ai-workspace/config/control-plane.json"
)

type Budget struct {
	EstimatedTokens int `json:"estimated_tokens"`
	OutputTokens    int `json:"output_tokens"`
}

type Budgets struct {
	Answer Budget `json:"answer"`
	Small  Budget `json:"small"`
	Full   Budget `json:"full"`
}

type Classifier struct {
	HighRiskKeywords []string `json:"high_risk_keywords"`
	FullKeywords     []string `json:"full_keywords"`
	AnswerKeywords   []string `json:"answer_keywords"`
	SmallKeywords    []string `json:"small_keywords"`
}

type CRG struct {
	Mode               string   `json:"mode"`
	MinLane            string   `json:"min_lane"`
	StructuralKeywords []string `json:"structural_keywords"`
	MinSourceFiles     int      `json:"min_source_files"`
	ChangedThreshold   int      `json:"changed_files_threshold"`
}

type Sufficiency struct {
	Threshold float64 `json:"threshold"`
}

type AdaptiveBudget struct {
	Enabled                 bool    `json:"enabled"`
	HighSufficiencyFraction float64 `json:"high_sufficiency_fraction"`
	MediumFraction          float64 `json:"medium_sufficiency_fraction"`
	MinimumChars            int     `json:"minimum_chars"`
}

type Selector struct {
	Enabled                     bool    `json:"enabled"`
	TightBudgetFraction         float64 `json:"tight_budget_fraction"`
	MandatoryStructuralEvidence bool    `json:"mandatory_structural_evidence"`
	MaxSelectorCandidates       int     `json:"max_selector_candidates"`
}

type Context struct {
	MaxResultsPerSource int                `json:"max_results_per_source"`
	SourceShares        map[string]float64 `json:"source_shares"`
	CRG                 CRG                `json:"crg"`
	Sufficiency         Sufficiency        `json:"sufficiency"`
	AdaptiveBudget      AdaptiveBudget     `json:"adaptive_budget"`
	Selector            Selector           `json:"selector"`
	SelectiveRetrieval  SelectiveRetrieval `json:"selective_retrieval"`
	SCIP                SCIP               `json:"scip"`
	Telemetry           Telemetry          `json:"telemetry"`
	Semantic            Semantic           `json:"semantic"`
	ExternalRetrievers  []json.RawMessage  `json:"external_retrievers"`
	// SnippetLines is V3-only; omitempty keeps it out of the frozen V2 document.
	SnippetLines int `json:"snippet_lines,omitempty"`
}

// BuiltinSemanticProvider is the dependency-free local hybrid retriever.
const BuiltinSemanticProvider = "builtin-local"

// Semantic configures semantic retrieval (V2 context.semantic). Executable
// authority never comes from here: provider_id resolves through the trusted
// registry, and Command is honoured only with an explicit operator opt-in.
type Semantic struct {
	Mode           string          `json:"mode"` // auto | off
	ProviderID     string          `json:"provider_id"`
	Command        json.RawMessage `json:"command,omitempty"`
	MaxResults     int             `json:"max_results"`
	TimeoutSeconds float64         `json:"timeout_seconds,omitempty"`
	MaxOutputBytes int64           `json:"max_output_bytes,omitempty"`
}

// HasCommand reports a configured (string or argv) semantic command.
func (s Semantic) HasCommand() bool {
	c := strings.TrimSpace(string(s.Command))
	return c != "" && c != `""` && c != "[]" && c != "null" || strings.TrimSpace(os.Getenv("AI_WORKFLOW_SEMANTIC_CMD")) != ""
}

// SemanticProviderID returns the active semantic provider ID ("" = none or
// an explicit command), following V2 configured_provider_id.
func (s Semantic) SemanticProviderID() string {
	if strings.EqualFold(s.Mode, "off") {
		return ""
	}
	if env := strings.TrimSpace(os.Getenv("AI_WORKFLOW_SEMANTIC_PROVIDER_ID")); env != "" {
		return env
	}
	if id := strings.TrimSpace(s.ProviderID); id != "" {
		return id
	}
	if s.HasCommand() {
		return "" // an explicit command keeps its own (opt-in) security semantics
	}
	return BuiltinSemanticProvider
}

// Telemetry controls local retrieval traces (V2 context.telemetry).
type Telemetry struct {
	Mode           string   `json:"mode"` // off | mutations | all
	RetentionDays  *int     `json:"retention_days,omitempty"`
	MaxTraceFiles  int      `json:"max_trace_files,omitempty"`
	RedactPatterns []string `json:"redact_patterns,omitempty"`
}

// SCIP gates SCIP index use: auto, on or off.
type SCIP struct {
	Mode string `json:"mode"`
}

type SelectiveRetrieval struct {
	Enabled         bool    `json:"enabled"`
	MinimumCoverage float64 `json:"minimum_coverage"`
}

// Snippet returns the brief source-window radius in lines, defaulting to 6.
func (c Context) Snippet() int {
	if c.SnippetLines <= 0 {
		return 6
	}
	return c.SnippetLines
}

type Discovery struct {
	MaxDepth           int  `json:"max_depth"`
	RequireAcceptance  bool `json:"require_acceptance"`
	AutoIncludeOnSetup bool `json:"auto_include_on_setup"`
}

type Workspace struct {
	Roots           []string  `json:"roots"`
	MaxRoots        int       `json:"max_roots"`
	Registry        string    `json:"registry"`
	RepositoryGraph string    `json:"repository_graph"`
	Discovery       Discovery `json:"discovery"`
	// Hierarchical is V2 workspace.hierarchical_retrieval; absent fields take V2 defaults.
	Hierarchical HierarchicalRetrieval `json:"hierarchical_retrieval"`
}

// HierarchicalRetrieval bounds which repositories a task searches.
type HierarchicalRetrieval struct {
	Enabled                *bool    `json:"enabled,omitempty"`
	MaxPrimaryRepositories int      `json:"max_primary_repositories,omitempty"`
	MaxGraphExpansions     *int     `json:"max_graph_expansions,omitempty"`
	Relationships          []string `json:"relationships,omitempty"`
}

// Resolved returns enabled, max primary, max expansions and relationships with V2 defaults.
func (h HierarchicalRetrieval) Resolved() (bool, int, int, []string) {
	enabled, primary, expansions, rels := true, 2, 2, h.Relationships
	if h.Enabled != nil {
		enabled = *h.Enabled
	}
	if h.MaxPrimaryRepositories > 0 {
		primary = h.MaxPrimaryRepositories
	}
	if h.MaxGraphExpansions != nil {
		expansions = max(0, *h.MaxGraphExpansions)
	}
	if rels == nil {
		rels = []string{"depends_on", "publishes_api", "consumes_schema", "deploys"}
	}
	return enabled, primary, expansions, rels
}

type OrchestrationBudget struct {
	MaxAgentSlots      int `json:"max_agent_slots"`
	MaxCRGCalls        int `json:"max_crg_calls"`
	MaxGraphDepth      int `json:"max_graph_depth"`
	ReviewPasses       int `json:"review_passes"`
	VerificationPasses int `json:"verification_passes"`
}

type Superpowers struct {
	Mode string `json:"mode"`
}

type Execution struct {
	PreferSuperpowersForFull bool                `json:"prefer_superpowers_for_full"`
	NativeFallback           bool                `json:"native_fallback"`
	Superpowers              Superpowers         `json:"superpowers"`
	OrchestrationBudget      OrchestrationBudget `json:"orchestration_budget"`
}

type Handoff struct {
	MaxLines int `json:"max_lines"`
}
type Memory struct {
	MaxResults        int     `json:"max_results"`
	MinimumConfidence float64 `json:"minimum_confidence"`
}
type Models struct {
	Answer     string `json:"answer"`
	Small      string `json:"small"`
	FullMedium string `json:"full_medium"`
	FullHigh   string `json:"full_high"`
}

type Config struct {
	Version    int        `json:"version"`
	Budgets    Budgets    `json:"budgets"`
	Classifier Classifier `json:"classifier"`
	Context    Context    `json:"context"`
	Workspace  Workspace  `json:"workspace"`
	Execution  Execution  `json:"execution"`
	Handoff    Handoff    `json:"handoff"`
	Memory     Memory     `json:"memory"`
	Models     Models     `json:"models"`
}

// Default returns the built-in V3 configuration used when no configuration
// file exists yet or as the base for the full config document.
func Default() Config {
	return Config{
		Version: CurrentVersion,
		Budgets: Budgets{
			Answer: Budget{EstimatedTokens: 1200, OutputTokens: 450},
			Small:  Budget{EstimatedTokens: 2500, OutputTokens: 700},
			Full:   Budget{EstimatedTokens: 6000, OutputTokens: 1200},
		},
		Classifier: Classifier{
			HighRiskKeywords: []string{"auth", "authentication", "authorization", "security", "payment", "billing", "migration", "schema", "database", "delete data", "destructive", "concurrency", "race condition", "deploy", "production", "public api", "contract change", "permission", "credential", "secret"},
			FullKeywords:     []string{"refactor", "architecture", "multi-file", "cross-cutting", "end-to-end", "redesign", "performance", "distributed", "integration", "implement feature"},
			AnswerKeywords:   []string{"explain", "what is", "how does", "why does", "compare", "difference", "where is", "show me", "understand"},
			SmallKeywords:    []string{"rename", "typo", "copy change", "small fix", "one-line", "one line", "adjust", "update text"},
		},
		Context: Context{
			MaxResultsPerSource: 6,
			SourceShares:        map[string]float64{"hot_cache": .15, "lightweight": .30, "crg": .40, "source_fallback": .15},
			CRG:                 CRG{Mode: "auto", MinLane: "full", StructuralKeywords: []string{"caller", "callee", "dependency", "dependents", "impact", "blast radius", "flow", "architecture", "tests for", "refactor", "what breaks", "affected"}, MinSourceFiles: 250, ChangedThreshold: 3},
			Sufficiency:         Sufficiency{Threshold: .72},
			AdaptiveBudget:      AdaptiveBudget{Enabled: true, HighSufficiencyFraction: .45, MediumFraction: .70, MinimumChars: 900},
			Selector:            Selector{Enabled: true, TightBudgetFraction: .30, MandatoryStructuralEvidence: true, MaxSelectorCandidates: 200},
			SelectiveRetrieval:  SelectiveRetrieval{Enabled: true, MinimumCoverage: .15},
			SCIP:                SCIP{Mode: "auto"},
			Telemetry:           Telemetry{Mode: "mutations"},
			Semantic:            Semantic{Mode: "auto", ProviderID: BuiltinSemanticProvider, MaxResults: 6},
		},
		Workspace: Workspace{Roots: []string{}, MaxRoots: 4, Registry: "ai-workspace/config/repositories.json", RepositoryGraph: "ai-workspace/config/repository-graph.json", Discovery: Discovery{MaxDepth: 8, RequireAcceptance: false, AutoIncludeOnSetup: true}},
		Execution: Execution{
			PreferSuperpowersForFull: true, NativeFallback: true,
			Superpowers:         Superpowers{Mode: "auto"},
			OrchestrationBudget: OrchestrationBudget{MaxAgentSlots: 4, MaxCRGCalls: 6, MaxGraphDepth: 3, ReviewPasses: 2, VerificationPasses: 2},
		},
		Handoff: Handoff{MaxLines: 30},
		Memory:  Memory{MaxResults: 5, MinimumConfidence: .55},
		Models:  Models{Answer: "fast", Small: "fast", FullMedium: "standard", FullHigh: "capable"},
	}
}

// Path returns the absolute path to the control-plane config file for the
// workspace rooted at root.
func Path(root string) string { return filepath.Join(root, filepath.FromSlash(RelativePath)) }

// Load reads and decodes the config file for the workspace rooted at root,
// returning the default configuration if no file exists yet.
func Load(root string) (Config, error) {
	p := Path(root)
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(b))
	// V3 validates known safety-critical fields while tolerating additive V2 fields
	// during the migration window. Existing config files are never rewritten on load.
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate checks that c's safety-critical fields are within supported
// ranges, returning an error describing the first invalid field found.
func (c Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	if c.Workspace.Discovery.MaxDepth < 0 || c.Workspace.Discovery.MaxDepth > 64 {
		return fmt.Errorf("workspace.discovery.max_depth must be 0..64")
	}
	if c.Context.Sufficiency.Threshold < 0 || c.Context.Sufficiency.Threshold > 1 {
		return fmt.Errorf("context.sufficiency.threshold must be 0..1")
	}
	if c.Context.Selector.TightBudgetFraction <= 0 || c.Context.Selector.TightBudgetFraction > 1 {
		return fmt.Errorf("context.selector.tight_budget_fraction must be >0 and <=1")
	}
	if c.Context.Selector.MaxSelectorCandidates <= 0 {
		return fmt.Errorf("context.selector.max_selector_candidates must be positive")
	}
	switch c.Context.Telemetry.Mode {
	case "", "off", "mutations", "all":
	default:
		return fmt.Errorf("context.telemetry.mode must be off, mutations, or all")
	}
	if c.Budgets.Answer.EstimatedTokens <= 0 || c.Budgets.Small.EstimatedTokens <= 0 || c.Budgets.Full.EstimatedTokens <= 0 {
		return fmt.Errorf("budgets must be positive")
	}
	return nil
}

// Digest is the SHA-256 of the raw config document in canonical JSON, used to
// bind traces and run journals to the exact configuration (V2 config_digest).
func Digest(root string) string {
	var doc any = DefaultDocument()
	if b, err := os.ReadFile(Path(root)); err == nil {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if dec.Decode(&doc) != nil {
			doc = map[string]any{"invalid": true}
		}
	}
	b, _ := json.Marshal(doc) // encoding/json sorts map keys
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// DefaultDocument returns the V2-compatible configuration document used when
// creating a workspace. The typed Config above intentionally models only the
// fields the V3 foundation consumes today; the complete document preserves V2
// keys so either runtime can inspect the same workspace during migration.
func DefaultDocument() map[string]any {
	baseBytes, _ := json.Marshal(Default())
	var doc map[string]any
	_ = json.Unmarshal(baseBytes, &doc)
	ctx := doc["context"].(map[string]any)
	ctx["scip"] = map[string]any{"mode": "auto"}
	ctx["semantic"] = map[string]any{
		"mode": "auto", "provider_id": "builtin-local", "command": "",
		"timeout_seconds": 8, "max_results": 6, "max_output_bytes": 8388608,
		"env_allowlist": []string{},
	}
	ctx["external_retrievers"] = []any{}
	ctx["selective_retrieval"] = map[string]any{"enabled": true, "minimum_coverage": 0.15}
	ctx["telemetry"] = map[string]any{"mode": "mutations"}
	ctx["learning"] = map[string]any{
		"mode": "off", "kill_switch": false, "exploration_probability": 0.05,
		"allowed_risks": []string{"low"},
		"eligible_arms": []string{"adaptive_math", "source_rank", "bm25_rank", "rrf_only", "rrf_mmr_050", "rrf_mmr_075", "rrf_mmr_090"},
	}
	ctx["deployment"] = map[string]any{
		"enabled":         false,
		"state_path":      "ai-workspace/generated/learning/deployment/active.json",
		"signing_key_env": "AI_WORKFLOW_POLICY_SIGNING_KEY",
		"auto_rollback":   true, "incident_bundles": true,
	}
	ctx["production"] = map[string]any{
		"enabled":         false,
		"sqlite_path":     "ai-workspace/generated/learning/production/events.sqlite3",
		"busy_timeout_ms": 5000,
	}
	ctx["targeted_search"] = map[string]any{"max_matches": 12, "max_file_bytes": 500000}

	workspace := doc["workspace"].(map[string]any)
	workspace["hierarchical_retrieval"] = map[string]any{
		"enabled": true, "max_primary_repositories": 2, "max_graph_expansions": 2,
		"relationships": []string{"depends_on", "publishes_api", "consumes_schema", "deploys"},
	}

	execution := doc["execution"].(map[string]any)
	execution["superpowers"] = map[string]any{"mode": "auto"}
	execution["orchestration_budget"] = map[string]any{
		"max_agent_slots": 4, "max_crg_calls": 6, "max_graph_depth": 3,
		"review_passes": 2, "verification_passes": 2,
	}
	return doc
}
