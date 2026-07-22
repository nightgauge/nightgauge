---
name: nightgauge-adversarial-review
description: Re-review the current change with fresh-eyes critics that have no
  authoring context, each attacking it from a distinct lens (correctness,
  security, reuse/simplification, tests), then drive a fix loop until findings
  degrade to nitpicks. Use to harden a diff before opening or merging a PR, when
  asked for an adversarial/critical review, or from feature-dev/pr-create as an
  optional hardening gate.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Edit Write Glob Grep Bash Task
orchestration:
  mode: fanout
  phase: adversarial-critics
  ceiling: fanout
  units:
    - id: correctness
      role: critic
      promptRef: SKILL.md#critic-lens-prompt
    - id: security
      role: critic
      promptRef: SKILL.md#critic-lens-prompt
    - id: reuse-simplification
      role: critic
      promptRef: SKILL.md#critic-lens-prompt
    - id: tests
      role: critic
      promptRef: SKILL.md#critic-lens-prompt
  judge:
    mode: merge
    quorum: 1
    promptRef: SKILL.md#judge-and-fix-loop
---

# Adversarial Review

> Fresh-eyes critique → fix loop. Runs on the capability-routed workflow spine
> (#3899): the `orchestration:` block fans out the critics and the judge merges;
> the prose **Workflow** below is the single-agent portability floor.

<!-- phase-registry: standalone-skill -->

## Description

Authors are blind to their own assumptions. This skill spawns **fresh-context**
critics over the current diff — each told to **refute**, not bless — from
distinct lenses, merges what survives, fixes it, and re-runs until only nitpicks
remain. It complements `feature-dev`'s quality fanout by adding a no-prior-context
adversarial pass.

## Invocation

| Tool        | Command                                        |
| ----------- | ---------------------------------------------- |
| Claude Code | `/nightgauge-adversarial-review [--base main]` |
| Codex       | `$nightgauge-adversarial-review`               |

## When to use

- Before opening/merging a non-trivial PR — harden the diff first.
- When the user asks for an adversarial, critical, or "try to break this" review.
- From `feature-dev` Phase 5 or `pr-create` as an optional hardening gate.

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

### Phase 1: Resolve the change under review

```bash
BASE="${BASE:-main}"
git diff --stat "$BASE"...HEAD
git diff "$BASE"...HEAD > /tmp/adversarial-diff.patch
```

If the diff is empty, report "nothing to review" and exit 0.

### Phase 2: Fan out fresh-eyes critics

Spawn one critic per lens (correctness, security, reuse/simplification, tests).
Each critic gets the diff and the **critic lens prompt** below with **no**
authoring context. When the orchestration spine is active these run as parallel
units; otherwise spawn them with `Task`. Each returns structured findings.

#### Critic lens prompt

> You are a fresh-eyes reviewer with **no prior context** on this change. Your
> job is to **refute** it from the **{lens}** lens. Read the diff and the files
> it touches. Report concrete, actionable findings only — each with `file:line`,
> a one-line claim, severity (`blocker` | `major` | `nitpick`), and the fix.
> Default to skepticism: if something is unclear, flag it. Do **not** restate
> what the code does or praise it. Return JSON:
> `{ "findings": [ { "file", "line", "claim", "severity", "fix" } ] }`.
>
> Lens focus:
>
> - **correctness** — logic errors, edge cases, error handling, race conditions,
>   contract/handoff violations.
> - **security** — input validation, authz, secret handling, injection, unsafe
>   defaults (see `standards/security.md`).
> - **reuse-simplification** — duplicated logic, reinvented helpers, dead code,
>   needless complexity, altitude.
> - **tests** — missing/weak assertions, untested branches, tests that can't
>   fail, brittle fixtures.

### Phase 3: Judge and fix loop

#### Judge and fix loop

1. **Merge + dedup** findings across critics by `file:line` + claim.
2. **Triage**: keep `blocker`/`major`; collect `nitpick`s separately.
3. **Fix** the kept findings (`Edit`), running the repo's build/tests after each
   batch — never report success on a failing check (#2779).
4. **Re-review**: re-run the critics on the new diff. Repeat until a round
   produces no new `blocker`/`major` (findings have "degraded to nitpicks") or a
   max of 3 rounds.
5. **Stop conditions are explicit** — log the round count and why you stopped
   (dry round vs. round cap). Never loop silently.

### Phase 4: Report

Summarize: rounds run, findings fixed (by lens/severity), remaining nitpicks
(listed, not silently dropped), and the final build/test status.

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

## Gotchas

- **Critics must have no authoring context.** The value is fresh eyes — passing
  them the plan/rationale defeats the purpose.
- **Prompt to refute, not to bless.** A reviewer asked "is this fine?" rubber
  stamps; one asked to "find what's wrong" finds it.
- **Bound the loop and say why you stopped.** Without an explicit dry-round /
  round-cap condition the fix loop can oscillate or run forever.
- **Don't silently drop nitpicks.** List them so the author decides — a silent
  truncation reads as "all clear" when it isn't.

<!-- include: ../_shared/GOTCHAS.md -->

## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) Issue-to-PR pipeline.
See [docs/WORKFLOW_ORCHESTRATION.md](../../docs/WORKFLOW_ORCHESTRATION.md) for the orchestration spine.
