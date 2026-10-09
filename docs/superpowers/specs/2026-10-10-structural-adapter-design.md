# Structural Retrieval Adapter Design (CRG + SCIP)

**Date:** 2026-10-10
**Repository:** `Taki7980/Ai-workflow-V3`
**Base:** `f5ba3d26833188b3c3cd3eba1538dfc5f043ac8a`
**V2 oracle:** `554302c378f69809fbd09ff001eb7cfdb9af5005` (unchanged pin)
**Tracker:** #2, roadmap item 6
**Status:** design approved in chat; written spec awaiting review

## 1. Goal

`ai-workflow brief` returns real structural evidence when routing asks for it. Today every structural task (for example "who calls Build") ends with `structural evidence incomplete; source fallback used`.

Success criteria:

1. A structural brief against a repository with a fresh graph returns `code_review_graph` items with `structural_valid=true`, and `sufficiency.structural_complete=true` when the requested patterns are covered.
2. Missing, stale or failing graph/SCIP state is never used. It produces a note, a `provider_errors`/`providers_skipped` entry, and source fallback.
3. Non-structural briefs do no extra work (no subprocess, no file reads beyond today).
4. Pure functions (CRG payload compaction, CRG item, verified-empty rule, SCIP payload parsing, CRG validation tiers) match V2 on frozen fixtures.
5. No new Go module dependency. `go test ./...`, `go vet ./...`, compat harness, Linux/macOS/Windows CI green.

## 2. Decisions from brainstorming

- Scope A: full adapter with V3-owned state. V3 writes its own freshness manifests. Graphs or SCIP indexes synced by V2 report stale until `ai-workflow graph sync` / `scip sync` runs once.
- Approach 1: one package `internal/structural`. No retriever interface or registry. No `code-review-graph serve` daemon.

## 3. Non-goals

- SQLite validation of `graph.db` (V2 `validate_graph_database`: quick_check, tables, schema version, node count). Go stdlib has no SQLite driver. V3 trusts CRG's exit code, a non-empty `graph.db`, and the manifest SHA-256. Marked with a `ponytail:` comment.
- Multi-repository structural fan-out. A brief queries one anchor repository, as V2 does.
- Installing SCIP indexers or protobuf decoding of `index.scip`. V3 reads the `index.json` produced by `scip print --json`.
- V2 `repository_fingerprint` byte parity, `workspace_graph_fingerprint`, `graph_state` diagnostics, run journal.
- CRG `embed`, `postprocess`, `watch`, MCP install.

## 4. Package `internal/structural`

### 4.1 `paths.go` — state locations

- `RepoKey(rel string) string` — V2 `_repo_key`: `""`/`"."` map to `root`; each `/` segment has `[^A-Za-z0-9._-]+` replaced by `-`, trimmed of `.-_`, empty becomes `repo`; segments joined with `__`.
- Graph data dir: `<workspace>/ai-workspace/code-review-graph/<RepoKey>/` holding `graph.db` and `manifest.json`.
- SCIP data dir: `<workspace>/ai-workspace/scip/<RepoKey>/` holding `index.scip`, `index.json`, `manifest.json`.
- Both resolve through `workspace.Within`; an escaping path is an error, never followed.

### 4.2 `manifest.go` — freshness

- `RepoFingerprint(ctx, repoRoot string) (fingerprint, gitHead string)`: SHA-256 of canonical JSON `{"schema":1,"git_head":<head or null>,"changed_files":<changedState of git status for this repo>}`. Reuses `workspace.CanonicalJSON` and the existing changed-file hashing (exported as `workspace.ChangedState`).
- Manifest (`manifest_schema: 1`), written atomically via `storage.WriteFileAtomic`:
  - graph: `repository_relative_path`, `repository_fingerprint`, `git_head`, `graph_sha256`, `crg_version`, `generation_mode` (`build`|`update`), `generated_at`.
  - scip: `repository_relative_path`, `repository_fingerprint`, `git_head`, `language`, `indexer`, `index_sha256`, `json_sha256`, `generated_at`.
- `GraphStatus(ctx, workspaceRoot, repoRel string) Status` and `ScipStatus(...) Status` with `Status{Ready bool; Reason string; DataDir string}`. Reasons, checked in order and copied from V2: `graph.db is missing` / `index.scip is missing`, `manifest.json is missing`, `index.json is missing`, `manifest.json is unreadable`, `unsupported manifest schema`, `repository path identity mismatch`, `graph hash mismatch` / `SCIP index hash mismatch` / `SCIP JSON hash mismatch`, `repository fingerprint mismatch`, `Git HEAD mismatch`. Ready reason: `graph provenance matches repository state` / `SCIP provenance matches repository state`.

### 4.3 `crg.go` — graph queries

