# Compatibility Contract

V3 follows a contract-level compatibility model.

## Must remain compatible unless an ADR says otherwise

- lane values: `answer`, `small`, `full`;
- risk values: `low`, `medium`, `high`;
- control workspace: `ai-workspace/`;
- control config version 2 during the migration window;
- repository registry semantics;
- SHA-256 freshness authority;
- provider request/response envelope;
- failure degradation for optional retrieval providers;
- deterministic escalation of high-risk mutation categories.

## Allowed to improve internally

- concurrency model;
- startup time;
- memory use;
- index representation;
- parser implementation;
- installation and release packaging;
- data structures used by BM25/RRF/MMR;
- internal package layout.

## Differential compatibility harness

The first executable V2 -> V3 compatibility gate lives under `compat/`.

`compat/v2.lock` pins the exact V2 oracle commit. `compat/cases.json` contains reviewable inputs. `tools/compat/capture_v2.py` executes those inputs against the pinned V2 implementation and emits deterministic golden outputs. `compat/fixtures/v2-contracts.json` is the committed snapshot of those outputs.

CI performs two independent checks:

1. check out V2 at the pinned commit, regenerate the golden file, and compare it semantically against the committed fixture;
2. execute the same cases through V3 using `cmd/compat-harness` and fail on any contract-level mismatch.

JSON object-key ordering is the only ignored representation detail. Values, array ordering, reasons, confidence values, structural patterns, safety flags, hashes, and configuration fields must match exactly.

The harness currently freezes:

- routing lane, risk, reasons, structural flag, and confidence;
- retrieval intent, lexical/semantic/structural requirements, reasons, and structural patterns;
- credential-free remote identity normalization;
- repository ID hashing;
- the complete V2 default control-plane configuration document;
- the orchestration contract (complexity vector/score, Superpowers skills, CRG plan, slots, review/verification passes, graph depth, budget floors);
- evidence sufficiency score and the selective-retrieval gate;
- evidence state (`sufficient`, `requires_exploration`, `abstain`);
- handoff validation errors and the rendered handoff template;
- output compression;
- `brief --format markdown|prompt` rendering;
- model-tier selection;
- code-review-graph payload compaction, context items and the verified-empty rule;
- SCIP index items (definitions, references, path confinement, query anchor);
- structural state directory keys;
- the selective gate's verified structural-conflict check.

### Deliberate differences in the workflow loop

- `brief --format json` emits the agent-facing subset of V2's packet. Research-only diagnostics (`algorithm_policy`, `learning`, `token_funnel`, `scheduler`, `authorization_policy`, `task_retrieval_policy`, `policy_identity`, `graph_state`, `stage_latency_ms`) are not emitted.
- Durable memory is stored as `ai-workspace/memory/memory.jsonl` (same record schema as V2). Migrate with V2 `memory export` and V3 `memory import`.
- `retrieval.workspace_state.index_state_sha256` keeps V2 semantics (changes iff indexed content changes) but is computed over the V3 index layout, so its value is not comparable with V2.
- `lightweight_index` context items carry a fresh source window after the symbol row; stale files are never served.
- The selective gate's `missing_code_owned_evidence_identity` branch is not ported; V3 items carry `repository_id` metadata instead of V2 evidence objects.

### Deliberate differences in the structural adapter

- Freshness manifests are V3-owned (`manifest_schema` 1, fingerprint = git HEAD + content hashes of changed paths, excluding `ai-workspace/`). V2-synced graphs and SCIP indexes read as stale until `ai-workflow graph sync` / `scip sync` runs.
- `graph.db` is not opened (no SQLite in the stdlib-only build); trust rests on the CRG exit code, a non-empty database and the manifest SHA-256.
- Absolute paths in CRG output are rewritten repository-relative and rows outside the repository are dropped, so machine paths never reach a brief.
- `providers_skipped` is a `{label: reason}` object, as in V2's trace.
- Nested JSON objects inside CRG item text are key-sorted (V2 keeps CRG's order); the top-level compact payload keeps V2 order. Compat cases use key-sorted rows.

A deliberate incompatibility requires changing the cases/fixture provenance and documenting the decision rather than weakening comparison.

Run locally:

```bash
go run ./cmd/compat-harness \
  --cases compat/cases.json \
  --fixtures compat/fixtures/v2-contracts.json \
  --lock compat/v2.lock
```

To refresh the oracle after intentionally advancing the V2 pin:

```bash
python tools/compat/capture_v2.py \
  --v2-root /path/to/ai-workflow-control-plane-v2 \
  --cases compat/cases.json \
  --output compat/fixtures/v2-contracts.json \
  --expected-commit "$(cat compat/v2.lock)"
```
