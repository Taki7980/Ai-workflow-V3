# Core Workflow Loop Design

**Date:** 2026-10-10
**Repository:** `Taki7980/Ai-workflow-V3`
**Base:** `0de9c7fe4e1a07f3136f03ba407476bfb41672ec`
**V2 oracle:** `554302c378f69809fbd09ff001eb7cfdb9af5005` (unchanged pin)
**Tracker:** #2
**Status:** design approved in chat; written spec awaiting review

## 1. Goal

Make V3 usable as a daily replacement for V2's user-facing loop:

```text
setup -> brief -> (agent works) -> handoff -> verify -> memory
```

V3 already ships `setup`, `route`, `repos`, `index`, `context`, `doctor`. This PR adds the missing loop commands — `brief`, `handoff`, `verify`, `compress`, `memory` — with contract-level V2 parity enforced by the differential harness.

Success criteria:

1. `ai-workflow brief "<task>" --format prompt|markdown|json` emits the V2 agent contract (evidence state, workspace fingerprint, Superpowers skills, CRG plan, agent slots, budget, bounded context).
2. Pure decision functions (orchestration, sufficiency, selective gate, evidence state, handoff validation, compress, prompt/markdown rendering) match V2 outputs exactly on frozen fixtures.
3. No new Go module dependency. Core binary stays CGO-optional.
4. `go test ./...`, `go vet ./...`, compat harness, Linux/macOS/Windows CI green.

## 2. Explicit non-goals

Decided in brainstorming (option A scope):

- V2 research stacks: learning/contextual policy, deployment canaries/rollback, SQLite production mirror, benchmark ablation/intervention/calibration, replay. Advisory-only in V2; not ported.
- Structural adapters (CRG MCP/CLI calls, SCIP) — sub-project 2. This PR only *detects* CRG and *emits* the CRG plan.
- Semantic retrievers, async retrieval scheduler, external command retrievers.
- Telemetry traces and `stats`.
- `last-brief.json` research diagnostics: `algorithm_policy`, `learning`, `token_funnel`, `scheduler`, `authorization_policy`, `task_retrieval_policy`, `policy_identity`, `graph_state`, `stage_latency_ms`.
- SQLite memory. V3 uses JSONL (decided: option A).

## 3. Package layout

New packages sit on existing V3 primitives (`routing`, `indexer.Search`, `retrieval.NewBM25`, `retrieval.SelectMMR`, `storage.WriteJSON`, `workspace.Load`).

| Package | Responsibility | V2 source |
|---|---|---|
| `internal/providers` | `Detect(root, cfg) Status`; `ExecutionProvider`; `ModelTier` | `providers.py` |
| `internal/brief` | `Build(ctx, root, task, Options) (Packet, error)`; `Format(Packet, fmt) string`; sufficiency, selective gate, evidence state, orchestration | `commands/workspace.py:cmd_brief,_format_brief`, `retrieval_policy.evaluate_sufficiency`, `selective_retrieval.py`, `orchestration.py`, `workflow_engine._evidence_state` |
| `internal/memory` | JSONL store: `Add`, `Search`, `List`, `Prune`, `Export`, `Import`; SHA-256 staleness | `memory.py` |
| `internal/handoff` | `Path`, `Render`, `Validate` | `handoff.py` |
| `internal/verify` | `Compress`, `RunChecks`, `Verify` | `compress.py`, `verify.py` |
| `internal/workspace` | add `Fingerprint`, `ChangedFiles` | `workspace_state.workspace_fingerprint`, `context_broker.detect_changed_files` |

## 4. Component contracts

### 4.1 providers

```go
type Status struct {
    Superpowers     bool `json:"superpowers"`
    CodeReviewGraph bool `json:"code_review_graph"`
    RTK             bool `json:"rtk"`
    Ripgrep         bool `json:"ripgrep"`
    Semantic        bool `json:"semantic"` // always false in this PR
    SCIP            bool `json:"scip"`     // always false in this PR
}
```

