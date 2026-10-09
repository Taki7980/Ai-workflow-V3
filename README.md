# AI Workflow V3

**A Go-first, agent-agnostic control plane for efficient AI-assisted software development.**

AI Workflow V3 is a clean reimplementation of [AI Workflow Control Plane V2](https://github.com/Taki7980/ai-workflow-control-plane-v2). It keeps V2's core idea — **escalate capability, not context volume** — while making the runtime easier to install, faster to start, safer to distribute, and simpler to maintain across Windows, Linux, macOS, and WSL.

> Status: V3 covers the daily V2 loop (setup, brief, handoff, verify, compress, memory) with contract parity enforced by the differential harness. Structural (CRG/SCIP) adapters and semantic retrieval are still in progress; V2 remains the behavioral reference for them.

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

## Quick start

```bash
cd path/to/your-project
ai-workflow setup                                        # discover repos, write config, build indexes
ai-workflow brief "refactor payment retry" --format prompt
# ... agent works; do not edit while EVIDENCE_STATE=requires_exploration ...
ai-workflow verify --check "go test ./..." --strict      # checks run without a shell + handoff validation
ai-workflow memory add --type verified-fix --keywords "payment retry" \
  --summary "retry uses jittered backoff" --file svc/retry.go
```

`brief` emits the V2 agent contract: lane/risk, evidence state (`sufficient`, `requires_exploration`, `abstain`), workspace fingerprint, Superpowers skill sequence, CRG plan, agent slots, budget, and bounded fresh context (index hits with source windows, targeted source matches, durable memory). Formats: `json` (default), `markdown`, `prompt`.

## Commands

```bash
ai-workflow setup
ai-workflow brief "fix payment validation" [--format json|markdown|prompt] [--write-handoff] [--changed-file F]...
ai-workflow route "fix payment validation"
ai-workflow context "DuplicateCharge"
ai-workflow handoff                       # validate ai-workspace/handoff/HANDOFF.md
ai-workflow verify --check "go test ./..." [--strict]
ai-workflow compress [--file F] [--max-lines 80] [--max-chars 12000] [--prefer-rtk] < noisy.log
ai-workflow memory add|search|list|prune|export|import
ai-workflow repos list
ai-workflow repos refresh
ai-workflow index
ai-workflow doctor --strict
ai-workflow version
```

Migrating V2 memory: run `ai-workflow memory export memory.jsonl` with V2, then `ai-workflow memory import memory.jsonl` with V3.

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
go build -o bin/ai-workflow ./cmd/ai-workflow
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
