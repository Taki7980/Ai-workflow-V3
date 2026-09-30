# Parser Abstraction + Tree-sitter Pilot Design

**Date:** 2026-09-30  
**Repository:** `Taki7980/Ai-workflow-V3`  
**Base:** `a564a4c6a2670f77cfa1e234c2666881fd77032e`  
**Tracker:** #2 — Stage 4  
**Status:** implemented pilot; final validation in progress

## Goal

Introduce a parser boundary into the V3 indexer without rewriting indexing, and pilot Tree-sitter for two high-value language families:

- Python
- JavaScript / JSX / TypeScript / TSX

The pilot must preserve V3's current default portability and existing Go parsing behavior while measuring the cost and benefit of Tree-sitter before wider adoption.

## Research snapshot — 2026-09-30

### Repository retrieval remains task-dependent

Agent Retrieval Bench (2026-07-27) reports that no single retrieval family dominates across repository tasks; RepoMap leads budgeted context yield while embedding systems lead other ranking metrics. Structural indexing should therefore be introduced as a measured capability, not assumed to improve every retrieval task.

Source: https://arxiv.org/abs/2607.24882

### Tree-sitter can support low-token structural exploration

Codebase-Memory (2026-03-28) reports a Tree-sitter-based multi-language knowledge graph across 66 languages, with substantially lower token/tool use for structural exploration, but not universal answer-quality dominance.

Source: https://arxiv.org/abs/2603.27277

### TypeScript semantics require more than syntax trees

TypeScript Repository Indexing for Code Agent Retrieval (2026-04-20) shows that compiler-native AST + semantic/module-resolution information can outperform architectures relying on per-symbol language-server calls at repository scale.

Implication: this PR uses Tree-sitter only for **syntactic symbol extraction**. It does not claim semantic resolution, call-graph precision, import resolution, or type-aware edges.

Source: https://arxiv.org/abs/2604.18413

### Upstream state

As of 2026-09-30:

- Tree-sitter core latest stable: **v0.27.0** (2026-08-30)
- official Go binding module latest: **v0.25.0**
- Python grammar latest: **v0.25.0**
- JavaScript grammar latest: **v0.25.0**
- TypeScript grammar latest: **v0.23.2**

The official Go binding is CGO-based. An open 2026 issue reports an unreleased handle leak when using non-nil `ParseWithOptions`; this pilot therefore uses only `Parser.Parse(source, nil)` and never installs a progress callback.

Sources:
- https://github.com/tree-sitter/tree-sitter/releases
- https://pkg.go.dev/github.com/tree-sitter/go-tree-sitter
- https://github.com/tree-sitter/go-tree-sitter/issues/55
- https://github.com/tree-sitter/tree-sitter-python/releases
- https://github.com/tree-sitter/tree-sitter-javascript/releases
- https://github.com/tree-sitter/tree-sitter-typescript/releases

## Design decision

Use an **optional build-tag pilot**.

Default build:

```text
indexFile
  -> parser registry
      -> Go stdlib AST for .go
      -> regex fallback for all other supported source files
```

Pilot build (`-tags treesitter`):

```text
indexFile
  -> parser registry
      -> Go stdlib AST for .go
      -> Tree-sitter Python for .py
      -> Tree-sitter JS for .js/.jsx
      -> Tree-sitter TypeScript for .ts
      -> Tree-sitter TSX for .tsx
      -> regex fallback on parser error / unsupported extension
```

This keeps the normal V3 build CGO-free and preserves the "single native binary" migration path. Tree-sitter is opt-in until the pilot data justifies promotion.

## Alternatives considered

### Mandatory Tree-sitter in the normal binary

Simpler runtime path, but it would immediately make the default build CGO-dependent and expand release/packaging risk before we have measurements. Rejected for this stage.

### External parser process/provider

Would isolate native parsing dependencies but adds process startup, protocol, timeout, and distribution complexity to a task that is currently in-process and deterministic. Rejected as over-engineering for the pilot.

### Compiler/LSP-only parsing

Can deliver richer semantic information for languages such as TypeScript, but conflicts with this stage's narrow syntactic-parser goal and would couple indexing to language-specific toolchains. Deferred to structural retrieval / precise-code-intelligence work.

## Parser boundary

Create a private indexer parser interface:

```go
type symbolParser interface {
    Name() string
    Supports(extension string) bool
    Parse(source []byte, relativePath, sha256 string) ([]Symbol, error)
}
```

The indexer owns parser ordering and fallback.

### Selection rules

1. Go stdlib AST always owns `.go`.
2. Tagged Tree-sitter parsers own their pilot extensions.
3. Parser failure falls through to the next supporting parser.
4. Regex is the final fallback.
5. Successful parsing with zero symbols is still success and must not trigger regex duplicate extraction.
6. Parsing must be deterministic for identical bytes.

No parser identity is persisted into the index schema in this PR.

## Go parser

Move existing Go AST logic behind the parser interface.

Behavior must remain equivalent:

- functions/methods => `kind: function`
- type specs => `kind: type`
- one-based start/end lines
- SHA-256 retained
- syntax errors fall back to regex behavior

The parser receives already-read bytes; no second file read is introduced.

## Tree-sitter pilot

### Dependency pins

```text
github.com/tree-sitter/go-tree-sitter              v0.25.0
github.com/tree-sitter/tree-sitter-python          v0.25.0
github.com/tree-sitter/tree-sitter-javascript      v0.25.0
github.com/tree-sitter/tree-sitter-typescript      v0.23.2
```

The implementation is compiled only under:

```go
//go:build treesitter
```

A matching `!treesitter` file returns no Tree-sitter parsers.