- Runner `runCRG(ctx, workspaceRoot, repoRoot string, args ...string) map[string]any`:
  - `exec.LookPath("code-review-graph")`; missing returns nil.
  - `GraphStatus` must be ready; otherwise nil.
  - No shell. Per-call timeout 8 s. `cmd.Dir = repoRoot`. Environment adds `CRG_DATA_DIR=<graph data dir>`, `CRG_REPO_ROOT=<repoRoot>`.
  - Accept only exit 0, non-blank stdout, a JSON object with `status == "ok"`. Anything else returns nil.
  - Stdout read capped at 8 MiB.
- `CRGContext(ctx, workspaceRoot, repoRoot, query, symbol string, changed []string, limit int, patterns []string) []model.ContextItem` — V2 `crg_context` sequencing:
  1. Requested patterns default: changed files present gives `impact`; else symbol gives `callers_of`; else `architecture`.
  2. Anchor needed for `callers_of`/`callees_of`/`tests_for`, or `impact` without changed files: `search <query> --limit <limit>`, first result with `qualified_name` or `name`.
  3. `impact`: files are changed files, else the anchor's path; call `impact --files <files...> --max-results <limit>`; score 9.
  4. Other patterns except `impact`, `architecture`, `references_to`: `query <pattern> <anchor>`; score 9; stop at `limit` items.
  5. `architecture` when below limit: `architecture`; score 8.
  6. Return `Validate(...)` of the items.
  - Total CRG subprocess calls per brief are capped by `execution.orchestration_budget.max_crg_calls` (default 6); a call over the cap is not made.
- Item text: compact JSON (`ensure_ascii=False`, `,`/`:` separators) of V2 `_compact_crg_payload`. Metadata: `pattern`, `structural_valid`, `result_count`, `empty_verified`, `truncated`, `anchor` when set.
- Verified empty (V2 `_verified_empty_crg`): casefolded `confidence` contains `real absence` and `current` and not `unverified`. CRG 2.3.8 `not_found` responses are rejected by the `status` gate before this rule.
- Path hygiene: CRG returns absolute `file_path` and `qualified_name` values (observed on 2.3.8: `D:/.../internal/brief/build.go::Build`). Before compaction, each row's `file_path`/`relative_path`/`path` and the path prefix of `qualified_name` are rewritten to repository-relative slash paths. Rows whose path is outside the repository are dropped. Absolute machine paths never reach the brief.

### 4.4 `scip.go` — SCIP reading and sync

- `ScipContext(ctx, workspaceRoot, repoRoot, query, symbol string, changed []string, limit int, patterns []string) []model.ContextItem`: requires `ScipStatus` ready, reads `index.json` (cap 64 MiB), then `ItemsFromScipPayload`.
- `ItemsFromScipPayload(repoRoot string, payload map[string]any, query, symbol string, changed []string, limit int, patterns []string) []model.ContextItem` mirrors V2 `items_from_scip_payload` exactly: camel/snake field lookup, `workspace.Within` path check, target = casefolded symbol or V2 `_query_anchor(query)`, `symbolRoles & 1` is a definition, `references_to` skips definitions, score 10 for definitions else 9 (+0.25 when the path is changed), text `<path>:<line+1>: <source line or display> [<role>] <display>`, metadata `path line language symbol symbol_name role pattern structural_valid=true result_count=1`.
- `DetectIndexer(repoRoot, language string) (Indexer, bool)` mirrors V2 `detect_indexer`: markers plus a source-suffix walk skipping V2 `_SKIP_DIRS`; returns an indexer only when exactly one language matches.

### 4.5 `validate.go` — evidence confidence

`Validate(ctx, workspaceRoot, repoRoot string, items []model.ContextItem, query, symbol string, changed []string, limit int) []model.ContextItem` mirrors V2 `validate_crg_item`:

- Non-CRG items pass through.
- No result rows: `empty_verified` gives `corroborated` with basis `["crg_verified_empty"]` and `structural_valid=true`; otherwise `candidate`, `structural_valid=false`.
- Rows: a row is source-confirmed when its path resolves inside the repository and the file contains one of the row's names; SCIP-confirmed when SCIP is ready and a SCIP item shares its path or name. Both gives `verified`; one gives `corroborated`; neither gives `candidate`.
- Metadata: `evidence_confidence`, `confidence_basis`, `source_confirmed_results`, `scip_confirmed_results`, `verified_results`, `structural_valid` (not candidate), `high_risk_eligible` (corroborated or verified).

## 5. CLI

All JSON output uses the existing `printJSON`. Exit 0 on success, 1 when any repository failed, 2 on usage errors.

