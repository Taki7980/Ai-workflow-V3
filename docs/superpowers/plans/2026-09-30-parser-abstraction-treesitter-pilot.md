# Parser Abstraction + Tree-sitter Pilot Implementation Plan

**Spec:** `docs/superpowers/specs/2026-09-30-parser-abstraction-treesitter-pilot-design.md`  
**Base:** `a564a4c6a2670f77cfa1e234c2666881fd77032e`  
**Execution:** Superpowers TDD, inline execution

## Global constraints

- Default V3 remains CGO-free.
- Go stdlib AST behavior is preserved.
- Tree-sitter is opt-in behind `treesitter` build tag.
- Use only official Tree-sitter Go binding/grammar modules at pinned versions.
- Never use `ParseWithOptions`.
- No semantic/call-graph claims.
- No retrieval/context threshold changes.
- Every implementation task starts with RED tests and finishes with green CI evidence.

## Task 1 — Parser abstraction with zero default behavior drift

**Files**
- Create `internal/indexer/parser.go`
- Create `internal/indexer/parser_go.go`
- Create `internal/indexer/parser_regex.go`
- Create `internal/indexer/parser_treesitter_disabled.go`
- Modify `internal/indexer/indexer.go`
- Create/extend `internal/indexer/parser_test.go`

**RED tests**
- Go function/type extraction unchanged.
- Go syntax error falls through to regex.
- Python default build still uses regex.
- successful parser returning zero symbols does not fall through.
- parser order deterministic.

**Implementation**
- introduce private `symbolParser`;
- pass already-read source bytes;
- move Go AST and regex extraction behind parsers;
- add disabled `treeSitterParsers() nil`.

**Verification**
```bash
go test ./internal/indexer -count=1
CGO_ENABLED=0 go test ./...
```

## Task 2 — Tagged Tree-sitter Python + JS/TS/TSX pilot

**Files**
- Create `internal/indexer/parser_treesitter.go` with `//go:build treesitter`
- Create `internal/indexer/parser_treesitter_test.go` with same build tag
- Modify `go.mod`
- Add `go.sum`

**RED tests**
- Python function/class.
- recoverable malformed Python.
- JavaScript function/class/arrow.
- TypeScript function/interface/type/enum/arrow.
- TSX component.
- correct line/end-line and SHA.
- parser names show tagged path is selected.

**Implementation**
- pinned official runtime/grammars;
- per-file parser lifetime;
- `Parser.Parse(source, nil)` only;
- recursive named-node walk;
- regex fallback on integration failure.

**Verification**
```bash
go test -tags treesitter ./internal/indexer -count=1
go test -tags treesitter ./...
```

## Task 3 — Portability and pilot measurements

**Files**
- Create `internal/indexer/parser_benchmark_test.go` with `//go:build treesitter`
- Modify `.github/workflows/ci.yml`
- Create/update `docs/PARSER_PILOT.md`

**RED / gate changes**
- add tagged parser test to every Go/OS matrix lane;
- add Linux parser-pilot job;
- add CGO-disabled default build/test;
- add parser benchmark + benchmem;
- add default/tagged binary-size measurement.

**Verification**
- all six Go/OS lanes compile tagged parser;
- parser-pilot job succeeds;
- capture benchmark and size results into PR/documentation.

## Task 4 — Whole-branch review and readiness

- run/review full CI matrix;
- verify raw + selector retrieval benchmarks;
- verify pinned V2 compatibility;
- verify CodeQL;
- inspect dependency/version drift;
- inspect PR review comments;
- fix Critical/Important findings using RED→GREEN;
- record measured pilot costs;
- mark PR ready only after all gates are green.

## Review focus

- accidental CGO dependency in default build;
- duplicate symbol extraction caused by fallback;
- Tree-sitter resource leaks (parser/tree close paths);
- unstable ordering from traversal/maps;
- TypeScript grammar/runtime ABI mismatch;
- malformed syntax causing file loss;
- arrow-function duplicates;
- build-tag files importing pilot dependencies into default path;
- platform-specific C compiler failures.
