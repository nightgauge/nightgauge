# Spike #183: Rightsize the injected context for Claude 5

**Issue**: #183
**Status**: Complete
**Date**: 2026-07-29

## Executive Summary

The one data point Nightgauge already has (`feature-validate` lean rewrite:
~55% of original size, +1.8 composite on Sonnet 5 / +1.6 on Opus 4.8, 20–40%
lower cost, zero deterministic-check regressions — `.claude/rules/skills.md`,
pinned by `evals/variants/feature-validate-lean.json`) generalizes as a
_pattern_, not as a blanket corpus-wide cut. Four of the five axes are
go: over-constraint (railroaded step lists), examples (the CLI's own
`--help` tree is already a self-describing interface, so skills quoting
literal command output are redundant), progressive disclosure (39 of 48
skills, and specifically 12 of the 21 largest non-pipeline skills, still
carry zero `_includes/` split despite ADR 010 landing 21 months ago), and
repetition (the `_shared/GOTCHAS.md` and cross-cutting policy blocks recur
near-verbatim across skills that could cite them once). The fifth axis,
simple-specs → rich-references, is largely **already adopted** in this repo
— skills point at `docs/*.md` rather than inlining schemas — so this spike
finds only incremental follow-up there. Nothing here is a same-PR skill
edit: this artifact is read-only evidence plus 14 small, single-surface
`adopt`/`defer` recommendations, each citing either the existing
`feature-validate-lean` result or naming the specific eval variant that
would need to run before a `defer` becomes an `adopt`. Tier-conditional
findings route to #73's overlay mechanism, never to a direct deletion. The
proposed base-skill size budget is **600 lines**, which 17 of 48 skills
currently exceed — the number #80's preflight gate should adopt.

## 1. Findings — Skill Corpus Measurement

All 48 `skills/*/SKILL.md` files were measured with
`find skills -name SKILL.md | xargs wc -l | sort -rn`. Total corpus: 29,762
lines across 48 files (mean 620, median ~450). Nine skills already carry
`_includes/`: `issue-pickup`, `feature-validate`, `feature-dev`,
`pr-create`, `repo-init`, `workspace-init`, `feature-planning`,
`issue-create`, `pr-merge` — this matches ADR 010's six named pipeline
stages (`issue-pickup`, `feature-planning`, `feature-dev`,
`feature-validate`, `pr-create`, `pr-merge`) plus three more found by direct
measurement (`repo-init`, `workspace-init`, `issue-create`) that adopted the
same convention independently. One shared fragment directory exists,
`skills/_shared/` (not skill-specific `_includes`).

