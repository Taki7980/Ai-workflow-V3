#!/usr/bin/env python3
from __future__ import annotations

import argparse
import difflib
import json
from pathlib import Path


def canonical(value: object) -> str:
    return json.dumps(value, indent=2, sort_keys=True) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Compare compatibility fixtures semantically while ignoring JSON object-key order."
    )
    parser.add_argument("expected")
    parser.add_argument("actual")
    args = parser.parse_args()

    expected = json.loads(Path(args.expected).read_text(encoding="utf-8"))
    actual = json.loads(Path(args.actual).read_text(encoding="utf-8"))
    if expected == actual:
        return 0

    expected_text = canonical(expected).splitlines(keepends=True)
    actual_text = canonical(actual).splitlines(keepends=True)
    print(
        "".join(
            difflib.unified_diff(
                expected_text,
                actual_text,
                fromfile=args.expected,
                tofile=args.actual,
            )
        ),
        end="",
    )
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
