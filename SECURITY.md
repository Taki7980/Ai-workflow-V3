# Security Policy

AI Workflow executes local tools and reads source repositories, so process and path boundaries are security-critical.

## Reporting

Do not open a public issue for a suspected vulnerability that could expose credentials, execute unintended commands, escape workspace confinement, or corrupt state. Use GitHub's private vulnerability reporting when enabled.

## Core guarantees under migration

- external provider commands are tokenized arguments, never shell strings;
- provider environment is allowlisted;
- stdout/stderr are bounded;
- provider execution is time bounded;
- atomic state writes are used for generated JSON;
- repository content is data, not executable instruction text.

V3 is not considered a production replacement for V2 until the security parity gate in `docs/MIGRATION_FROM_V2.md` is complete.
