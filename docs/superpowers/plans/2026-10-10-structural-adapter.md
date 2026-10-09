# Structural Retrieval Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `ai-workflow brief` returns validated CRG and SCIP structural evidence when routing asks for it, with V3-owned freshness manifests and sync commands.

**Architecture:** One package `internal/structural` (paths, manifest, crg, scip, validate, sync). `internal/brief/build.go` calls it as an expansion step only when `plan.UseStructural` and the first sufficiency pass is not structurally complete. CLI gains `graph` and `scip` subcommands.

**Tech Stack:** Go 1.26 stdlib only. External tools invoked as subprocesses: `code-review-graph` (2.3.8 observed), `scip`, `scip-go`/`scip-python`/`scip-typescript`/`scip-java`.

**Spec:** `docs/superpowers/specs/2026-10-10-structural-adapter-design.md`

## Global Constraints

- No new Go module dependency; core binary stays CGO-optional.
- No shell: every subprocess via `exec.CommandContext(exe, args...)`.
- CRG per-call timeout 8 s; brief structural deadline 10 s total; sync default timeout 180 s.
- CRG calls per brief capped by `execution.orchestration_budget.max_crg_calls` (default 6).
- Accept CRG output only on exit 0, non-blank stdout, JSON object, `status == "ok"`; stdout capped at 8 MiB. `index.json` read cap 64 MiB.
- Absolute machine paths never reach a brief: CRG paths rewritten repo-relative via `workspace.Within`; outside rows dropped.
- State dirs: `ai-workspace/code-review-graph/<RepoKey>/`, `ai-workspace/scip/<RepoKey>/`; manifests `manifest_schema: 1`, written with `storage.WriteFileAtomic`.
- `config.DefaultDocument()` output unchanged.
- Compat oracle pin stays `554302c378f69809fbd09ff001eb7cfdb9af5005`.
- `go test ./...`, `go vet ./...`, compat harness green; CI needs no CRG/SCIP tools.

## Review Focus

1. Repository path with spaces (`D:/empty template/...`) in CRG `qualified_name` (`<abs path>::Name`): prefix rewrite must strip the repo root exactly, case-insensitively on Windows — pinned in Task 2 `TestRewritePathsWindowsSpaces`.
2. Edit after sync: any tracked or untracked file change must flip graph status to `repository fingerprint mismatch` — pinned in Task 1 `TestGraphStatusStaleAfterEdit`.
3. CRG hangs: brief returns within the deadline with `provider_errors["structural-expansion"]` set, no hang — pinned in Task 2 `TestRunCRGTimeout` and Task 6 deadline test.
4. Multi-repo workspace where the anchor item is in a nested repo: CRG must run with that repo's root and data dir, not the control root — pinned in Task 6 `TestBriefStructuralNestedRepoAnchor`.
5. Non-structural brief must never execute CRG — pinned in Task 6 `TestBriefNonStructuralSkipsCRG`.

---

### Task 1: State paths, repository fingerprint, manifests

**Files:**
- Create: `internal/structural/paths.go`, `internal/structural/manifest.go`
- Modify: `internal/workspace/fingerprint.go` (export `ChangedState`, `GitStatus`; keep old names as callers)
- Test: `internal/structural/manifest_test.go`

**Interfaces:**
- Consumes: `workspace.Within`, `workspace.CanonicalJSON`, `storage.WriteFileAtomic`.
- Produces:
  - `func RepoKey(rel string) string`
  - `func GraphDir(ws, rel string) (string, error)`, `func ScipDir(ws, rel string) (string, error)`
  - `func RepoFingerprint(ctx context.Context, repoRoot string) (fingerprint, gitHead string)`
  - `type Status struct { Ready bool \`json:"ready"\`; Reason string \`json:"reason"\`; DataDir string \`json:"data_dir"\` }`
  - `type GraphManifest struct` (fields per spec 4.2, json snake_case), `type ScipManifest struct`
  - `func WriteGraphManifest(ctx context.Context, ws, rel, crgVersion, mode string) (GraphManifest, error)`
  - `func WriteScipManifest(ctx context.Context, ws, rel, language, indexer string) (ScipManifest, error)`
  - `func GraphStatus(ctx context.Context, ws, rel string) Status`, `func ScipStatus(ctx context.Context, ws, rel string) Status`
  - `func sha256File(path string) (string, error)` (package-private)
  - `workspace.ChangedState(root string, changed []string) []ChangedFile`, `workspace.GitStatus(ctx, dir string) []string`

