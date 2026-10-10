# Foundation Status

This repository contains the first native V3 migration slice.

## Implemented

- native Go CLI entrypoint;
- V2-compatible Answer / Small / Full deterministic routing baseline;
- retrieval-intent planning baseline;
- V2-shaped default control-plane document for migration workspaces;
- nested Git repository discovery from a non-Git control root;
- V2-compatible credential-free remote identity and repository ID semantics;
- V2-shaped repository registry (`version`, `review_required`, `repository_id`);
- per-repository SHA-256 source index;
- Go AST symbol indexing with deterministic fallback symbol extraction;
- nested repository exclusion during parent-repository indexing;
- BM25 and reciprocal-rank-fusion primitives;
- bounded external provider process protocol foundation;
- `setup`, `bootstrap`, `init`, `route`, `repos list|refresh|include|exclude`, `index`, `context`, `search`, `doctor`, `version`;
- release pipeline (six static targets, `SHA256SUMS`, build provenance) and checksum-verifying `install.sh` / `install.ps1`;
- structural adapters: code-review-graph queries and SCIP index items with freshness manifests, evidence confidence tiers (candidate/corroborated/verified) and `graph`/`scip` sync and status commands;
- Linux/macOS/Windows CI matrix for Go 1.26 and Go 1.27;
- CodeQL workflow.

## Verified locally

The foundation passes:

- `go test ./...`;
- `go vet ./...`;
- a smoke workspace where the control root is not a Git repository and contains two independent child Git repositories;
- independent indexing for both child repositories;
- symbol retrieval from the expected repository;
- strict doctor checks;
- compatibility-shape checks for the V2 control config and repository registry.

The local verification host currently has an older Go toolchain, so the source was compiled there using compatibility syntax after temporarily lowering only the local `go` directive. The committed module requires Go 1.26+, and CI is configured to validate the supported 1.26 and 1.27 toolchains.

## Not yet parity-complete

The core workflow loop is migrated: `brief` (evidence sufficiency, selective gate, evidence state, adaptive token budget, workspace fingerprint, orchestration contract, json/markdown/prompt formats), `handoff`, `verify`, `compress`, and JSONL durable `memory`, all covered by differential fixtures.

Remaining migration gates: semantic retrieval, multi-repository structural fan-out, provider sandbox/process-tree parity, telemetry/replay, and the advisory research stacks (learning, deployment policy, SQLite production state), which are intentionally not ported unless users need them.
