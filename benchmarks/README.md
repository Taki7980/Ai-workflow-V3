# Retrieval benchmark

This directory contains small, deterministic evaluation fixtures for AI Workflow V3 retrieval.

The initial fixture is intentionally **not** a claim of state-of-the-art quality. It freezes the current lexical baseline so later PRs can prove that MMR, structural retrieval, semantic retrieval, or context selection improve useful evidence without silently increasing context cost.

## Metrics

- **Recall@1 / Recall@K** — fraction of required files retrieved.
- **MRR** — reciprocal rank of the first required file.
- **File precision / F1** — relevance of the deduplicated file set.
- **Context yield** — estimated relevant-file tokens divided by total retrieved-file tokens.
- **Average retrieved tokens** — estimated context cost for positive cases.
- **No-gold false-positive rate** — fraction of no-gold/counterfactual queries that still retrieve files.

The fixture task labels follow repository-retrieval patterns used in 2026 evaluation work: `code2test`, `comment2context`, `trace2code`, and explicit no-gold/counterfactual controls.

Run:

```bash
go run ./cmd/retrieval-bench --fixture benchmarks/retrieval/baseline.json
```

Enforce the frozen baseline thresholds:

```bash
go run ./cmd/retrieval-bench --fixture benchmarks/retrieval/baseline.json --enforce
```

The current fixture intentionally records a known weakness: lexical retrieval returns a false positive for the wrong-repository payment/UI query. Future selective/semantic retrieval work should lower that rate rather than relaxing the threshold.

## Benchmark policy

1. Do not change a threshold just to make a PR green.
2. If a fixture is wrong, explain the correction in the PR.
3. Retrieval PRs must include before/after metrics.
4. Context-cost increases require a measured quality benefit.
5. Heavy public benchmarks may be run outside normal CI, but a deterministic smoke fixture stays in CI.
