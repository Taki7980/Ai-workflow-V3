# Migration from V2

V3 uses a strangler migration rather than a big-bang rewrite.

## Gate 0 — contract freeze

Capture V2 behavior for:

- CLI commands and exit codes;
- routing and risk classification;
- retrieval-intent classification;
- `control-plane.json` validation/migration;
- repository discovery and IDs;
- index freshness semantics;
- provider JSON protocol;
- context provenance;
- telemetry and handoff schemas.

## Gate 1 — native foundation

Implemented in the initial V3 scaffold:

- Go CLI;
- typed config;
- Answer/Small/Full classifier;
- retrieval-intent classifier;
- nested Git repository discovery;
- SHA-256 index state;
- Go AST symbol extraction + deterministic fallback extraction;
- BM25 and RRF primitives;
- bounded external-provider runner;
- setup/index/context/doctor flows.

## Gate 2 — differential parity

Run equivalent V2 and V3 commands against frozen workspaces and compare normalized JSON outputs. Differences must be documented as either:

1. compatibility bug;
2. intentional V3 improvement with an ADR;
3. nondeterministic field excluded by normalization.

## Gate 3 — retrieval parity

Port or adapt:

- evidence sufficiency;
- adaptive context caps;
- semantic provider arbitration;
- CRG integration;
- SCIP integration;
- external retriever registry;
- structural validation;
- RRF + MMR selection policy;
- hot cache and targeted source fallback.

## Gate 4 — execution parity

Port:

- provider semantics;
- Superpowers detection/selection;
- native Plan -> Build -> Review;
- verification contracts;
- output compression;
- handoff generation.

## Gate 5 — state, learning, deployment

Port:

- telemetry traces;
- run journal/replay;
- durable memory;
- SQLite production mirror;
- evaluation/benchmark protocols;
- advisory learning and shadow policy;
- deployment state, promotion, rollback, incident bundles.

## Gate 6 — security parity

Required before V3 replaces V2:

- OS process-group/tree termination on all supported platforms;
- sandbox backend parity;
- path confinement and symlink tests;
- provider executable trust/pinning;
- hostile JSON/provider fuzzing;
- secret/environment leakage tests;
- Windows/Linux/macOS CI.

## Gate 7 — cutover

Only after all mandatory differential suites pass:

- make V3 the recommended installation;
- keep a documented V2 rollback path for at least one release line;
- do not silently mutate existing workspace state during first V3 startup.
