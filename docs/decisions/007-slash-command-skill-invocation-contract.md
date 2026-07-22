# Slash-Command → Skill Invocation Contract

**Date:** 2026-05-09 **Author:** nightgauge **Status:** Superseded
(2026-06-01, #3876;
revised 2026-07-07, #4194)
**Issue:** #3343
**Epic:** #3342 — Slash-command-vs-skill enforcement
**Implemented:** 2026-05-11 via epic #3342 Phases 2–5 (#3343–#3347)

---

## Amendment (2026-07-07, #4194) — Selective injection via `metadata.chainable`

The #3876 amendment below made `disable-model-invocation: true` (DMI) a
blanket injection: every generated plugin skill copy gets it, with no
distinction between side-effecting stages (correctly blocked from spontaneous
model invocation) and read-only/advisory skills that other skills
**document chaining into** as an automatic sub-step (e.g. `issue-create` Phase
6 unconditionally invoking `issue-audit`; `queue` auto-invoking `assess-epic`
when an epic is detected).

DMI blocks any model-issued `Skill()` call that isn't the literal human-typed
slash command for _this_ turn — a parent skill's own instructions telling the
model to chain into a child skill are exactly such a call. So the blanket
injection made every documented skill-to-skill chain unreachable whenever a
skill, not a human, is the caller — the chain silently no-ops or the agent
improvises a deterministic-equivalent workaround instead of running the
audited flow. This is unaffected by whether the outer session is interactive
or headless (`HeadlessOrchestrator` spawns the same Claude CLI reading the
same generated plugin skill copies) — DMI is a property of the skill artifact,
not the invocation context.

**Fix:** `scripts/install-agent-skills.sh` `sync_plugin_skills()` now injects
DMI selectively. A canonical skill opts out by setting `metadata.chainable:
true` — an explicit, reviewable marker added at PR time, not an inferred
heuristic. `scripts/validate-skill-metadata.sh` validates the marker's shape
(must be exactly `true`) and continues to reject a raw `disable-model-invocation`
key on any canonical skill. Side-effecting stages (the six core pipeline
stages, and any other mutating flow) never set the marker and keep DMI
unchanged.

Marked `chainable: true` as of this revision: `issue-audit`, `retro`,
`pipeline-audit`, `pipeline-health`, `health-check`, `security-audit`,
`assess-epic` — all read-only/advisory (analysis or audit; any mutation is
behind an explicit opt-in flag like `--fix`, or absent entirely). See
[CODE_STANDARDS.md](../CODE_STANDARDS.md#skillmd-frontmatter-schema) for the
field definition and [CONTRIBUTING.md](../../CONTRIBUTING.md#authoring-checklist-new-slash-command)
for the authoring checklist.

---

## Amendment (2026-06-01, #3876) — Skills ARE the slash commands

The wrapper contract below is **superseded**. The command-wrapper-invokes-skill
model produced two defects once the canonical skills were bundled into the
plugin (#3871):

1. **Duplicate slash entries.** A plugin registers a `/nightgauge:<name>`
   entry for BOTH a `commands/<name>.md` file AND a `skills/<name>/SKILL.md`.
   Claude Code does not dedupe them, and `disable-model-invocation: true` does
   not hide a skill from the `/` menu (only `user-invocable: false` does). So
   every name with both a wrapper and a bundled skill appeared twice.
2. **The wrapper's `Skill()` call was blocked.** The banner instructs
   `Skill(skill="nightgauge:<name>")`, but the bundled skill carries
   `disable-model-invocation: true`, so the model's `Skill` tool call is
   rejected (`cannot be used … due to disable-model-invocation`) and the agent
   falls back to improvising from the command `.md` — the exact failure mode
   this ADR set out to prevent.

**New contract: the skill IS the namespaced slash command.** The bundled
`skills/<name>/SKILL.md` is the single `/nightgauge:<name>` entry. Typing
the command loads `SKILL.md` directly — there is no command `.md` to improvise
from, so the original failure mode is retired structurally rather than by
banner. Skills keep `disable-model-invocation: true` (user-invokable; the model
won't auto-run side-effecting workflows). The `claude-plugins/nightgauge/`
plugin ships **no** command wrappers except `model-routing-report.md` (a
self-contained utility with no skill counterpart and no `Skill()` invocation).
`scripts/install-agent-skills.sh` `sync_plugin_skills()` generates the plugin
skills tree from EVERY canonical `skills/` source. The standalone `smart-setup`
and `docs` plugins are removed; their capabilities are bundled into the single
`nightgauge` plugin as skills.

The historical analysis below is retained as the record of why the wrapper
banner existed; it no longer describes the shipped state.

---

### Post-Implementation State (as of 2026-05-11)

| Metric                                      | Pre-implementation | Post-implementation                                                     |
| ------------------------------------------- | ------------------ | ----------------------------------------------------------------------- |
| Command files total                         | 34                 | 34                                                                      |
| Files with canonical banner                 | 0                  | 33 (all applicable; `model-routing-report.md` exempt — no paired skill) |
| Files with `disable-model-invocation: true` | 4                  | 34 (all)                                                                |
| Runnable enforcement tests                  | 0                  | 31 (14 banner + 8 spike-contract + 9 epic-decomp)                       |

The Catalog of Command Files below reflects the **pre-implementation audit** state. DMI and Banner columns show values as of the decision date.

---

## Executive Summary

Every slash-command file in `claude-plugins/nightgauge/commands/*.md` is
loaded by the harness as `<command-message>` content and injected into the
agent's context. Today, those files describe their workflow inline but never
instruct the agent to invoke the corresponding `skills/nightgauge-<name>/SKILL.md`
via the `Skill` tool. The agent reads the command file, treats it as the spec,
and improvises — bypassing the rich SKILL.md logic (pipeline phases, gates,
spike-contract enforcement, decomposition) that lives only in the skill body.

This is a structural failure mode, not a series of one-off bugs. The audit in
this ADR shows:

- 34 of 34 command files (100%) **duplicate or describe** workflow content the
  skill owns.
- 0 of 34 (0%) reference the `Skill` tool or instruct skill invocation.
- 30 of 34 (88%) lack `disable-model-invocation: true` in YAML frontmatter, so
  the harness may auto-invoke the agent against the command file's text.
- 33 of 34 commands have a corresponding skill that owns the source-of-truth
  workflow; only 1 (`model-routing-report`) is a plugin-only utility with no
  skill counterpart.

Concrete incidents traceable to this gap:

- **#3329** — `/nightgauge-issue-create` produced an empty epic with no
  sub-issues. The agent read `commands/issue-create.md`'s "Quick Reference"
  section, took it as the full spec, and skipped Phase 2.5 of
  `skills/nightgauge-issue-create/SKILL.md` (epic decomposition).
- **#3331** — A `type:spike` issue was created without the mandatory
  `yaml recommendations` block. The agent read the command file and skipped
  Phase 3 of the issue-create skill (spike-contract enforcement).

Both incidents share the same root cause: **the command file is treated as the
spec; SKILL.md is never read.**

The lever we control is the **first instruction** in the file body the harness
injects. This ADR defines a canonical enforcement banner that Phase 2 of epic
#3342 will mechanically prepend to every applicable command file. The banner
explicitly invokes the `Skill` tool with the user's `$ARGUMENTS` BEFORE any
other content is read, so the agent runs the SKILL.md workflow — never the
command file's transcription of it.

---

## Harness Behavior We Cannot Change (the lever we control)

### Loader behavior (out of scope)

When a user types `/nightgauge-<name>` in the harness, the loader:

1. Locates `claude-plugins/nightgauge/commands/<name>.md`.
2. Wraps the file's body in `<command-message>` and `<command-name>` tags.
3. Injects the wrapped content into the agent's context as a user-turn message.
4. Optionally invokes the model against the wrapped content (unless the
   frontmatter sets `disable-model-invocation: true`, in which case the harness
   suppresses the auto-invocation path).

We do not own the loader. Future harness changes could alter the wrapping
shape, the auto-invocation policy, or the file lookup path. **This ADR's
contract is on file content, not loader behavior.** If the harness ever stops
injecting `<command-message>` blocks containing file content, this ADR's
contract is void and must be reauthored.

### The lever (in scope)

The agent processes the injected `<command-message>` content top-to-bottom.
The **first non-frontmatter content** sets behavior — once the agent commits
to a path, it follows that path. We control the file body, so we control the
first instruction the agent sees.

The fix is therefore positional, not semantic: prepend a single, unambiguous
banner that names the skill to invoke and the tool to invoke it with, and
ensure the banner appears before the body's existing prose.

---

## Findings

### Finding 1 — All 34 command files describe workflow inline

The audit (see [Catalog of Command Files](#catalog-of-command-files)) shows
that every command file currently re-states some portion of the corresponding
skill's workflow inside the command file. Even the four "thin pointer" files
(`update-docs.md`, `config-show.md`, `docs-watch.md`, `issue-refine.md`)
include enough Quick Start text and a "What This Command Does" / "Full
Workflow" section that an agent can plausibly treat the command file as the
spec.

**The shape of the duplication does not matter.** Whether a file repeats the
skill's full workflow or just its summary, the failure mode is the same: the
agent never reaches `skills/nightgauge-<name>/SKILL.md`.

### Finding 2 — Zero command files reference the `Skill` tool

A literal search across all 34 command files for the string `Skill(` and the
phrase `Skill tool` returns zero matches. No file currently instructs the
agent to invoke the underlying skill, by name or by tool. The agent has no
positional reason to look at SKILL.md unless it independently decides to —
which it does not, because the command file appears to be a complete spec.

### Finding 3 — Most command files lack `disable-model-invocation: true`

Only 4 of 34 (`issue-audit.md`, `issue-create.md`, `issue-refine.md`,
`model-routing-report.md`) include `disable-model-invocation: true` in YAML
frontmatter. The other 30 (88%) leave the harness free to auto-invoke the
agent against the command file's text. This compounds Finding 2: not only is
there no instruction to invoke the skill, but the harness may eagerly run the
model against the command file before any banner has a chance to redirect
flow.

### Finding 4 — Both #3329 and #3331 root-cause to the same gap

| Incident | Symptom                                       | Skill phase bypassed                   | Root cause           |
| -------- | --------------------------------------------- | -------------------------------------- | -------------------- |
| #3329    | Empty epic created; no sub-issues; invisible. | issue-create Phase 2.5 (decomposition) | Command file = spec. |
| #3331    | Spike created without `yaml recommendations`. | issue-create Phase 3 (spike-contract)  | Command file = spec. |

Neither incident is a logic bug in the skill. The skill's logic was never
loaded. Both are instances of the agent treating the command file as the
authoritative workflow.

### Finding 5 — One command has no corresponding skill

`model-routing-report.md` is a plugin-only utility (it generates a routing
report from local config) with no `skills/nightgauge-model-routing-report/`
counterpart. The banner is **not applicable** to this file. Phase 2 must
exclude it from the mechanical-prepend pass.

All other 33 commands have a corresponding skill, including:

- `update-docs` → `skills/update-docs/` (cross-plugin namespace alias)
- `smart-setup` → `skills/smart-setup/` (cross-plugin namespace alias)
- `pr-preflight` → `skills/pr-preflight/`

The banner template uses the `nightgauge:<name>` namespace for all
banners regardless of physical skill directory location, because that is the
canonical invocation namespace exposed by the harness.

---

## Banner Template

The canonical enforcement banner. Phase 2 prepends this verbatim to every
applicable command file (33 of 34 files), substituting `<name>` with the
command's basename (e.g., `issue-create`, `feature-planning`).

````markdown
> **INVOKE THE SKILL — DO NOT IMPLEMENT FROM THIS FILE**
>
> This file is loaded as `<command-message>` content. Before reading any other
> content here, invoke the `Skill` tool:
>
> ```
> Skill(skill="nightgauge:<name>", args="$ARGUMENTS")
> ```
>
> The body below is reference-only. The skill's `SKILL.md` is the
> source-of-truth workflow — improvising from this file's text is a known
> failure mode (see [ADR 007](../../../docs/decisions/007-slash-command-skill-invocation-contract.md)).
````

The banner is 12 lines (≤15 required). It:

1. Names the failure mode in the title so an agent that reads only the title
   still receives the correction.
2. References the harness wrapping (`<command-message>`) so the agent
   understands why the instruction is positional, not editorial.
3. Names the `Skill` tool literally and the `nightgauge:<name>` namespace
   so the invocation is mechanical, not interpretive.
4. Forwards `$ARGUMENTS` so any user-supplied flags reach the skill unchanged.
5. Cross-references this ADR so a curious reader can find the rationale
   without re-deriving it.

### Frontmatter recommendation (companion to the banner)

Phase 2 should also add `disable-model-invocation: true` to the YAML
frontmatter of each banner-bearing file that lacks it (30 files). Without
this, the harness may auto-invoke the agent against the command file's text
before the banner takes effect. The banner is the primary contract; the
frontmatter flag is a defensive belt-and-suspenders against harness eagerness.

---

## Catalog of Command Files

All 34 files in `claude-plugins/nightgauge/commands/`. **Lines** is the
file's current line count. **Skill?** marks whether a corresponding
`skills/<...>/SKILL.md` exists. **Shape** is `thin pointer` (small file,
primarily redirects to SKILL.md) or `duplicates workflow` (substantial
workflow content inline). **DMI** is whether the file currently sets
`disable-model-invocation: true`. **Banner applies** is `yes` for every file
with a corresponding skill; `no` for plugin-only utilities.

| #   | File                        | Lines | Skill? | Shape               | DMI | Banner applies |
| --- | --------------------------- | ----- | ------ | ------------------- | --- | -------------- |
| 1   | `assess-epic.md`            | 134   | yes    | duplicates workflow | no  | yes            |
| 2   | `backlog-groom.md`          | 322   | yes    | duplicates workflow | no  | yes            |
| 3   | `backlog-preflight.md`      | 123   | yes    | duplicates workflow | no  | yes            |
| 4   | `config-show.md`            | 63    | yes    | thin pointer        | no  | yes            |
| 5   | `continuous-improvement.md` | 120   | yes    | duplicates workflow | no  | yes            |
| 6   | `dep-modernize.md`          | 241   | yes    | duplicates workflow | no  | yes            |
| 7   | `doc-gen.md`                | 134   | yes    | duplicates workflow | no  | yes            |
| 8   | `docs-watch.md`             | 67    | yes    | thin pointer        | no  | yes            |
| 9   | `docs-write.md`             | 119   | yes    | duplicates workflow | no  | yes            |
| 10  | `feature-dev.md`            | 152   | yes    | duplicates workflow | no  | yes            |
| 11  | `feature-planning.md`       | 106   | yes    | duplicates workflow | no  | yes            |
| 12  | `feature-validate.md`       | 150   | yes    | duplicates workflow | no  | yes            |
| 13  | `health-check.md`           | 190   | yes    | duplicates workflow | no  | yes            |
| 14  | `issue-audit.md`            | 136   | yes    | duplicates workflow | yes | yes            |
| 15  | `issue-create.md`           | 238   | yes    | duplicates workflow | yes | yes            |
| 16  | `issue-pickup.md`           | 254   | yes    | duplicates workflow | no  | yes            |
| 17  | `issue-refine.md`           | 70    | yes    | thin pointer        | yes | yes            |
| 18  | `model-routing-report.md`   | 134   | **no** | duplicates workflow | yes | **no**         |
| 19  | `modernize-plan.md`         | 215   | yes    | duplicates workflow | no  | yes            |
| 20  | `pipeline-audit.md`         | 211   | yes    | duplicates workflow | no  | yes            |
| 21  | `pipeline-health.md`        | 261   | yes    | duplicates workflow | no  | yes            |
| 22  | `pr-create.md`              | 209   | yes    | duplicates workflow | no  | yes            |
| 23  | `pr-merge.md`               | 150   | yes    | duplicates workflow | no  | yes            |
| 24  | `pr-preflight.md`           | 126   | yes    | duplicates workflow | no  | yes            |
| 25  | `project-sync.md`           | 453   | yes    | duplicates workflow | no  | yes            |
| 26  | `queue.md`                  | 246   | yes    | duplicates workflow | no  | yes            |
| 27  | `refactor-rewrite.md`       | 213   | yes    | duplicates workflow | no  | yes            |
| 28  | `repo-init.md`              | 213   | yes    | duplicates workflow | no  | yes            |
| 29  | `retro.md`                  | 198   | yes    | duplicates workflow | no  | yes            |
| 30  | `security-audit.md`         | 214   | yes    | duplicates workflow | no  | yes            |
| 31  | `smart-setup.md`            | 357   | yes    | duplicates workflow | no  | yes            |
| 32  | `test-gen.md`               | 138   | yes    | duplicates workflow | no  | yes            |
| 33  | `test-scaffold.md`          | 211   | yes    | duplicates workflow | no  | yes            |
| 34  | `update-docs.md`            | 58    | yes    | thin pointer        | no  | yes            |

**Totals:** 34 files audited · 33 banner-applicable · 1 skipped
(`model-routing-report.md`) · 4 already set DMI · 30 need DMI added by Phase 2.

The row count matches `ls claude-plugins/nightgauge/commands/ | wc -l`
(34) with no omissions.

---

## Recommendations

The recommendations below are written in the
[`docs/SPIKE_CONTRACT.md`](../SPIKE_CONTRACT.md) `yaml recommendations` schema
for traceability. Because this artifact lives at `docs/decisions/` rather than
`docs/spikes/`, `nightgauge spike materialize` will **not** auto-discover
it — that is the desired behavior here. Epic #3342's Phases 2-5 have already
been filed manually as sibling issues, so each entry below uses
`action: skip` to record the decision and link without creating a duplicate.

```yaml recommendations
spike: 3343
recommendations:
  - id: apply-banner
    action: skip
    title: "Phase 2: Apply enforcement banner to 33 applicable command files"
    type: chore
    priority: high
    size: M
    labels: ["component:plugins", "epic:3342"]
    body: |
      Mechanically prepend the banner from ADR-007 (Banner Template section)
      to every command file in `claude-plugins/nightgauge/commands/*.md`
      except `model-routing-report.md`. Substitute `<name>` with each file's
      basename. Also add `disable-model-invocation: true` to the YAML
      frontmatter of the 30 files that do not currently set it.

      Filed under epic #3342. This `skip` entry exists for traceability —
      `nightgauge spike materialize` does not run on this artifact
      (it lives at `docs/decisions/`, not `docs/spikes/`), so the entry
      records the decision without creating a duplicate issue.
    depends_on: []
  - id: skill-spike-contract-gate
    action: skip
    title: "Phase 3: SKILL.md hard-gate for spike-contract enforcement"
    type: feature
    priority: high
    size: M
    labels: ["component:skills", "epic:3342"]
    body: |
      Add a hard-gate to `skills/nightgauge-issue-create/SKILL.md`
      Phase 3 that fails issue creation if a `type:spike` issue body lacks
      a parseable `yaml recommendations` block matching
      `docs/SPIKE_CONTRACT.md`. Belt-and-suspenders to the banner contract
      from #3342 Phase 2.

      Filed under epic #3342. See note in `apply-banner` for why this is
      `action: skip`.
    depends_on: ["apply-banner"]
  - id: skill-epic-decomposition-gate
    action: skip
    title: "Phase 4: SKILL.md hard-gate for epic-decomposition completeness"
    type: feature
    priority: high
    size: M
    labels: ["component:skills", "epic:3342"]
    body: |
      Add a hard-gate to `skills/nightgauge-issue-create/SKILL.md`
      Phase 2.5 that fails issue creation if a `type:epic` issue is created
      without sub-issues (the #3329 failure mode). Belt-and-suspenders to
      the banner contract from #3342 Phase 2.

      Filed under epic #3342. See note in `apply-banner` for why this is
      `action: skip`.
    depends_on: ["apply-banner"]
  - id: tests-and-docs-capstone
    action: skip
    title: "Phase 5: Tests + CONTRIBUTING.md update for slash-command authoring"
    type: docs
    priority: medium
    size: S
    labels: ["component:plugins", "epic:3342"]
    body: |
      Add tests asserting that every file in
      `claude-plugins/nightgauge/commands/*.md` (except
      `model-routing-report.md`) begins with the banner from ADR-007 and
      sets `disable-model-invocation: true`. Update `CONTRIBUTING.md` to
      document the requirement so new commands are author-correct from day
      one.

      Filed under epic #3342. See note in `apply-banner` for why this is
      `action: skip`.
    depends_on: ["apply-banner", "skill-spike-contract-gate", "skill-epic-decomposition-gate"]
```

> **Note on the hybrid shape**: this ADR embeds a `yaml recommendations`
> block — a construct from `docs/SPIKE_CONTRACT.md` that normally lives in
> `docs/spikes/{N}-...md`. The block is informational here. The
> `nightgauge spike materialize` stage scans `docs/spikes/{N}-*.md`
> and will not discover this file at `docs/decisions/`, by design. Epic
> #3342's sibling phases are already filed manually; the block records that
> decision in a machine-parseable way without re-creating duplicates.

---

## Open Questions

These are out of scope for Phase 1 (this issue) and Phase 2 (banner
application). They are recorded so future readers know what this ADR does
**not** decide.

1. **`model-routing-report.md` — banner-exempt or merge into a skill?**
   Today this is the only command without a corresponding skill. Phase 2
   excludes it from the mechanical prepend pass. A future decision might
   either (a) author `skills/nightgauge-model-routing-report/SKILL.md`
   and bring it under the banner contract, or (b) move the utility out of
   `commands/` into a `tools/` namespace where the banner contract does not
   apply. Either choice is non-blocking for #3342.

2. **Cross-plugin command files (`update-docs`, `smart-setup`).** These are
   exposed under multiple plugin namespaces (`nightgauge:`, `docs:`,
   `smart-setup:`). The banner uses the `nightgauge:<name>` namespace
   per the harness skill registry naming convention. If a future decision
   re-homes either skill to a different canonical namespace, the banner in
   that command file must be updated to match. Tracked here so the dependency
   is explicit.

3. **Banner stability against harness changes.** This ADR's contract is on
   file content, not on the harness loader behavior (see [Harness Behavior
   We Cannot Change](#harness-behavior-we-cannot-change-the-lever-we-control)).
   If a future Claude harness release changes how command files are loaded
   (different wrapping tags, no auto-injection, etc.), the banner's
   effectiveness must be re-validated and the ADR re-authored.

4. **Catalog drift after Phase 2 ships.** The catalog table above is
   accurate as of 2026-05-09. New commands added after Phase 2 will need
   the banner from day one. Phase 5 is responsible for adding a
   `CONTRIBUTING.md` rule and a test asserting banner presence. This ADR
   is a snapshot, not an evergreen index — when commands are added or
   removed, this catalog will fall out of date.

---

## Consequences

- **Phase 2 has a single source-of-truth template to copy.** No per-file
  judgement calls; the banner is mechanical.
- **30 of 34 command files will gain `disable-model-invocation: true`** as a
  side-effect of Phase 2. The 4 that already set it are unchanged.
- **`model-routing-report.md` is the sole exemption** from Phase 2's pass.
  It remains today's plugin-only utility. Future decisions tracked in [Open
  Questions](#open-questions) Item 1.
- **The materializer's contract is unchanged.** This artifact lives at
  `docs/decisions/`, so `nightgauge spike materialize` does not scan
  it. The `yaml recommendations` block is informational/auditable only.
- **The contract is forward-only.** Existing already-merged invocations of
  the affected commands are unaffected. Future invocations route through
  the banner.

---

## Files Changed

- `docs/decisions/007-slash-command-skill-invocation-contract.md` — this
  document.
- `docs/decisions/README.md` — Active Decisions table row 007 added.

No code changes. Banner application to command files happens in Phase 2 of
epic #3342, tracked separately.
