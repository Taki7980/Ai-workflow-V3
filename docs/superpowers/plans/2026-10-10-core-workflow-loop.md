# Core Workflow Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `brief`, `handoff`, `verify`, `compress`, `memory` to the V3 CLI with V2 contract parity proven by the differential harness.

**Architecture:** Five small packages (`providers`, `handoff`, `verify`, `memory`, `brief`) plus `workspace.Fingerprint`, all built on existing V3 primitives (`routing`, `indexer.Search`, `retrieval.NewBM25`, `retrieval.SelectMMR`, `storage.WriteJSON`). Pure decision functions are verified against frozen V2 outputs captured by `tools/compat/capture_v2.py`.

**Tech Stack:** Go 1.26 stdlib only; Python 3.11+ for the V2 oracle capture script.

**Spec:** `docs/superpowers/specs/2026-10-10-core-workflow-loop-design.md`

## Global Constraints

- No new Go module dependency (`go.mod` require block unchanged).
- Never run a subprocess through a shell; every subprocess has a timeout.
- `config.DefaultDocument()` output must not change (frozen V2 config fixture).
- Existing `context` command behavior and benchmark fixtures unchanged.
- Floats that V2 rounds with `round(x, 4)` are rounded in Go with `strconv.ParseFloat(strconv.FormatFloat(x, 'f', 4, 64), 64)` (correct rounding, matches CPython).
- Error/reason strings copied verbatim from V2 source.
- V2 oracle pin stays `554302c378f69809fbd09ff001eb7cfdb9af5005`.
- Must pass: `go test ./...`, `go vet ./...`, `go run ./cmd/compat-harness --cases compat/cases.json --fixtures compat/fixtures/v2-contracts.json --lock compat/v2.lock`.

## Review Focus

1. Task text containing quotes/newlines/unicode in `brief --format prompt` — must render without escaping changes (pinned in Task 7 compat `brief_format` case with `"fix \"quoted\" ñ"`).
2. Workspace whose index is missing or stale for one repo — `brief` must still succeed with a `fallbacks` entry (Task 8 test `TestBriefMissingIndexDegrades`).
3. `memory add --file ../outside.txt` — must error, never hash outside root (Task 4 test `TestAddRejectsEscapingFile`).
4. `verify --check` with a hanging command — must be killed at timeout and report 124 (Task 3 test `TestRunChecksTimeout`, timeout injectable).
5. Windows CRLF files in snippet windows and handoff validation — line counting must treat `\r\n` as one line (Task 2 test `TestValidateCRLF`, Task 8 snippet test uses CRLF fixture).

---

### Task 1: Config fields + providers

**Files:**
- Modify: `internal/config/config.go` (types only; `Default()` gains V2 defaults; `DefaultDocument()` untouched)
- Create: `internal/providers/providers.go`
- Test: `internal/providers/providers_test.go`, `internal/config/config_test.go`

**Interfaces:**
- Produces:
  - `config.Execution` adds `Superpowers struct{Mode string \`json:"mode"\`}` and `OrchestrationBudget OrchestrationBudget \`json:"orchestration_budget"\``
  - `config.OrchestrationBudget{MaxAgentSlots, MaxCRGCalls, MaxGraphDepth, ReviewPasses, VerificationPasses int}` json `max_agent_slots,max_crg_calls,max_graph_depth,review_passes,verification_passes`; defaults 4,6,3,2,2
  - `config.Context` adds `SelectiveRetrieval{Enabled bool; MinimumCoverage float64}` (json `selective_retrieval`, defaults true, 0.15) and `SnippetLines int \`json:"snippet_lines"\`` (default 6)
  - `providers.Status{Superpowers, CodeReviewGraph, RTK, Ripgrep, Semantic, SCIP bool}` (json snake_case per spec §4.1)
  - `providers.Detect(root string, cfg config.Config, reg workspace.Registry) Status`
  - `providers.ExecutionProvider(lane model.Lane, cfg config.Config, s Status) string`
  - `providers.ModelTier(d model.RouteDecision, cfg config.Config) string`
  - package var `lookPath = exec.LookPath` and `homeDir = os.UserHomeDir` for test injection

