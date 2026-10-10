# Security Policy

AI Workflow executes local tools and reads source repositories, so process and path boundaries are security-critical.

## Reporting

Do not open a public issue for a suspected vulnerability that could expose credentials, execute unintended commands, escape workspace confinement, or corrupt state. Use GitHub's private vulnerability reporting when enabled.

## Core guarantees

- every subprocess (providers, ripgrep, git, code-review-graph, SCIP, verify checks) runs through `internal/procx`: argv only, never a shell; wall-clock timeout; bounded stdout and stderr tail; the whole process tree is killed on timeout, overflow or cancellation (process group + SIGKILL on Unix, `taskkill /T /F` from `%SystemRoot%` on Windows);
- the child environment is explicit: providers and ripgrep get an allowlist (`RIPGREP_CONFIG_PATH` and secrets never pass); only trusted user tools (git, CRG, SCIP, your own `verify --check` commands) inherit the environment;
- repository config cannot grant executable authority: a provider is selected by `provider_id` from a user-owned registry outside the repository (`AI_WORKFLOW_PROVIDER_REGISTRY`, default `<user config>/ai-workflow/providers.json`), opened without following symlinks and refused when group/other-writable or foreign-owned (POSIX);
- registry executables must be absolute, non-symlink, outside the repository and SHA-256 pinned; the digest is re-checked at every launch, and a pinned interpreter may not be pointed at repository-owned scripts. Inline repository commands require `AI_WORKFLOW_ALLOW_REPO_PROVIDER_COMMANDS=1`;
- `restricted` providers get an isolated temporary HOME/TMP, `PATH` limited to the executable directory, and cannot import runtime identity variables;
- optional Linux sandbox (bubblewrap): read-only host root, writes only to runtime directories, PID/IPC/UTS namespaces, all capabilities dropped, optional network denial and CPU/memory/file/open-file rlimits; `required` policies and any network/limit request fail closed when no backend works;
- provider output is parsed strictly (no duplicate keys, nesting <= 8, int64 integers, finite numbers, bounded strings/arrays/records), scores must be in [0, 1], and `path`/`file` metadata that is absolute, drive-qualified, traversing or symlinked outside the root is dropped;
- provider stderr is redacted (allowlisted secret values, bearer tokens, `token=`/`password=`-style assignments) before it is reported;
- traces never store task text; run journals store descriptors and digests only;
- state files are written atomically; run journals are create-only and hash-chained;
- repository content is data, never instruction text.

CI exercises process-tree termination, environment leakage, symlink confinement, the real bubblewrap backend and fuzzes every hostile-input parser on each change.