- [ ] **Step 1: Write failing tests** in `manifest_test.go`:
  - `TestRepoKey`: `""`, `"."` give `root`; `"services/api"` gives `services__api`; `"a b/c!d"` gives `a-b__c-d`; `"..."` gives `repo`.
  - `TestGraphStatusReasons`: temp git repo (helper `gitRepo(t)` runs `git init`, commits one file). Assert reasons in order as each artifact is added: `graph.db is missing`, `manifest.json is missing`; after `WriteGraphManifest` ready with reason `graph provenance matches repository state`; overwrite `graph.db` bytes gives `graph hash mismatch`; manifest with `manifest_schema: 2` gives `unsupported manifest schema`; manifest `repository_relative_path: "x"` gives `repository path identity mismatch`.
  - `TestGraphStatusStaleAfterEdit`: ready, then edit a tracked file gives `repository fingerprint mismatch`; restore and add an untracked file gives `repository fingerprint mismatch`.
  - `TestGraphStatusHeadChange`: ready, then new commit with same worktree state (commit the untracked-free change) gives `repository fingerprint mismatch` or `Git HEAD mismatch` (fingerprint includes head, so assert `!Ready`).
  - `TestScipStatusReasons`: `index.scip is missing`, `manifest.json is missing` (with both index files), `index.json is missing`, ready reason `SCIP provenance matches repository state`, edited `index.json` gives `SCIP JSON hash mismatch`.