- [ ] **Step 1: Write failing tests**
  - `TestDefaultsMatchV2Document`: `config.Default()` orchestration budget == {4,6,3,2,2}, selective {true,0.15}, superpowers mode "auto", snippet lines 6; and decoding `DefaultDocument()` JSON yields the same values.
  - `TestDetectSuperpowersEnvOverride`: `t.Setenv("AI_WORKFLOW_SUPERPOWERS","yes")` → true; `"0"` → false even when `<root>/.agents/skills/superpowers` exists.
  - `TestDetectSuperpowersPluginGlob`: homeDir → temp; create `.claude/plugins/cache/x/superpowers/1/`; → true.
  - `TestDetectCRGRequiresBinaryAndGraph`: lookPath stub found + `ai-workspace/code-review-graph/svc/graph.db` for included repo `svc` → true; missing db → false; mode "off" → false; mode "on" without anything → true.
  - `TestExecutionProvider`: full+detected → "superpowers"; full+`PreferSuperpowersForFull=false` → "native"; small+detected → "native".
  - `TestModelTier`: answer→fast, small→fast, full/high→capable, full/medium→standard.
- [ ] **Step 2: Run** `go test ./internal/providers ./internal/config` — Expected: FAIL (undefined).
- [ ] **Step 3: Implement.** Plugin walk: `filepath.WalkDir` per base, return `fs.SkipAll` on first dir whose name contains `superpowers`; skip dirs deeper than 6 below base.
- [ ] **Step 4: Run** same command — Expected: PASS. Run compat harness — Expected: `"ok": true` (config doc unchanged).
- [ ] **Step 5: Commit** `feat(providers): detect superpowers/crg and pick execution provider`

### Task 2: handoff

**Files:**
- Create: `internal/handoff/handoff.go`, `internal/handoff/handoff_test.go`

**Interfaces:**
- Produces:
  - `handoff.RelativePath = "ai-workspace/handoff/HANDOFF.md"`, `LegacyRelativePath = ".ai/HANDOFF.md"`
  - `handoff.Path(root string) string`
  - `handoff.Validate(root string, maxLines int) []string` (never nil; empty slice when valid)
  - `handoff.Render(d model.RouteDecision, provider string, sources []string, goal string) string`

- [ ] **Step 1: Write failing tests**
  - `TestValidateMissing`: → `[]string{"missing ai-workspace/handoff/HANDOFF.md"}`.
  - `TestValidateRenderedIsValid`: write `Render(full/high decision,"native",["a","a","b"],"  goal ")` → `Validate` empty; rendered text contains `- **Context sources**: a, b` and `- **Goal / state**: goal / routed`.
  - `TestValidateErrors`: 31-line file without fields → contains `"handoff has 31 lines; cap is 30"` and `"missing field: Lane / risk"`; file containing `[TODO later]` → contains `"handoff still contains template placeholders"`.
  - `TestValidateLegacyFallback`: only `.ai/HANDOFF.md` present → validated.
  - `TestValidateCRLF`: rendered text with `\r\n` → valid, line count same as LF.
- [ ] **Step 2: Run** `go test ./internal/handoff` — FAIL.
- [ ] **Step 3: Implement.** Placeholder regex (Go RE2, case-insensitive), verbatim from V2:

```go
var placeholderRE = regexp.MustCompile(`(?i)\[(?:answer\||small\||full\||low\||medium\||high\||goal and|bounded edit|cache/index|max \d+|contracts that|files changed|exact verification|one action|TODO|YOUR_)[^\]]*\]|\{\{[^}]+\}\}|<(?:insert|replace|TODO)[^>]*>`)
```

  Line count = Python `splitlines()` semantics: split on `\n` after normalizing `\r\n`/`\r`, drop final empty element when text ends with newline. Sources deduped preserving order; empty → `none`.
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** `feat(handoff): render and validate compact handoff`

### Task 3: verify + compress

**Files:**
- Create: `internal/verify/compress.go`, `internal/verify/verify.go`, `internal/verify/split.go`, `internal/verify/verify_test.go`

**Interfaces:**
- Consumes: `handoff.Validate`.
- Produces:
  - `verify.Compress(text string, maxLines, maxChars int) string`
  - `verify.CompressPreferRTK(text string, maxLines, maxChars int, filter string) string`
  - `verify.SplitCommand(s string) ([]string, error)` — POSIX shlex
  - `verify.CheckResult{Check string; ReturnCode int; Output string}` json `check,returncode,output`
  - `verify.Result{OK bool; Checks []CheckResult; HandoffErrors []string}` json `ok,checks,handoff_errors`
  - `verify.RunChecks(ctx context.Context, root string, checks []string) Result`
  - `verify.Verify(ctx context.Context, root string, checks []string, handoffMaxLines int) Result`
  - package var `checkTimeout = 120 * time.Second`

