# MMR + Budget-Aware Context Selector Design

**Date:** 2026-09-30  
**Repository:** `Taki7980/Ai-workflow-V3`  
**Base:** `5c4b7b237680d5d0d4be718c993589130bc9ab92`  
**Tracker:** #2  
**Status:** design approved in chat; written spec awaiting review

## 1. Goal

Add a deterministic context-selection stage after retrieval that improves evidence diversity and token efficiency without changing retrieval generation, weakening V2 compatibility, or increasing lane context ceilings.

The selector must:

- preserve high-relevance evidence;
- reduce redundant evidence;
- enforce a hard token ceiling;
- preserve required structural evidence when configured;
- fail closed when required evidence cannot fit;
- remain deterministic across platforms;
- remain dependency-free;
- expose measurable quality/cost changes through the existing retrieval benchmark.

This PR does **not** add embeddings, learned pruning, Tree-sitter, SCIP, CRG, or adaptive policy.

## 2. Current State

V3 currently has:

- deterministic BM25 search in `internal/indexer/search.go`;
- BM25, RRF, and Jaccard primitives in `internal/retrieval/math.go`;
- a V2-compatible configuration document that already contains selector fields;
- a retrieval benchmark measuring Recall@K, MRR, file F1, context yield, average retrieved tokens, and no-gold false-positive rate;
- a pinned V2 compatibility harness.

The current `ai-workflow context` command directly returns up to six BM25 hits and has no diversity reranking or hard context-budget selection.

The index already stores per-file byte size in `indexer.FileState.Size`. This can provide a deterministic conservative token estimate without reading full file contents.

## 3. Latest Evidence Considered

Research was refreshed on 2026-09-30.

### Agent Retrieval Bench — July 2026

Agent Retrieval Bench evaluates repository-context retrieval across `code2test`, `comment2context`, `trace2code`, `edit2ripple`, natural no-gold, and wrong-repository controls. It reports that no single retrieval family dominates; RepoMap performs best on 8K-token budgeted context yield while other methods lead ranking metrics. Logged agents miss every gold file on 27–35% of cases.

Implication for V3: context yield must be measured independently from rank quality, and context-budget efficiency is a first-class metric.

Source: https://arxiv.org/abs/2607.24882

### ContextBench — February 2026

ContextBench reports that coding agents commonly favor recall over precision and that explored context substantially exceeds utilized context.

Implication for V3: selecting less but more useful evidence is justified; blindly expanding context is not.

Source: https://arxiv.org/abs/2602.05892

### CORE-Bench — June 2026

CORE-Bench shows a large quality drop from conventional code search to repository-level agentic retrieval.

Implication for V3: keep retrieval and selection as separately measurable stages; do not assume a better ranker alone solves context quality.

Source: https://arxiv.org/abs/2606.11864

### Context Pruning for Coding Agents — May 2026

Recent learned pruning work reports substantial token reduction with small or positive accuracy effects, but requires additional learned models and structured labeling.

Implication for V3: token pruning is valuable, but a learned pruner is unjustified at this migration stage. A deterministic selector gives a lower-risk baseline that later learned methods would have to beat.

Source: https://arxiv.org/abs/2605.15315

### ContextSniper — July 2026

ContextSniper reports sizable token reductions through targeted evidence selection, with a small decrease in submitted-resolution rate.

Implication for V3: token savings must never be accepted without paired quality gates.

Source: https://arxiv.org/abs/2607.01916

### Go status

Go 1.27.1 is the latest stable patch as of 2026-09-30. V3 will continue compiling against both Go 1.26.x and 1.27.x during migration. This PR requires no Go 1.27-only language feature.

Sources:
- https://go.dev/doc/go1.27
- https://go.dev/doc/devel/release

## 4. Design Choice

Use a deterministic, V2-aligned greedy maximal marginal relevance selector with an explicit token budget.

For each remaining candidate:

```text
MMR = lambda * normalized_relevance
      - (1 - lambda) * max_similarity_to_selected
```

Defaults:

- `lambda = 0.70`;
- `max_selector_candidates = 200`;
- hard `max_tokens` supplied by the caller;
- similarity = Jaccard over V3's existing tokenizer output;
- deterministic stable tie-breaking by original candidate order.

This follows V2's relevance/diversity behavior while adding explicit budget semantics.

## 5. Package Boundary

Create `internal/retrieval/selector.go`.

The selector remains independent of the indexer and CLI.

### Public package API

