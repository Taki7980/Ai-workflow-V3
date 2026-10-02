# Incremental Index Freshness

Stage 5 adds hash-authoritative incremental indexing to AI Workflow V3.

## Freshness model

The persisted index format is version 2. A persisted index is reusable only when all of these match the current repository/build:

- index version;
- repository relative path;
- repository ID from the accepted workspace registry;
- extractor revision for the active parser profile.

Every current source file is read and SHA-256 checked during an incremental build. File size and modification time are recorded as metadata only; they never decide freshness. This deliberately avoids relying on stat-only shortcuts that can miss same-size/same-mtime content replacement.

If the SHA-256 digest is unchanged and the prior symbol rows for that path carry the same digest, those rows are reused. Changed/new files are reparsed. Removed files and their symbol rows disappear from the rewritten index.

## v1 to v2 migration

A missing, malformed, v1, wrong-repository, or wrong-extractor index is not partially reused.

- `--mode auto`: falls back to a full rebuild and writes a current v2 index.
- `--mode incremental`: also falls back safely to a full rebuild when prior state is not reusable.
- `--mode full`: ignores prior index contents and reparses every current source file.

Runtime `Load` fails closed on unsupported/stale index state so `context` cannot silently consume old v1 or wrong-profile rows.

## Extractor revisions

The default parser build and the optional `treesitter` build use different deterministic extractor revisions.

That means an index produced by the default Go-AST/regex profile cannot be reused by a Tree-sitter-enabled binary, and a tagged index cannot be reused by the default binary. Parser/grammar changes that can alter emitted symbols must bump the revision.

Current Tree-sitter pilot modules remain pinned to:

- `github.com/tree-sitter/go-tree-sitter v0.25.0`
- `github.com/tree-sitter/tree-sitter-python v0.25.0`
- `github.com/tree-sitter/tree-sitter-javascript v0.25.0`
- `github.com/tree-sitter/tree-sitter-typescript v0.23.2`

## CLI

```bash
ai-workflow index --mode auto
ai-workflow index --mode incremental
ai-workflow index --mode full
```

Omitting `--mode` is equivalent to `auto`.

Per-repository output includes:

- `requested_mode`
- `effective_mode`
- `full_reason` when a safe full fallback occurred
- `files`
- `symbols`
- `hashed`
- `reparsed`
- `reused`
- `added`
- `changed`
- `removed`

For a valid no-change incremental run over N files, the deterministic contract is:

```text
hashed   = N
reparsed = 0
reused   = N
added    = 0
changed  = 0
removed  = 0
```

## Repository isolation and failure behavior

Nested Git repositories remain excluded from their parent index and are indexed through their own accepted registry entries. Repository-ID mismatches invalidate reuse even if a different repository appears at the same relative path.

The complete new index is assembled before `storage.WriteJSON` performs the atomic temporary-file write and rename. Cancellation or a read failure before persistence leaves the previous valid index unchanged.

Rename detection is intentionally not inferred: a renamed file is treated as one removal plus one addition because path is part of symbol identity.

## Benchmark

Command used in CI:

```bash
go test ./internal/indexer -run '^$' -bench 'BenchmarkIndex(Full|Incremental)' -benchmem -benchtime=20x
```

Measured on GitHub Actions Ubuntu 24.04, Go 1.27.1, AMD EPYC 7763, 40 small Go files:

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Full rebuild | 4,009,645 | 204,523 | 1,969 |
| Incremental, no change | 1,221,691 | 190,308 | 873 |
| Incremental, one changed file | 1,252,777 | 183,434 | 896 |

On this fixture, no-change incremental indexing used about **69.5% less time** and **55.7% fewer allocations** than a full rebuild. The one-file-changed case used about **68.8% less time** and **54.5% fewer allocations**. These hosted-runner numbers are diagnostic, not CI performance thresholds.

Correctness is gated by deterministic work counters instead of wall-clock timing.

## Deferred optimization

Stage 5 still hashes every current source file. A stat/fsmonitor fast path should be considered only if profiling on large repositories shows hashing is a material part of indexing cost.

Any later fast path must preserve SHA verification for ambiguous/racy cases and keep adversarial same-size/same-mtime tests before becoming freshness authority.
