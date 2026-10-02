# AI Workflow V3

**A Go-first, agent-agnostic control plane for efficient AI-assisted software development.**

AI Workflow V3 is a clean reimplementation of [AI Workflow Control Plane V2](https://github.com/Taki7980/ai-workflow-control-plane-v2). It keeps V2's core idea — **escalate capability, not context volume** — while making the runtime easier to install, faster to start, safer to distribute, and simpler to maintain across Windows, Linux, macOS, and WSL.

> Status: early V3 foundation. V2 remains the behavioral reference until parity gates are passed.

## Why V3 exists

V2 proved the control-plane model. V3 changes the runtime and internal architecture without discarding the behavior that already works.

- **Single native binary** instead of a Python environment.
- **Typed configuration and contracts** at process boundaries.
- **Native multi-repository discovery** from a non-Git parent workspace.
- **Bounded concurrent indexing** with SHA-256 freshness metadata.
- **Deterministic routing** for Answer / Small / Full lanes.
- **Retrieval intent separated from execution lane**.
- **BM25 + RRF-ready retrieval primitives** in the standard runtime.
- **Hardened external-provider execution**: no shell, bounded output, timeouts, constrained environment.
- **Compatibility-first migration**: V2 inputs and behavior become executable fixtures, not assumptions.

## Current commands

```bash
ai-workflow setup
ai-workflow route "fix payment validation"
ai-workflow repos list
ai-workflow repos refresh                 # keeps your include/exclude decisions
ai-workflow repos exclude admin-panel     # by path, repository id, remote, or name
ai-workflow repos include admin-panel
ai-workflow index                         # incremental; only changed files are parsed
ai-workflow index --full                  # force a full rebuild
ai-workflow context "DuplicateCharge"     # stale hits are flagged
ai-workflow context "DuplicateCharge" --refresh --lane small
ai-workflow doctor --strict               # fails on missing or stale indexes
ai-workflow version
```

### Index freshness

`index` reuses the previous index: files whose size and mtime are unchanged are not read, touched-but-identical files keep their cached symbols, and only new or modified files are parsed. Files modified within two seconds of the last scan are always re-hashed (Git's "racily clean" rule). A no-op `index` writes nothing.

`context` marks any hit whose file changed since indexing with `"stale": true` and warns on stderr; `--refresh` updates indexes before searching. See [`docs/superpowers/specs/2026-10-02-incremental-index-freshness-design.md`](docs/superpowers/specs/2026-10-02-incremental-index-freshness-design.md).

A project can use a non-Git parent directory:

```text
RevenueOS/
├── admin-panel/.git/
├── backend/.git/
└── ai-workspace/
```

`ai-workflow setup` recursively discovers nested Git roots and keeps control-plane state in the parent `ai-workspace/` directory.

## Build

V3 targets the supported Go toolchain line beginning with Go 1.26. The CI release lane tests Go 1.26 and 1.27.

```bash
go test ./...
make build        # static binary with embedded version at bin/ai-workflow
```

## Architecture

```text
Task
  -> deterministic lane + risk classification
  -> retrieval-intent classification
  -> hard context budget
  -> repository-aware retrieval
       -> local index / BM25
       -> structural provider adapters
       -> optional semantic providers
  -> evidence sufficiency
  -> bounded context
  -> execution provider
  -> verification
  -> compact handoff
  -> verified durable memory only
  -> telemetry / policy feedback
```

Read [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) and [`docs/MIGRATION_FROM_V2.md`](docs/MIGRATION_FROM_V2.md).

## Migration rule

V3 does **not** get to call itself compatible because a feature was reimplemented. A V2 behavior is considered migrated only after a differential fixture proves that the same input produces an equivalent contract-level result.

See [`docs/COMPATIBILITY.md`](docs/COMPATIBILITY.md).

## License

MIT