- [ ] **Step 2: Run** `go test ./internal/structural/ -run 'RepoKey|Status' -v`. Expected: FAIL, package/functions undefined.
- [ ] **Step 3: Implement.** `RepoFingerprint`: `sha256(CanonicalJSON({"schema":1,"git_head":head|nil,"changed_files":ChangedState(repoRoot, GitStatus(ctx, repoRoot))}))`; head from `git rev-parse HEAD` (nil on error). `GraphDir`/`ScipDir` build `ai-workspace/<kind>/<RepoKey(rel)>` then `workspace.Within(ws, ...)`. Status reason strings copied verbatim from spec 4.2. Manifest `generated_at` UTC RFC3339 with `Z`.
- [ ] **Step 4: Run** `go test ./internal/structural/ ./internal/workspace/ -v`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(structural): state paths, repository fingerprint, freshness manifests`.

### Task 2: CRG runner, payload compaction, CRGContext

**Files:**
- Create: `internal/structural/crg.go`, `internal/structural/testdata/fakecrg/main.go`, `internal/structural/structuraltest/fake.go`, `internal/structural/testdata/crg/{search,callers_of,not_found,impact,architecture}.json`
- Test: `internal/structural/crg_test.go`

**Interfaces:**
- Consumes: Task 1 `GraphStatus`, `GraphDir`.
- Produces:
  - `type Query struct { Text, Symbol string; Changed []string; Limit int; Patterns []string; MaxCalls int }`
  - `func runCRG(ctx context.Context, ws, rel string, calls *int, maxCalls int, args ...string) map[string]any` (increments `*calls`; returns nil when `*calls >= maxCalls`)
  - `func CompactCRG(payload map[string]any, pattern string, limit int) map[string]any` (V2 `_compact_crg_payload`)
  - `func CRGItem(payload map[string]any, pattern string, score float64, limit int, anchor string) model.ContextItem` (V2 `_crg_item`)
  - `func VerifiedEmptyCRG(payload map[string]any) bool`
  - `func ResultCount(payload map[string]any, pattern string) int`
  - `func rewritePaths(repoRoot string, payload map[string]any)` (in place)
  - `func CRGContext(ctx context.Context, ws, rel string, q Query) []model.ContextItem` (returns items already passed through `Validate` from Task 4; until Task 4 lands, return unvalidated — Task 4 adds the call)
  - `const crgTimeout = 8 * time.Second` as `var crgTimeout` for tests.

- [ ] **Step 1: Write fixtures.** Copy shapes observed from CRG 2.3.8 (spec 4.3) with `{{ROOT}}` in place of the absolute repo path, e.g. `"qualified_name": "{{ROOT}}/pkg/a.go::Foo"`, `"file_path": "{{ROOT}}/pkg/a.go"`. `not_found.json`: `{"status":"not_found","summary":"No node found matching 'X'.","confidence":"target not indexed: ..."}`.
- [ ] **Step 2: Write failing tests** in `crg_test.go`. Fake binary: `internal/structural/testdata/fakecrg/main.go` (`package main`) picks a fixture by subcommand (`search`, `query <pattern>`, `impact`, `architecture`, `--version` prints `code-review-graph 2.3.8`; `build`/`update` write `graph.db` bytes `fake` into `CRG_DATA_DIR`), honours `CRG_FAKE_MODE` (`fail` exits 3, `hang` sleeps 30 s, `garbage` prints `not json`, `nodb` exits 0 without writing), appends `CRG_REPO_ROOT` + args to `CRG_FAKE_LOG` when set, substitutes `{{ROOT}}` with slash-form `CRG_REPO_ROOT`, reads fixtures from `CRG_FAKE_FIXTURES`. Exported test helper `structuraltest.FakeCRG(t) (binDir string)` in `internal/structural/structuraltest/fake.go` builds it once per process (`go build -o <tmp>/code-review-graph[.exe]`, `sync.Once`) and sets `CRG_FAKE_FIXTURES`; callers prepend `binDir` to `PATH` with `t.Setenv`. Task 2, 5 and 6 tests all use it; production lookup stays `exec.LookPath`.
  - `TestCompactCRGImpact`/`Architecture`/`Query`: key sets and truncation to `limit` match V2 (`total_impacted`, `impacted_files`, `impacted_nodes`, `edges`; `communities`, `entry_points`, `hub_nodes`, `bridge_nodes`; `result_count`, `results`, `edges`), empty values omitted for `target summary confidence truncated`.
  - `TestVerifiedEmptyCRG`: `"Real absence; graph current"` true; `"real absence, current, unverified"` false; `""` false.
  - `TestRewritePathsWindowsSpaces`: repo root `D:/empty template/R` (use `t.TempDir()` + `"with space"` subdir for real FS); row `file_path` `<root>/pkg/a.go` becomes `pkg/a.go`; `qualified_name` `<root>/pkg/a.go::Foo` becomes `pkg/a.go::Foo`; row with `/elsewhere/x.go` removed from `results`.
  - `TestCRGContextCallers`: graph manifest ready (Task 1 helper), `Query{Text:"who calls Foo", Patterns:["callers_of"], Limit:6, MaxCalls:6}` returns one item: source `code_review_graph`, metadata `pattern=callers_of`, `result_count>0`, `anchor` = rewritten qualified name; item text contains no `{{ROOT}}` and no temp-dir prefix.
  - `TestCRGContextStaleGraphNoCall`: graph without manifest: returns nil and fake binary never ran (helper appends to a `CRG_FAKE_LOG` file; assert file absent).
  - `TestRunCRGRejects`: `not_found` fixture, `fail`, `garbage` each give nil.
  - `TestRunCRGTimeout`: `crgTimeout=300ms`, mode `hang`: nil within 10 s.
  - `TestCRGMaxCalls`: `MaxCalls:1` with `callers_of` (needs search + query) gives no items and exactly one logged call.
- [ ] **Step 3: Run** `go test ./internal/structural/ -run 'CRG|Rewrite' -v`. Expected: FAIL, undefined.
- [ ] **Step 4: Implement** per spec 4.3. Environment: `os.Environ()` plus `CRG_DATA_DIR`, `CRG_REPO_ROOT`. Args always end with `--repo <repoRoot>` for `query`, `impact`, `search`, `architecture`. `rewritePaths` walks `results`, `impacted_nodes`, `changed_nodes`, `impacted_files` (strings), `changed_files`; for strings use `workspace.Within(repoRoot, p)`; for `qualified_name` split on the last `::`. Item text via the brief's compact encoding (copy `compactJSON` helper: `json.Encoder` with `SetEscapeHTML(false)`).
- [ ] **Step 5: Run** `go test ./internal/structural/ -v`. Expected: PASS.
- [ ] **Step 6: Commit** `feat(structural): code-review-graph runner and context items`.

### Task 3: SCIP payload items, indexer detection, ScipContext

**Files:**
- Create: `internal/structural/scip.go`, `internal/structural/testdata/scip/index.json`, `internal/structural/testdata/scip/repo/pkg/a.go`
- Test: `internal/structural/scip_test.go`

**Interfaces:**
- Consumes: Task 1 `ScipStatus`, `ScipDir`; Task 2 `Query`.
- Produces:
  - `func ItemsFromScipPayload(repoRoot string, payload map[string]any, q Query) []model.ContextItem`
  - `func QueryAnchor(query string) string` (V2 `_query_anchor`)
  - `type Indexer struct { Language, Executable string; Args []string }`
  - `func DetectIndexer(repoRoot, language string) (Indexer, bool)`
  - `func ScipContext(ctx context.Context, ws, rel string, q Query) []model.ContextItem`

- [ ] **Step 1: Read V2** `ai_workflow/scip.py` `_field`, `_symbol_tail`, `_query_anchor`, `_source_line` (lines 229-258) and port them verbatim in behavior.
- [ ] **Step 2: Write failing tests:**
  - `TestItemsFromScipPayload`: fixture with one definition (`symbolRoles: 1`) and one reference of `Foo` in `pkg/a.go`, plus a document with `relativePath: "../escape.go"`. `Query{Symbol:"Foo", Limit:10}` gives 2 items, definition score 10, reference 9; text `pkg/a.go:<line+1>: <source line> [definition] Foo`; metadata `structural_valid=true`, `pattern=definition_of`/`references_to`; escape document skipped. With `Patterns:["references_to"]` gives only the reference. With `Changed:["pkg/a.go"]` reference score 9.25. `Limit:1` gives 1.
  - `TestDetectIndexer`: `go.mod`+`.go` gives `go`/`scip-go`/`["./..."]`; `go.mod`+`pyproject.toml` with both sources gives `false`; explicit language `"py"` gives `python` with args `index . --project-name <base>`; `node_modules/x.py` alone does not count.
  - `TestScipContextNotReady`: no manifest gives nil.
- [ ] **Step 3: Run** `go test ./internal/structural/ -run 'Scip|Indexer' -v`. Expected: FAIL.
- [ ] **Step 4: Implement** per spec 4.4; JSON numbers decode as `float64`; `index.json` read via `io.LimitReader` 64 MiB, over-cap returns nil.
- [ ] **Step 5: Run** `go test ./internal/structural/ -v`. Expected: PASS.
- [ ] **Step 6: Commit** `feat(structural): SCIP payload items and indexer detection`.

### Task 4: Validation tiers and structural conflict gate

**Files:**
- Create: `internal/structural/validate.go`
- Modify: `internal/structural/crg.go` (CRGContext returns `Validate(...)`), `internal/brief/evidence.go` (`EvaluateSelective`)
- Test: `internal/structural/validate_test.go`, `internal/brief/evidence_test.go`

**Interfaces:**
- Consumes: Task 2 `Query`, Task 3 `ScipContext`.
- Produces: `func Validate(ctx context.Context, ws, rel string, items []model.ContextItem, q Query) []model.ContextItem`; `brief.structuralConflict(items []model.ContextItem) bool`.

- [ ] **Step 1: Read V2** `structural_validation.py` lines 25-106 (`_rows`, `_row_path`, `_row_names`, `_source_confirms`, `_scip_keys`, `_scip_confirms`) and port behavior.
- [ ] **Step 2: Write failing tests:**
  - `TestValidateCandidate`: CRG item, row path `pkg/a.go` but file lacks the name: `evidence_confidence=candidate`, `structural_valid=false`, `high_risk_eligible=false`.
  - `TestValidateCorroboratedSource`: file contains `Foo`: `corroborated`, basis `["source"]`, `source_confirmed_results=1`.
  - `TestValidateVerified`: also SCIP ready with `Foo` in `pkg/a.go`: `verified`, basis `["source","scip"]`, `verified_results=1`.
  - `TestValidateEmptyVerified`: no rows, `empty_verified=true`: `corroborated`, basis `["crg_verified_empty"]`, `structural_valid=true`.
  - `TestValidatePassesNonCRG`: `scip` item unchanged.
  - `TestEvaluateSelectiveStructuralConflict` (brief): two `code_review_graph` items, both `structural_valid`, `evidence_confidence=verified`, `pattern=callers_of`, metadata `symbol=Foo`, one `empty_verified=true`: returns condition `conflicting`, accept false, reasons `["verified_structural_evidence_conflicts"]`. Same items with one `candidate`: no conflict.
- [ ] **Step 3: Run** `go test ./internal/structural/ ./internal/brief/ -run 'Validate|Conflict' -v`. Expected: FAIL.
- [ ] **Step 4: Implement.** Conflict check placed after the repository gate and before the stale gate; delete the `ponytail:` line above `EvaluateSelective`.
- [ ] **Step 5: Run** `go test ./internal/structural/ ./internal/brief/ -v`. Expected: PASS.
- [ ] **Step 6: Commit** `feat(structural): evidence confidence tiers and conflict gate`.

### Task 5: Sync functions and `graph` / `scip` CLI

**Files:**
- Create: `internal/structural/sync.go`, `internal/cli/structural_commands.go`
- Modify: `internal/cli/cli.go` (dispatch `graph`, `scip`; usage text)
- Test: `internal/structural/sync_test.go`, `internal/cli/structural_commands_test.go`

**Interfaces:**
- Consumes: Task 1 manifests and statuses, Task 3 `DetectIndexer`, Task 2 `structuraltest.FakeCRG`.
- Produces:
  - `type SyncEntry struct { RelativePath string \`json:"relative_path"\`; Action string \`json:"action,omitempty"\`; OK bool \`json:"ok"\`; Error string \`json:"error,omitempty"\`; SHA256 string \`json:"graph_sha256,omitempty"\` }`
  - `type SyncReport struct { Installed bool \`json:"installed"\`; Attempted int \`json:"attempted"\`; Ready int \`json:"ready"\`; DataRoot string \`json:"data_root"\`; Repositories []SyncEntry \`json:"repositories"\` }`
  - `func SyncGraphs(ctx context.Context, ws string, rels []string, timeout time.Duration) SyncReport`
  - `func SyncScip(ctx context.Context, ws string, rels []string, language string, timeout time.Duration) SyncReport`
  - `var scipPath = exec.LookPath` (tests swap)
  - CLI: `graphCmd(root string, args []string, out, errOut io.Writer) int`, `scipCmd(...)`.

- [ ] **Step 1: Write failing tests:**
  - `TestSyncGraphsBuildThenUpdate`: fake CRG on `PATH`. First sync action `build`, `ok=true`, manifest written, `GraphStatus` ready; second sync action `update`.
  - `TestSyncGraphsNotInstalled`: `PATH` set to an empty temp dir: `Installed=false`, `Attempted=0`.
  - `TestSyncGraphsEmptyDB`: `CRG_FAKE_MODE=nodb`: `ok=false`, `error="graph database was not created"`.
  - `TestSyncScipMissingTool`: `go.mod` repo, `scipPath` fails for `scip-go`: `ok=false`, `error="scip-go not installed"`.
  - `TestSyncScipAmbiguous`: `error="no unambiguous SCIP indexer; pass --language"`.
  - CLI `TestGraphStatusCmd`: `RunWithStdin(["graph","status"])` prints JSON with `repositories[0].reason`, exit 0; `graph bogus` exit 2; `graph sync` with CRG missing exit 1.
- [ ] **Step 2: Run** `go test ./internal/structural/ ./internal/cli/ -run 'Sync|GraphStatusCmd' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** `rels` empty means all included repositories from `workspace.Load`. Graph: `<crg> build|update --repo <root> --quiet` with `CRG_DATA_DIR`, `crg_version` from `<crg> --version` (5 s timeout; trimmed last field, `unknown` on error). Error text trimmed to 500 chars. `ponytail: no SQLite validation; trusts CRG exit code, non-empty graph.db and manifest SHA-256`. SCIP: run indexer in repo root to produce `index.scip` there, then `scip print --json index.scip` stdout into `<ScipDir>/index.json`, move `index.scip` into `<ScipDir>` (rename, fall back to copy+remove), write manifest. CLI flags: `--repo` (repeatable via existing `stringList`), `--timeout` seconds (default 180), `--language` (scip only).
- [ ] **Step 4: Run** `go test ./internal/structural/ ./internal/cli/ -v`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(cli): graph and scip sync/status commands`.

### Task 6: Provider detection, config, brief integration

**Files:**
- Modify: `internal/config/config.go` (typed `SCIP{Mode string \`json:"mode"\`}` under `Context` as `SCIP SCIP \`json:"scip"\``), `internal/providers/providers.go` (`hasCRG` uses `structural.GraphStatus`; `SCIP` detection), `internal/brief/build.go` (expansion step, mandatory selection), `internal/brief/gather.go` if anchor helpers fit there
- Test: `internal/brief/structural_test.go`, `internal/providers/providers_test.go`, `internal/config/config_test.go`

**Interfaces:**
- Consumes: Task 1 `GraphStatus`, `ScipStatus`, `WriteGraphManifest`; Task 2 `CRGContext`, `Query`, `structuraltest.FakeCRG`; Task 3 `ScipContext`.
- Produces: `func expandStructural(ctx context.Context, g *gathered, root string, reg []workspace.Repository, task string, opt Options, plan model.RetrievalPlan, changed []string, status providers.Status, cfg config.Config, threshold float64) (skipped map[string]string, errors map[string]string)`.

- [ ] **Step 1: Write failing tests:**
  - `TestConfigDefaultDocumentUnchanged` (existing test must still pass) and `cfg.Context.SCIP.Mode == "auto"`.
  - `TestDetectCRGRequiresFreshGraph`: binary on PATH + `graph.db` without manifest gives `CodeReviewGraph=false`; after `WriteGraphManifest` gives true.
  - `TestBriefStructuralCRG`: temp workspace with `pkg/a.go` defining `Foo` and a caller, setup + index + graph manifest, fake CRG on PATH; `Build(ctx, root, "who calls Foo", Options{})` gives `Retrieval.Sufficiency.StructuralComplete=true`, `ProvidersAttempted` contains `structural-expansion`, a selected item with source `code_review_graph`, `ProvidersSkipped` lacks `structural`, no `source fallback used` note.
  - `TestBriefStructuralStaleGraph`: same without manifest: fallbacks contain `code-review-graph stale for .: manifest.json is missing; run ai-workflow graph sync`, `StructuralComplete=false`.
  - `TestBriefNonStructuralSkipsCRG`: task `fix typo in README`: fake CRG log file absent.
  - `TestBriefStructuralNestedRepoAnchor`: control root plus included nested repo `svc` containing `Foo`; fake logs `CRG_REPO_ROOT`; assert it ends with `svc`.
  - `TestBriefStructuralDeadline`: fake mode `hang`, deadline var `structuralDeadline=500ms`: `Build` returns within 10 s, `ProviderErrors["structural-expansion"]` non-empty.
- [ ] **Step 2: Run** `go test ./internal/brief/ ./internal/providers/ ./internal/config/ -v`. Expected: new tests FAIL.
- [ ] **Step 3: Implement** per spec 6. Anchor: highest-score non-stale item whose metadata has `repository` (index items) or whose text prefix maps to an included repo (targeted items `rel/path:line:`); else single included repo. Anchor symbol: `opt.Symbol`, else `indexHeader.Symbol` decoded from the top index item text. `var structuralDeadline = 10 * time.Second`. Selection: `Required: true` for items with `structural_valid` and requested `pattern`, and pass `MandatoryRequired: cfg.Context.Selector.MandatoryStructuralEvidence` to `SelectMMR`. Replace `hasCRG` body; delete its `ponytail:` line.
- [ ] **Step 4: Run** `go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(brief): structural expansion via code-review-graph and SCIP`.

### Task 7: Compat oracle cases, E2E, docs

**Files:**
- Modify: `tools/compat/capture_v2.py`, `compat/cases.json`, `compat/fixtures/v2-contracts.json`, `internal/compat/workflow.go`
- Create: `compat/fixtures/scip-root/pkg/a.go`
- Create: `internal/structural/e2e_test.go`
- Modify: `README.md`, `docs/COMPATIBILITY.md`, `docs/FOUNDATION_STATUS.md`, `docs/ROADMAP.md`

**Interfaces:**
- Consumes: `structural.CompactCRG`, `CRGItem`, `VerifiedEmptyCRG`, `ItemsFromScipPayload`, `RepoKey`, `brief.EvaluateSelective`.
- Produces: new case/fixture groups `crg_compact`, `crg_item`, `crg_verified_empty`, `scip_items`, `repo_key`, `structural_conflict`.

- [ ] **Step 1: Add cases** to `compat/cases.json` (at least: 3 `crg_compact` — impact/architecture/callers_of with `limit` 2 truncating; 3 `crg_item` — non-empty, verified-empty, not-verified-empty; 4 `crg_verified_empty`; 2 `scip_items` — symbol and `references_to` over `compat/fixtures/scip-root`; 5 `repo_key`; 2 `structural_conflict`). Payloads use repo-relative paths only (no rewriting in compat).
- [ ] **Step 2: Extend `capture_v2.py`** `capture_workflow_contracts` with the six groups calling V2 `_compact_crg_payload`, `_crg_item(...).to_dict()`-equivalent (`{"source","text","score","stale","metadata"}`), `_verified_empty_crg`, `items_from_scip_payload` (root = `compat/fixtures/scip-root` resolved), `_repo_key` from `code_review_graph`, and `evaluate_selective_retrieval(...).to_dict()`. Run: `python tools/compat/capture_v2.py --v2-root "../ai-workflow-control-plane-v2" --cases compat/cases.json --output compat/fixtures/v2-contracts.json --expected-commit 554302c378f69809fbd09ff001eb7cfdb9af5005` (check out the pin in V2 first if HEAD differs; restore after). Expected: exit 0, new keys present.
- [ ] **Step 3: Extend `internal/compat/workflow.go`** with the case/fixture types and checks. Run `go test ./internal/compat/ -v`. Expected: PASS; on mismatch, fix Go code, never the fixture.
- [ ] **Step 4: E2E** `TestE2EGraphSyncBrief`: `t.Skip` unless `exec.LookPath("code-review-graph")` succeeds and `testing.Short()` is false. Temp git repo with `pkg/a.go` (`func Foo()`, `func Bar(){ Foo() }`), commit, `ai-workflow setup` equivalent via `cli.RunWithStdin`, `graph sync`, then `brief "who calls Foo" --format json`: `structural_complete=true`, a `code_review_graph` item whose text contains `Bar` and no absolute temp path. Run `go test ./internal/structural/ -run E2E -v`. Expected: PASS locally (CRG installed), SKIP in CI.
- [ ] **Step 5: Docs.** README: `graph sync|status`, `scip sync|status` usage and the "run sync after V2" note. COMPATIBILITY: new contract groups and count. FOUNDATION_STATUS/ROADMAP: item 6 done; deviations (V3 manifests, no SQLite validation, single anchor repo).
- [ ] **Step 6: Full check** `go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 7: Commit** `test(compat): structural adapter V2 contracts; docs`.