```go
type Candidate[T any] struct {
    Key             string
    Text            string
    Value           T
    Relevance       float64
    EstimatedTokens int
    Required        bool
}

type SelectorOptions struct {
    MaxTokens          int
    MaxCandidates      int
    Lambda             float64
    MandatoryRequired  bool
}

type Selection[T any] struct {
    Items      []Candidate[T]
    UsedTokens int
}

var ErrRequiredEvidenceOverBudget error
var ErrRequiredEvidenceOverCandidateLimit error

func SelectMMR[T any](
    candidates []Candidate[T],
    options SelectorOptions,
) (Selection[T], error)
```

No exported interface or abstraction layer is added beyond this function and data contract.

## 6. Candidate Normalization

Before MMR:

1. reject invalid options:
   - `MaxTokens <= 0`;
   - `MaxCandidates <= 0`;
   - `Lambda < 0 || Lambda > 1`;
2. reject candidates with:
   - empty `Key`;
   - non-positive `EstimatedTokens`;
3. deduplicate by `Key`;
4. for duplicates:
   - keep the candidate with the highest relevance;
   - on equal relevance keep the earliest candidate;
   - `Required` is ORed across duplicates;
   - token estimate becomes the maximum observed estimate for fail-safe budgeting;
5. preserve deterministic original-order information for tie-breaking.

## 7. Candidate Cap

The candidate cap exists to bound the O(n²) greedy MMR step.

Rules:

- required candidates always participate;
- if required candidate count alone exceeds `MaxCandidates`, return `ErrRequiredEvidenceOverCandidateLimit`;
- otherwise include all required candidates plus the highest-relevance non-required candidates until `MaxCandidates` is reached;
- relevance ties preserve original input order.

No candidate cap may silently discard required evidence.

## 8. Required Evidence

When `MandatoryRequired == true`:

1. all required candidates are selected before optional candidates;
2. required candidates are ordered by relevance descending, then original order;
3. if required token total exceeds `MaxTokens`, return `ErrRequiredEvidenceOverBudget`;
4. the function must not truncate required evidence to make the request fit.

When `MandatoryRequired == false`, `Required` is only metadata and all candidates participate normally.

This is the future integration seam for CRG/SCIP structural evidence.

## 9. Budget-Aware MMR Fill

After required evidence has been placed:

1. normalize non-required relevance scores by the maximum positive relevance in the candidate set;
2. tokenize candidate `Text` once and reuse token sets;
3. repeatedly compute MMR for unselected candidates;
4. among candidates that fit the remaining budget, select the highest MMR score;
5. ties resolve by:
   - higher raw relevance;
   - earlier input order;
6. if the current best candidate does not fit, continue evaluating smaller candidates rather than terminating selection;
7. stop when:
   - no remaining candidate fits;
   - no candidate remains;
   - the token budget is exhausted.

The final `UsedTokens` must always be `<= MaxTokens`.

## 10. Token Estimation

The selector consumes explicit token estimates and does not own tokenization for accounting.

For the current CLI integration, estimate file tokens conservatively from indexed file bytes:

```text
estimated_tokens = ceil(file_bytes / 4)
```

Reasons:

- deterministic;
- no model-specific tokenizer dependency;
- does not require reading source files;
- errs toward conservative budgeting for ordinary source text.

The selector API allows future callers to provide better model-specific estimates without changing selection logic.

A zero-byte indexed file is assigned one token for accounting so every candidate has a positive budget cost.

## 11. CLI Integration

Modify `ai-workflow context` so it:

1. loads the current config;
2. retrieves a wider deterministic BM25 candidate pool, capped by `context.selector.max_selector_candidates`;
3. deduplicates file candidates by repository + path;
4. builds selector candidates using:
   - key = repository + NUL + path;
   - text = symbol name + kind + path;
   - relevance = BM25 hit score;
   - estimated tokens from indexed file size;
   - required = false for this PR;
5. selects under the lane-independent context budget chosen for the read-only `context` command.

For this PR, the command uses the **Answer lane estimated-token budget** from config because `context` is a read-only retrieval command with no task classifier input.

The JSON output remains an array of `indexer.Hit` values so existing CLI consumers do not receive a schema break.

Raw `indexer.Search` behavior remains unchanged.

## 12. Typed Configuration

Add the already-existing V2-compatible selector shape to typed config:

```go
type Selector struct {
    Enabled                     bool
    TightBudgetFraction         float64
    MandatoryStructuralEvidence bool
    MaxSelectorCandidates       int
}
```

Default values must remain exactly:

