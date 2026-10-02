# Incremental Index Freshness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add hash-authoritative incremental index rebuilding that reparses only source files whose content or extractor semantics changed, while preserving repository isolation and atomic persistence.

**Architecture:** Upgrade the persisted index to version 2 with repository identity and extractor revision. Keep full and incremental builds behind one indexer API: every current source file is read and SHA-256 checked; unchanged rows are reused only from a valid v2 index; changed/new files are reparsed; removed paths are dropped; the complete result is atomically rewritten. Existing `Build`/`BuildWorkspace` remain source-compatible and default to `auto`.

**Tech Stack:** Go 1.26/1.27, stdlib SHA-256/filesystem APIs, existing `storage.WriteJSON`, current parser abstraction, optional `treesitter` build tag, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-09-30-incremental-index-freshness-design.md`

## Global Constraints

- `IndexVersion = 2`.
- SHA-256 is the only freshness authority; size/mtime are metadata only.
- No stat-only shortcut, filesystem watcher, fsmonitor, Git-index change detector, rename inference, SQLite, SCIP, or semantic retrieval in this stage.
- Default and `treesitter` parser builds must have different deterministic extractor revisions.
- Repository ID mismatch or extractor-revision mismatch makes prior index state non-reusable.
- Existing `Build` and `BuildWorkspace` APIs remain source-compatible and use `auto` mode.
- `context` must fail closed on stale/unsupported indexes.
- Existing Linux/Windows/macOS and Go 1.26/1.27 CI stays green.
- Existing retrieval/compatibility thresholds and context ceilings are unchanged.

## Review Focus

- Same-size content replacement with restored mtime must still reparse because SHA-256 changed.
- A file with zero extracted symbols must still be reusable without reparsing.
- Old symbols whose stored SHA does not match the reusable file state must force that file to reparse instead of silently reusing corrupt rows.
- A repository moved/replaced at the same relative path must not reuse the prior repository's index.
- Cancellation or read failure before persistence must leave the previous valid index bytes unchanged.

---

### Task 1: Index v2 identity and extractor revision

**Files:**
- Modify: `internal/indexer/indexer.go`
- Modify: `internal/indexer/search.go`
- Modify: `internal/indexer/parser.go`
- Create: `internal/indexer/extractor_revision_default.go`
- Create: `internal/indexer/extractor_revision_treesitter.go`
- Create/Modify: `internal/indexer/index_version_test.go`

**Interfaces:**
- Produces `const IndexVersion = 2`.
- Extends `Index` with `RepositoryID string` and `ExtractorRevision string`.
- Produces private `currentExtractorRevision() string` with distinct default and `treesitter` build-tag implementations.
- Produces private `loadRaw(controlRoot string, repo workspace.Repository) (Index, error)` for build-time migration decisions.
- `Load(controlRoot string, repo workspace.Repository) (Index, error)` becomes strict runtime validation.

- [ ] **Step 1: Write failing index-validation tests**

Add tests covering:
- `TestLoadRejectsV1Index`
- `TestLoadRejectsWrongRepositoryID`
- `TestLoadRejectsWrongExtractorRevision`
- `TestLoadAcceptsCurrentV2Index`
- `TestExtractorRevisionDiffersForTreeSitterBuild` in tagged/default-appropriate test files.

- [ ] **Step 2: Run targeted tests to verify RED**

Run:
```bash
go test ./internal/indexer -run 'TestLoad|TestExtractorRevision' -count=1
```
Expected: FAIL because v2 validation/revision APIs do not exist.

- [ ] **Step 3: Implement v2 schema and strict load validation**

Implement:
```go
const IndexVersion = 2

func currentExtractorRevision() string
func loadRaw(controlRoot string, repo workspace.Repository) (Index, error)
func Load(controlRoot string, repo workspace.Repository) (Index, error)
```

The Tree-sitter revision string must include the currently pinned parser family/version profile; default build must use a different deterministic value.

- [ ] **Step 4: Run indexer tests**

Run:
```bash
go test ./internal/indexer -count=1
go test -tags treesitter ./internal/indexer -count=1
```
Expected: PASS.

- [ ] **Step 5: Commit**

Commit message:
```text
feat: version index freshness state
```

### Task 2: Hash-authoritative incremental builder

**Files:**
- Modify: `internal/indexer/indexer.go`
- Create: `internal/indexer/incremental.go`
- Create: `internal/indexer/incremental_test.go`

**Interfaces:**
- Consumes Task 1's v2 schema, strict/raw loaders, extractor revision.
- Produces:
```go
type BuildMode string
const (
    BuildAuto BuildMode = "auto"
    BuildIncremental BuildMode = "incremental"
    BuildFull BuildMode = "full"
)

