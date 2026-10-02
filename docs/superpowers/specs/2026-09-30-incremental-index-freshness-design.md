# Incremental Index Freshness Design

**Date:** 2026-09-30  
**Repository:** `Taki7980/Ai-workflow-V3`  
**Base:** `76424fb986e93a8b78e7641a2c16d8f164e47c89`  
**Tracker:** #2  
**Stage:** 5 — Incremental index freshness

## 1. Goal

Replace unconditional full index rebuilds with a deterministic incremental path that reuses symbol extraction only when source content and extractor semantics are provably unchanged.

The design must preserve:

- SHA-256 as freshness authority;
- multi-repository isolation;
- nested Git/worktree behavior;
- parser correctness across default and `treesitter` builds;
- atomic index persistence;
- deterministic symbol ordering;
- safe migration from the existing v1 index;
- the current raw retrieval/search contract.

The optimization target is **parser work**, not correctness checks.

## 2. Current State

V3 currently persists one JSON index per repository:

```text
version
repository
built_at
files[path] -> sha256, size, mtime_ns
symbols[]
```

`Build` currently:

1. scans every supported source file;
2. reads every file;
3. computes SHA-256;
4. parses every file;
5. rewrites the complete index.

The merged parser stage adds an important new freshness dimension:

- default build: Go AST + regex fallback;
- `treesitter` build: Go AST + Tree-sitter Python/JavaScript/TypeScript/TSX + regex fallback.

A source file can therefore have an unchanged SHA while its extracted symbols become stale because parser code, parser profile, or grammar versions changed.

The current `Load` function also accepts any decodable index without validating version, repository identity, or parser/extractor revision.

## 3. Evidence and Design Implications

Research was refreshed on 2026-09-30.

### Git racy-index behavior

Git records filesystem stat data to avoid expensive content checks, but its own documentation describes the “racy Git” case: a file can change in place without size or observable mtime changing, causing stat metadata to appear clean even when content changed.

Source: https://git-scm.com/docs/racy-git

**Implication:** V3 must not use `size + mtime` as freshness authority.

### Git filesystem monitoring

Git supports fsmonitor and untracked-cache optimizations to reduce filesystem scanning/stat work in large working trees.

Source: https://git-scm.com/docs/git-update-index

**Implication:** watchers and stat caches are legitimate later optimizations, but they are hints. Stage 5 should establish a correct content-hash baseline first.

### 2026 repository indexing work

Recent repository-indexing work continues to show that parsing and semantic/index construction are meaningful costs at repository scale and benefit from avoiding repeated extraction work.

Reference: https://arxiv.org/abs/2604.18413

**Implication:** reusing parsed symbol rows for content-identical files is useful even when every source file is still hashed.

### V2 behavior

V2 already has incremental indexing semantics, including changed/new/removed file handling and SHA-256 freshness checks. Its default incremental path can skip hashing when stat metadata matches, with an optional strict-hash mode.

V3 intentionally diverges at this stage: **hash every current source file in incremental mode**. This is simpler and avoids importing V2's stat/racy-file complexity before profiling proves hashing itself is the bottleneck.

## 4. Alternatives Considered

### A. Hash-authoritative incremental parsing — selected

Scan and hash every current source file. Reparse only files whose digest changed, whose path is new, or whose previous symbol state is invalid.

**Advantages**
- simple correctness model;
- catches same-size/same-mtime content changes;
- works for Git and non-Git roots;
- includes untracked files naturally;
- no watcher state;
- portable across Linux, macOS, and Windows.

**Cost**
- still performs O(total source bytes) reads/hashing.

### B. Stat-first cache with racy-file defense — deferred

Use size/mtime as a fast path, with Git-like safeguards for potentially racy entries.

**Advantage:** fewer file reads.

**Why not now:** substantially more state and edge-case logic. Stage 5 must first measure whether hashing is actually a meaningful bottleneck.

### C. Git diff/fsmonitor/watcher-driven indexing — deferred

Use Git/fsmonitor or native filesystem events to determine changed paths.

**Why not now:** V3 supports untracked files, non-Git workspace parents, nested repositories, and worktrees. Cross-platform event loss/recovery would create another correctness subsystem.

## 5. Persistent Index v2

Introduce:

```go
const IndexVersion = 2

type Index struct {
    Version           int                  `json:"version"`
    Repository        string               `json:"repository"`
    RepositoryID      string               `json:"repository_id"`
    ExtractorRevision string               `json:"extractor_revision"`
    BuiltAt           string               `json:"built_at"`
    Files             map[string]FileState `json:"files"`
    Symbols           []Symbol             `json:"symbols"`
}
```

### Repository identity

`RepositoryID` must equal the accepted workspace registry repository ID.

If the relative path is reused by another repository identity, the prior index is not reusable.

### Extractor revision

The index must encode the extraction semantics used to produce its symbols.

Use one shared schema revision plus a build-profile revision.

Conceptually:

```text
symbols-v1:go-ast+regex
symbols-v1:go-ast+tree-sitter-python@0.25.0+javascript@0.25.0+typescript@0.23.2+regex
```

The exact string is deterministic and compiled into the binary.

The Tree-sitter profile must distinguish the currently pinned grammar/runtime versions from the default build. Any extraction behavior change or dependency upgrade that can alter emitted symbols must bump the revision.

**Hard rule:** same file SHA + different extractor revision = reparse.

## 6. Index Loading and Validation

Split raw decoding from current-index validation.

### Raw decode

A private loader may decode an existing index of any version so the builder can decide whether it is reusable.

Malformed JSON is treated as non-reusable state.

### Runtime `Load`

`Load(controlRoot, repo)` must fail closed unless all are true:

- `Version == IndexVersion`;
- `Repository == repo.RelativePath`;
- `RepositoryID == repo.RepositoryID`;
- `ExtractorRevision == currentExtractorRevision()`.

This prevents `context` from silently consuming stale v1 or wrong-parser indexes.

## 7. Build Modes

Add:

```go
type BuildMode string

const (
    BuildAuto        BuildMode = "auto"
    BuildIncremental BuildMode = "incremental"
    BuildFull        BuildMode = "full"
)
```

Semantics:

### `auto`

- reusable current v2 index exists → incremental;
- missing, malformed, v1, wrong repository ID, or wrong extractor revision → full.

### `incremental`

- reusable current v2 index exists → incremental;
- otherwise → safe full rebuild, with the reason reported.

Explicit incremental mode never attempts partial reuse from an invalid index.

### `full`

Ignore previous index content and parse every current source file.

Existing `Build` and `BuildWorkspace` APIs remain source-compatible and use `auto`.

New mode-aware APIs provide statistics for the CLI.

## 8. Incremental Algorithm

For one repository:

1. resolve the repository root;
2. enumerate supported source files using the existing exclusions and nested-Git-root skip rules;
3. load the previous index for reuse eligibility;
4. read each current source file exactly once for this build;
5. compute SHA-256 from those bytes;
6. compare the digest with the previous `FileState`;
7. reuse old symbols only when all are true:
   - previous index is reusable;
   - the same relative path existed;
   - SHA-256 matches;
   - old symbol rows for the path are internally consistent with that file digest;
8. otherwise run the current parser pipeline on the already-read bytes;
9. files absent from the new scan are removed along with their symbols;
10. sort symbols using the current deterministic path/line order;
11. atomically persist the complete new v2 index.

### Important accounting rule

`size` and `mtime_ns` are refreshed from current filesystem metadata but never determine reuse.

Therefore:

- metadata-only change + same SHA → symbols reused;
- same-size content change + new SHA → reparsed;
- same-mtime content change + new SHA → reparsed.

## 9. Rename Semantics

Stage 5 does not infer renames.

A rename is:

```text
old path → removed
new path → added + reparsed
```

Even if the digest is identical, path is part of symbol identity, so re-emitting rows is the simplest correct behavior.

Digest-based rename reuse is deferred unless profiling demonstrates meaningful value.

## 10. Build Statistics

Each repository build reports:

```go
type BuildStats struct {
    RequestedMode string `json:"requested_mode"`
    EffectiveMode string `json:"effective_mode"`
    FullReason    string `json:"full_reason,omitempty"`

    Files    int `json:"files"`
    Symbols  int `json:"symbols"`
    Hashed   int `json:"hashed"`
    Reparsed int `json:"reparsed"`
    Reused   int `json:"reused"`
    Added    int `json:"added"`
    Changed  int `json:"changed"`
    Removed  int `json:"removed"`
}
```

Definitions:

- `Hashed`: every successfully read current source file;
- `Reparsed`: files sent through symbol extraction;
- `Reused`: content-identical files whose previous symbol rows were reused;
- `Added`: current paths absent from reusable old state;
- `Changed`: same path with different SHA;
- `Removed`: old paths absent from current scan.

For a no-change valid incremental run:

```text
hashed == files
reparsed == 0
reused == files
added == changed == removed == 0
```

This is the primary deterministic efficiency gate.

## 11. CLI

Extend:

```bash
ai-workflow index --mode auto
ai-workflow index --mode incremental
ai-workflow index --mode full
```

No `--mode` remains equivalent to `auto`.

Reject unsupported modes and unexpected positional arguments with exit code 2.

Output remains JSON, now with per-repository `BuildStats`.

No `strict-hash` flag is added because Stage 5 incremental mode is always content-hash authoritative.

## 12. Workspace and Multi-Repo Rules

- only `Included` registry repositories are indexed;
- each repository's old state is considered independently;
- nested repositories remain excluded from their parent scan;
- worktrees continue to be independent accepted registry entries;
- repository-ID mismatch prevents old-state reuse;
- one repository failing must preserve its previous valid index file.