- [ ] **Step 1: Write failing tests**
  - `TestCompressShortUnchanged`: `"a\nb"` → `"a\nb\n"`; `""` → `""`.
  - `TestCompressLines`: 100 lines `l0..l99`, maxLines 80 → 60 head lines, `"... [20 LINES OMITTED] ..."`, 20 tail lines ending `l99\n`.
  - `TestCompressChars`: 20000 `x`, maxChars 12000 → length `11960 + len("\n... [CHARACTER CAP REACHED]") + 1`, suffix `"[CHARACTER CAP REACHED]\n"`.
  - `TestSplitCommand`: `go test "./a b" 'c d' e\ f` → `[go test ./a b c d e f]` with `./a b`, `c d`, `e f` as single args; `"unterminated` → error.
  - `TestRunChecksPassFail`: checks `go version` → rc 0; `go definitely-not-a-command` → rc != 0; `ok=false`.
  - `TestRunChecksParseError`: `"\"bad` → rc 2, output prefix `parse error:`.
  - `TestRunChecksTimeout`: `checkTimeout = 200ms`; check = test binary re-exec sleeping (`os.Args[0] -test.run=TestHelperSleep`, env `VERIFY_HELPER=1`) → rc 124.
  - `TestVerifyRequiresChecksAndHandoff`: no checks → ok false; passing check + missing handoff → ok false with handoff error.
- [ ] **Step 2: Run** `go test ./internal/verify` — FAIL.
- [ ] **Step 3: Implement.** Head = `max(1, int(maxLines*0.75))`, tail = `maxLines-head`. Char cap: `out[:maxChars-40]` right-trimmed of whitespace + `"\n... [CHARACTER CAP REACHED]"`. Operate on runes for the char cap (Python counts code points). `exec.CommandContext` with `cmd.Dir=root`, combined output, `cmd.WaitDelay = 2*time.Second`; timeout → rc 124 with output = error text. Output compressed `Compress(out, 60, 10000)`. RTK: `exec.LookPath("rtk")`, `rtk pipe [--filter F]`, 3 s timeout, fallback when error or blank.
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** `feat(verify): shell-free checks and output compression`

### Task 4: memory (JSONL)

**Files:**
- Create: `internal/memory/memory.go`, `internal/memory/memory_test.go`

**Interfaces:**
- Consumes: `retrieval.Tokenize`, `retrieval.NewBM25`, `storage` (add `storage.WriteFileAtomic(path string, data []byte) error`, refactor `WriteJSON` onto it).
- Produces:
  - `memory.Record{ID, Type, CreatedAt, VerifiedAt string; Keywords []string; Summary, Evidence string; Files []string; SourceHashes map[string]string; Confidence float64}` json per spec §4.12, plus `Stale bool \`json:"stale,omitempty"\`` and `Score float64 \`json:"score,omitempty"\`` on output copies only
  - `memory.Path(root string) string` → `ai-workspace/memory/memory.jsonl`
  - `memory.Add(root, typ, keywords, summary, evidence string, files []string, confidence float64) (Record, error)`
  - `memory.Search(root, query string, limit int, minConfidence float64, excludeStale bool) ([]Record, error)`
  - `memory.List(root string) ([]Record, error)`
  - `memory.Prune(root string) (kept, pruned int, err error)`
  - `memory.Export(root, dest string) (int, error)`
  - `memory.Import(root, src string) (imported, skipped, invalid int, err error)`

- [ ] **Step 1: Write failing tests**
  - `TestAddAndSearch`: add `decision` with keywords `"PaymentRetry backoff"` → keywords include `paymentretry,payment,retry,backoff`; `Search("retry backoff",5,0,false)` returns it with score > 0.
  - `TestAddRejectsType`: type `"guess"` → error `unsupported memory type: guess`.
  - `TestAddRejectsEscapingFile`: file `../outside.txt` → error containing `memory file path must stay within workspace`.
  - `TestStaleAfterEdit`: add with file `a.go`, edit `a.go` → `List` marks stale; `Search(excludeStale=true)` omits it; `Prune` → kept 0 pruned 1.
  - `TestConfidenceClamp`: 1.7 → 1.0; -1 → 0.
  - `TestImportDedupe`: export then import into fresh root → imported N; import again → skipped N; line `{"id":""}` → invalid 1.
  - `TestCorruptLine`: file with a bad JSON line → `List` error mentions `line 2`.
