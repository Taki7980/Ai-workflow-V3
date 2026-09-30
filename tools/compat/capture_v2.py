#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path


def load_cases(path: Path) -> dict:
    data = json.loads(path.read_text(encoding="utf-8"))
    if data.get("schema_version") != 1:
        raise SystemExit("unsupported cases schema")
    return data


def main() -> int:
    parser = argparse.ArgumentParser(description="Capture frozen AI Workflow V2 compatibility contracts.")
    parser.add_argument("--v2-root", required=True)
    parser.add_argument("--cases", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--expected-commit", required=True)
    args = parser.parse_args()

    v2_root = Path(args.v2_root).resolve()
    cases = load_cases(Path(args.cases))
    commit = subprocess.check_output(
        ["git", "rev-parse", "HEAD"], cwd=v2_root, text=True
    ).strip()
    if commit != args.expected_commit:
        raise SystemExit(
            f"V2 oracle commit mismatch: checked out {commit}, expected {args.expected_commit}"
        )

    sys.path.insert(0, str(v2_root))
    from ai_workflow.classifier import classify
    from ai_workflow.config import default_config
    from ai_workflow.models import Lane, Risk, RouteDecision
    from ai_workflow.repository_registry import remote_identity, repository_id
    from ai_workflow.retrieval_policy import classify_retrieval_intent
    from ai_workflow.math_retrieval import BM25Scorer, maximal_marginal_relevance, tokenize

    outputs = {
        "schema_version": 1,
        "source": {
            "repository": "Taki7980/ai-workflow-control-plane-v2",
            "commit": commit,
        },
        "routing": {},
        "retrieval": {},
        "remote_identity": {},
        "repository_id": {},
        "mmr": {},
        "config": default_config() if cases.get("config_snapshot") else None,
    }

    cfg = default_config()
    for case in cases.get("routing", []):
        outputs["routing"][case["name"]] = classify(case["task"], cfg).to_dict()

    for case in cases.get("retrieval", []):
        raw = case["decision"]
        decision = RouteDecision(
            Lane(raw["lane"]),
            Risk(raw["risk"]),
            list(raw.get("reasons", [])),
            bool(raw.get("structural_context", False)),
            float(raw.get("confidence", 0.5)),
        )
        plan = classify_retrieval_intent(
            case["query"],
            decision,
            symbol=case.get("symbol"),
            endpoint=case.get("endpoint"),
        )
        outputs["retrieval"][case["name"]] = {
            "intent": plan.intent.value,
            "use_lexical": plan.use_lexical,
            "use_semantic": plan.use_semantic,
            "use_structural": plan.use_structural,
            "reason": plan.reason,
        }
        if plan.structural_patterns:
            outputs["retrieval"][case["name"]]["structural_patterns"] = list(
                plan.structural_patterns
            )

    for case in cases.get("remote_identity", []):
        outputs["remote_identity"][case["name"]] = remote_identity(case["input"])

    for case in cases.get("repository_id", []):
        outputs["repository_id"][case["name"]] = repository_id(
            case["relative_path"], case.get("remote_identity")
        )

    for case in cases.get("mmr", []):
        scorer = BM25Scorer()
        texts = [candidate["text"] for candidate in case["candidates"]]
        keys = [candidate["key"] for candidate in case["candidates"]]
        scores = [float(candidate["score"]) for candidate in case["candidates"]]
        scorer.fit(texts, keys)
        outputs["mmr"][case["name"]] = maximal_marginal_relevance(
            tokenize(case["query"]),
            scorer.docs,
            scores,
            lambda_param=float(case["lambda"]),
            max_items=int(case["max_items"]),
        )

    Path(args.output).write_text(
        json.dumps(outputs, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