- `enabled: true`;
- `tight_budget_fraction: 0.3`;
- `mandatory_structural_evidence: true`;
- `max_selector_candidates: 200`.

Validation:

- `tight_budget_fraction` must be `> 0 && <= 1`;
- `max_selector_candidates` must be `> 0`.

The complete `DefaultDocument()` JSON must remain byte-semantically compatible with the pinned V2 fixture after canonicalization.

## 13. V2 Compatibility Harness Extension

Extend the compatibility harness with deterministic V2 MMR fixtures.

The V2 oracle must generate expected order from `maximal_marginal_relevance` using fixed:

- candidate texts;
- relevance scores;
- query;
- `lambda = 0.70`;
- `max_items`.

V3 must produce the same selected key order for the parity case.

The fixture covers only pure MMR ordering. V3's explicit hard-budget behavior is a V3 safety extension and is tested separately.

## 14. Benchmark Extension

Keep `benchmarks/retrieval/baseline.json` unchanged as the raw lexical baseline.

Add a selector-focused fixture containing deliberately redundant files and varied file sizes.

Report both:

- raw candidate metrics;
- selected metrics.

Required acceptance gates for the selector fixture:

- selected Recall@K >= raw Recall@K;
- selected MRR >= raw MRR;
- selected context yield improves by at least 0.10 absolute;
- selected average retrieved tokens <= 75% of raw candidate tokens;
- no-gold false-positive rate does not exceed the existing 0.50 baseline;
- every case respects its hard token budget.

Thresholds must not be weakened to make the PR pass.

## 15. Determinism

The selector must return identical ordering on Linux, macOS, and Windows for identical inputs.

Do not depend on:

- map iteration order;
- filesystem iteration order;
- timestamps;
- randomness;
- floating-point epsilon-sensitive unordered comparisons.

Stable input order is the final tie-break.

## 16. Error Handling

Invalid selector input returns descriptive errors.

Safety-significant failures are explicit:

- required evidence exceeds token budget;
- required evidence exceeds candidate cap.

The CLI should surface selector errors to stderr and exit non-zero rather than silently falling back to unbounded raw results.

## 17. Testing Strategy

### Unit tests

`internal/retrieval/selector_test.go`:

- redundant candidates are diversified;
- hard token budget is never exceeded;
- oversized top candidate can be skipped for smaller fitting evidence;
- duplicate keys collapse deterministically;
- required evidence is retained;
- required evidence over budget errors;
- required evidence over candidate cap errors;
- lambda boundaries are deterministic;
- equal scores have stable ordering;
- invalid token estimates/options fail.

### Config tests

`internal/config/config_test.go`:

- selector defaults match V2;
- selector validation rejects invalid fraction/cap;
- additive V2 config still loads.

### CLI tests

Add focused CLI tests for:

- context output remains the existing hit-array schema;
- selected output respects the Answer lane budget;
- selector error becomes non-zero exit.

### Compatibility tests

Pinned V2 MMR fixture regenerates exactly and V3 ordering matches.

### Benchmark

Run raw baseline unchanged and selector fixture with quality/cost gates.

## 18. CI Gates

PR cannot be merge-ready until all pass:

- `go test ./...`;
- `go vet ./...`;
- Go 1.26.x Linux/Windows/macOS;
- Go 1.27.x Linux/Windows/macOS;
- build;
- compatibility harness;
- raw retrieval benchmark;
- selector benchmark;
- CodeQL.

No context budget increase is allowed in this PR.

## 19. Non-Goals

Not in this PR:

- semantic embeddings;
- vector database;
- learned relevance/pruning model;
- adaptive or learned lambda;
- task-conditioned lambda;
- Tree-sitter;
- SCIP;
- CRG execution;
- evidence sufficiency scoring;
- telemetry/replay;
- SQLite state;
- workflow engine;
- durable memory;
- new third-party dependency.

## 20. Rollback

Rollback is simple:

- `indexer.Search` remains unchanged;
- selector is isolated in `internal/retrieval`;
- CLI integration can be reverted without changing index format;
- config document remains V2-compatible.

No persistent data migration is introduced.

## 21. Success Definition

This stage succeeds only if V3 gains a deterministic, reusable context selector that:

- matches V2 MMR ordering on frozen parity fixtures;
- never exceeds a hard token budget;
- preserves mandatory evidence or fails explicitly;
- materially improves context efficiency on the selector benchmark;
- does not regress existing retrieval quality gates;
- keeps all cross-platform, compatibility, and security checks green.
