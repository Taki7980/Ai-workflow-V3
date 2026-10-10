# AI Workflow V3

**A Go-first, agent-agnostic control plane for efficient AI-assisted software development.**

AI Workflow V3 is a clean reimplementation of [AI Workflow Control Plane V2](https://github.com/Taki7980/ai-workflow-control-plane-v2). It keeps V2's core idea — **escalate capability, not context volume** — while making the runtime easier to install, faster to start, safer to distribute, and simpler to maintain across Windows, Linux, macOS, and WSL.

> Status: V3 covers the daily V2 loop (setup, brief, handoff, verify, compress, memory) and the structural adapters (code-review-graph, SCIP) with contract parity enforced by the differential harness. Semantic retrieval is still in progress; V2 remains the behavioral reference for it.

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

## Install

Linux / macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/Taki7980/Ai-workflow-V3/main/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/Taki7980/Ai-workflow-V3/main/install.ps1 | iex
```

The installers download the release binary for your OS/architecture, verify its SHA-256 against the release `SHA256SUMS`, and refuse to install on mismatch. Pin a version with `AI_WORKFLOW_VERSION=X.Y.Z`; choose a directory with `AI_WORKFLOW_INSTALL_DIR`. Every release binary carries a GitHub build-provenance attestation:

```sh
gh attestation verify "$(command -v ai-workflow)" --repo Taki7980/Ai-workflow-V3
```

With a Go toolchain (1.26+): `go install github.com/Taki7980/ai-workflow-v3/cmd/ai-workflow@latest`.

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
ai-workflow setup [--project-name N] [--json] [--create] [--no-index|--full-index] [--no-crg-sync]
ai-workflow bootstrap --project-name N    # strict: refuses existing control-plane files
ai-workflow init --project-name N
ai-workflow brief "fix payment validation" [--format json|markdown|prompt] [--write-handoff] [--changed-file F]...
ai-workflow route "fix payment validation"
ai-workflow context "fix DuplicateCharge" [--symbol S] [--endpoint E] [--changed-file F]...   # evidence packet, writes no state
ai-workflow search "DuplicateCharge"      # raw indexed-symbol hits
ai-workflow handoff [validate]            # validate ai-workspace/handoff/HANDOFF.md
ai-workflow verify --check "go test ./..." [--strict]
ai-workflow compress [--file F] [--max-lines 80] [--max-chars 12000] [--prefer-rtk] < noisy.log
ai-workflow memory add|search|list|prune|export|import
ai-workflow graph sync|status [--repo REL]... [--timeout 180]
ai-workflow scip sync|status [--repo REL]... [--language go|python|typescript|javascript|java]
ai-workflow repos list|refresh
ai-workflow repos include|exclude <path|id|remote|name>   # refresh keeps these decisions
ai-workflow index [--mode auto|incremental|full]
ai-workflow stats [--limit 200] [--recommend] [--minimum-runs 20]
ai-workflow replay RUN_ID [--strict]     # verified decision history; never re-executes anything
ai-workflow run inspect|verify RUN_ID
ai-workflow doctor --strict
ai-workflow version
```

Mutation briefs (and any `brief`/`context` with `--trace`) write a privacy-preserving trace under `ai-workspace/generated/traces/` and an immutable hash-chained run journal under `ai-workspace/generated/run-journal/`; the run ID is in `retrieval.run_id`. Traces never contain task text (set `AI_WORKFLOW_TELEMETRY_HMAC_KEY` to add a keyed task fingerprint). `context.telemetry.mode` is `off`, `mutations` (default) or `all`; `retention_days`, `max_trace_files` and `redact_patterns` bound and scrub traces. Optional OTLP export is configured only through `AI_WORKFLOW_OTLP_ENDPOINT` plus an explicit `AI_WORKFLOW_OTLP_ALLOWED_HOSTS` allowlist (HTTPS only, no redirects, no loopback/link-local targets).

Migrating V2 memory: run `ai-workflow memory export memory.jsonl` with V2, then `ai-workflow memory import memory.jsonl` with V3.

Structural evidence: with `code-review-graph` installed, run `ai-workflow graph sync` once (and after large changes). Structural tasks ("who calls X", "impact of changing Y") then get validated graph evidence in `brief`; a graph that no longer matches the repository is never used and the brief notes `run ai-workflow graph sync`. `ai-workflow scip sync` adds SCIP references when a SCIP indexer (`scip-go`, `scip-python`, ...) and `scip` are installed. V3 writes its own freshness manifests, so graphs synced by V2 report stale until synced once with V3.

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

Releases are cut by pushing a `vX.Y.Z` tag: `.github/workflows/release.yml` tests, cross-compiles static binaries for linux/darwin/windows × amd64/arm64, writes `SHA256SUMS`, attests build provenance and publishes the GitHub release.

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
