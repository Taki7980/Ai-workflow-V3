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
- `setup`, `route`, `repos list`, `repos refresh`, `index`, `context`, `doctor`, `version`;
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

V3 must not replace V2 yet. Remaining migration gates include evidence sufficiency, adaptive budgets, CRG/SCIP adapters, provider sandbox/process-tree parity, workflow execution, memory, telemetry/replay, benchmarking/learning, SQLite production state, deployment policy, and full differential fixtures.
