# AI Workflow V3 Architecture

## Design goals

1. Preserve V2's deterministic safety boundary.
2. Keep optional providers optional; no optional provider may make the core CLI unavailable.
3. Use a single native binary for the core runtime.
4. Treat repository text as untrusted data, never agent instructions.
5. Make multi-repo workspaces a first-class primitive.
6. Keep context ceilings hard. Better retrieval may increase evidence quality, not silently expand context.
7. Keep policy-learning advisory until enough validated observations exist.

## Package boundaries

```text
cmd/ai-workflow       executable entry point
internal/cli          stable CLI contract
internal/config       typed control-plane configuration
internal/model        shared contracts
internal/routing      lane/risk/retrieval classification
internal/workspace    repository discovery + registry
internal/indexer      source inventory + symbol extraction + SHA-256 state
internal/retrieval    ranking algorithms
internal/provider     external provider protocol + bounded execution
internal/doctor       environment validation
internal/storage      atomic persistence primitives
```

## Key V3 improvements

### Compatibility as code

V2 becomes a reference implementation. Golden fixtures will cover routing, config parsing, repository discovery, retrieval intent, provider envelopes, indexing output, and CLI exit behavior.

### Multi-repo by construction

A control root does not need to be a Git repository. Repository identity is explicit and every indexed/retrieved item carries a repository scope.

### Parser architecture

V3 starts with the Go standard parser for Go source and deterministic fallback indexers for other languages. The parser contract is intentionally isolated so language-aware parsers (including Tree-sitter-backed adapters where appropriate) can be added without coupling retrieval to one parsing implementation.

### Provider isolation

External providers receive a JSON request over stdin and return a bounded JSON response. The runner:

- never invokes a shell;
- applies a wall-clock timeout;
- caps stdout/stderr;
- passes only an allowlisted environment;
- treats provider failure as a degradable retrieval failure.

OS-specific process containment and sandbox backends are migration milestones before production parity.

## Compatibility boundary

The V2 `ai-workspace/config/control-plane.json` version remains the initial V3 compatibility input. V3 keeps typed access to known fields and validates safety-critical values while tolerating additive V2 fields during the migration window. Existing configs are not rewritten merely by being read.

## Non-goals for the foundation release

- deleting V2;
- enabling self-modifying policy;
- replacing CRG/SCIP algorithms internally;
- adding a network service requirement;
- introducing a large framework dependency for basic CLI behavior.