The current index-directory naming scheme is unchanged in this PR. Repository-ID validation prevents wrong-state reuse. Collision-proof directory naming is a separate migration concern and should not be bundled into Stage 5.

## 13. Parser Profile Correctness

Because Stage 4 introduced an opt-in Tree-sitter build, Stage 5 must test both parser profiles.

A default-built index cannot be reused by a Tree-sitter-enabled binary, and vice versa, because their extractor revisions differ.

The same rule applies to future parser/grammar upgrades.

This is more important than file freshness alone: **index freshness = content freshness + extraction-semantics freshness**.

## 14. Atomicity and Failure Behavior

Reuse the existing `storage.WriteJSON` path, which writes to a temporary file, fsyncs, closes, and renames atomically.

Rules:

- no persistent index mutation occurs until the complete new index is assembled;
- cancellation/read failure/parser-fatal error before persistence leaves the prior index untouched;
- malformed prior index causes safe full rebuild in build commands;
- runtime `Load` rejects malformed/unsupported state rather than returning partial data.

## 15. Compatibility with V2

V3 intentionally preserves V2 semantic expectations:

- changed content invalidates prior extracted rows;
- deleted files remove rows;
- new files are indexed;
- SHA-256 is authoritative for row freshness.

V3 intentionally does **not** copy V2's stat-first default optimization.

The pinned differential compatibility harness should not compare persistent index JSON because the V2 and V3 storage formats are intentionally different.

Instead, V3 tests should port the behavioral cases and explicitly document the divergence.

## 16. Testing

### Index version / migration

- v1 index + `auto` → full rebuild to v2;
- malformed index + `auto` → full rebuild;
- wrong repository ID → full rebuild;
- changed extractor revision → full rebuild;
- runtime `Load` rejects v1/wrong-repository/wrong-extractor state.

### Incremental correctness

- full and incremental results have equivalent files/symbols ignoring `BuiltAt`;
- no-change run: `reparsed == 0`;
- one modified file: exactly one reparse;
- same-size content change is detected;
- forced same mtime with changed content is detected;
- metadata-only touch with identical bytes reparses zero;
- new file increments `Added`;
- deleted file and symbols disappear;
- rename behaves as one removal + one addition;
- file with zero symbols is reusable without reparsing;
- stale/corrupt symbol SHA for an otherwise unchanged file forces reparse.

### Repository isolation

- parent index still excludes nested Git repository files;
- child repository changes do not invalidate parent index;
- changed repository identity cannot reuse old index.

### Parser profiles

- Go AST symbols survive reuse;
- regex symbols survive reuse;
- Tree-sitter Python/JS/TS/TSX symbols survive reuse in tagged tests;
- default and Tree-sitter extractor revisions differ.

### Failure/atomicity

- cancelled build returns error and leaves previous index bytes unchanged;
- failed file read leaves previous index unchanged.

## 17. Benchmarking

CI gates deterministic work counters, not wall-clock timing.

Add manual Go benchmarks for:

- full rebuild;
- no-change incremental rebuild;
- one-file-changed incremental rebuild.

PR reporting should include:

- ns/op;
- allocations/op;
- bytes allocated;
- `reparsed/files` ratio.

No wall-clock performance threshold is enforced in GitHub Actions because hosted-runner timing is noisy.

## 18. CI Matrix

Existing gates remain:

- Go 1.26.x and 1.27.x;
- Ubuntu, Windows, macOS;
- default CGO-free build;
- compatibility harness;
- retrieval benchmarks;
- CodeQL.

Tree-sitter tagged index freshness tests run on the same tagged lanes introduced by Stage 4.

No existing retrieval threshold or context budget changes in this stage.

## 19. Non-Goals

Not in Stage 5:

- filesystem watchers;
- Git fsmonitor integration;
- Git-index-based change detection;
- stat-first freshness shortcuts;
- rename inference;
- content-addressed symbol cache across paths;
- structural graph indexing;
- SCIP;
- semantic/vector retrieval;
- changing index-directory naming;
- SQLite state.

## 20. Acceptance Criteria

Stage 5 is merge-ready only when:

1. valid unchanged files are never reparsed in incremental mode;
2. every current source file is SHA-256 checked;
3. same-size/same-mtime content changes are detected;
4. new/deleted/renamed files produce correct final state;
5. v1/wrong-repository/wrong-extractor indexes cannot be consumed as current;
6. default and Tree-sitter parser profiles cannot reuse each other's indexes;
7. nested-repository isolation remains intact;
8. cancellation/failure does not corrupt the prior index;
9. full and incremental indexes are symbol-equivalent;
10. all cross-platform, tagged parser, retrieval, compatibility, and CodeQL gates are green.

## 21. Deferred Optimization Trigger

A stat/fsmonitor fast path should be considered only after benchmark evidence shows hashing is a material portion of index time on large repositories.

Any future fast path must preserve SHA verification for ambiguous/racy cases and include adversarial same-size/same-mtime tests before replacing the hash-authoritative baseline.