- `ai-workflow graph sync [--repo REL] [--timeout SECONDS]` — for each included repository (or one): `build` when `graph.db` is missing else `update`, args `--repo <root> --quiet`, default timeout 180 s, then write the manifest. Output `{installed, attempted, ready, data_root, repositories:[{relative_path, action, ok, error?, graph_sha256?}]}`. CRG missing: `{"installed":false,...}`, exit 1.
- `ai-workflow graph status` — `{repositories:[{relative_path, ready, reason}]}`.
- `ai-workflow scip sync [--repo REL] [--language L] [--timeout SECONDS]` — detect indexer, run it in the repository writing `index.scip`, run `scip print --json index.scip` into `index.json`, move both into the data dir, write the manifest. Missing tool: `ok:false`, `error:"<exe> not installed"`. Ambiguous or unknown language: `error:"no unambiguous SCIP indexer; pass --language"`.
- `ai-workflow scip status` — same shape as `graph status`.

## 6. Brief integration (`internal/brief/build.go`)

After the first sufficiency pass and before selection:

1. Run only when `plan.UseStructural && !pre.StructuralComplete`.
2. Anchor repository: highest-scoring non-stale item carrying a repository path (index and targeted items carry their repo prefix); else the only included repository; else skip both steps with `providers_skipped` reason `no unambiguous repository anchor`.
3. Anchor symbol: `opt.Symbol`, else the top index item's symbol. Structural files: changed files under the anchor repository, else the anchor item's path.
4. Steps in order, each guarded by "structural evidence still incomplete":
   - `structural-expansion` (CRG): enabled when `providers.Status.CodeReviewGraph` and `GraphStatus` ready.
   - `scip-structural-expansion`: relevant only when `references_to` is requested; enabled when `ScipStatus` ready and `context.scip.mode != "off"`.
5. Not enabled: `providers_skipped["<label>"]="provider not configured"` or the status reason. Stale state also adds the note `code-review-graph stale for <rel>: <reason>; run ai-workflow graph sync` (SCIP: `scip ... run ai-workflow scip sync`).
6. Attempted steps append to `providers_attempted`; zero items record `provider_errors["<label>"]="no structural results"` and fallback `<label> returned no evidence`.
7. All steps share one 10 s deadline from `ctx`.
8. Structural items join the candidate pool. When `context.selector.mandatory_structural_evidence` is true, valid structural items whose pattern is requested are selected first, then MMR fills the remaining budget.
9. The final `"structural"` entry in `providers_skipped` is emitted only when no structural step ran.

`providers.Detect`: `code_review_graph` becomes true only when the binary exists and at least one included repository has a ready `GraphStatus`. New `Status.SCIP` is true when any included repository has a ready `ScipStatus` and `context.scip.mode != "off"`. Config gains typed `Context.SCIP{Mode}` (already present in the default document, so `DefaultDocument()` output is unchanged).

`evidence.go`: replace the `ponytail:` note in `EvaluateSelective` with V2 `_structural_conflict`, checked after the repository gate and before the stale gate. Among `code_review_graph`/`scip` items with `structural_valid` (CRG items only when `evidence_confidence` is `corroborated` or `verified`), group by `(pattern, symbol|qualified_name|path|file)`; a group holding both an `empty_verified` and a non-empty item returns `{"conflicting", false, 0, ["verified_structural_evidence_conflicts"]}`. Compat cases cover it.

## 7. Testing

- Unit:
  - `RepoKey` cases, path rewriting (absolute inside, outside dropped, `qualified_name` prefix), `GraphStatus`/`ScipStatus` each reason, `RepoFingerprint` changes on edit and on commit.
  - CRG runner with a fake `code-review-graph` (the test binary re-executed with an env flag, as `verify` tests do) emitting fixture JSON captured from CRG 2.3.8: `search`, `callers_of`, `not_found`, `impact`, a non-zero exit, a timeout, invalid JSON.
  - `ItemsFromScipPayload` on a small fixture `index.json`; `DetectIndexer` marker matrix.
  - `Validate` tiers: candidate, corroborated via source, corroborated via SCIP, verified, empty verified.
  - Brief: structural task with fake CRG ready reaches `structural_complete`; stale graph emits the note and falls back; non-structural task never executes the fake binary.
- Compat (V2 oracle, captured by `tools/compat/capture_v2.py`): `crg_compact` (`_compact_crg_payload` for impact, architecture, query), `crg_item` (`_crg_item` metadata), `crg_verified_empty`, `structural_conflict` (`evaluate_selective_retrieval` with conflicting CRG/SCIP items), `scip_items` (`items_from_scip_payload` over a fixture root), `repo_key`.
- E2E (skipped when `code-review-graph` is not on PATH): temp git repo with a Go caller/callee, `graph sync`, then `brief "who calls Foo"` reports `structural_complete=true`.
- CI needs no CRG or SCIP tools.

## 8. Risks

- CRG output shape drift across versions: parsing is permissive (missing keys mean zero results), and the `status=="ok"` gate rejects errors.
- Graph query latency (~0.8 s per call on this repository): only structural briefs pay it; capped by `max_crg_calls` and the 10 s deadline.
- Without SQLite validation a corrupt `graph.db` is detected only when CRG fails, which the runner already treats as no evidence.
