# Contributing

AI Workflow V3 is a compatibility-sensitive rewrite.

## Rules

- Preserve deterministic safety escalation.
- Do not increase context ceilings as a workaround for weak retrieval.
- Add tests for every behavior change.
- Keep provider failures degradable unless the provider is explicitly required by policy.
- Avoid runtime dependencies unless the benefit is measurable and documented.
- Any intentional incompatibility with V2 requires an ADR and a migration note.

## Local checks

```bash
gofmt -w .
go vet ./...
go test ./...
```
