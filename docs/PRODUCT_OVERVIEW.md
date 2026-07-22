# Product Overview

Nightgauge turns a structured work item into a verified pull request by pairing
AI agents with deterministic orchestration. Agents handle planning and code
generation; the Nightgauge CLI verifies repository state, enforces gates, and
records outcomes.

## Core workflow

```text
issue-pickup → feature-planning → feature-dev → feature-validate → pr-create → pr-merge
```

Each stage has a bounded responsibility and exchanges structured state with the
next stage. Validation, Git operations, work-item updates, and stage completion
checks are performed by deterministic code wherever possible.

## Product surfaces

| Surface           | Purpose                                                                     |
| ----------------- | --------------------------------------------------------------------------- |
| Go CLI            | Deterministic pipeline operations, validation, state, and forge integration |
| Agent Skills      | Portable instructions for supported AI coding tools                         |
| TypeScript SDK    | Programmatic orchestration and integration APIs                             |
| VS Code extension | Local workflow control, progress, and repository context                    |
| Claude plugin     | Claude Code commands backed by the same skills and CLI                      |

## Capabilities

### Governed execution

- Stage preconditions and postconditions
- Build, test, lint, type-check, and policy gates
- Explicit approval points for consequential decisions
- Retry, recovery, budget, and circuit-breaker controls
- Structured exit diagnostics and audit records

### Repository and work tracking

- GitHub and GitLab forge adapters
- Issue claiming, branch creation, pull-request creation, and merge handling
- Dependency-aware issue and epic workflows
- Multi-repository workspace configuration

### Context and learning

- Fresh context per pipeline stage
- Structured handoff files instead of an ever-growing chat transcript
- Repository knowledge bases and architecture-decision recall
- Outcome recording and human-reviewed improvement recommendations

### Model portability

- Claude, Codex, Gemini, GitHub Copilot, and compatible local-model adapters
- Configurable model routing and performance policies
- Local Ollama and LM Studio support
- Provider-neutral usage and evaluation records

## Local-first operation

The open-source workflow runs locally with credentials supplied directly to the
chosen model and forge providers. Nightgauge cloud connectivity is disabled by
default, and the local pipeline does not require a Nightgauge account, license
key, or hosted control plane.

Telemetry is optional. See [TELEMETRY_PRIVACY.md](TELEMETRY_PRIVACY.md) for the
data contract and opt-in controls.

## Extending Nightgauge

- Add or adapt skills under `skills/`
- Configure agent tools under `configs/`
- Implement forge behavior behind the forge abstraction
- Embed orchestration using `packages/nightgauge-sdk`
- Add policy and validation through documented hooks

Start with [GETTING_STARTED.md](GETTING_STARTED.md), then use
[CONFIGURATION.md](CONFIGURATION.md) and [ARCHITECTURE.md](ARCHITECTURE.md) as
the detailed references.