- [ ] **Step 2: Run** `go test ./internal/memory ./internal/storage` — FAIL.
- [ ] **Step 3: Implement.** Keywords: `Tokenize` then order-preserving dedupe. Search score = BM25 × confidence, sort `(stale asc, score desc, confidence desc)` stable, cap `limit`. Records with no BM25 match excluded (V2 `rank` yields only positive scores — confirm in Task 7 capture). Writes: whole-file rewrite via `WriteFileAtomic` under a package `sync.Mutex`; add `// ponytail: process-local lock; cross-process writers can race, add file lock if multi-agent writes appear`. Valid record for import: non-empty `id`, type in the seven, `summary` non-empty.
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** `feat(memory): evidence-aware JSONL durable memory`

### Task 5: workspace fingerprint + changed files

**Files:**
- Create: `internal/workspace/fingerprint.go`, `internal/workspace/fingerprint_test.go`

**Interfaces:**
- Consumes: `indexer.Index` is in a package that imports `workspace` → to avoid a cycle, `Fingerprint` takes the digest input pre-built.
- Produces:
  - `workspace.ChangedFile{Path string; State string; SHA256 *string}` json `path,state,sha256`
  - `workspace.State{Root string; Schema int; GitHead *string; IndexStateSHA256 *string; ChangedFiles []ChangedFile; Fingerprint string}` json `root,schema,git_head,index_state_sha256,changed_files,fingerprint`
  - `workspace.Fingerprint(ctx context.Context, root string, indexFiles map[string]any, changed []string) State` — `indexFiles` nil → `index_state_sha256` null
  - `workspace.ChangedFiles(ctx context.Context, root string, reg Registry) []string`
  - `workspace.CanonicalJSON(v any) ([]byte, error)` — sorted keys, separators `,` `:`, no HTML escaping, non-ASCII escaped as `\uXXXX` (Python `json.dumps` default `ensure_ascii=True`)

- [ ] **Step 1: Write failing tests**
  - `TestCanonicalJSONMatchesPython`: `{"b":1,"a":[true,null,"ñ"]}` → `{"a":[true,null,"ñ"],"b":1}`.
  - `TestFingerprintStableAndSensitive`: temp git repo (skip if `git` missing), same inputs twice → equal; change a changed-file's content → different; `Schema==2`.
  - `TestChangedFileStates`: `../x` → `rejected`; missing → `missing`; present → sha of content.
  - `TestChangedFilesNested`: control root git repo + nested included repo `svc` with dirty `svc/a.go` → result contains `svc/a.go`; control-root dirty `README.md` included.
- [ ] **Step 2: Run** `go test ./internal/workspace` — FAIL.
- [ ] **Step 3: Implement.** Identity payload `{"schema":2,"git_head":...,"index_state_sha256":...,"changed_files":[...]}` → sha256 hex of `CanonicalJSON`. Git calls reuse existing `gitText` with 3 s timeout. Changed files: `git status --porcelain=v1 -z --untracked-files=all`, entry path after the 3-char prefix; for renames (`R`/`C`) take the new path and skip the following NUL field. Dedupe preserving order. Path escape check via `filepath.Rel` not starting with `..`.
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** `feat(workspace): V2-shaped workspace fingerprint`

### Task 6: brief decision functions

**Files:**
- Create: `internal/brief/evidence.go`, `internal/brief/orchestration.go`, `internal/brief/format.go`, `internal/brief/decision_test.go`

