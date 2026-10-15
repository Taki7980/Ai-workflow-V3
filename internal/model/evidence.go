package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// EvidenceAuthority lists capabilities retrieved evidence may grant: none.
type EvidenceAuthority struct {
	Instructions         bool `json:"instructions"`
	Tools                bool `json:"tools"`
	Policy               bool `json:"policy"`
	RepositoryActivation bool `json:"repository_activation"`
	NetworkAccess        bool `json:"network_access"`
	SecretAccess         bool `json:"secret_access"`
	MemoryWrite          bool `json:"memory_write"`
	VerificationBypass   bool `json:"verification_bypass"`
}

// Evidence is the content-bound identity of one evidence item (V2 evidence-v1).
type Evidence struct {
	EvidenceID    string            `json:"evidence_id"`
	RepositoryID  string            `json:"repository_id"`
	Kind          string            `json:"kind"`
	ContentSHA256 string            `json:"content_sha256"`
	TrustClass    string            `json:"trust_class"`
	Authority     EvidenceAuthority `json:"authority"`
	Provenance    map[string]any    `json:"provenance"`
	Confidence    string            `json:"confidence"`
}

func evidenceKind(source string) string {
	switch {
	case strings.HasPrefix(source, "external:"):
		return "external"
	case source == "lightweight_index":
		return "index"
	case source == "targeted_source":
		return "repository"
	case source == "code_review_graph" || source == "scip":
		return "structural"
	case source == "semantic":
		return "semantic"
	case source == "test_resolver":
		return "test"
	case source == "durable_memory":
		return "memory"
	case source == "hot_cache" || source == "research_cache" || source == "domain_manifest":
		return "generated"
	}
	return "context"
}

func trustClass(source string) string {
	switch {
	case strings.HasPrefix(source, "external:"):
		return "untrusted_external_provider"
	case source == "durable_memory":
		return "untrusted_durable_memory"
	case source == "hot_cache" || source == "research_cache" || source == "domain_manifest":
		return "untrusted_generated_context"
	case source == "lightweight_index" || source == "targeted_source" || source == "code_review_graph" ||
		source == "scip" || source == "semantic" || source == "test_resolver":
		return "untrusted_repository_content"
	}
	return "untrusted_context_data"
}

var locatorKeys = []string{"path", "file", "line", "start_line", "end_line", "symbol", "endpoint", "pattern", "role", "language"}

// NewEvidence builds the system-owned envelope for it. Provider metadata can
// never raise confidence or grant authority.
func NewEvidence(it ContextItem, repositoryID string) *Evidence {
	sum := sha256.Sum256([]byte(it.Text))
	content := "sha256:" + hex.EncodeToString(sum[:])
	locator := map[string]any{}
	for _, k := range locatorKeys {
		switch v := it.Metadata[k].(type) {
		case string, int, int64, float64:
			locator[k] = v
		}
	}
	confidence := "candidate"
	if c, _ := it.Metadata["evidence_confidence"].(string); it.Source == "code_review_graph" {
		switch c = strings.ToLower(strings.TrimSpace(c)); c {
		case "candidate", "corroborated", "verified":
			confidence = c
		}
	}
	prov := map[string]any{"retriever": it.Source, "fresh": !it.Stale}
	if len(locator) > 0 {
		prov["locator"] = locator
	}
	p, _ := it.Provenance["provider"].(string)
	if p == "" {
		p, _ = it.Metadata["provider"].(string)
	}
	if p = strings.TrimSpace(p); p != "" {
		prov["provider"] = p[:min(len(p), 160)]
	}
	kind := evidenceKind(it.Source)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{"schema": "evidence-v1", "repository_id": repositoryID, "source": it.Source,
		"kind": kind, "content_sha256": content, "locator": locator})
	id := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return &Evidence{
		EvidenceID: "evidence-v1:" + hex.EncodeToString(id[:]), RepositoryID: repositoryID, Kind: kind,
		ContentSHA256: content, TrustClass: trustClass(it.Source), Provenance: prov, Confidence: confidence,
	}
}