### Runtime safety

For every source file:

1. create a parser;
2. set the pinned grammar language;
3. call `Parser.Parse(source, nil)`;
4. walk the returned tree;
5. close the tree;
6. close the parser.

Do not use `ParseWithOptions`, callbacks, cancellation payloads, old-tree incremental reuse, or logger callbacks in this pilot.

## Symbol extraction

### Python

Extract recursively:

- `function_definition` => function
- `class_definition` => type

Decorated/async definitions are discovered through their underlying named definition nodes.

### JavaScript / TypeScript family

Extract recursively:

- `function_declaration` => function
- `class_declaration` => type
- `abstract_class_declaration` => type
- `interface_declaration` => type
- `type_alias_declaration` => type
- `enum_declaration` => type

Also extract `variable_declarator` as a function when its `value` is:

- `arrow_function`
- `function_expression`

This restores a useful capability V2's regex indexer had for arrow-function bindings while remaining purely syntactic.

Node names come from the Tree-sitter `name` field. Lines are one-based.

## Error handling

Tree-sitter's error-recovery tree is accepted when parsing returns a tree.

Fallback to regex occurs only if:

- language setup fails;
- parsing returns nil;
- the parser returns an explicit integration error.

Syntax error nodes inside a returned tree do not automatically force regex fallback.

No source file may be dropped merely because the pilot parser fails.

## Portability contract

The default build must remain CGO-free:

```bash
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./cmd/ai-workflow
```

The tagged pilot must compile and pass tests on:

- Linux
- Windows
- macOS
- Go 1.26.x
- Go 1.27.x

This is the promotion gate for any future default enablement.

## Measurement plan

### Accuracy

Tagged fixtures cover:

- Python function/class
- malformed-but-recoverable Python
- JS function/class/arrow binding
- TS function/interface/type/enum/arrow binding
- TSX component declaration
- exact line numbers and stable ordering

Default-build tests prove non-Go languages still use regex behavior when the tag is absent.

### Performance / memory

Add tagged Go benchmarks over fixed Python, JavaScript, TypeScript and TSX fixtures.

CI records:

```bash
go test -tags treesitter ./internal/indexer   -run '^$'   -bench BenchmarkParserPilot   -benchmem   -benchtime=50x
```

Timing is observational in this stage; no flaky wall-clock threshold is used.

### Binary-size impact

Linux CI builds both:

```text
ai-workflow-default
ai-workflow-treesitter
```

and prints byte sizes + delta.

The PR must record the measured delta before being marked merge-ready.

### Index behavior

Existing retrieval benchmarks, compatibility harness, and index tests remain mandatory. No context budget or retrieval threshold may be weakened.

## Dependency and packaging policy

- exact module versions are pinned;
- `go.sum` is committed;
- default build does not require CGO;
- no dynamic grammar download;
- no runtime network access;
- no generated grammar sources copied into V3;
- official grammar modules are used directly.

## CI

Existing test matrix remains.

Each matrix lane additionally runs:

```bash
go test -tags treesitter ./internal/indexer -count=1
```

Add a Linux `parser-pilot` job:

1. `CGO_ENABLED=0 go test ./...`
2. `CGO_ENABLED=0 go build ./cmd/ai-workflow`
3. tagged parser tests
4. tagged parser benchmark with `-benchmem`
5. default/tagged binary-size measurement

Existing:

- build
- retrieval benchmarks
- V2 compatibility
- CodeQL

remain required.

## Non-goals

Not in this PR:

- replacing Go stdlib AST;
- incremental Tree-sitter tree reuse;
- call graph construction;
- semantic/type resolution;
- import/module resolution;
- SCIP;
- LSP/compiler integration;
- parsing all currently supported languages;
- making Tree-sitter default;
- changing index schema version;
- changing retrieval budgets.

## Rollback

Rollback is low-risk:

- default parser behavior is still Go AST + regex;
- Tree-sitter code is isolated behind a build tag;
- no persistent index-format migration exists;
- removing the tagged files and module requirements restores the previous runtime.

## Success definition

Stage 4 is merge-ready only if:

1. parser abstraction is small and internal;
2. current Go behavior is preserved;
3. tagged Python/JS/TS/TSX symbol fixtures pass;
4. parser failures safely degrade to regex;
5. default CGO-free build/test succeeds;
6. tagged parser tests pass across all supported OS/Go matrix lanes;
7. binary-size and benchmark-memory/time impact are recorded;
8. existing retrieval, compatibility, build and CodeQL gates remain green;
9. no benchmark threshold or context ceiling is weakened.


## Pilot measurements

Measured on GitHub Actions Ubuntu 24.04, Go 1.27.1, at implementation head `234bb43224a0eff8510598f3f537a75f332d267c`.

| Parser fixture | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| Python | 57,130 | 3,456 | 70 |
| JavaScript | 60,407 | 3,376 | 65 |
| TypeScript | 82,262 | 4,168 | 97 |
| TSX | 38,828 | 3,160 | 65 |

Binary-size measurement:

- default binary: 5,928,264 bytes
- `treesitter` tagged binary: 10,292,760 bytes
- delta: +4,364,496 bytes (**+73.62%**)

Observed portability:

- Go 1.26.x: Ubuntu, Windows, macOS — pass
- Go 1.27.x: Ubuntu, Windows, macOS — pass
- `CGO_ENABLED=0 go test ./...` — pass
- `CGO_ENABLED=0 go build ./cmd/ai-workflow` — pass

The pilot therefore remains **opt-in**. The syntax-quality and multi-language benefits are useful, but the measured binary-size increase is too large to make Tree-sitter mandatory without a later packaging/runtime decision.