**Interfaces:**
- Consumes: `model.*`, `retrieval.Tokenize`, `providers.Status`, `config.OrchestrationBudget`.
- Produces:
  - `brief.Sufficiency{Score float64; Sufficient bool; LexicalCoverage float64; SourceDiversity int; ExactMatch, StructuralComplete bool}` json `score,sufficient,lexical_coverage,source_diversity,exact_match,structural_complete`
  - `brief.EvaluateSufficiency(query string, items []model.ContextItem, structuralRequired bool, patterns []string, threshold float64) Sufficiency`
  - `brief.Selective{Condition string; Accept bool; Score float64; Reasons []string}` json `condition,accept,score,reasons`
  - `brief.EvaluateSelective(items []model.ContextItem, s Sufficiency, expectedRepoIDs map[string]bool, minCoverage float64) Selective` — repo identity read from `item.Metadata["repository_id"]`
  - `brief.EvidenceState(lane model.Lane, sufficient bool) string`
  - `brief.Orchestration{...}` json fields exactly V2 `OrchestrationContract` (`complexity_vector{lane_weight,risk_weight,changed_file_count,workspace_root_count,structural_intent,evidence_gap}`, `complexity_score`, `superpowers_skills`, `crg_plan`, `agent_slots`, `review_passes`, `verification_passes`, `graph_depth`, `budget{max_agent_slots,...}`, `native_fallback`); slices never nil
  - `brief.BuildOrchestration(d model.RouteDecision, intent string, sufficient bool, selectiveEnabled, selectiveAccept bool, changed []string, rootCount int, p providers.Status, b config.OrchestrationBudget) Orchestration` — applies V2 floors (`max(1,..)`, `max(0,..)` for CRG)
  - `brief.Packet` (spec §4.8; `Retrieval` sub-struct named `RetrievalDiagnostics`) and `brief.Format(p Packet, format string) (string, error)` — formats `json|markdown|prompt`, unknown → error

- [ ] **Step 1: Write failing tests** (table tests; compat fixtures in Task 7 are the parity proof):
  - sufficiency: empty items → zero struct; coverage/exact/diversity example `query "retry payment"`, items `[{lightweight_index,"func RetryPayment"},{durable_memory,"retry payment backoff"}]` → coverage 1, diversity 2, exact true, score 1.0, sufficient.
  - selective: each of the 6 condition branches once.
  - evidence: 3 branches.
  - orchestration: full/high, structural, 3 changed, 2 roots, gap, superpowers+crg → score 2*2+2*2+3+1+2+2=16, slots min(4,4)=4, crg plan all four, review 2, verification 2, depth 3.
  - format: prompt contains `[EVIDENCE_STATE] requires_exploration`, `[CONTEXT_START]`, last line `[INVARIANTS] preserve existing contracts unless task explicitly changes them`; empty skills → `[SUPERPOWERS_SKILLS] none`; confidence 0.5 → `0.50`.
- [ ] **Step 2: Run** `go test ./internal/brief` — FAIL.
- [ ] **Step 3: Implement.** Port V2 formulas from spec §4.3–4.6 and `_format_brief` line-for-line. `exact`: lowercase via `strings.ToLower` after Unicode case folding is approximated — use `strings.ToLower` and document `// ponytail: ToLower approximates casefold (ß/ς differ); swap for x/text/cases if non-ASCII queries matter`. Markdown execution line uses an em dash `—` exactly as V2.
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** `feat(brief): V2 evidence, orchestration, and prompt contracts`

### Task 7: compat harness extension

**Files:**
- Modify: `tools/compat/capture_v2.py`, `compat/cases.json`, `compat/fixtures/v2-contracts.json` (regenerated), `internal/compat/harness.go`, `internal/compat/harness_test.go`

**Interfaces:**
- Consumes: Tasks 2, 3, 6 functions; `providers.ModelTier`.
- Produces: new `cases.json` sections `orchestration, sufficiency, selective, evidence_state, handoff_validate, handoff_render, compress, brief_format, model_tier` (spec §6); fixture sections of the same names stored as raw JSON; Go side compares `normalize(actual)` (JSON marshal → unmarshal into `any`) with the fixture value via `reflect.DeepEqual`.

