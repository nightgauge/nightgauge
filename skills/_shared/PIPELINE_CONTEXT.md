## System Context

**Product**: Nightgauge — AI-powered Issue-to-PR pipeline.

**Architecture**: Three-layer stack: portable Skills (SKILL.md), TypeScript SDK
(`packages/nightgauge-sdk/`), and VSCode Extension
(`packages/nightgauge-vscode/`). Six pipeline stages execute as isolated
subagents with JSON context handoff files.

**Execution model**: Each pipeline stage runs in a fresh conversation. Context
is passed exclusively through `.nightgauge/pipeline/*.json` files — never
through conversation history. Every stage reads its predecessor's context file
and writes its own.

**Configuration**: 6-tier config system — built-in defaults → global config →
project config → local config → env vars → CLI flags. Schema defined in
`packages/nightgauge-vscode/src/config/schema.ts`. Documentation in
`docs/CONFIGURATION.md`.

**Standards**: Code standards in `docs/CODE_STANDARDS.md`. Security rules in
`standards/security.md`. Testing patterns in `docs/TESTING.md`. Git workflow in
`docs/GIT_WORKFLOW.md`.

**Critical rules**: Never push to main. Never hardcode secrets. Never downgrade
versions. Pre-submission validation is mandatory.
