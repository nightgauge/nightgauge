---
name: nightgauge-cli-reference
description: Reference for Nightgauge' own surfaces — the `nightgauge
  forge` abstraction, the `nightgauge` Go binary subcommands, and the
  @nightgauge/sdk API — with the gotchas that bite when you
  use them. Use when authoring or editing a skill, plugin command, automation, or
  script that shells out to `nightgauge`/`forge` or imports the SDK, or
  whenever you are about to reach for a bare `gh`/`glab` call and need the forge
  equivalent.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Bash Grep Glob
---

# CLI / SDK Reference

> Category-1 (library & API reference) skill for Nightgauge' own surfaces.

<!-- phase-registry: standalone-skill -->

## Description

Nightgauge has three first-party surfaces that skills, plugin commands, and
automations call constantly:

1. **`nightgauge forge …`** — the forge abstraction (issue / pr / project /
   label / repo / auth / graphql / webhook). This is the **mandatory** path for
   every forge operation in a skill — direct `gh`/`glab` is forbidden and
   CI-blocked.
2. **`nightgauge …`** — the deterministic Go binary (board sync, epics,
   gates, health, pipeline history, hooks, …).
3. **`@nightgauge/sdk`** — the TypeScript orchestration
   library the VSCode extension and CI use programmatically.

This skill is a concise index plus a **Gotchas** section. Detailed command tables
and snippets live in the `references/` files — read the one you need on demand.

## Invocation

| Tool        | Command                     |
| ----------- | --------------------------- |
| Claude Code | `/nightgauge-cli-reference` |
| Codex       | `$nightgauge-cli-reference` |
| Others      | Invoke via Agent Skills     |

## When to use

- You are writing/editing a `skills/*/SKILL.md` and need the forge command for an
  issue/PR/project/label operation (never hand-roll `gh`/`glab`).
- You are adding a plugin command or `scripts/` automation that shells out to the
  Go binary and want the right subcommand + flags.
- You are writing code against the SDK (`PipelineOrchestrator`, `ContextManager`,
  `RunStateManager`, events) and need the import surface.
- A `no-direct-gh` lint failure pointed you here.

## Reference files (read on demand)

- **`references/forge.md`** — the `nightgauge forge` surface: every
  subcommand group, the `forge graphql` carve-out for project view/link/list, and
  the GitLab CE/EE caveats.
- **`references/go-binary.md`** — the deterministic Go binary: command groups
  most-used by skills (`project`, `epic`, `issue`, `gate`, `ci`, `health`,
  `pipeline`, `hook`) with examples and the PATH/preflight contract.
- **`references/sdk.md`** — the SDK import surface and CLI entry points.

## How to keep this accurate

These references mirror `--help` output. To check a command is current, run
`nightgauge <group> --help` (or `nightgauge forge <group> --help`) and
reconcile. For the SDK, the source of truth is
`packages/nightgauge-sdk/src/index.ts`. Treat any drift as a bug to fix
here.

## Gotchas

- **Never call `gh`/`glab` directly from a skill.** Use `nightgauge forge …`
  — the `no-direct-gh` lint fails CI on regression, and the abstraction is what
  lets `IB_FORGE=gitlab` work unchanged. (A short allowlist of legacy skills
  exists in `scripts/lint-skills/allowlist.txt`; do not add to it without review.)
- **Project view-create / link / list are a carve-out.** They route through
  `nightgauge forge graphql`, not a dedicated subcommand (ADR-008). The
  GitLab adapter returns `ErrUnsupported` for `forge graphql`.
- **`nightgauge forge project item-remove` is not implemented yet** — don't
  depend on it.
- **Scope a GitHub token per-command in multi-account workspaces** rather than
  switching the global active account (which silently breaks every other open
  workspace). The concrete one-shot-token pattern is in `references/go-binary.md`.
- **The binary is not always on `PATH`.** Resolve it via the Phase-0 PREFLIGHT
  cascade before bare `nightgauge …` calls.

<!-- include: ../_shared/GOTCHAS.md -->

## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) Issue-to-PR pipeline.
