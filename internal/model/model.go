package model

import "crypto/sha256"

// Lane controls the amount of execution and context granted to a task.
type Lane string

const (
	LaneAnswer Lane = "answer"
	LaneSmall  Lane = "small"
	LaneFull   Lane = "full"
)

type Risk string

const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskHigh   Risk = "high"
)

type RetrievalIntent string

const (
	RetrievalExact      RetrievalIntent = "exact"
	RetrievalSemantic   RetrievalIntent = "semantic"
	RetrievalStructural RetrievalIntent = "structural"
	RetrievalMixed      RetrievalIntent = "mixed"
)

type RouteDecision struct {
	Lane              Lane     `json:"lane"`
	Risk              Risk     `json:"risk"`
	Reasons           []string `json:"reasons"`
	StructuralContext bool     `json:"structural_context"`
	Confidence        float64  `json:"confidence"`
}

type RetrievalPlan struct {
	Intent             RetrievalIntent `json:"intent"`
	UseLexical         bool            `json:"use_lexical"`
	UseSemantic        bool            `json:"use_semantic"`
	UseStructural      bool            `json:"use_structural"`
	Reason             string          `json:"reason"`
	StructuralPatterns []string        `json:"structural_patterns,omitempty"`
}

type ContextItem struct {
	Source     string         `json:"source"`
	Text       string         `json:"text"`
	Score      float64        `json:"score"`
	Stale      bool           `json:"stale"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Provenance map[string]any `json:"provenance,omitempty"`
}

// DedupeKey returns a SHA-256 hash of the item's text, used to identify and
// remove duplicate context items.
func (c ContextItem) DedupeKey() [32]byte {
	return sha256.Sum256([]byte(c.Text))
}
