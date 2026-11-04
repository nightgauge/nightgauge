---
name: cli-reference
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
disable-model-invocation: true
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
reconcile. For the SDK, the source of truth is the export surface of the
published `@nightgauge/sdk` package. Treat any drift as a bug to fix here.

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

### Cross-cutting gotchas (every Nightgauge skill)

> These apply to **all** skills. Skill-specific gotchas live above this line in
> each skill's `## Gotchas` section. Keep both terse: symptom → why → do-instead.
> When a new failure mode is root-caused (retro / failure-taxonomy), add it here
> or to the owning skill — Gotchas are the highest-signal part of a skill.

- **Bare `nightgauge` is not always on `PATH`.** Run the Phase 0 PREFLIGHT
  binary-discovery cascade first and prepend `dirname($BINARY)` to `PATH`.
  Skipping it surfaces as `command not found` only on some machines.
- **Never emit a foreground `sleep` wait loop.** The harness denies foreground
  `sleep` — an unbounded shell idiom that polls a completion marker in a
  `while`/`until` loop with no iteration cap — the tool call is rejected
  outright. To wait on a long-running background job, start it with the
  backgrounded-command form the harness provides (e.g. a Bash call with
  `run_in_background: true`) and then block on its completion notification /
  the Monitor tool instead of polling on a timer. Never invent a foreground
  poll loop to bridge the wait (#289).
- **Never call the forge CLI directly.** Use `nightgauge forge …`
  (`issue`/`pr`/`project`/`label`/`repo`/`auth`/`graphql`), never a bare
  `gh`/`glab`. The `no-direct-gh` lint fails CI on regressions; the abstraction
  is what lets `IB_FORGE=gitlab` work unchanged.
- **Headless skills must not block on input.** If `AskUserQuestion` is not in
  `allowed-tools`, the skill runs headless — make autonomous decisions or fail
  with a clear error. A blocked prompt hangs the whole pipeline run.
- **Write your handoff/context file before exiting — even on failure.** Pipeline
  stages that exit without their `{stage}-{N}.json` in the clone's pipeline
  state directory (`nightgauge layout path pipeline`) force the orchestrator
  onto a repo-blind deterministic fallback that may misreport state (issue 3114). Do not rely on the fallback.
- **An assertion that cannot go red is decoration.** A test that passes both
  before and after a fix constrains nothing — a regression test written for an
  SSH-push bug asserted "a push succeeds with no auth configured" and passed
  identically on the broken code, because a `file://` remote needs no
  credentials either way. Prove it: `cp <file> /tmp/<file>.bak`, restore the
  pre-fix behavior **from the copy**, confirm the new test FAILS, restore, and
  confirm it passes. Never `git checkout -- <file>` to back a fix out — on an
  uncommitted branch that destroys the fix. See
  [Vacuous Assertion](https://github.com/nightgauge/nightgauge/blob/main/docs/FAILURE_TAXONOMY.md#vacuous-assertion-the-test-that-cannot-go-red).
- **A green suite is a statement about the cases that RAN.** After adding
  assertions, check the reported PASS count rose by exactly the number you
  added; a suite can skip a case silently and still print "all tests passed".
- **Verify the issue's premise before implementing it.** An issue's stated cause
  can be flatly wrong — a request to delete a sweep that was never built, a
  claim that a failure category already classified an error when it fell through
  to `CatUnknown`, a "build it or delete it" fork where building was
  architecturally impossible. Check the claim against the tree first; if the
  premise is false, do the smaller correct thing and say so in the handoff.
- **Never report success when a step failed.** Swallowing a failed build/test
  lets a broken change flow downstream where it is caught later at higher cost
  (#2779). Surface the failure with its output.
- **Never push to `main`.** Use `feat/`/`fix/`/`docs/` branches; merges are
  manual squash only (no `--auto`, no `--admin`).
- **Production safety.** Never run `docker compose down -v` against a production
  stack — `-v` destroys `postgres_data`. Fix credential mismatches with
  `ALTER USER`, not a volume wipe.


## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) Issue-to-PR pipeline.