type BuildStats struct {
    RequestedMode string `json:"requested_mode"`
    EffectiveMode string `json:"effective_mode"`
    FullReason    string `json:"full_reason,omitempty"`
    Files         int    `json:"files"`
    Symbols       int    `json:"symbols"`
    Hashed        int    `json:"hashed"`
    Reparsed      int    `json:"reparsed"`
    Reused        int    `json:"reused"`
    Added         int    `json:"added"`
    Changed       int    `json:"changed"`
    Removed       int    `json:"removed"`
}

func BuildWithMode(ctx context.Context, controlRoot string, repo workspace.Repository, mode BuildMode) (Index, BuildStats, error)
```
- Existing `Build(...) (Index, error)` delegates to `BuildWithMode(..., BuildAuto)`.

- [ ] **Step 1: Write failing incremental-behavior tests**

Add tests:
- `TestIncrementalNoChangeReparsesZeroFiles`
- `TestIncrementalOneChangedFileReparsesExactlyOne`
- `TestIncrementalSameSizeSameMTimeContentChangeIsDetected`
- `TestIncrementalMetadataOnlyChangeReusesSymbols`
- `TestIncrementalAddsNewFile`
- `TestIncrementalRemovesDeletedFileAndSymbols`
- `TestIncrementalRenameIsRemovePlusAdd`
- `TestIncrementalZeroSymbolFileIsReusable`
- `TestIncrementalCorruptSymbolSHAForcesReparse`
- `TestFullAndIncrementalIndexesAreEquivalent`

- [ ] **Step 2: Run tests to verify RED**

Run:
```bash
go test ./internal/indexer -run 'TestIncremental|TestFullAndIncremental' -count=1
```
Expected: FAIL because `BuildWithMode`/stats do not exist.

- [ ] **Step 3: Implement one-read hash-authoritative scan and reuse**

Implementation rules:
- enumerate source files using existing exclusions/nested-repo logic;
- read each current file once;
- compute SHA-256 from those bytes;
- refresh `FileState` metadata from current stat data;
- reuse symbols only when previous index is reusable, path exists, SHA matches, and every previous symbol for that path has the same SHA;
- otherwise call the current parser on the bytes already read;
- remove rows for missing paths;
- sort final symbols identically to full build;
- assemble the complete new `Index` before `storage.WriteJSON`.

- [ ] **Step 4: Add invalid-old-state fallback tests**

Add:
- `TestAutoV1IndexFallsBackToFull`
- `TestIncrementalMalformedIndexFallsBackToFull`
- `TestIncrementalWrongRepositoryIDFallsBackToFull`
- `TestIncrementalChangedExtractorRevisionFallsBackToFull`

Assert `EffectiveMode == "full"` and a non-empty deterministic `FullReason`.

- [ ] **Step 5: Add atomic failure tests**

Add:
- `TestCancelledIncrementalBuildPreservesPreviousIndex`
- `TestIncrementalReadFailurePreservesPreviousIndex`

Capture previous index bytes before the failing build and assert exact byte equality afterward.

- [ ] **Step 6: Run full indexer tests**

Run:
```bash
go test ./internal/indexer -count=1
```
Expected: PASS.

- [ ] **Step 7: Commit**

Commit message:
```text
feat: add hash-authoritative incremental indexing
```

### Task 3: Workspace modes, CLI stats, and repository isolation

**Files:**
- Modify: `internal/indexer/indexer.go`
- Modify: `internal/cli/cli.go`
- Modify/Create: `internal/cli/cli_test.go`
- Modify: `internal/indexer/indexer_test.go`

**Interfaces:**
- Produces:
```go
type WorkspaceBuildResult struct {
    Index Index      `json:"index"`
    Stats BuildStats `json:"stats"`
}

