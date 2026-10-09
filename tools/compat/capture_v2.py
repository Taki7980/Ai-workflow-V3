#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path


def load_cases(path: Path) -> dict:
    """Load the compatibility cases JSON file and validate its schema version."""
    data = json.loads(path.read_text(encoding="utf-8"))
    if data.get("schema_version") != 1:
        raise SystemExit("unsupported cases schema")
    return data


def main() -> int:
    """Run each compatibility case against the pinned AI Workflow V2 checkout
    and write the frozen outputs to the fixture file."""
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

    capture_workflow_contracts(cases, outputs, cfg)

    Path(args.output).write_text(
        json.dumps(outputs, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return 0


def compress_input(case: dict) -> str:
    """Build compress case text; generators keep large inputs reviewable."""
    if "generate_lines" in case:
        return "\n".join(f"l{i}" for i in range(int(case["generate_lines"])))
    if "repeat_char" in case:
        return case["repeat_char"] * int(case["repeat_count"])
    return case["text"]


def capture_workflow_contracts(cases: dict, outputs: dict, cfg: dict) -> None:
    """Capture the core workflow-loop contracts (brief, handoff, compress, orchestration)."""
    import tempfile

    from ai_workflow import handoff as v2_handoff
    from ai_workflow.commands.workspace import _format_brief
    from ai_workflow.compress import compress_text
    from ai_workflow.models import ContextItem, Lane, Risk, RouteDecision
    from ai_workflow.orchestration import build_orchestration_contract
    from ai_workflow.providers import ProviderStatus, model_tier
    from ai_workflow.retrieval_policy import Sufficiency, evaluate_sufficiency
    from ai_workflow.selective_retrieval import evaluate_selective_retrieval
    from ai_workflow.workflow_engine import _evidence_state

    def decision(raw: dict) -> RouteDecision:
        return RouteDecision(
            Lane(raw["lane"]),
            Risk(raw["risk"]),
            list(raw.get("reasons", [])),
            bool(raw.get("structural_context", False)),
            float(raw.get("confidence", 0.5)),
        )

    def items(raw: list) -> list:
        return [
            ContextItem(
                i["source"], i["text"], float(i.get("score", 0.0)),
                bool(i.get("stale", False)), dict(i.get("metadata", {})),
            )
            for i in raw
        ]

    for key in ("orchestration", "sufficiency", "selective", "evidence_state",
                "handoff_validate", "handoff_render", "compress", "brief_format", "model_tier"):
        outputs[key] = {}

    for case in cases.get("orchestration", []):
        config = {"execution": {"orchestration_budget": case["budget"]}} if "budget" in case else {}
        prov = case["providers"]
        outputs["orchestration"][case["name"]] = dict(build_orchestration_contract(
            decision(case["decision"]),
            {
                "retrieval_intent": case["intent"],
                "sufficiency": {"sufficient": case["sufficient"]},
                "selective_retrieval": {"enabled": case["selective_enabled"], "accept": case["selective_accept"]},
            },
            list(case["changed_files"]),
            int(case["workspace_count"]),
            ProviderStatus(superpowers=prov["superpowers"], code_review_graph=prov["code_review_graph"], rtk=False, ripgrep=False),
            config,
        ))

    for case in cases.get("sufficiency", []):
        result = evaluate_sufficiency(
            case["query"], items(case["items"]),
            structural_required=case["structural_required"],
            structural_patterns=tuple(case["structural_patterns"]),
            threshold=case["threshold"],
        )
        outputs["sufficiency"][case["name"]] = {
            "score": result.score, "sufficient": result.sufficient,
            "lexical_coverage": result.lexical_coverage, "source_diversity": result.source_diversity,
            "exact_match": result.exact_match, "structural_complete": result.structural_complete,
        }

    for case in cases.get("selective", []):
        outputs["selective"][case["name"]] = evaluate_selective_retrieval(
            items(case["items"]), Sufficiency(**case["sufficiency"]),
            minimum_coverage=case["minimum_coverage"],
        ).to_dict()

    for case in cases.get("evidence_state", []):
        outputs["evidence_state"][case["name"]] = _evidence_state(
            RouteDecision(Lane(case["lane"]), Risk.LOW), case["sufficient"]
        )

    for case in cases.get("handoff_validate", []):
        with tempfile.TemporaryDirectory() as tmp:
            path = v2_handoff.handoff_path(Path(tmp))
            path.parent.mkdir(parents=True)
            path.write_bytes(case["text"].encode("utf-8"))
            outputs["handoff_validate"][case["name"]] = v2_handoff.validate(Path(tmp), case["max_lines"])

    for case in cases.get("handoff_render", []):
        outputs["handoff_render"][case["name"]] = v2_handoff.render(
            decision(case["decision"]), case["provider"], case["sources"], case["goal"]
        )

    for case in cases.get("compress", []):
        outputs["compress"][case["name"]] = compress_text(
            compress_input(case), case["max_lines"], case["max_chars"]
        )

    for case in cases.get("brief_format", []):
        outputs["brief_format"][case["name"]] = _format_brief(case["packet"], case["format"])

    for case in cases.get("model_tier", []):
        outputs["model_tier"][case["name"]] = model_tier(decision(case["decision"]), cfg)


if __name__ == "__main__":
    raise SystemExit(main())
