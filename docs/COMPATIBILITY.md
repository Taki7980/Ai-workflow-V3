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

1. check out V2 at the pinned commit, regenerate the golden file, and fail if the committed fixture differs;
2. execute the same cases through V3 using `cmd/compat-harness` and fail on any contract-level mismatch.

The harness currently freezes:

- routing lane, risk, reasons, structural flag, and confidence;
- retrieval intent, lexical/semantic/structural requirements, reasons, and structural patterns;
- credential-free remote identity normalization;
- repository ID hashing;
- the complete V2 default control-plane configuration document.

No safety-relevant field is normalized away. A deliberate incompatibility requires changing the cases/fixture provenance and documenting the decision rather than weakening comparison.

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