- Superpowers: `AI_WORKFLOW_SUPERPOWERS` env override (`1,true,yes,on` → true, any other set value → false); else same candidate dirs as V2 (`<root>/.agents/skills/superpowers`, `~/.agents/skills/superpowers`, `~/.codex/superpowers`, `~/.codex/skills/superpowers`, `~/.gemini/skills/superpowers`, `~/.config/opencode/skills/superpowers`); else any path containing `superpowers` under `~/.claude/plugins`, `~/.codex/plugins`, `~/.gemini/extensions` (walk stops at first match, depth-bounded to 6).
- Config `execution.superpowers.mode` / `context.crg.mode`: `on`/`off` force; `auto` detects.
- CRG auto: `code-review-graph` on `PATH` **and** at least one included repo has `ai-workspace/code-review-graph/<relative_path>/graph.db`. Freshness/schema validation of the graph DB is sub-project 2 (documented ceiling).
- `ExecutionProvider`: `superpowers` iff lane=full, `prefer_superpowers_for_full`, detected; else `native`.
- `ModelTier`: answer→`models.answer`, small→`models.small`, full+high→`models.full_high`, full→`models.full_medium`.

### 4.2 Context gathering (brief)

Candidate sources, each yielding `model.ContextItem` with V2 source labels:

1. `lightweight_index` — `indexer.Search` hits over included repos. `Text` = compact JSON `{"symbol","kind","file","line","repository"}` followed by a newline and a source window of ±`context.snippet_lines` (default 6) lines around the hit. Before reading, the file's SHA-256 is compared with the index `FileState`; mismatch or read error → item dropped (V2 `row_fresh` semantics: stale index rows are never served).
2. `targeted_source` — when `ripgrep` is available run `rg -n --no-heading -m 2 -F <longest query identifier>` per included repo (argv, no shell, 6 s timeout, nested repos and `ai-workspace` excluded); otherwise scan indexed files (≤500 KB) line-by-line. Text `<repo>/<path>:<line>: <trimmed line>`, score 1.0, limit `context.max_results_per_source`.
3. `durable_memory` — `memory.Search(..., excludeStale=true, minConfidence=memory.minimum_confidence)`; text = compact JSON of `id,type,summary,evidence,files,confidence`.

Selection: existing `retrieval.SelectMMR` with `MaxTokens` = adaptive token limit:

```text
fraction = high_sufficiency_fraction if pre_score >= 0.86
         = medium_sufficiency_fraction if pre_score >= 0.72
         = 1.0 otherwise
limit = min(lane_budget, max(1, int(lane_budget * fraction)))   // adaptive_budget.enabled=false → lane_budget
```

`pre_score` = sufficiency over all candidates before selection (V2 order). Lane budget = `budgets.<lane>.estimated_tokens` (fixes current `context` command, which always uses the answer budget).

Token estimate: `ceil(len(text)/4)` on the item text actually emitted (replaces whole-file size estimate for brief; `context` keeps its existing behavior).

### 4.3 Sufficiency (exact V2 port)

```text
coverage  = |query_tokens ∩ ∪item_tokens| / max(1,|query_tokens|)
diversity = |distinct sources|
exact     = normalized(query) ⊂ normalized(item.text)   (casefold, whitespace removed)
structural_complete = true if !required; else patterns ⊆ matched; else any matched
score = clamp(0.58*coverage + 0.17*min(1,diversity/2) + 0.15*exact + 0.10*structural_complete, 0, 1)
sufficient = score >= threshold && (structural_complete || !required)
```

Rounded to 4 decimals with Python `round` semantics (half-to-even) — fixture-verified. Tokenizer: existing `retrieval.Tokenize` (already V2-compatible). Structural items only exist once sub-project 2 lands; until then `structural_required` tasks yield `structural_complete=false` exactly as V2 does without CRG.

### 4.4 Selective gate (exact V2 port)

Order: no items → `no_context`; repository outside routing plan → `wrong_repository` (expected IDs = included repo IDs); structural conflict (no structural items yet → never); all stale → `irrelevant`; then reasons `required_structural_evidence_incomplete`, `query_coverage_below_floor` (`< context.selective_retrieval.minimum_coverage`, default 0.15), `sufficiency_below_threshold` → `irrelevant` if coverage below floor else `partial`; otherwise `supported`, reason `evidence_quality_gate_passed`.

### 4.5 Evidence state

```text
sufficient            if suff.sufficient && (selective.accept || !selective.enabled)
abstain               else if lane == answer
requires_exploration  otherwise
```

