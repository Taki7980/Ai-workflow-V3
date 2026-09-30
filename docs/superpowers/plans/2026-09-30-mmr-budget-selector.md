# MMR + Budget-Aware Context Selector Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a deterministic, V2-compatible MMR context selector with hard token ceilings, required-evidence preservation, benchmark gates, and CLI integration.

**Architecture:** Keep raw BM25 retrieval unchanged. Add a reusable selector in `internal/retrieval` that consumes already-ranked candidates, deduplicates them, preserves required evidence, then fills the remaining hard token budget with deterministic MMR. Wire it into the existing `context` CLI and benchmark it separately from raw retrieval.

**Tech Stack:** Go 1.26/1.27, stdlib only, existing V3 BM25/RRF/Jaccard primitives, GitHub Actions, pinned V2 compatibility oracle.

**Spec:** `docs/superpowers/specs/2026-09-30-mmr-budget-selector-design.md`

## Global Constraints

- No new third-party runtime dependency.
- Preserve raw `indexer.Search` behavior and index file format.
- Preserve V2-compatible default config JSON exactly after canonicalization.
- Default MMR lambda is `0.70`.
- Default `max_selector_candidates` is `200`.
- Hard context budget must never be exceeded.
- Mandatory required evidence must be preserved or selection must fail explicitly.
- No embeddings, learned pruning, Tree-sitter, SCIP, CRG execution, telemetry, or SQLite in this PR.
- Go 1.26.x and Go 1.27.x must remain green on Linux, Windows, and macOS.
- Existing compatibility and raw retrieval benchmark thresholds may not be weakened.

## Review Focus

- Duplicate candidates where the lower-ranked duplicate is marked required: dedupe must OR the required flag and preserve fail-safe token accounting.
- A highest-MMR candidate that does not fit while a smaller lower-MMR candidate does fit: selector must continue searching rather than stop early.
- Required evidence total fits token budget but exceeds candidate cap: explicit candidate-limit error, never silent dropping.
- Equal relevance/MMR scores across platforms: original input order must be the final tie-break.
- Zero-byte indexed files: CLI accounting must still assign a positive token estimate and never create a free candidate.

---

### Task 1: Core deterministic selector

**Files:**
- Create: `internal/retrieval/selector.go`
- Create: `internal/retrieval/selector_test.go`

**Interfaces:**
- Consumes: existing `retrieval.Tokenize(string) []string` and `retrieval.Jaccard(map[string]struct{}, map[string]struct{}) float64`
- Produces:
  - `type Candidate[T any]`
  - `type SelectorOptions`
  - `type Selection[T any]`
  - `var ErrRequiredEvidenceOverBudget error`
  - `var ErrRequiredEvidenceOverCandidateLimit error`
  - `func SelectMMR[T any](candidates []Candidate[T], options SelectorOptions) (Selection[T], error)`

- [ ] **Step 1: Write failing selector tests**
  - `TestSelectMMR_DiversifiesRedundantCandidates`
  - `TestSelectMMR_NeverExceedsHardBudget`
  - `TestSelectMMR_SkipsOversizedBestCandidateForSmallerFit`
  - `TestSelectMMR_DeduplicatesByKeyDeterministically`
  - `TestSelectMMR_PreservesRequiredEvidence`
  - `TestSelectMMR_RequiredEvidenceOverBudgetFails`
  - `TestSelectMMR_RequiredEvidenceOverCandidateLimitFails`
  - `TestSelectMMR_StableTieBreakByInputOrder`
  - `TestSelectMMR_RejectsInvalidOptionsAndCandidates`

- [ ] **Step 2: Run tests to verify RED**

Run:
```bash
go test ./internal/retrieval -run TestSelectMMR -count=1
```

Expected: FAIL because `SelectMMR` and selector types do not exist.

- [ ] **Step 3: Implement minimal selector**

Implement the spec exactly:
- validate options/candidates;
- deterministic dedupe by key;
- required OR semantics;
- maximum token estimate on duplicate keys;
- required-first handling when mandatory;
- candidate-cap handling;
- one-time token-set construction;
- normalized relevance;
- greedy MMR with `lambda`;
- skip non-fitting candidates and continue;
- stable deterministic tie-break;
- return `Selection.UsedTokens <= MaxTokens`.

- [ ] **Step 4: Run selector tests and full retrieval tests**

