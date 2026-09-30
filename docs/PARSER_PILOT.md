# Parser Pilot

## Status

Tree-sitter is an **opt-in parser pilot** in AI Workflow V3. The default build remains CGO-free and continues to use:

- Go stdlib AST for Go;
- deterministic regex fallback for other supported languages.

Enable the pilot with:

```bash
go test -tags treesitter ./internal/indexer
go build -tags treesitter ./cmd/ai-workflow
```

## Scope

The tagged pilot adds syntactic symbol extraction for:

- Python
- JavaScript / JSX
- TypeScript
- TSX

It does **not** add semantic type resolution, import resolution, call graphs, LSP integration, or SCIP.

## Pinned modules

| Module | Version |
| --- | --- |
| `github.com/tree-sitter/go-tree-sitter` | `v0.25.0` |
| `github.com/tree-sitter/tree-sitter-python` | `v0.25.0` |
| `github.com/tree-sitter/tree-sitter-javascript` | `v0.25.0` |
| `github.com/tree-sitter/tree-sitter-typescript` | `v0.23.2` |

The implementation deliberately uses `Parser.Parse(source, nil)` and does not use `ParseWithOptions`.

## Cross-platform validation

The tagged parser tests passed on:

| Go | Ubuntu | Windows | macOS |
| --- | --- | --- | --- |
| 1.26.x | pass | pass | pass |
| 1.27.x | pass | pass | pass |

The default path also passed:

```bash
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./cmd/ai-workflow
```

Existing V2 compatibility and both retrieval benchmark gates remained green.

## Performance snapshot

Measured in GitHub Actions on Ubuntu 24.04 / Go 1.27.1 with `-benchtime=50x`.

| Fixture | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| Python | 57,130 | 3,456 | 70 |
| JavaScript | 60,407 | 3,376 | 65 |
| TypeScript | 82,262 | 4,168 | 97 |
| TSX | 38,828 | 3,160 | 65 |

These are pilot-scale microbenchmarks, not repository-index throughput claims. Future index/freshness work should measure end-to-end repository indexing separately.

## Binary-size impact

| Build | Bytes |
| --- | ---: |
| Default | 5,928,264 |
| `treesitter` tagged | 10,292,760 |
| Delta | +4,364,496 (+73.62%) |

This cost is the main reason the pilot remains behind a build tag.

## Decision

Do **not** make Tree-sitter mandatory yet.

The pilot demonstrates:

- stronger syntax-aware symbol extraction for Python/JS/TS/TSX;
- successful cross-platform compilation;
- safe regex fallback;
- preserved CGO-free default builds.

But the tagged binary is materially larger, and current research also indicates that syntax trees alone do not replace semantic/compiler-native indexing for TypeScript repository reasoning. A later stage can decide whether to:

1. keep Tree-sitter opt-in;
2. distribute a separate enhanced binary;
3. use an external parser provider;
4. promote selected grammars after packaging improvements.

## Reproduction

```bash
go test -tags treesitter ./internal/indexer -count=1
go test -tags treesitter ./internal/indexer -run '^$' -bench BenchmarkParserPilot -benchmem -benchtime=50x
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./cmd/ai-workflow
```