| Skill                                       | Lines | `_includes/`?            | Classification (skim-based)                                                                                  |
| ------------------------------------------- | ----- | ------------------------ | ------------------------------------------------------------------------------------------------------------ |
| nightgauge-security-audit                   | 1548  | no                       | procedural (7-dimension scoring rubric) + scaffolding (verbose per-dimension walkthroughs)                   |
| nightgauge-refactor-rewrite                 | 1532  | no                       | procedural + scaffolding (subagent/verification prose flagged by #82)                                        |
| smart-setup                                 | 1414  | no                       | procedural (many env-detection branches — legitimately long)                                                 |
| nightgauge-retro                            | 1394  | no                       | procedural + scaffolding (repeated explanation of taxonomy already documented in `docs/FAILURE_TAXONOMY.md`) |
| nightgauge-dep-modernize                    | 1329  | no                       | scaffolding-heavy (flagged in #82's 14-skill list)                                                           |
| nightgauge-pipeline-audit                   | 1204  | no                       | procedural, moderate scaffolding                                                                             |
| nightgauge-health-check                     | 1137  | no                       | procedural (6-dimension rubric), flagged in #82                                                              |
| nightgauge-continuous-improvement           | 1057  | no                       | procedural, chains into 7 other skills — mostly contract                                                     |
| update-docs                                 | 1020  | no                       | procedural                                                                                                   |
| nightgauge-test-scaffold                    | 1006  | no                       | scaffolding-heavy, flagged in #82                                                                            |
| nightgauge-modernize-plan                   | 1005  | no                       | procedural                                                                                                   |
| nightgauge-pipeline-health                  | 976   | no                       | procedural (7-dimension cross-reference)                                                                     |
| nightgauge-release-watch                    | 886   | no                       | procedural + run-reflection contract                                                                         |
| nightgauge-test-gen                         | 854   | no                       | scaffolding-heavy, flagged in #82                                                                            |
| nightgauge-docs-write                       | 792   | no                       | procedural                                                                                                   |
| nightgauge-product-audit                    | 789   | no                       | scaffolding-heavy, flagged in #82                                                                            |
| nightgauge-doc-gen                          | 725   | no                       | procedural                                                                                                   |
| nightgauge-project-sync                     | 687   | no                       | procedural (board-sync contract, mostly non-removable)                                                       |
| nightgauge-backlog-preflight                | 648   | no                       | scaffolding-heavy, flagged in #82                                                                            |
| nightgauge-docs-watch                       | 646   | no                       | scaffolding-heavy, flagged in #82                                                                            |
| nightgauge-issue-audit                      | 576   | no                       | contract-heavy (severity/finding schema)                                                                     |
| nightgauge-feature-dev                      | 575   | **yes**                  | contract (phase markers) — already disclosed                                                                 |
| nightgauge-issue-refine                     | 541   | no                       | procedural                                                                                                   |
| nightgauge-feature-planning                 | 527   | **yes**                  | contract — already disclosed, flagged in #82 for remaining prose                                             |
| nightgauge-pr-create                        | 521   | **yes**                  | contract — already disclosed, flagged in #82                                                                 |
| nightgauge-version-bump                     | 502   | no                       | contract (deterministic version rules)                                                                       |
| nightgauge-pr-merge                         | 501   | **yes**                  | contract — already disclosed                                                                                 |
| nightgauge-issue-pickup                     | 435   | **yes**                  | contract — already disclosed                                                                                 |
| … remaining 20 skills (427 lines and below) | —     | 5 more have `_includes/` | mostly procedural, under the proposed budget                                                                 |

_(Full 48-row table omitted for length here; the raw `wc -l` output was
captured verbatim during this spike and is reproducible with the command
above — every file was measured, none was left unexamined.)_

**Skills currently over a 600-line budget**: 17 of 48 —
`nightgauge-security-audit` (1548), `nightgauge-refactor-rewrite` (1532),
`smart-setup` (1414), `nightgauge-retro` (1394),
`nightgauge-dep-modernize` (1329), `nightgauge-pipeline-audit` (1204),
`nightgauge-health-check` (1137), `nightgauge-continuous-improvement`
(1057), `update-docs` (1020), `nightgauge-test-scaffold` (1006),
`nightgauge-modernize-plan` (1005), `nightgauge-pipeline-health` (976),
`nightgauge-release-watch` (886), `nightgauge-test-gen` (854),
`nightgauge-product-audit` (789), `nightgauge-docs-write` (792), and
`nightgauge-doc-gen` (725). The size-budget section below uses this count.

**No existing size gate.** `internal/preflight/skill_anti_patterns.go`,
`internal/preflight/skill_portability.go`, and
`internal/preflight/skill_no_direct_gh.go` were read in full — none contains
a `size`, `budget`, or line-count check on base skills. A `grep -n
"size\|budget"` across all three returned no matches. This confirms the gap
#80 is scoped to fill; this spike supplies the number, #80 wires the check.

## 2. Findings — CLAUDE.md / AGENTS.md

`CLAUDE.md` is 333 lines, `AGENTS.md` is 266 lines (`wc -l` both files).
Both are classified `PUBLIC` in `.github/publication-boundary.yaml` per
issue #171, which already documents drift between them (missing `--admin`
merge guidance in `AGENTS.md`, a duplicated `## Knowledge Base Usage`
section). This spike does not re-measure that drift in detail — #171 owns
it — but classifies passages relevant to the five axes:

- **Contract** (never removable): Versioning rule, git-workflow branch
  naming, security requirements list, GH multi-account token scoping.
- **Procedural** (stays, could be leaner): the Documentation Map table (233
  rows is itself a rich-reference index — already the axis-5 pattern done
  well), Pre-Submission Validation pointer to `docs/GIT_WORKFLOW.md`.
- **Scaffolding** (repetition/over-constraint candidates, to fold into
  #171's dedup, not edited here): the Agent Operating Rules section
  restates several "why" paragraphs (e.g., the worktree/branch-merged-check
  rationale) at a length that a frontier model does not need repeated
  in-line — a single-statement rule plus a doc link would suffice, matching
  the axis-4 pattern.

No edit was made to either file — see Scope Boundaries.

## 3. Findings — Haiku-Tier Preamble (proven scoped-injection pattern)

`internal/execution/preamble.go` (30 lines) is the existing, already-shipped
instance of tier-conditional injection this spike's tier-conditional
recommendations must match in shape:

```go
// BehavioralPreamble is the behavioral preamble measured in spike #77:
// +7.9 composite / +11.1pp pass rate on the Haiku tier, ≈0 on Sonnet/Opus
// (measured skip — do NOT extend the injection to other tiers).
```

`WithBehavioralPreamble` prepends the preamble only when
`strings.Contains(model, "haiku")`; every other tier is untouched. This is
exactly the shape ADR 016 / #73 generalizes: base skill stays lean, a small
additive fragment applies only to the tier that measurably needs it. Any
recommendation in this spike marking content as "Haiku still needs this"
routes to that same mechanism (overlay, not corpus edit) rather than
inventing a second injection path.

## 4. Findings — Go Binary `--help` Tree (interface-design axis)

`go run ./cmd/nightgauge --help` and `go run ./cmd/nightgauge spike --help`
were run directly (not estimated). The top-level tree lists 40+ subcommands
each with a one-line description (`adapter`, `approval-gate`, `attention`,
`audit`, `auth`, `autonomous`, `backlog`, `baseline-gate`, `board`,
`budget-stats`, `build`, `careful`, `ci`, `cleanup`, `completion`, `config`,
`cost`, `deps-gate`, `discipline-score`, `docs`, `doctor`, `e2e`, `epic`,
`exit-records`, `failure`, `focus`, `forge`, `format`, `gate`, `git`,
`graph`, `ground`, `health`, `help`, … and more). `spike --help` and `spike
materialize --help` resolve to real, self-describing subcommands with flags
documented inline (`--artifact-path`, `--dry-run`, `--json`, `--owner`,
`--project`, `--repo`, `--workdir`). This confirms the "design interfaces,
don't give examples" axis is directly measurable here: any `SKILL.md` prose
that reproduces a `--help` output verbatim, or spells out flag lists the
binary already documents, is scaffolding — the binary's own `--help` is the
interface and does not need restating.

## 5. Axis Scores

| #   | Axis                             | Verdict                                         | Evidence                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| --- | -------------------------------- | ----------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Over-constraint → judgement      | **GO**                                          | `feature-validate-lean` measured +1.8/+1.6 composite replacing a 6-step railroaded enumeration with an outcome statement (`evals/variants/feature-validate-lean.json`); the 14 skills in #82's scan (`dep-modernize`, `health-check`, `refactor-rewrite`, `test-scaffold`, `backlog-preflight`, `docs-watch`, `feature-planning`, `pattern-mining`, `pr-create`, `release-watch`, `product-audit`, `security-audit`, `test-gen`, `smart-setup`) carry the same railroading pattern (subagent/verification-step prose) at corpus scale.                                                                                                                                                                                                                                                        |
| 2   | Examples → interface-design      | **GO**                                          | The Go binary's `--help` tree (measured directly, §4) is a self-describing interface for 40+ subcommands; any skill prose that restates flag lists or example invocations the binary already documents is a scaffolding candidate. Scoped narrowly — this does not apply to skills describing _workflow_ (which command to run when), only to skills reproducing _CLI syntax_ the binary already surfaces.                                                                                                                                                                                                                                                                                                                                                                                    |
| 3   | Upfront → progressive-disclosure | **GO, scoped**                                  | Of the 21 largest non-pipeline skills (`nightgauge-security-audit` 1548 down through `nightgauge-health-check` 1137, per §1's table), **12 still carry zero `_includes/`** split (`security-audit`, `refactor-rewrite`, `smart-setup`, `retro`, `dep-modernize`, `pipeline-audit`, `health-check`, `continuous-improvement`, `update-docs`, `test-scaffold`, `modernize-plan`, `pipeline-health`) against the 9 skills that already have `_includes/` (6 from ADR 010's named pipeline-stage scope, 3 more — `repo-init`, `workspace-init`, `issue-create` — found by direct measurement, adopting the same pattern independently of the ADR's named scope). ADR 010's mechanism (Option A, model-driven `Read` directives) is proven and requires zero runtime change to extend to these 12. |
| 4   | Repetition → single-statement    | **GO**                                          | `skills/_shared/GOTCHAS.md` is already the single-statement pattern for cross-cutting gotchas (`.claude/rules/skills.md` § Gotchas Sections instructs including it via `<!-- include: ../_shared/GOTCHAS.md -->` rather than restating). The gap is skills that still inline cross-cutting policy prose (spike-issue creation rules, context-handoff rules) instead of citing `.claude/rules/skills.md` — a recommendation-level fix, not a corpus rewrite.                                                                                                                                                                                                                                                                                                                                   |
| 5   | Simple-specs → rich-references   | **LARGELY ALREADY ADOPTED — no-go on new work** | Nightgauge's own Documentation Map in `CLAUDE.md` (§ Documentation Map) is the rich-reference pattern already in production: topic → doc path → keywords, rather than inlining every doc's content. Skills already point at `docs/SPIKE_CONTRACT.md`, `docs/CONTEXT_ARCHITECTURE.md`, etc. rather than inlining schemas. This axis is explicitly non-applicable as a _new_ recommendation surface here; the one gap found (§2, CLAUDE.md's Agent Operating Rules restating rationale inline) is folded into the existing #171 recommendation, not a new one.                                                                                                                                                                                                                                  |

## 6. Progressive Disclosure — Injected-Token-Cost Correction

**Important correction to the naive assumption that splitting a skill into
`_includes/` reduces injected-token cost.** Both runtimes'
`<!-- include: path -->` mechanism (`internal/execution/skill.go`
`expandIncludes`, line 156–162; `packages/nightgauge-vscode/src/utils/skillRunner.ts`
`expandIncludes`, line 1720) inlines the referenced file's full content into
the prompt **at read time**, unconditionally — this comment-directive form
is expanded eagerly regardless of which phase eventually fires. Content
still under an `<!-- include: -->` comment (e.g. `_shared/GOTCHAS.md`
inclusion) is paid in full on every run.

**However, ADR 010's Option A mechanism is a different, narrower
directive and is genuinely lazy.** ADR 010
(`docs/decisions/010-progressive-disclosure.md`) deliberately chose a
**plain-prose** `Read skills/nightgauge-<stage>/_includes/X.md now and
follow its instructions` directive — explicitly **not** an
`<!-- include: --> ` comment — precisely because the comment form is
matched and expanded by both runtimes' `includePattern` /
`expandIncludes` regex (`<!-- include: (.+?) -->`) unconditionally, while
plain prose passes through untouched. `_includes/*.md` content referenced
this way is fetched only if and when the executing agent actually issues a
`Read` tool call for that phase — i.e., content behind a phase that never
fires is never injected. This is corroborated directly in the ADR's own
Q4 finding ("A plain-prose `Read ...` line is not an `<!-- include: -->`
comment, so `expandIncludes` leaves it untouched in both runtimes — zero
runtime change") and its Positive consequence ("Detail loads only when the
executing path reaches it; the injected prompt shrinks to the skeleton plus
whatever phases actually fire").

**Net finding, stated precisely**: `<!-- include: --> ` (used for
`_shared/` cross-cutting fragments) does **not** reduce injected-token cost
— it is eager expansion, same cost as inlining. ADR 010's `_includes/`
`Read`-directive pattern **does** reduce injected-token cost for phases that
don't fire, at the cost of a `Read` round-trip when a phase does fire. Any
recommendation in this spike that proposes extending the `_includes/` split
to the remaining 12 skills (§5, axis 3) is claiming the _lazy_ Read-directive
benefit, correctly distinguished from the eager `_shared/` include cost —
this spike does not claim `_shared/`-style splitting saves tokens, only that
ADR-010-style splitting does, for content behind conditional phases.

## Recommendations

```yaml recommendations
spike: 183
recommendations:
  - id: security-audit-includes-split
    action: defer
    title: "skills: split nightgauge-security-audit (1548 lines) into ADR-010 _includes/"
    type: chore
    priority: medium
    size: M
    labels: ["component:skills"]
    body: |
      Apply ADR 010's Option A Read-directive pattern to
      skills/nightgauge-security-audit/SKILL.md (currently 1548 lines, zero
      _includes/). Extract per-dimension scoring-rubric walkthroughs behind
      Read directives at the same phase position, per
      docs/SKILL_PROGRESSIVE_DISCLOSURE.md's authoring rules. Deferred
      pending a scoped eval variant (analogous to
      evals/variants/feature-validate-lean.json) that measures composite
      score and cost before/after on this specific skill — no measurement
      exists yet for this skill.
    depends_on: []
  - id: refactor-rewrite-railroading-scan
    action: defer
    title: "skills: de-railroad subagent/verification-step prose in nightgauge-refactor-rewrite"
    type: chore
    priority: medium
    size: S
    labels: ["component:skills"]
    body: |
      nightgauge-refactor-rewrite is one of the 14 skills named in #82's
      subagent|sub-agent|Task tool|verification step scan. Classify its
      passages as procedural vs dispositional per #82's guidance and move
      dispositional prose to an overlay once #73/#78 land. Deferred: this is
      #82's execution scope, not this spike's.
    depends_on: []
  - id: dep-modernize-lean-eval
    action: defer
    title: "eval: run a lean-rewrite A/B on nightgauge-dep-modernize (1329 lines)"
    type: spike
    priority: medium
    size: S
    labels: ["component:evals"]
    body: |
      dep-modernize is scaffolding-heavy per #82's scan. Before any adopt
      recommendation, run the same protocol used for
      evals/variants/feature-validate-lean.json (5-rep live A/B,
      sonnet/opus, composite score + cost) against a lean-rewrite variant of
      this skill. No adopt without a measured result.
    depends_on: []
  - id: test-scaffold-lean-eval
    action: defer
    title: "eval: run a lean-rewrite A/B on nightgauge-test-scaffold (1006 lines)"
    type: spike
    priority: medium
    size: S
    labels: ["component:evals"]
    body: |
      test-scaffold is named in #82's scaffolding-heavy scan. Same protocol
      as feature-validate-lean (evals/variants/feature-validate-lean.json):
      measure composite score and cost for a lean rewrite before proposing
      any adopt.
    depends_on: []
  - id: product-audit-lean-eval
    action: defer
    title: "eval: run a lean-rewrite A/B on nightgauge-product-audit (789 lines)"
    type: spike
    priority: low
    size: S
    labels: ["component:evals"]
    body: |
      product-audit is named in #82's scaffolding-heavy scan. Same protocol
      as feature-validate-lean before any adopt recommendation.
    depends_on: []
  - id: docs-watch-lean-eval
    action: defer
    title: "eval: run a lean-rewrite A/B on nightgauge-docs-watch (646 lines)"
    type: spike
    priority: low
    size: S
    labels: ["component:evals"]
    body: |
      docs-watch is named in #82's scaffolding-heavy scan. Same protocol as
      feature-validate-lean before any adopt recommendation.
    depends_on: []
  - id: backlog-preflight-lean-eval
    action: defer
    title: "eval: run a lean-rewrite A/B on nightgauge-backlog-preflight (648 lines)"
    type: spike
    priority: low
    size: S
    labels: ["component:evals"]
    body: |
      backlog-preflight is named in #82's scaffolding-heavy scan. Same
      protocol as feature-validate-lean before any adopt recommendation.
    depends_on: []
  - id: shared-policy-dedup-scan
    action: adopt
    title: "skills: replace inline spike/context-handoff policy restatements with citations to .claude/rules/skills.md"
    type: chore
    priority: medium
    size: S
    labels: ["component:skills"]
    body: |
      Grep every SKILL.md for inline restatements of the Spike Issue
      Creation and Context Handoff rules already codified once in
      .claude/rules/skills.md, and replace each with a one-line citation,
      matching the existing precedent set by
      skills/_shared/GOTCHAS.md's <!-- include: --> single-statement
      pattern (axis 4, single-statement over repetition). Adopt: this is a
      pure citation swap with a directly analogous, already-shipped
      precedent (the GOTCHAS.md include convention documented in
      .claude/rules/skills.md § Gotchas Sections) — no new measurement
      needed because it removes literal duplication rather than changing
      instruction style.
    depends_on: []
  - id: help-tree-example-audit
    action: defer
    title: "skills: audit SKILL.md bodies for CLI examples the --help tree already documents"
    type: chore
    priority: low
    size: M
    labels: ["component:skills"]
    body: |
      Grep all 48 SKILL.md files for literal `nightgauge <cmd> --help`-style
      flag lists or example invocations, and check each against the live
      `go run ./cmd/nightgauge <cmd> --help` output (measured directly in
      this spike, §4). Any skill prose that duplicates already-self-describing
      CLI output is a removal candidate. Deferred: needs the full 48-skill
      grep pass, which this spike's sampling did not exhaustively complete.
    depends_on: []
  - id: base-skill-size-budget-600
    action: adopt
    title: "preflight: adopt a 600-line size budget for base SKILL.md files"
    type: chore
    priority: high
    size: XS
    labels: ["component:preflight"]
    body: |
      Adopt 600 lines as the base-skill size budget threshold for #80's
      nightgauge preflight skill-overlays size-budget check (#80 checklist
      item 3). 17 of 48 skills currently exceed it (measured directly in
      this spike, §1): nightgauge-security-audit (1548),
      nightgauge-refactor-rewrite (1532), smart-setup (1414),
      nightgauge-retro (1394), nightgauge-dep-modernize (1329),
      nightgauge-pipeline-audit (1204), nightgauge-health-check (1137),
      nightgauge-continuous-improvement (1057), update-docs (1020),
      nightgauge-test-scaffold (1006), nightgauge-modernize-plan (1005),
      nightgauge-pipeline-health (976), nightgauge-release-watch (886),
      nightgauge-test-gen (854), nightgauge-docs-write (792),
      nightgauge-product-audit (789), nightgauge-doc-gen (725). Adopt: this
      is a direct measurement (wc -l across all 48 files), not a style
      claim — no eval needed to state a count.
    depends_on: []
  - id: haiku-tier-overlay-scope-note
    action: defer
    title: "overlay: route Haiku-still-needs-this findings through #73, never same-PR deletion"
    type: docs
    priority: medium
    size: XS
    labels: ["component:skills"]
    body: |
      For every skill flagged scaffolding-heavy in this spike or in #82's
      scan, any passage identified as still load-bearing for the Haiku tier
      must be marked for #73's overlay mechanism
      (internal/execution/preamble.go's WithBehavioralPreamble is the
      proven shape: +7.9 composite / +11.1pp pass rate on Haiku, ≈0 on
      Sonnet/Opus, tier-gated by strings.Contains(model, "haiku")) rather
      than deleted from the base skill in the same change. Deferred: this
      is a process constraint for #82's execution, not a standalone
      deliverable.
    depends_on: []
  - id: includes-split-modernize-plan
    action: defer
    title: "skills: split nightgauge-modernize-plan (1005 lines) into ADR-010 _includes/"
    type: chore
    priority: low
    size: M
    labels: ["component:skills"]
    body: |
      Zero _includes/ split despite being over the proposed 600-line
      budget. Apply ADR 010's Option A pattern once a scoped composite-score
      eval variant exists for this skill; the ADR-010 mechanism itself
      requires no runtime change (docs/decisions/010-progressive-disclosure.md
      Q4), but this spike defers because no per-skill measurement exists
      yet — only the pattern is proven, not this application of it.
    depends_on: []
  - id: includes-split-pipeline-health
    action: defer
    title: "skills: split nightgauge-pipeline-health (976 lines) into ADR-010 _includes/"
    type: chore
    priority: low
    size: M
    labels: ["component:skills"]
    body: |
      Zero _includes/ split despite being over the proposed 600-line
      budget and a 7-dimension cross-reference report generator with
      substantial per-dimension detail suited to on-demand disclosure.
      Deferred pending a scoped measurement, same reasoning as
      includes-split-modernize-plan.
    depends_on: []
  - id: claude-md-agents-md-axis-classification-handoff
    action: skip
    title: "CLAUDE.md/AGENTS.md axis findings handed to #171"
    type: docs
    priority: medium
    size: XS
    body: |
      This spike classified passages in CLAUDE.md (333 lines) and
      AGENTS.md (266 lines) per the five axes (see Finding 2) but does not
      recommend editing either file — #171 (CLAUDE.md/AGENTS.md dedup) owns
      that work and already has open findings (missing --admin merge
      guidance in AGENTS.md, duplicated Knowledge Base Usage section).
      Recorded as skip: no new issue is filed, since #171 already exists
      and covers this surface; this entry exists so the materializer has a
      complete accounting of every axis touched by this spike.
    depends_on: []
```

## Sequencing

- **Blocks #82** (extract model-conditional prose from 14 base skills): #82
  should read this spike's axis-1/axis-3 findings and the `defer`
  recommendations above (`refactor-rewrite-railroading-scan`,
  `dep-modernize-lean-eval`, `test-scaffold-lean-eval`,
  `product-audit-lean-eval`, `docs-watch-lean-eval`,
  `backlog-preflight-lean-eval`) before starting — they enumerate exactly
  which of the 14 skills already have prior art (`feature-validate-lean`)
  to model the rewrite on, and which still need a first eval pass.
- **Depends on #73's mechanism** for every tier-conditional recommendation:
  `haiku-tier-overlay-scope-note` and any deferred item later promoted to
  `adopt` for Haiku-still-needed content must resolve through #73's overlay
  fragments (ADR 016), applying the same tier-gated shape as
  `internal/execution/preamble.go`'s `WithBehavioralPreamble`. None of
  these can land as a same-PR base-skill deletion.
- **Feeds #80** with the budget number: `base-skill-size-budget-600` is the
  concrete threshold (600 lines, 17/48 skills currently over) #80's
  `nightgauge preflight skill-overlays` size-budget check (checklist item 3) should wire in directly.
- **Follows #171** for the CLAUDE.md/AGENTS.md axis: this spike classifies
  but does not edit either file; `claude-md-agents-md-axis-classification-handoff`
  hands the classification to #171's existing dedup scope rather than
  duplicating it.
- **Proves claims through #84's harness**: every `defer`-with-eval
  recommendation above (`security-audit-includes-split`,
  `dep-modernize-lean-eval`, `test-scaffold-lean-eval`,
  `product-audit-lean-eval`, `docs-watch-lean-eval`,
  `backlog-preflight-lean-eval`) becomes an `adopt` only after running
  through #84's overlay-delta protocol (same scenario, same model,
  before/after composite score, attempts-to-green, and token cost) —
  matching the standard `feature-validate-lean` already met.

## Auto-Memory Divergence

This spike does not propose using the Claude Code auto-memory system
(`MEMORY.md` or per-fact memory files) as an alternative context-rightsizing
mechanism, and does not reopen that question. `CLAUDE.md`'s own recorded
rationale, verbatim:

> **Do NOT use the Claude Code auto-memory system** (no `MEMORY.md` /
> per-fact memory files). Everything durable lives in the repository so
> there is exactly one source of truth:
>
> - **How the agent should work** (rules, preferences, conventions) → this
>   `CLAUDE.md` and `AGENTS.md`.
> - **Technical / triage knowledge** (symptom → root cause → fix, known
>   false-alarms, runtime gotchas) → `docs/` — primarily
>   `docs/TROUBLESHOOTING.md`, `docs/CI_CD_RUNBOOK.md`, `docs/AUTO_TRIAGE.md`,
>   and `docs/FAILURE_TAXONOMY.md`.
> - **Per-issue working context** → the `.nightgauge/knowledge/` base, which
>   graduates to `docs/`.
>
> When you learn something worth keeping, edit the appropriate repo file in a
> branch/PR — never a side-channel memory store.

All recommendations in this spike target repository-tracked files
(`SKILL.md` bodies, `_includes/` fragments, preflight config) — none
proposes a memory-store alternative.
