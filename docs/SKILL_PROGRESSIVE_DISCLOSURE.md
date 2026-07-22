# Skill Progressive Disclosure — Authoring Convention

> Mechanism decision: [ADR-010](decisions/010-progressive-disclosure.md).
> Spike: #3809.

This is the authoring guide for keeping `SKILL.md` bodies small by moving
phase-level **detail** into on-demand reference files. A skill body should read
like a **skeleton**: the phases, their markers, their one-line purpose, the
contracts and gates — with the procedural detail one `Read` away.

## Contents

- [When to extract](#when-to-extract)
- [Directory layout](#directory-layout)
- [The Read directive](#the-read-directive)
- [The "Supporting files" TOC](#the-supporting-files-toc)
- [Reference files (`_includes/*.md`)](#reference-files-_includesmd)
- [What stays inline (load-bearing)](#what-stays-inline-load-bearing)
- [The one-level-deep rule](#the-one-level-deep-rule)
- [The platform injected-string caveat](#the-platform-injected-string-caveat)
- [Verification checklist](#verification-checklist)

---

## When to extract

Extract a phase's body when the `SKILL.md` exceeds ~500 lines, or proactively
for any phase whose detail is long, conditional, or rarely-fired. The target is
a body that fits on a couple of screens.

Extract: bash heredocs, step-by-step walkthroughs, edge-case handling, examples,
templates, long tables that only matter once a phase fires.

Do **not** extract: phase markers, headings, one-line phase purposes, contracts,
gate language, the Completion Checklist, Error Handling — these are the
skeleton (see [What stays inline](#what-stays-inline-load-bearing)).

## Directory layout

- **Per-skill detail** — `skills/<skill>/_includes/*.md`. Content referenced by
  exactly one skill lives here. This is what this convention adds.
- **Shared content** — `skills/_shared/*.md`, unchanged. Content shared across
  multiple skills stays here. The existing `<!-- include: ../_shared/X.md -->`
  directives are **not** touched by the body refactor (they are the separate
  `refactor-shared-includes` work).

Group adjacent, related phases into a single `_includes` file so the body has
few references rather than one per phase. Name files by theme, e.g.
`_includes/testing.md`, `_includes/merge.md`, `_includes/context-and-board.md`.

## The Read directive

Replace each extracted body with a model-facing directive **at the same
position** — immediately after the phase's `<!-- phase:start -->` marker and
heading:

> **Read `skills/<skill>/_includes/X.md` now and follow its instructions before
> continuing this phase.**

Rules:

- Use the **repo-root-relative** path (`skills/<skill>/...`). Do not use `../`
  relative paths. The runner rewrites these to absolute host paths at
  prompt-build time and exports `NIGHTGAUGE_SKILL_DIR` into the agent env
  (#196) — the original "CWD is the repo root" assumption (spike Q3) only
  held when dogfooding the nightgauge repo itself; cross-repo pipeline runs
  spawn in the target repo's worktree, which has no `skills/` directory.
  Only the skill's OWN paths (and `skills/_shared/`) are rewritten —
  cross-skill references pass through untouched.
- The directive is **plain prose**, never an `<!-- include: -->` comment — that
  is what lets it pass through `expandIncludes` untouched (zero runtime change).
- One directive per extracted block, at the block's original position, so phase
  ordering is preserved.

## The "Supporting files" TOC

Add a single index block near the top of each `SKILL.md` — after the
frontmatter, before the first phase — so the model knows what exists without
loading it:

```markdown
## Supporting files (load on demand)

- `skills/<skill>/_includes/testing.md` — read in Phase 2 (run tests)
- `skills/<skill>/_includes/merge.md` — read in Phase 6 (merge gate)
```

This is the **one-level-deep index** the acceptance criteria require. List every
`_includes` file the body references, each with the phase that reads it.

## Reference files (`_includes/*.md`)

- Any `_includes/*.md` **over 100 lines** must begin with a `## Contents` table
  of contents, so a partial read still sees the file's scope.
- A reference file contains the procedural detail for one or more phases. Begin
  it with a short heading naming the phase(s) it serves.
- A reference file MUST NOT reference further `_includes` files (see
  [one-level-deep](#the-one-level-deep-rule)).

## What stays inline (load-bearing)

These are consumed by the agent, the orchestrator, or downstream stages and MUST
remain in the `SKILL.md` skeleton:

- Frontmatter (`name`, `version`, `description`, `allowed-tools`, …).
- Description / Invocation / Arguments / Prerequisites / Philosophy.
- **Input / Exit / Output Contract** sections (downstream stages key on these).
- Every `<!-- phase:start name=… index=N total=T stage=… -->` marker, at its
  current position, with unchanged `name`/`index`/`total`. **Phase count never
  changes** — detail relocates, phases do not.
- Each phase heading + a one-line purpose.
- Gate language (HARD GATE / blocking-check wording).
- Completion Checklist and Error Handling.

## The one-level-deep rule

A `SKILL.md` references its `_includes/*.md` files. An `_includes/*.md` file
MUST NOT reference further `_includes` files. References are exactly one level
deep — the model reads the body, then at most one reference file per phase. This
keeps the read graph flat and predictable.

## The platform injected-string caveat

The TS platform path (`skillRunner.ts` `parseSkillContent`, Issue #1473) serves
skills as an **injected string with no on-disk file**, so a `Read
skills/.../_includes/X.md` directive cannot resolve there. Therefore:

- **Keep inline** anything the platform path strictly needs to function before
  reaching the next on-disk read — environment preflight, contract definitions,
  gates.
- **Extract only procedural phase detail** that an on-disk run will `Read`.

The platform-side fix (ship the support set for injected skills) is the deferred
`platform-injection-bundle` recommendation from spike #3809.

## Verification checklist

After refactoring a skill, confirm:

- [ ] `wc -l skills/<skill>/SKILL.md` is under ~500.
- [ ] Phase-marker lines diff clean pre/post (count + every `index=`/`total=`).
- [ ] Contract and gate sections are still inline.
- [ ] Each `_includes/*.md` over 100 lines starts with a `## Contents` TOC.
- [ ] No `_includes/*.md` references another `_includes` file.
- [ ] Every `Read skills/.../_includes/X.md` directive path resolves to a real
      file.
- [ ] No `gh ` token moved into `_includes/` (the `no-direct-gh` linter scopes
      to `SKILL.md` only).
- [ ] `nightgauge preflight links`, `skill-versions`, `skill-banners`,
      `skill-no-direct-gh` and `scripts/validate-skill-metadata.sh` pass.