func BuildWorkspaceWithMode(ctx context.Context, root string, reg workspace.Registry, mode BuildMode) (map[string]WorkspaceBuildResult, error)
```
- Existing `BuildWorkspace(...) (map[string]Index, error)` remains source-compatible and delegates to `auto`.
- CLI accepts exactly `--mode auto|incremental|full`; default is `auto`.

- [ ] **Step 1: Write failing CLI tests**

Add:
- `TestIndexCommandDefaultsToAuto`
- `TestIndexCommandAcceptsIncrementalAndFull`
- `TestIndexCommandRejectsUnknownMode`
- `TestIndexCommandRejectsUnexpectedPositionals`
- `TestIndexCommandOutputsPerRepositoryBuildStats`

- [ ] **Step 2: Write repository-isolation tests**

Add:
- `TestParentIncrementalIndexIgnoresNestedRepoChanges`
- `TestRepositoryIDReplacementForcesFullRebuild`

Use the existing nested-Git test pattern and workspace registry IDs.

- [ ] **Step 3: Run tests to verify RED**

Run:
```bash
go test ./internal/cli ./internal/indexer -run 'TestIndexCommand|TestParentIncremental|TestRepositoryIDReplacement' -count=1
```
Expected: FAIL because mode-aware workspace/CLI behavior is absent.

- [ ] **Step 4: Implement workspace and CLI mode handling**

`index` command behavior:
- parse `--mode` with default `auto`;
- reject invalid modes/extra args with exit code 2;
- load accepted repository registry;
- invoke `BuildWorkspaceWithMode`;
- emit JSON keyed by repository relative path with `BuildStats` fields.

- [ ] **Step 5: Run CLI/indexer tests**

Run:
```bash
go test ./internal/cli ./internal/indexer -count=1
```
Expected: PASS.

- [ ] **Step 6: Commit**

Commit message:
```text
feat: expose incremental index modes
```

### Task 4: Parser-profile freshness and benchmarks

**Files:**
- Create/Modify: `internal/indexer/incremental_treesitter_test.go`
- Modify: `.github/workflows/ci.yml`
- Create: `internal/indexer/incremental_benchmark_test.go`
- Create/Modify: `docs/INDEX_FRESHNESS.md`
- Modify: `docs/ROADMAP.md`

**Interfaces:**
- Consumes all prior tasks.
- No new production API.

- [ ] **Step 1: Add tagged parser-profile tests**

Under `//go:build treesitter`, add cases proving:
- Python Tree-sitter symbols survive no-change incremental reuse;
- JavaScript/TypeScript/TSX symbols survive reuse;
- a default-build index cannot be considered current under the Tree-sitter revision.

Default tests must also assert the inverse revision mismatch behavior.

- [ ] **Step 2: Add deterministic incremental counters to CI assertions**

Add a focused test fixture where:
- initial full build parses N files;
- no-change incremental run has `Hashed == N`, `Reparsed == 0`, `Reused == N`;
- one-file edit has `Reparsed == 1`, `Changed == 1`.

These counters—not wall-clock time—are CI gates.

- [ ] **Step 3: Add manual Go benchmarks**

Add:
```go
BenchmarkIndexFull
BenchmarkIndexIncrementalNoChange
BenchmarkIndexIncrementalOneChanged
```

Benchmarks report `ns/op`, allocations, and bytes allocated using Go's standard benchmark output.

- [ ] **Step 4: Update CI**

Retain all existing jobs and ensure:
- default Go 1.26/1.27 Linux/Windows/macOS tests pass;
- existing `treesitter` tagged lanes include incremental freshness tests;
- default `CGO_ENABLED=0` path remains green;
- retrieval benchmarks and V2 compatibility still run;
- CodeQL remains unchanged.

- [ ] **Step 5: Document migration and measured behavior**

`docs/INDEX_FRESHNESS.md` must record:
- v1 → v2 automatic full rebuild behavior;
- hash-authoritative freshness model;
- extractor-revision invalidation;
- CLI modes and stats definitions;
- benchmark command/results;
- deferred stat/fsmonitor fast path and its evidence trigger.

Update `docs/ROADMAP.md` to mark Stage 5 complete only after final verification.

- [ ] **Step 6: Run final local-equivalent verification**

Run:
```bash
go test ./...
go test -tags treesitter ./...
go vet ./...
go vet -tags treesitter ./...
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./cmd/ai-workflow
go run ./cmd/retrieval-bench --fixture benchmarks/retrieval/baseline.json --enforce
go run ./cmd/retrieval-bench --fixture benchmarks/retrieval/selector.json --enforce
go run ./cmd/compat-harness --cases compat/cases.json --fixtures compat/fixtures/v2-contracts.json --lock compat/v2.lock
go test ./internal/indexer -run '^$' -bench 'BenchmarkIndex(Full|Incremental)' -benchmem
```
Expected: all correctness commands exit 0; benchmark command completes and reports results without a hard wall-clock threshold.

- [ ] **Step 7: Commit**

Commit message:
```text
test: verify incremental index freshness
```

## Final Verification Before PR Readiness

- [ ] Compare final branch against the written spec requirement-by-requirement.
- [ ] Confirm `context` rejects stale v1/wrong-repository/wrong-extractor indexes.
- [ ] Confirm no-change incremental run reparses zero files.
- [ ] Confirm same-size/same-mtime changed content is detected.
- [ ] Confirm previous index bytes survive cancellation/read failure.
- [ ] Confirm default and Tree-sitter revisions cannot cross-reuse indexes.
- [ ] Confirm nested repositories remain isolated.
- [ ] Confirm all Go 1.26/1.27 Linux/Windows/macOS lanes, tagged parser tests, retrieval benchmarks, compatibility, build, and CodeQL are green.
- [ ] Record benchmark numbers in the PR body without adding a noisy hosted-runner performance gate.
- [ ] Run whole-branch review and fix Critical/Important issues before marking the PR ready.
