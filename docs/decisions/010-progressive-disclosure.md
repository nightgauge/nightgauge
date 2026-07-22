# Skill Progressive Disclosure (Option A — model-driven `Read` directives)

**Date:** 2026-05-29
**Author:** nightgauge
**Status:** Decided (epic #3808 — skill progressive-disclosure; mechanism chosen by spike #3809)
**Issue:** #3810 — refactor the six monolithic pipeline skills to <500-line bodies
**Builds on:** [ADR-007](007-slash-command-skill-invocation-contract.md) — skill-invocation contract

---

## Executive Summary

The six core pipeline-stage `SKILL.md` files have grown to 1,294–2,224 lines
each. Both skill runtimes inject the **entire** skill body into the agent's
stdin prompt on every stage run (Go `internal/execution/skill.go` `BuildPrompt`;
TS `packages/nightgauge-vscode/src/utils/skillRunner.ts`
`buildStagePrompt`), so the agent pays the full body cost regardless of which
conditional phases actually fire.

This ADR records the decision — chosen by spike #3809 — to adopt **Option A:
model-driven `Read` directives**. Phase-level detail (bash heredocs,
walkthroughs, edge cases, examples) moves out of each `SKILL.md` body into
on-demand reference files, and is replaced in the body by a plain-prose
directive at the same position:

> **Read `skills/nightgauge-<stage>/_includes/X.md` now and follow its
> instructions before continuing this phase.**

Because the directive is plain prose — **not** an `<!-- include: ... -->`
comment — it passes through both runtimes' `expandIncludes` regex untouched, so
**no runtime change is required** for on-disk skills.

---

## Context

### How skills reach the agent today

Both runtimes read `SKILL.md`, expand `<!-- include: ../_shared/X.md -->`
directives **inline at read time**, and write the fully expanded result as the
literal stdin prompt. The agent never receives a file handle to the skill — it
receives a pre-expanded blob.

- **Go**: `internal/execution/skill.go` — `expandIncludes` (line 116) matches
  only `<!-- include: (.+?) -->` via `includePattern`; `BuildPrompt` (line 90)
  writes `skillContent` directly to the prompt.
- **TS**: `packages/nightgauge-vscode/src/utils/skillRunner.ts` —
  `expandIncludes` (~line 1189) uses the same regex; the expanded body is
  injected as stdin; the agent is spawned with `cwd: workspaceRoot` (~line 2486)
  so a relative `Read skills/.../X.md` resolves.

### What the spike confirmed (Q1–Q5)

- **Q1** — Both runtimes inline includes then inject a literal stdin prompt; the
  model has no on-disk file handle.
- **Q2** — Every pipeline stage grants the agent `Read`/`Glob`/`Grep`.
- **Q3** — Both runtimes spawn with `cwd: workspaceRoot`, so repo-root-relative
  `Read` paths resolve. **Superseded for cross-repo runs (#196):**
  `workspaceRoot` is the TARGET repo's worktree, which has no `skills/`
  directory — both runtimes now rewrite the skill's own relative directives to
  absolute host paths at prompt-build time and export `NIGHTGAUGE_SKILL_DIR`;
  authors keep writing repo-root-relative paths.
- **Q4** — A plain-prose `Read ...` line is not an `<!-- include: -->` comment,
  so `expandIncludes` leaves it untouched in both runtimes — zero runtime change.
- **Q5** — The TS platform injected-string path (`parseSkillContent`, Issue
  #1473) has **no on-disk file**, so an Option A `Read` directive cannot resolve
  there. The platform must keep shipping fully rendered content for that path —
  captured as the deferred `platform-injection-bundle` recommendation.

---

## Options Considered

| Option                                                   | Mechanism                                                                                               | Verdict                                                                                                      |
| -------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| **A** — model-driven `Read` references                   | Replace extracted detail with a "Read `…/X.md` now and follow it" prose directive at the same position. | **ADOPT**                                                                                                    |
| **B** — lazy/conditional `<!-- include: -->` expansion   | Teach `expandIncludes` to expand only when a phase fires.                                               | Defer — requires a runtime change in both expanders and a conditional model the runtimes don't have.         |
| **C** — frontmatter manifest + harness-native disclosure | Declare supporting files in frontmatter; rely on a harness Skill tool to disclose them.                 | Defer — the runtimes inject a literal stdin prompt and expose no harness-native Skill-disclosure tool today. |
| **D** — status quo                                       | Keep monolithic bodies.                                                                                 | Reject — the prompt-bloat problem this epic exists to solve.                                                 |

---

## Decision

Adopt **Option A**. For the six pipeline-stage skills:

1. Extract procedural phase **detail** into
   `skills/nightgauge-<stage>/_includes/*.md`.
2. Replace each extracted block with the model-facing `Read` directive **at the
   same position** (immediately after the phase's `<!-- phase:start -->` marker
   and heading), so phase ordering is preserved.
3. Add a **"Supporting files (load on demand)"** TOC near the top of each
   `SKILL.md` (after frontmatter, before the first phase) as the one-level-deep
   index of what exists.
4. Keep **load-bearing** content inline in the skeleton: frontmatter,
   Description/Invocation/Arguments, Input/Exit/Output Contract sections, every
   `<!-- phase:start name=… index=N total=T -->` marker (unchanged position,
   `index`, `total`), each phase heading + one-line purpose, the Completion
   Checklist, and Error Handling.

The full authoring rules live in
[docs/SKILL_PROGRESSIVE_DISCLOSURE.md](../SKILL_PROGRESSIVE_DISCLOSURE.md).

### Scope of this ADR

This ADR governs the **per-skill body refactor** (`_includes/`). Converting the
existing `<!-- include: ../_shared/X.md -->` directives to Option A references is
the separate `refactor-shared-includes` recommendation and is **out of scope**
here — `_shared/` content and its include sites are left untouched.

---

## Consequences

**Positive**

- Cheapest, lowest-risk mechanism — a pure content edit with **zero runtime
  change** for on-disk skills.
- Detail loads only when the executing path reaches it; the injected prompt
  shrinks to the skeleton plus whatever phases actually fire.
- Each stage is an independently mergeable unit.

**Negative / trade-offs**

- The agent pays a `Read` round-trip when a phase fires.
- A mistyped directive path **fails silently at run time** — the directive is
  prose, so `expandIncludes` will not error on a bad path the way a missing
  `<!-- include: -->` would. Mitigated by verifying every Read-directive path
  resolves before merge (and by a possible future Read-directive path linter).
- **Platform injected-string path (Q5)**: `_includes` files do not resolve for
  `parseSkillContent`-served skills. Mitigation: keep platform-critical content
  (environment preflight, contract definitions) inline; extract only procedural
  phase detail. True platform-side savings wait on the deferred
  `platform-injection-bundle` follow-up.

---

## Verification

A skill refactored under this ADR must satisfy:

- `wc -l skills/nightgauge-<stage>/SKILL.md` is under ~500.
- Phase-marker count and every `index=`/`total=` are unchanged (diff the marker
  lines pre/post).
- Contract and gate sections remain inline.
- `nightgauge preflight links`, `skill-versions`, `skill-banners`,
  `skill-no-direct-gh` and `scripts/validate-skill-metadata.sh` pass.
- Every `Read skills/.../_includes/X.md` directive path resolves to a real file.
- No `gh ` token migrates into `_includes/` (the `no-direct-gh` linter scopes to
  `SKILL.md` only).

---

## References

- Spike #3809 —
  the progressive-disclosure research completed for issue #3809
- [docs/SKILL_PROGRESSIVE_DISCLOSURE.md](../SKILL_PROGRESSIVE_DISCLOSURE.md) —
  authoring convention
- `internal/execution/skill.go` — Go runtime (`expandIncludes`, `BuildPrompt`)
- `packages/nightgauge-vscode/src/utils/skillRunner.ts` — TS runtime
  (`expandIncludes`, `parseSkillContent`, spawn `cwd`)
