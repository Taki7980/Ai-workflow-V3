# Incremental Index Freshness Design

**Date:** 2026-10-02
**Repository:** `Taki7980/Ai-workflow-V3`
**Base:** `36f4c70` (main after PR #6)
**Tracker:** #2, roadmap item 5
**Status:** implemented; awaiting review

## 1. Goal

Make `ai-workflow index` proportional to what changed, and make every consumer of the index able to tell when it is stale, without weakening determinism, V2 compatibility, or context ceilings.

Non-goals: file watchers, git hooks, SQLite state, structural (SCIP/CRG) or semantic indexes. Those remain later roadmap items.

## 2. Current state (before this change)

- `Build` re-read, re-hashed, and re-parsed every file on every run, then always rewrote `index.json`. `FileState` already stored `sha256`, `size`, and `mtime_ns`, but nothing used them.
- `context` served hits from whatever index was on disk with no staleness signal.
- `doctor` did not look at indexes at all.

## 3. Evidence considered

Research refreshed on 2026-10-02.

- **Git racy-clean handling** (`Documentation/technical/racy-git.txt`). Git's index trusts cached stat data, but treats entries whose mtime is not older than the index write time as "racily clean" and re-checks their content. Coarse timestamp granularity means a same-size rewrite within the same tick is otherwise invisible. V3 adopts the same rule with an explicit safety window.
- **Stale index as a correctness failure.** A June 2026 write-up on coding agents grounding on an index built from an outdated checkout argues that index/working-tree drift produces confident wrong claims, and recommends exposing index provenance (the indexed commit) and a cheap "re-index" affordance. V3 records `head_sha` and adds `context --refresh`.
- **Peer tools converge on the same shape.** Recent agent code-index projects (intent-code, CocoIndex Code, code-memory-agent) skip files whose mtime and size are unchanged, confirm with content hashes, cascade deletions to symbols, avoid writes when nothing changed, and treat staleness as a hard signal rather than silently serving old entries.

Implication: a stat fast-path plus hash confirmation is the established design; the risk to manage is the racy-clean window, which must be tested explicitly.

## 4. Design

### 4.1 Index metadata (additive, schema version stays 1)

| Field | Purpose |
|---|---|
| `built_at_ns` | Scan start time; the racy-clean reference point. |
| `parsers` | Fingerprint of the active parser set (`v1:go-ast,…,regex`). Cached symbols are reused only on an exact match, so toggling the `treesitter` build tag re-parses everything. |
| `head_sha` | HEAD at build time; provenance only, never a freshness input. |

Indexes written before this change lack these fields and are rebuilt in full once.

### 4.2 Per-file classification

For each source file found by the walk:

1. **Reused** — indexed before, size and mtime unchanged, and `mtime < built_at_ns − RacyWindow`. Not read.
2. **Rehashed** — read and hashed; SHA-256 equals the cached hash, so cached symbols are kept without parsing (e.g. `touch`, branch switch back).
3. **Parsed** — new or content changed.
4. **Removed** — in the previous index but no longer on disk; its symbols are dropped.
5. **Skipped** — vanished or became unreadable between walk and read; does not fail the build.

`RacyWindow` is 2 s (covers FAT's 2 s granularity). Files are stat'ed *before* being read, so a write racing the read leaves an older mtime that forces a re-hash next time rather than pairing a new mtime with old content.

### 4.3 Write policy

The index is rewritten only when something was parsed, removed, or rehashed, or HEAD moved. A no-op `index` performs no writes. Rehashed entries force a write so the new scan time lets the fast path trust them next run.

### 4.4 Query-time freshness

- `context` checks each selected hit (`FileStale`): missing file, size/mtime mismatch, or (for racily-clean entries) hash mismatch marks the hit `"stale": true` and prints a warning to stderr. The field is `omitempty`, so fresh output is byte-identical to before.
- `context --refresh` runs the incremental build first.
- `doctor` adds an `index:<repo>` check (added/modified/removed counts by stat). Missing or stale indexes fail only under `--strict`.

### 4.5 Large files

Files over `MaxParseBytes` (4 MiB) are stream-hashed and tracked for freshness but not parsed, bounding memory on generated bundles.

## 5. Related fixes shipped in the same PR

- Worker goroutines leaked when a file read failed mid-build; the pool now cancels and drains.
- The regex fallback parser stopped at the first line over 64 KiB (`bufio.Scanner` limit), silently dropping later symbols.
- Cross-repository search ties depended on Go map order.
- `setup` / `repos refresh` overwrote include/exclude decisions. Now matches V2 `refresh_registry` at the pinned oracle commit, and adds `repos include|exclude <selector>`.
- Provider runner could block past its timeout when a grandchild held stdout (`cmd.WaitDelay`), and reported output overflow as a JSON decode error.

## 6. Known limitations

- A file replaced with identical size *and* an mtime set back to an old value outside the racy window (e.g. `rsync -t`, `touch -r`) is not detected by the stat path. Git has the same limitation. `index --full` is the escape hatch.
- New files are invisible to `context` until the next `index`/`--refresh`; `doctor` reports them.

## 7. Verification

- Unit tests for every classification path, including a reproduction of the racy-clean hazard (same size, restored mtime, different content).
- Mutation checks: each of these tests fails against the pre-change code or with its guard removed — racy window, worker drain, sorted search order, regex line splitting, registry merge, provider `WaitDelay`.
- End-to-end CLI tests on a non-Git control root with two Git repositories.
- Unchanged gates: V2 compatibility harness (29/29 contracts), frozen retrieval and MMR selector benchmarks, CGO-free default build, `go test -race`.
