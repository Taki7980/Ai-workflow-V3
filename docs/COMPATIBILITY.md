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

## Differential fixtures

Every migrated behavior should have at least one fixture with:

```text
input/
expected-v2.json
actual-v3.json
normalization.json
```

A fixture must never normalize away a safety-relevant difference.