- [ ] **Step 1: Add ≥30 cases** to `compat/cases.json`: orchestration ×8 (each lane × superpowers on/off, structural, ≥2 changed, budget override `max_crg_calls:1`, `max_agent_slots:1`, high risk), sufficiency ×6 (empty, partial coverage, exact match, structural required with/without matching pattern, threshold 0.9), selective ×6 (`no_context`, `wrong_repository` via `evidence_repository_outside_routing_plan`, all-stale `irrelevant`, below-floor `irrelevant`, `partial`, `supported`; V3 items always carry repository identity, so V2's `missing_code_owned_evidence_identity` branch is not ported), evidence_state ×3, handoff_validate ×4 (valid rendered, over cap, missing fields, placeholder), handoff_render ×1, compress ×4 (short, empty, line cap, char cap), brief_format ×3 (markdown, prompt with context, prompt without context and quoted/unicode task), model_tier ×4.
- [ ] **Step 2: Extend `capture_v2.py`** to call the V2 functions named in spec §6. Items for sufficiency/selective built as V2 `ContextItem(source, text, score, stale, metadata)`; selective cases needing repository identity set `item.evidence` via V2's evidence envelope type (read `ai_workflow/models.py` for the constructor) — if unavailable without deep setup, drop expected-repository cases and cover that branch only in Task 6 unit tests. `handoff_validate` writes text into a `tempfile.TemporaryDirectory()` root.
- [ ] **Step 3: Regenerate fixture**: `python tools/compat/capture_v2.py --v2-root ../ai-workflow-control-plane-v2 --cases compat/cases.json --output compat/fixtures/v2-contracts.json --expected-commit $(cat compat/v2.lock)`; then `python tools/compat/verify_fixture.py` against itself — Expected: exit 0. Diff must only add sections.
- [ ] **Step 4: Run harness** — Expected FAIL first (sections not wired), then implement Go replay in `harness.go`, run again — Expected `"ok": true`, case count +≥30. Add `TestHarnessCoversNewSections` asserting each new section has ≥1 case.
- [ ] **Step 5: Commit** `test(compat): freeze V2 brief, handoff, compress, orchestration contracts`

### Task 8: brief pipeline

**Files:**
- Create: `internal/brief/build.go`, `internal/brief/gather.go`, `internal/brief/build_test.go`

**Interfaces:**
- Consumes: Tasks 1–6; `indexer.Load`, `indexer.Search`, `retrieval.SelectMMR`, `routing.Classify`, `routing.PlanRetrievalWithAnchors`, `storage.WriteJSON`.
- Produces:
  - `brief.Options{Symbol, Endpoint string; ChangedFiles []string; WriteHandoff bool}`
  - `brief.Build(ctx context.Context, root, task string, opt Options) (Packet, error)`
  - package var `rgPath = func() (string, error) { return exec.LookPath("rg") }`

- [ ] **Step 1: Write failing tests** (temp workspace helper: `setup`-equivalent via `workspace.Discover` + `workspace.Save` + `indexer.BuildWorkspace`; skip when `git` missing):
  - `TestBriefPacketShape`: Go file defining `func RetryPayment()`; task `"fix RetryPayment backoff"` → lane small or full, `Context` non-empty, first `lightweight_index` item text starts with `{"symbol":"RetryPayment"` and contains the source line; `Retrieval.WorkspaceState.Fingerprint` 64 hex; `last-brief.json` exists.
  - `TestBriefFingerprintChangesOnEdit`: two Builds equal fingerprint; edit file (with `--changed-file`) → different.
  - `TestBriefStaleSnippetDropped`: build index, edit file without reindex → no `lightweight_index` item for it.
  - `TestBriefMissingIndexDegrades`: delete index file → Build succeeds, `Fallbacks` contains `index unavailable: <repo>`.
  - `TestBriefTargetedSourceFallback`: `rgPath` stub error → `targeted_source` items via scan, `Fallbacks` has `ripgrep unavailable; file scan used`.
  - `TestBriefAnswerAbstains`: task `"explain how quantum flux works"` in empty repo → `evidence_state == "abstain"`, no `last-brief.json`.
  - `TestBriefWriteHandoff`: full-lane task with `WriteHandoff` → `HandoffWritten == "ai-workspace/handoff/HANDOFF.md"`, `handoff.Validate` empty.
  - `TestBriefCRLFSnippet`: CRLF source file → snippet lines contain no `\r`.
- [ ] **Step 2: Run** `go test ./internal/brief -run TestBrief` — FAIL.
- [ ] **Step 3: Implement** order (spec §4.2, V2 order):
  1. load config + registry; `Classify`; `PlanRetrievalWithAnchors`; `providers.Detect`.
  2. changed = opt.ChangedFiles or `workspace.ChangedFiles`.
  3. gather per source (index hits limit `MaxSelectorCandidates`, snippet ±`SnippetLines`, SHA check against `idx.Files[path].SHA256`; targeted_source limit `MaxResultsPerSource`; memory limit `Memory.MaxResults`). Item `Metadata`: `repository`, `repository_id`, `path`, `line`, `kind`.
  4. `pre := EvaluateSufficiency(all candidates)`; adaptive token limit (spec §4.2); `SelectMMR` (λ 0.70, `MaxCandidates` = `MaxSelectorCandidates`, tokens `ceil(len(text)/4)`); `SelectMMR` error → fall back to relevance-order truncation within budget + `fallbacks` entry.
  5. final sufficiency, selective (expected IDs = included repo IDs), evidence state, fingerprint (index files map keyed by repo relative path), orchestration, packet; `execution_hint` verbatim V2 strings.
  6. non-answer: `storage.WriteJSON(root/ai-workspace/generated/last-brief.json)`; `WriteHandoff` && non-answer: atomic write `handoff.Render(d, provider, sources, task)`.
- [ ] **Step 4: Run** `go test ./internal/brief` — PASS.
- [ ] **Step 5: Commit** `feat(brief): bounded evidence-aware brief pipeline`

### Task 9: CLI wiring + docs + latency

**Files:**
- Modify: `internal/cli/cli.go` (dispatch + `usage`), `README.md`, `docs/COMPATIBILITY.md`, `docs/FOUNDATION_STATUS.md`, `docs/ROADMAP.md`
- Create: `internal/cli/workflow_commands.go`, `internal/cli/workflow_commands_test.go`

**Interfaces:**
- Consumes: all prior tasks.
- Produces CLI:
  - `brief TASK [--symbol S] [--endpoint E] [--changed-file F]... [--write-handoff] [--format json|markdown|prompt]` (default json)
  - `handoff` → `{"valid","errors"}`, exit 1 invalid
  - `verify --check CMD... [--strict]`
  - `compress [--file F] [--max-lines 80] [--max-chars 12000] [--prefer-rtk]` (stdin when no file)
  - `memory add|search|list|prune|export|import` (flags per spec §4.12)
  - flags may follow the positional task (`brief "x" --format prompt`): parse with `flag.FlagSet` after moving the first non-flag arg out.

- [ ] **Step 1: Write failing tests** via `cli.Run`:
  - `TestCLIBriefPrompt`: after `setup`, `brief "fix RetryPayment" --format prompt` exit 0, stdout first line `[TASK] fix RetryPayment`.
  - `TestCLIBriefBadFormat`: `--format xml` exit 2.
  - `TestCLIHandoffInvalidExit1`.
  - `TestCLIVerifyStrict`: `verify --check "go version" --strict` without handoff → exit 1, JSON `ok:false`.
  - `TestCLICompressStdin`: injectable stdin (add `RunWithStdin(args, in, out, errOut)`; `Run` delegates with `os.Stdin`).
  - `TestCLIMemoryRoundTrip`: add → search → list → export → import into second root.
- [ ] **Step 2: Run** `go test ./internal/cli` — FAIL.
- [ ] **Step 3: Implement** wiring; JSON via existing `printJSON` (note: V2 prints `ensure_ascii=False` for brief JSON; `printJSON` must set `SetEscapeHTML(false)` — switch to `json.NewEncoder` with indent 2).
- [ ] **Step 4: Run** `go test ./... && go vet ./...` and compat harness — PASS / `ok:true`.
- [ ] **Step 5: Docs** per spec §8; README quick start `ai-workflow setup` → `ai-workflow brief "<task>" --format prompt` → `ai-workflow verify --check "go test ./..." --strict`.
- [ ] **Step 6: Latency** `go build -o bin/ai-workflow ./cmd/ai-workflow`; on this repo run `setup` then 5× `brief "fix incremental index freshness" --format json`; record median wall time for PR body (PowerShell `Measure-Command`).
- [ ] **Step 7: Commit** `feat(cli): brief, handoff, verify, compress, memory commands`

### Task 10: PR + CI monitoring

- [ ] **Step 1:** `git push -u origin feat/core-workflow-loop`.
- [ ] **Step 2:** `gh pr create` — title `feat: core workflow loop (brief, handoff, verify, compress, memory)`; body: summary, spec/plan links, compat case delta, latency numbers, skipped list (spec §2), closes nothing (tracker #2 stays open); end with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- [ ] **Step 3:** `gh pr checks --watch`; fix failures at root cause; repeat until green (CI matrix 3 OS × Go 1.26/1.27, compatibility, CodeQL, benchmarks).
- [ ] **Step 4:** Address review bot findings; re-run checks.
- [ ] **Step 5:** Report green status to user. **Do not merge without explicit user approval.**