### 4.6 Orchestration (exact V2 port of `build_orchestration_contract`)

Complexity vector, score formula, slot/review/verification/graph-depth caps, skill lists, and CRG plan exactly as V2 `orchestration.py`. Budget from `execution.orchestration_budget` with V2 defaults (4 slots, 6 CRG calls, depth 3, 2 review, 2 verification) and V2 floors. `native_fallback = lane != answer && !superpowers`.

### 4.7 Workspace fingerprint

```json
{"root": "<abs>", "schema": 2, "git_head": "<sha|null>",
 "index_state_sha256": "<sha|null>", "changed_files": [{"path","state","sha256"}],
 "fingerprint": "<sha256 of canonical JSON of the 4 identity fields>"}
```

- `git_head`: `git rev-parse HEAD` at control root (3 s timeout), null when not a repo.
- `index_state_sha256`: SHA-256 of canonical JSON of `{repository_relative_path: index.Files}` for included repos (V3 index layout differs from V2; field name and semantics — "changes iff indexed content changes" — preserved, value not cross-comparable with V2).
- `changed_files`: explicit `--changed-file` values, else `git status --porcelain=v1 -z` of control root plus each included nested repo, prefixed by its relative path. States `present|missing|rejected|unreadable`; paths escaping the root are `rejected`.
- Canonical JSON = sorted keys, `,`/`:` separators, matching V2 `_stable_json_digest`.

### 4.8 Packet (JSON format)

```text
task, lane, risk, reasons, structural_context, confidence,
execution_provider, model_tier, execution_hint,
budget{estimated_context_tokens,max_output_tokens},
retrieval{retrieval_intent, retrieval_reason, workspace_roots, workspace_state,
          evidence_state, evidence_contract{schema,selected_count,authority},
          sufficiency{...,structural_patterns}, selective_retrieval{...,enabled},
          adaptive_context_tokens, hard_context_tokens,
          providers_attempted, providers_skipped, provider_errors, fallbacks,
          orchestration},
context[{source,text,score,stale,metadata}],
estimated_context_tokens_used, output_compression ("rtk"|"builtin"),
changed_files_detected, handoff_written?
```

`execution_hint` strings copied verbatim from V2. Non-answer lanes write `ai-workspace/generated/last-brief.json` atomically. `--write-handoff` on non-answer lanes writes `handoff.Render(...)` atomically.

### 4.9 Formats

`markdown` and `prompt` are line-for-line ports of V2 `_format_brief`, including `{confidence:.2f}`, `', '.join(...) or 'none'`, `[CONTEXT_START]`/`[CONTEXT_END]`, and the default `[INVARIANTS]` line.

### 4.10 handoff

`Path(root)` = `ai-workspace/handoff/HANDOFF.md`, legacy `.ai/HANDOFF.md` read fallback. `Validate(root, maxLines)` returns V2 error strings verbatim (missing file, line cap, `missing field: X` for the 10 required fields case-insensitively, placeholder regex). `Render` verbatim V2 template. CLI: `ai-workflow handoff` → `{"valid","errors"}`, exit 1 when invalid.

### 4.11 verify / compress

- `Compress(text, maxLines=80, maxChars=12000)`: V2 algorithm (75 % head, tail remainder, `... [N LINES OMITTED] ...`, `\n... [CHARACTER CAP REACHED]`, trailing newline). `--prefer-rtk` pipes through `rtk pipe` with 3 s timeout, falls back on failure.
- `RunChecks(root, checks)`: argv split with POSIX `shlex` rules (quotes, backslash escapes); **never** a shell; 120 s timeout per check, process killed on timeout (return code 124); stdout+stderr combined, compressed to 60 lines / 10000 chars; parse error → return code 2.
- `Verify` = checks + handoff validation; `ok` requires ≥1 check, all zero, no handoff errors. CLI `verify --check CMD... [--strict]`, exit 1 on `--strict` failure.

### 4.12 memory