Run:
```bash
go test ./internal/retrieval -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Commit message:
```text
feat: add deterministic MMR selector
```

### Task 2: Typed selector configuration

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Consumes: Task 1 does not depend on config.
- Produces:
  - `type Selector struct`
  - `Context.Selector Selector`
  - validation for selector fraction/candidate cap.

- [ ] **Step 1: Write failing config tests**
  - `TestDefaultSelectorMatchesV2Contract`
  - `TestSelectorValidationRejectsInvalidTightBudgetFraction`
  - `TestSelectorValidationRejectsInvalidCandidateCap`
  - retain existing canonical V2 default-document parity.

- [ ] **Step 2: Run tests to verify RED**

Run:
```bash
go test ./internal/config -count=1
```

Expected: FAIL because typed selector config is absent.

- [ ] **Step 3: Implement typed selector config**

Exact defaults:
- `Enabled: true`
- `TightBudgetFraction: 0.3`
- `MandatoryStructuralEvidence: true`
- `MaxSelectorCandidates: 200`

Validation:
- fraction `> 0 && <= 1`;
- candidate cap `> 0`.

Remove duplicate manual selector insertion from `DefaultDocument()` only if marshaling typed config produces the exact same canonical JSON shape.

- [ ] **Step 4: Run config + compatibility unit tests**

Run:
```bash
go test ./internal/config ./internal/compat -count=1
```

Expected: PASS and default config fixture parity preserved.

- [ ] **Step 5: Commit**

Commit message:
```text
feat: type selector configuration
```

### Task 3: Extend V2 differential harness with MMR parity

**Files:**
- Modify: `compat/cases.json`
- Modify: `compat/fixtures/v2-contracts.json`
- Modify: `tools/compat/capture_v2.py`
- Modify: `internal/compat/harness.go`
- Modify: `internal/compat/harness_test.go`

**Interfaces:**
- Consumes: Task 1 `SelectMMR`.
- Produces: frozen `mmr` contract section with selected key ordering from V2 oracle.

- [ ] **Step 1: Add failing V3 compatibility test for MMR ordering**

Add a deterministic case with:
- query: `payment retry policy`;
- fixed candidate texts;
- fixed positive relevance scores;
- `lambda = 0.70`;
- fixed `max_items`;
- expected order omitted initially from V3 implementation path so test fails.

- [ ] **Step 2: Run compatibility test to verify RED**

Run:
```bash
go test ./internal/compat -count=1
```

Expected: FAIL because the harness does not yet compare MMR contracts.

- [ ] **Step 3: Extend V2 capture tool and frozen fixture**

Use V2 `maximal_marginal_relevance` to generate selected candidate keys from the pinned V2 commit. Add the result under a new `mmr` contract section.

- [ ] **Step 4: Extend V3 harness**

Map the frozen MMR case into Task 1's `SelectMMR` with effectively unbounded token costs for parity ordering, then compare selected key order exactly.

- [ ] **Step 5: Run compatibility tests**

Run:
```bash
go test ./internal/compat ./internal/retrieval -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

Commit message:
```text
test: add V2 MMR parity contract
```

### Task 4: CLI selector integration and selector benchmark

**Files:**
- Modify: `internal/cli/cli.go`
- Create/Modify: `internal/cli/cli_test.go`
- Modify: `internal/eval/retrieval.go`
- Modify: `internal/eval/retrieval_test.go`
- Create: `benchmarks/retrieval/selector.json`
- Modify: `benchmarks/README.md`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes:
  - Task 1 `SelectMMR`;
  - Task 2 typed selector config.
- Produces:
  - budget-aware `context` command retaining hit-array JSON schema;
  - selector benchmark reporting raw and selected metrics.

- [ ] **Step 1: Write failing CLI tests**
  - `TestContextCommandRetainsHitArraySchema`
  - `TestContextCommandRespectsAnswerLaneTokenBudget`
  - `TestContextCommandSelectorErrorIsNonZero`
  - zero-byte file token estimate is at least one.

- [ ] **Step 2: Write failing selector benchmark test**

Add redundant-file fixture and assert:
- selected Recall@K >= raw Recall@K;
- selected MRR >= raw MRR;
- selected context yield improves >= 0.10 absolute;
- selected avg tokens <= 75% raw;
- no-gold FP <= 0.50;
- every selected case respects budget.

- [ ] **Step 3: Run targeted tests to verify RED**

Run:
```bash
go test ./internal/cli ./internal/eval -count=1
```

Expected: FAIL because CLI/eval selector integration does not exist.

- [ ] **Step 4: Implement CLI integration**

`context` command:
- load config;
- retrieve up to `MaxSelectorCandidates`;
- dedupe file-level candidates using repository + path;
- estimate tokens as `ceil(file_bytes/4)`, minimum one;
- use Answer-lane `EstimatedTokens` as hard max;
- preserve original `[]indexer.Hit` JSON output;
- surface selector errors to stderr and return non-zero.

- [ ] **Step 5: Implement selector benchmark path**

Keep raw baseline unchanged. Add selector fixture and report raw vs selected metrics using the same metric definitions.

- [ ] **Step 6: Run targeted tests**

Run:
```bash
go test ./internal/cli ./internal/eval -count=1
```

Expected: PASS.

- [ ] **Step 7: Update CI**

Add selector benchmark enforcement while retaining raw baseline enforcement.

- [ ] **Step 8: Run full suite**

Run:
```bash
go test ./...
go vet ./...
go run ./cmd/retrieval-bench --fixture benchmarks/retrieval/baseline.json --enforce
go run ./cmd/retrieval-bench --fixture benchmarks/retrieval/selector.json --enforce
go run ./cmd/compat-harness --cases compat/cases.json --fixtures compat/fixtures/v2-contracts.json --lock compat/v2.lock
```

Expected: all commands exit 0.

- [ ] **Step 9: Commit**

Commit message:
```text
feat: enforce budget-aware context selection
```

## Final Verification

Before PR readiness:

- run full GitHub CI matrix;
- verify compatibility oracle regeneration and V3 parity;
- verify raw retrieval benchmark remains green;
- verify selector benchmark quality/cost gates;
- verify CodeQL;
- review whole branch against spec and this plan;
- fix Critical/Important findings with RED→GREEN tests;
- record any deferred Minor findings.

