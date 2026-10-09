# V3 Rewrite Roadmap

The active implementation tracker is GitHub issue #2. The order below is intentionally dependency-driven.

1. Foundation and compatibility harness — done
2. Retrieval benchmark/evaluation baseline — done
3. MMR + budget-aware context selection — done
4. Parser abstraction + Tree-sitter pilot — done
5. Incremental index freshness — done
6. Structural retrieval adapter + SCIP path — next
7. Optional semantic retriever + RRF evaluation
8. Provider execution hardening / optional stronger sandbox
9. Telemetry + replay schema
10. SQLite production state
11. Supply-chain/release hardening
12. Workflow engine + verification/handoff parity — core loop done (brief, handoff, verify, compress)
13. Durable evidence-aware memory — done (JSONL)
14. Advisory learning/deployment policy
15. Full differential/performance gates and V3 release candidate

## Ordering rule

Do not add retrieval complexity before the benchmark can measure it. Do not promote adaptive behavior above deterministic safety rules. Do not increase context ceilings to compensate for poor retrieval.
