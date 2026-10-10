package brief

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/journal"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/structural"
	"github.com/Taki7980/ai-workflow-v3/internal/telemetry"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

// newRunID returns a random UUIDv4 string (safe as a journal file name).
func newRunID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// GraphFingerprint digests the code-review-graph manifests of the given
// repositories; it changes whenever any graph is rebuilt or removed.
func GraphFingerprint(root string, rels []string) string {
	parts := make([]map[string]string, 0, len(rels))
	for _, rel := range rels {
		state := "missing"
		if dir, err := structural.GraphDir(root, rel); err == nil {
			if b, err := os.ReadFile(filepath.Join(dir, "manifest.json")); err == nil {
				sum := sha256.Sum256(b)
				state = hex.EncodeToString(sum[:])
			}
		}
		parts = append(parts, map[string]string{"repository": rel, "manifest_sha256": state})
	}
	b, _ := workspace.CanonicalJSON(parts)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// CurrentWorkspaceFingerprint recomputes the brief workspace fingerprint for
// changed files using the indexes currently on disk.
func CurrentWorkspaceFingerprint(ctx context.Context, root string, changed []string) string {
	reg, err := workspace.Load(root)
	if err != nil {
		return ""
	}
	_, indexFiles, _, _ := loadIndexes(root, reg)
	return workspace.Fingerprint(ctx, root, indexFiles, changed).Fingerprint
}

func asMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

var safeMetadataKeys = map[string]bool{"path": true, "file": true, "line": true, "start_line": true, "end_line": true, "sha256": true, "symbol": true, "endpoint": true, "pattern": true, "role": true, "language": true, "repository": true}
var safeProvenanceKeys = map[string]bool{"path": true, "file": true, "line": true, "start_line": true, "end_line": true, "sha256": true, "retriever": true, "fresh": true, "trust": true}

// descriptor records an item's identity without its raw evidence text.
func descriptor(it model.ContextItem, rank int) map[string]any {
	pick := func(src map[string]any, keep map[string]bool) map[string]any {
		out := map[string]any{}
		for k, v := range src {
			if keep[k] {
				out[k] = v
			}
		}
		return out
	}
	key := it.DedupeKey()
	d := map[string]any{
		"source": it.Source, "dedupe_key": hex.EncodeToString(key[:]), "stale": it.Stale, "score": it.Score,
		"metadata": pick(it.Metadata, safeMetadataKeys), "provenance": pick(it.Provenance, safeProvenanceKeys),
	}
	if rank > 0 {
		d["rank"] = rank
	} else if it.Evidence != nil { // selected evidence keeps its envelope for later authorization
		d["evidence"] = it.Evidence
	}
	return d
}

func countBySource(items []model.ContextItem) map[string]int {
	out := map[string]int{}
	for _, it := range items {
		out[it.Source]++
	}
	return out
}

// record writes the trace and run journal for p and stores their paths in it.
// Failures degrade to empty paths: telemetry never blocks a brief.
func record(ctx context.Context, root, task string, cfg config.Config, p *Packet, candidates, selected []model.ContextItem,
	included []workspace.Repository, latency map[string]float64, stale int) {
	runID := newRunID()
	rels := make([]string, 0, len(included))
	for _, r := range included {
		rels = append(rels, r.RelativePath)
	}
	reg, _ := workspace.Load(root)
	repoSummary := workspace.Summarize(root, reg.Repositories)
	graphFP := GraphFingerprint(root, rels)
	policy := map[string]any{"config_digest": config.Digest(root), "retrieval_policy_version": strconv.Itoa(cfg.Version)}
	used := 0
	for _, it := range selected {
		used += len(it.Text)
	}
	r := &p.Retrieval
	r.RunID = runID
	trace := telemetry.Trace{
		Task: task, Lane: p.Lane, Risk: p.Risk, Intent: r.RetrievalIntent, RunID: runID,
		PolicyIdentity: policy, WorkspaceFingerprint: r.WorkspaceState.Fingerprint, GraphFingerprint: graphFP,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), ProvidersAttempted: r.ProvidersAttempted,
		ProvidersSkipped: r.ProvidersSkipped, StageLatencyMS: latency, Candidates: countBySource(candidates),
		Selected: countBySource(selected), Sufficiency: asMap(r.Sufficiency), Fallbacks: r.Fallbacks,
		StaleRejections: stale, BudgetChars: r.HardContextTokens * 4, UsedChars: used,
	}
	if path, err := telemetry.Write(ctx, root, cfg, trace); err == nil {
		r.Trace = path
	}

	ranked := make([]any, 0, len(candidates))
	for i, it := range candidates {
		ranked = append(ranked, descriptor(it, i+1))
	}
	evidence := make([]any, 0, len(selected))
	for _, it := range selected {
		evidence = append(evidence, descriptor(it, 0))
	}
	errorKinds := map[string]any{}
	for name := range r.ProviderErrors {
		errorKinds[name] = map[string]any{"kind": "provider_error", "timed_out": false}
	}
	retrievalRecord := map[string]any{
		"retrieval_intent": r.RetrievalIntent, "retrieval_reason": r.RetrievalReason, "evidence_state": r.EvidenceState,
		"providers_attempted": r.ProvidersAttempted, "providers_skipped": r.ProvidersSkipped, "provider_errors": errorKinds,
		"sufficiency": r.Sufficiency, "selective_retrieval": r.SelectiveRetrieval, "fallbacks": r.Fallbacks,
		"adaptive_context_tokens": r.AdaptiveContextTokens, "hard_context_tokens": r.HardContextTokens,
	}
	rec, err := journal.Build(map[string]any{
		"run_id": runID, "policy_identity": policy,
		"workspace_state":   map[string]any{"fingerprint": r.WorkspaceState.Fingerprint, "git_head": r.WorkspaceState.GitHead},
		"graph_state":       map[string]any{"fingerprint": graphFP, "repositories": rels},
		"repository_state":  map[string]any{"fingerprint": repoSummary.Fingerprint, "repository_count": len(reg.Repositories)},
		"changed_files":     p.ChangedFilesDetected,
		"provider_versions": map[string]any{},
		"retrieval":         retrievalRecord,
		"selected_evidence": evidence,
	}, []journal.Event{
		{Kind: "routing", Payload: p.RouteDecision},
		{Kind: "repository_routing", Payload: r.RepositoryRouting},
		{Kind: "retrieval", Payload: retrievalRecord},
		{Kind: "ranking", Payload: map[string]any{"algorithm_policy": "adaptive_math", "candidate_count": len(ranked), "candidates": ranked}},
		{Kind: "selection", Payload: map[string]any{"selected_count": len(evidence), "selected_evidence": evidence}},
		{Kind: "orchestration", Payload: r.Orchestration},
		{Kind: "authorization", Payload: p.Retrieval.AuthorizationPolicy},
	})
	if err != nil {
		return
	}
	if path, err := journal.Write(root, rec); err == nil {
		r.Journal = path
	}
}