- File `ai-workspace/memory/memory.jsonl`, one record per line, schema identical to V2 records (`id`, `type`, `created_at`, `verified_at`, `keywords`, `summary`, `evidence`, `files`, `source_hashes`, `confidence`).
- Types: V2's seven. `Add` rejects others, resolves files inside the root (escape → error), hashes existing files, clamps confidence to [0,1], id `mem-<12 hex>` from `crypto/rand`.
- Writes: read-all, append/replace, `storage` atomic write (temp + rename). A process-local mutex only; concurrent CLI processes may race (documented ceiling, same as V2 JSONL legacy).
- Staleness, search ranking (BM25 × confidence; stale last), list ordering, prune: V2 semantics.
- `Import(path)`: reads V2 `memory export` JSONL, validates each record, skips duplicate IDs, reports `{imported, skipped, invalid}`.
- CLI: `memory add --type T --keywords K --summary S [--evidence E] [--file F]... [--confidence C]`, `memory search Q [--limit N]`, `memory list`, `memory prune`, `memory export OUT`, `memory import IN`.

### 4.13 Configuration

Typed `config.Config` gains read access to V2 fields already present in the V2 default document: `execution.superpowers.mode`, `execution.orchestration_budget.*`, `context.crg.mode`, `context.selective_retrieval.{enabled,minimum_coverage}`. Absent → V2 defaults. `context.snippet_lines` is V3-only: read when present, default 6, **not** added to `DefaultDocument()` so the frozen V2 config fixture stays identical.

## 5. Error handling

- Missing config/registry → exit 1 with the existing load error and hint `run ai-workflow setup`.
- Missing/stale index for a repo → that repo contributes no `lightweight_index` items; recorded in `retrieval.fallbacks` as `index unavailable: <repo>`. Brief still succeeds.
- `rg` failure/timeout → file-scan fallback, `fallbacks` entry.
- Git unavailable → `git_head:null`, changed files empty unless explicit.
- Memory file corrupt line → that line skipped with a `fallbacks` entry in brief; `memory list` returns error exit 1 naming the line.
- No external process ever runs through a shell; all subprocesses get timeouts.

## 6. Compatibility harness extension

`compat/cases.json` gains sections captured by `tools/compat/capture_v2.py` from pure V2 functions:

| Section | V2 function | Input |
|---|---|---|
| `orchestration` | `build_orchestration_contract` | decision, diagnostics stub (intent, sufficiency, selective), changed files, root count, provider flags, budget overrides |
| `sufficiency` | `evaluate_sufficiency` | query, items (source, text, metadata), structural flags, threshold |
| `selective` | `evaluate_selective_retrieval` | items incl. stale flags, sufficiency, minimum coverage |
| `evidence_state` | `workflow_engine._evidence_state` | lane, sufficient |
| `handoff_validate` | `handoff.validate` | handoff text (written to temp dir), max lines |
| `handoff_render` | `handoff.render` | decision, provider, sources, goal |
| `compress` | `compress.compress_text` | text, max_lines, max_chars |
| `brief_format` | `commands.workspace._format_brief` | packet, fmt (`markdown`, `prompt`) |
| `model_tier` | `providers.model_tier` | decision |

`internal/compat/harness.go` executes the same cases through V3 and requires exact equality. ≥ 30 new cases covering every branch listed in §4.3–4.6, both formats, and all handoff error kinds.

## 7. Testing

- Unit tests per package for branches not reachable through compat cases (file I/O, staleness, path escape, timeout kill, rg fallback, import dedupe).
- CLI integration test: temp workspace with two child git repos → `setup` → `brief --format json` asserts packet keys, `evidence_state`, fingerprint stability across two runs, fingerprint change after a file edit, `last-brief.json` written; `--write-handoff` then `handoff` reports `valid: true` (V2's rendered template has every required field and no placeholders).
- `verify` test runs `go version` (present in CI) and a failing command.
- Benchmark: `brief` end-to-end latency on the existing retrieval fixture recorded in PR body (no regression gate in this PR).

## 8. Docs

- `README.md` command list and quick start (`setup` → `brief --format prompt`).
- `docs/COMPATIBILITY.md`: new frozen contracts, dropped diagnostic fields, JSONL memory + import path, `index_state_sha256` non-comparability.
- `docs/FOUNDATION_STATUS.md` and `docs/ROADMAP.md`: mark workflow/verification/handoff/memory parity (roadmap items 12–13 core) done; structural adapter next.
- `AGENTS.md`-style rulebook is not added in this PR.
