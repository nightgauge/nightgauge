# Skill Determinism Audit

> **Spike output for #3053.** Read-only audit of every `skills/*/SKILL.md` against the Deterministic vs Probabilistic principle in [docs/ARCHITECTURE.md](ARCHITECTURE.md#deterministic-vs-probabilistic-architecture). No skill files are modified by this spike — remediation lands as one follow-up issue per appendix row, gated on review of this doc.

**Audit SHA**: `de41a288` (run 2026-04-27). To refresh: re-walk the four passes described in [Methodology](#methodology) below against the current `main`.

---

## Summary

| Metric                                         | Value           |
| ---------------------------------------------- | --------------- |
| Skills audited                                 | **38**          |
| Skill prose lines audited                      | **36331**       |
| Total phase/step units classified              | ~310            |
| Borderline findings                            | **128**         |
| `adopt` findings (recommended for remediation) | **64**          |
| `defer` findings (watch list)                  | **39**          |
| `skip` findings (judgment-required)            | **25**          |
| Proposed binary verbs (deduplicated by reach)  | **22**          |
| Cumulative follow-up effort (rough)            | ~6 S, 13 M, 1 L |

**The audit doc records the actual skill count (38) explicitly — the issue body says "all 40 skills" but the canonical count under `skills/*/SKILL.md` is 38** (35 `nightgauge-*` skills plus `pr-preflight`, `smart-setup`, `update-docs`).

### Top-3 Priorities

> Top-3 reflects pipeline-stage tier first, then reach (number of skills benefiting), then cumulative line savings.

1. **`nightgauge issue route`** — pipeline-stage, reach 2 (`issue-pickup`, `feature-planning`). Consolidates the label/board-field mapping prose that was the canonical drift case behind #3051 and #3052/#3057. ~150 lines of fixed-table prose currently re-derived in two pipeline phases. **Effort: M. Token-cost savings: high.** ✅ landed in #3062 — proof consumer: `issue-pickup` Step 3.2.5; `feature-planning` Phase 2 migration deferred under the B4 banner.
2. **`nightgauge scan ecosystem` (+ `scan deps`, `scan debt`)** — user-invocable, reach 4 (`health-check`, `security-audit`, `refactor-rewrite`, `dep-modernize`). The single largest verbatim-duplicated phase across the four codebase-assessment skills (~50–80 lines each). **Effort: M. Token-cost savings: high.**
3. **`nightgauge pipeline aggregate`** — user-invocable, reach 4 (`pipeline-audit`, `pipeline-health`, `retro`, `continuous-improvement`). Consolidates ~300 lines of inline Python that re-derives the same per-stage metrics from `.nightgauge/pipeline/history/`. **Effort: M. Token-cost savings: high.**

> **Token-cost savings tiers are rough** (low/medium/high based on call frequency × prose length). Real numbers come from outcome data after each follow-up lands and the optimizer measures the delta — see [docs/SELF_IMPROVEMENT_LOOP.md](SELF_IMPROVEMENT_LOOP.md).

### Counts by Tier

| Tier             | Skills | `adopt` | `defer` | `skip` |
| ---------------- | ------ | ------- | ------- | ------ |
| pipeline-stage   | 6      | 13      | 2       | 2      |
| user-invocable   | 31     | 51      | 36      | 22     |
| internal-utility | 1      | 0       | 1       | 1      |

---

## Methodology

### Classification Rubric

> Reviewers can argue with this rubric — it's deliberately recorded in the audit doc itself.

| Class           | Definition                                                                                                                                                                                             |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `deterministic` | Already calls a Go binary subcommand, hook, or shell script with a fixed signature; or describes a single GraphQL/REST call with no judgment.                                                          |
| `probabilistic` | Requires reading code, summarizing, judging requirements, generating prose, or making a creative choice.                                                                                               |
| `borderline`    | Currently described in prose but has a fixed signature (label→field lookup, "after X run Y", verification checklist, sequenced `gh` commands without branching). These are the candidates for `adopt`. |

### Recommendation Rubric (borderline rows only)

| Recommendation              | When to apply                                                                                                                                                                                                                               |
| --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `adopt: <move to binary>`   | Clear deterministic win — name the proposed binary verb (or name an existing verb the skill should call instead). Every adopt row must name a fixed input/output. If naming the signature requires hedging, downgrade to `defer` or `skip`. |
| `defer: <reason>`           | Borderline today, not worth moving yet (low reach, edge cases, in-flight refactor).                                                                                                                                                         |
| `skip: <judgment-required>` | Looks mechanical at first glance but actually requires judgment in practice. Document the judgment so future audits don't flip it.                                                                                                          |

### Drift Signals Hunted For

- "remember to / don't forget / after X run Y" sequencing prose where Y has a fixed signature
- Prose parsing of fixed formats (YAML, JSON, label syntax)
- Field/label mapping prose ("if priority label is `P1`, set field to `High`")
- Sequenced `git`/`gh` commands with no branching logic
- Verification checklists at phase end ("verify Status === Ready")
- Multiple skills duplicating the same procedural sequence
- Skill prose that re-implements an existing `nightgauge` Go binary verb

### Tiering

- **pipeline-stage**: the six pipeline skills (`issue-pickup`, `feature-planning`, `feature-dev`, `feature-validate`, `pr-create`, `pr-merge`).
- **user-invocable**: every other `disable-model-invocation: true` skill that is exposed via `/nightgauge:*` slash commands.
- **internal-utility**: `pattern-mining` (subagent-only, never user-invoked).

### Four-Pass Audit (executed in `feature-dev` for #3053)

1. **Inventory pass** — list every `skills/*/SKILL.md`, capture frontmatter (`name`, `version`, `disable-model-invocation`), tier each.
2. **Per-skill classification pass** — read each SKILL.md, walk numbered phases, classify each step, flag borderlines with `path:line` citations, assign `adopt` / `defer` / `skip`.
3. **Cross-cutting consolidation pass** — group `adopt` recommendations into proposed Go binary commands, deduplicate, rank by reach.
4. **Risk + summary pass** — write the risk register, the top-of-doc summary, and the top-3 priority list.

---

## Pipeline-Stage Skills (6)

Findings are ordered by tier (pipeline-stage first), then by skill name within tier.

### nightgauge-issue-pickup (1199 lines, 14 phases)

Counts: deterministic 8, probabilistic 2, borderline 4.

| #   | Phase                                                | Lines      | Recommendation | Proposed mechanism                                                                                                                                             | Why                                                                                                                                                                                           |
| --- | ---------------------------------------------------- | ---------- | -------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Phase 2.7 — Size Gate Preflight (Read Configuration) | L222-L239  | **adopt**      | Extend `size-gate check` to read its enabled flag from config (or new `config get pipeline.size_gate.enabled`)                                                 | Same `grep+awk` YAML parse is duplicated for `baseline_ci_gate` (L309-L323).                                                                                                                  |
| 2   | Phase 3 — Issue Analysis / Routing                   | L450-L595  | **adopt**      | New `nightgauge issue route --issue N --json` returning `{change_type, task_type, complexity_score, suggested_route, skip_stages, foundation_task, rationale}` | ~145 lines of fixed-enum mapping (label→priority, size→Fibonacci, type→change_type, foundation-task title regex). Same JSON later written by Phase 8.4 — shell + LLM doing the mapping twice. |
| 3   | Phase 5 — Branch Creation prefix + slug              | L669-L688  | **adopt**      | Extend existing `nightgauge git branch-create` to accept `--issue N` and derive prefix+slug from issue labels+title                                            | Verb already exists; pushing the prefix table and slug regex inside it removes the only remaining shell prose in this phase.                                                                  |
| 4   | Phase 8.3 — Knowledge Scaffolding (config flag read) | L864-L1042 | **adopt**      | Existing `nightgauge knowledge scaffold` — extend to honor `knowledge.enabled` / `knowledge.workspace_scoped` config flags itself                              | Action is already a binary verb; embedding a Node ESM heredoc and two python YAML readers just to gate it on config flags is borderline drift the binary should own.                          |

### nightgauge-feature-planning (1185 lines, 10 phases)

Counts: deterministic 4, probabilistic 4, borderline 2.

| #   | Phase                                        | Lines     | Recommendation | Proposed mechanism                                                                                                  | Why                                                                                                        |
| --- | -------------------------------------------- | --------- | -------------- | ------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| 5   | Phase 2 — Assess Complexity                  | L346-L372 | **adopt**      | Fold into `nightgauge issue route` (Finding #2) — return `documentation_scope` + `complexity_score` alongside route | Identical inputs to issue-pickup's routing prose, identical output schema. Same labels parsed twice today. |
| 6   | Phase 3.5.1 — Cross-Repo Knowledge Detection | L592-L701 | **adopt**      | Extend `nightgauge knowledge index` with `--cross-repo --workspace --limit 20 --json` flags                         | Pure file walk with hard cap and fixed namespaces — zero judgment. Removes ~110 lines of embedded python.  |

### nightgauge-feature-dev (1658 lines, 18 phases)

Counts: deterministic 7, probabilistic 8, borderline 3.

| #   | Phase                                                          | Lines                  | Recommendation | Proposed mechanism                                                                                                                                         | Why                                                                                                                                                                                                    |
| --- | -------------------------------------------------------------- | ---------------------- | -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 7   | Phase 4.1.5 + 6.3 + 6.4 — Build / Format / CI-parity detection | L728-L775, L1206-L1273 | **adopt**      | New `nightgauge build run --json` (and/or `format run`, `ci-parity check`) detecting toolchain and reporting `{ran, status, commands, timestamp}`          | Three separate language-detection cascades (`go.mod`/`package.json`/`pubspec.yaml`) repeated three times in this one skill. Output is the exact `build_verification` JSON written into `dev-{N}.json`. |
| 8   | Phase 4.5 + 4b — E2E Test Generation/Detection                 | L804-L1062             | **adopt**      | New `nightgauge e2e detect --files <json> --json` returning `{has_ui_changes, framework, runner_command, test_dir}` + `nightgauge e2e run --framework <f>` | Two parallel/duplicated phase blocks — the same UI-pattern regex appears at L821 and L922; the same framework cascade at L839-L845 and L946-L957. Textbook drift.                                      |
| —   | Phase 6.5 — Feedback Signal Evaluation                         | L1282-L1474            | **skip**       | n/a                                                                                                                                                        | Output schema is deterministic but trigger conditions explicitly require agent judgment — the spec itself uses "agent uses judgment" in the comments.                                                  |

### nightgauge-feature-validate (1884 lines, 22 phases)

Counts: deterministic 13, probabilistic 5, borderline 4.

| #   | Phase                                         | Lines       | Recommendation | Proposed mechanism                                      | Why                                                                                                                                       |
| --- | --------------------------------------------- | ----------- | -------------- | ------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| 9   | Phase 0.6 — AC Completion Check (`type:docs`) | L260-L317   | **adopt**      | New `nightgauge issue ac-check <number> --json`         | Pure prose-parsing of fixed Markdown checkbox format with deterministic gating. No `ac-check` verb exists today.                          |
| —   | Phase 1.5 — Build minimum-duration check      | L552-L597   | **defer**      | Future `nightgauge build-duration check`                | Borderline mechanical (lookup + threshold compare) but tightly coupled to inline build-duration measurement.                              |
| 10  | Phase 2.5 — CI Parity / Discover CI Commands  | L1042-L1079 | **adopt**      | New `nightgauge ci discover-commands --workflow <path>` | Pure deterministic YAML parse + filter. Currently re-implemented twice in this skill.                                                     |
| —   | Phase 2.5.3 — CI Parity Auto-Fix Loop         | L1112-L1158 | **skip**       | n/a                                                     | Failure-type classification is deterministic, but the actual fix step requires reading error output and writing code — judgment-required. |

### nightgauge-pr-create (1229 lines, 14 phases)

Counts: deterministic 11, probabilistic 1, borderline 2.

| #   | Phase                               | Lines     | Recommendation | Proposed mechanism                                                      | Why                                                                                                                  |
| --- | ----------------------------------- | --------- | -------------- | ----------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| 11  | Phase 0.5 — Auto-Merge Guard        | L86-L141  | **adopt**      | New `nightgauge repo check-auto-merge --owner --repo` (gate-style verb) | Already calls a binary verb; the gating logic itself (jq filter + exit) matches the existing `*-gate check` pattern. |
| 12  | Phase 1.7 — Build Knowledge Section | L370-L416 | **adopt**      | New `nightgauge knowledge render-pr-section --issue N`                  | Pure deterministic mapping — fixed dictionary lookup + Markdown emission. ~45 lines of bash → 1 binary call.         |

### nightgauge-pr-merge (1574 lines, 14 phases)

Counts: deterministic 10, probabilistic 2, borderline 2.

| #   | Phase                                          | Lines       | Recommendation | Proposed mechanism                                            | Why                                                                                                                                        |
| --- | ---------------------------------------------- | ----------- | -------------- | ------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| 13  | Phase 6 / Step 6.0 — Ruleset Pre-Check         | L528-L597   | **adopt**      | New `nightgauge pr ruleset-precheck PR_NUMBER --auto-satisfy` | Single REST call + structured filter for two known rule types + idempotent reviewer-add + bounded poll loop. ~70 lines of bash → one verb. |
| —   | Phase 7.7 — Record Outcome to Complexity Model | L1154-L1209 | **defer**      | Future `outcome record-from-context` overload                 | Verb already exists; only the input-marshaling prose remains. Adding a context-aware overload is low-value polish.                         |

---

## User-Invocable Skills (31)

### Issue & Project Management (9)

#### nightgauge-issue-create (875 lines, 11 phases)

Counts: deterministic 5, probabilistic 3, borderline 5. **Source of the original drift case behind #3051 and #3052/#3057.**

| #   | Phase                                                    | Lines     | Recommendation | Proposed mechanism                                                                                                               |
| --- | -------------------------------------------------------- | --------- | -------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| 14  | Phase 2 — Size Prediction                                | L182-L228 | **adopt**      | `nightgauge size predict <issue-number>`                                                                                         |
| 15  | Phase 2.6 — Lightweight Dependency Analysis              | L290-L314 | **adopt**      | `nightgauge epic plan-waves --sub-issues <list> --json`                                                                          |
| 16  | Phase 3 — Sub-issue body annotations (Wave + Depends on) | L455-L462 | **adopt**      | Extend existing `nightgauge issue create-sub --wave <N> --depends-on <list>`                                                     |
| —   | Phase 3.5 Step 1 — Cross-Epic Dependency Detection       | L508-L555 | **defer**      | Pattern list is open-ended; technical refs require judgment.                                                                     |
| 17  | Phase 3.5 Step 2 — Circular blocker guard                | L580-L592 | **adopt**      | Extend `nightgauge issue add-blocked-by` to reject when blocker == parent epic (server-side guard)                               |
| —   | Phase 4 — Label→Field mapping note                       | L661-L678 | **skip**       | Mapping is already inside `project add`'s `syncLabelsToFields` step. Prose is descriptive, not a re-implementation.              |
| —   | Phase 4.7 — Verification (mandatory)                     | L716-L751 | **skip**       | True verification is encoded in command exit codes per the skill's own statement; the extra `jq` re-call is belt-and-suspenders. |

#### nightgauge-issue-refine (519 lines, 7 phases)

Counts: deterministic 2, probabilistic 4, borderline 1.

| #   | Phase                            | Lines     | Recommendation | Proposed mechanism                                                                                             |
| --- | -------------------------------- | --------- | -------------- | -------------------------------------------------------------------------------------------------------------- |
| —   | Phase 2.1 — Issue type detection | L138-L162 | **defer**      | Borderline tiny; consolidate with `backlog-preflight` Phase 4 (Finding #19) under one `issue infer-type` verb. |

#### nightgauge-assess-epic (484 lines, 5 phases)

Counts: deterministic 4, probabilistic 0, borderline 1.

| #   | Phase                                                       | Lines     | Recommendation | Proposed mechanism                                                                         |
| --- | ----------------------------------------------------------- | --------- | -------------- | ------------------------------------------------------------------------------------------ |
| 18  | Phases 2 + 3 — Signal extraction & strategy decision matrix | L166-L321 | **adopt**      | `nightgauge epic assess <epic-number> --json` (alongside existing `epic check-completion`) |

#### nightgauge-epic-validate (227 lines, 8 phases)

Counts: deterministic 3, probabilistic 2, borderline 3.

| #          | Phase                                                | Lines     | Recommendation | Proposed mechanism                                                    |
| ---------- | ---------------------------------------------------- | --------- | -------------- | --------------------------------------------------------------------- |
| 19         | Phase 2 — Project board validation                   | L62-L76   | **adopt**      | `nightgauge epic validate <number> --json` (per-sub-issue gap report) |
| (17 cont.) | Phase 3 — Circular epic blocker fix                  | L96-L109  | **adopt**      | Same server-side guard as Finding #17 (consolidate).                  |
| —          | Phase 3 — Stale blockers / Missing dependencies scan | L111-L121 | **skip**       | "Missing dependency from prose" check requires keyword judgment.      |

#### nightgauge-spike-materialize (185 lines, 5 phases)

Counts: deterministic 5, probabilistic 0, borderline 0. **No findings — already deterministic.**

#### nightgauge-backlog-preflight (679 lines, 5 phases)

Counts: deterministic 4, probabilistic 1, borderline 2.

| #   | Phase                                                                                        | Lines     | Recommendation | Proposed mechanism                                                                         |
| --- | -------------------------------------------------------------------------------------------- | --------- | -------------- | ------------------------------------------------------------------------------------------ |
| 20  | Phase 2 (Checks 2.1-2.5) — Required labels / board fields / AC quality / cycles / greenfield | L195-L399 | **adopt**      | `nightgauge backlog preflight --status <s> --json`                                         |
| 21  | Phase 4 — Auto-fix infer type from title keywords                                            | L600-L628 | **adopt**      | `nightgauge issue infer-type <number> --apply` (also serves issue-refine borderline above) |

#### nightgauge-backlog-groom (222 lines, 5 phases)

Counts: deterministic 1, probabilistic 2, borderline 0. **No borderlines — mostly delegated to wrapper doc.**

#### nightgauge-project-sync (668 lines, 6 phases)

Counts: deterministic 3, probabilistic 1, borderline 2.

| #   | Phase                               | Lines         | Recommendation | Proposed mechanism                                                                |
| --- | ----------------------------------- | ------------- | -------------- | --------------------------------------------------------------------------------- |
| 22  | Phase 3 — Bulk sync execution loop  | L326-L374     | **adopt**      | Extend `nightgauge project add --bulk --milestone <m> --label <l> --json`         |
| 23  | Phase 3 — Date sync from milestones | L24-L29, L113 | **adopt**      | Extend `nightgauge project set-field <number> --start-date <d> --target-date <d>` |

#### nightgauge-queue (430 lines, 4 phases)

Counts: deterministic 4, probabilistic 0, borderline 1.

| #   | Phase                             | Lines     | Recommendation | Proposed mechanism                                                                |
| --- | --------------------------------- | --------- | -------------- | --------------------------------------------------------------------------------- |
| 24  | Phase 2.2 — Epic detection branch | L207-L224 | **adopt**      | `nightgauge queue add <number>` — auto-detect `type:epic` and dispatch internally |

### Audit & Health (5)

#### nightgauge-pipeline-audit (1306 lines, 7 phases)

Counts: deterministic 2, probabilistic 2, borderline 7.

| #   | Phase                                                                               | Lines      | Recommendation | Proposed mechanism                                                                                    |
| --- | ----------------------------------------------------------------------------------- | ---------- | -------------- | ----------------------------------------------------------------------------------------------------- |
| 25  | Phase 2.1 + 2.2 + 2.3 + Phase 3 — JSONL aggregation, jq fallbacks, computed metrics | L229-L716  | **adopt**      | `nightgauge pipeline aggregate --runs N --since DATE --issue N --include analysis --json`             |
| —   | Phase 1.2 — Data source location/priority                                           | L148-L191  | **defer**      | Trivial filesystem cascade; fold into the aggregate verb.                                             |
| —   | Phase 4 — Severity classification                                                   | L720-L762  | **skip**       | Threshold tables deterministic; finding narrative is judgment.                                        |
| —   | Phase 6 — Issue creation (dedupe + epic+sub-issues)                                 | L962-L1049 | **defer**      | Already uses existing binary verbs correctly; severity→priority mapping is a nice-to-have not urgent. |

#### nightgauge-pipeline-health (967 lines, 10 phases)

Counts: deterministic 2, probabilistic 2, borderline 6.

| #          | Phase                                              | Lines     | Recommendation | Proposed mechanism                                                               |
| ---------- | -------------------------------------------------- | --------- | -------------- | -------------------------------------------------------------------------------- |
| (25 cont.) | Phase 2.1 — JSONL history extraction               | L383-L492 | **adopt**      | Same `pipeline aggregate` verb (Finding #25); duplicate code.                    |
| 26         | Phase 2.2 — Health trends read                     | L494-L517 | **adopt**      | `nightgauge health trends --limit N` (mirrors SDK's `HealthTrendsWriter.read()`) |
| 27         | Phase 2.6 — Gate metrics aggregation               | L559-L580 | **adopt**      | `nightgauge gate metrics --json`                                                 |
| —          | Phase 2.3 / 2.5 — analysis/experiments aggregation | L519-L557 | **defer**      | Simple file aggregation; not yet load-bearing.                                   |
| —          | Phase 5 — Severity classification                  | L610-L648 | **skip**       | Same reasoning as audit Phase 4.                                                 |

#### nightgauge-product-audit (769 lines, 8 phases)

Counts: deterministic 1, probabilistic 5, borderline 4.

| #   | Phase                                          | Lines     | Recommendation | Proposed mechanism                                                                                                              |
| --- | ---------------------------------------------- | --------- | -------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| 28  | Phase 2 — Subagent 2 (Lifecycle drift)         | L290-L329 | **adopt**      | `nightgauge epic check-lifecycle` (alongside existing `epic check-completion`)                                                  |
| 29  | Phase 6 — Auto-fix safe findings (raw GraphQL) | L580-L616 | **adopt**      | Replace inline GraphQL with existing `project move-status`, `issue remove-blocked-by`, `issue close` verbs (`component:skill`). |
| —   | Phase 1 — Workspace discovery                  | L138-L201 | **defer**      | Lower priority than other consolidations.                                                                                       |
| —   | Phase 3 — Synthesis                            | L428-L474 | **defer**      | Math straightforward but couples to per-dimension JSON schema this skill defines.                                               |

#### nightgauge-retro (1677 lines, 11 phases)

Counts: deterministic 3, probabilistic 3, borderline 7.

| #          | Phase                                                  | Lines       | Recommendation | Proposed mechanism                                                                                                                    |
| ---------- | ------------------------------------------------------ | ----------- | -------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| 30         | Phase 2.1 — Batch failure extraction                   | L271-L332   | **adopt**      | `nightgauge pipeline batch-failures --issue N`                                                                                        |
| (25 cont.) | Phase 2.2 — Non-complete runs from history             | L334-L403   | **adopt**      | Same `pipeline aggregate` verb with `--filter outcome!=complete`.                                                                     |
| 31         | Phase 2.3 — Session-log failure regex scan             | L406-L484   | **adopt**      | `nightgauge logs scan-failures --since DATE --issue N` (consolidates with existing `scripts/retro/classifiers/failure_classifier.py`) |
| 32         | Phase 4 — Failure category classification              | L567-L617   | **adopt**      | **Already exists**: `nightgauge failure classify`. Skill is drift — should call the existing verb (`component:skill`).                |
| 33         | Phase 9 — Append outcome to decisions.md / outcomes.md | L1131-L1324 | **adopt**      | `nightgauge knowledge record-outcome --issue N --status STR --duration MIN --tokens N --cost USD`                                     |
| —          | Phase 2.4 — Incomplete pipelines                       | L486-L521   | **defer**      | Tiny logic; fold into batch-failures.                                                                                                 |
| —          | Phase 7 — Recommendation render                        | L805-L1002  | **defer**      | Templates fixed but action descriptions still need AI to fill in.                                                                     |

#### nightgauge-continuous-improvement (960 lines, 6 phases)

Counts: deterministic 1, probabilistic 4, borderline 4.

| #          | Phase                                                  | Lines     | Recommendation | Proposed mechanism                                                                                                |
| ---------- | ------------------------------------------------------ | --------- | -------------- | ----------------------------------------------------------------------------------------------------------------- |
| 34         | Phase 1.1-1.3 — yaml grep + mode auto-detect + sources | L178-L226 | **adopt**      | Replace grep+awk yaml parse with existing `nightgauge config show` (`component:skill`).                           |
| (25 cont.) | Phase 2 — Six signal groups read                       | L286-L364 | **adopt**      | Extend `pipeline aggregate` to a `signals` mode (third+ consumer).                                                |
| 35         | Phase 3 — Loop verdicts                                | L368-L519 | **adopt**      | `nightgauge intelligence loop-verdicts` (under existing `intelligence:*` verb family)                             |
| 36         | Phase 4 — Focus-aware proposal prioritization          | L555-L675 | **adopt**      | `nightgauge focus rank --proposals <file> --lens <name>` (lens keyword sets already in `internal/focus/focus.go`) |

### Codebase Assessment (4)

> **Cross-cutting finding**: Phase 0 ecosystem/monorepo detection is verbatim-duplicated across all four skills below — strongest reach finding in the audit.

#### nightgauge-health-check (1211 lines, 9 phases)

Counts: deterministic 6, probabilistic 2, borderline 5.

| #   | Phase                                                      | Lines     | Recommendation | Proposed mechanism                                                                                   |
| --- | ---------------------------------------------------------- | --------- | -------------- | ---------------------------------------------------------------------------------------------------- |
| 37  | Phase 0 (Steps 0.2 + 0.3) — Ecosystem & monorepo detection | L270-L378 | **adopt**      | `nightgauge scan ecosystem` (returns `{ecosystems[], is_monorepo, packages[], lockfile}`) — reach 4. |
| 38  | Phase 2 (Step 2.2) — Test/source file count                | L520-L556 | **adopt**      | `nightgauge scan tests` (counts only — score stays in prose).                                        |
| 39  | Phase 3 (Step 3.1) — Debt marker count                     | L593-L608 | **adopt**      | `nightgauge scan debt` (identical regex/excludes duplicated in `refactor-rewrite` L598-L608).        |
| —   | Phase 1 (Step 1.1) — Dependency audit                      | L382-L446 | **defer**      | Mechanically deterministic; consolidate with `dep-modernize` under future `scan deps`.               |
| —   | Phase 5 (Step 5.1) — CI/CD detection                       | L745-L799 | **defer**      | Single consumer today.                                                                               |

#### nightgauge-security-audit (1552 lines, 10 phases)

Counts: deterministic 7, probabilistic 3, borderline 4.

| #          | Phase                                                   | Lines      | Recommendation | Proposed mechanism                                                                    |
| ---------- | ------------------------------------------------------- | ---------- | -------------- | ------------------------------------------------------------------------------------- |
| (37 cont.) | Phase 0 (0.2 + 0.3)                                     | L309-L383  | **adopt**      | Same `scan ecosystem` (Finding #37).                                                  |
| 40         | Phase 2 (Step 2.2) — Secret pattern scan                | L562-L649  | **adopt**      | `nightgauge scan secrets` (six fixed regex passes + fixed false-positive filter list) |
| —          | Phase 0 (Step 0.4) — `.gitignore` coverage              | L385-L408  | **defer**      | Tiny scope, single consumer.                                                          |
| —          | Phases 3-7 — OWASP/Crypto/Input/Auth/Config grep passes | L671-L1108 | **defer**      | Deterministic but security-sensitive — externalizing requires versioned rule files.   |

#### nightgauge-refactor-rewrite (1495 lines, 10 phases)

Counts: deterministic 5, probabilistic 5, borderline 4.

| #          | Phase                                         | Lines     | Recommendation | Proposed mechanism                                                                               |
| ---------- | --------------------------------------------- | --------- | -------------- | ------------------------------------------------------------------------------------------------ |
| (37 cont.) | Phase 0 (0.2 + 0.3)                           | L421-L485 | **adopt**      | Same `scan ecosystem`.                                                                           |
| (39 cont.) | Phase 2 (2.1 + 2.2) — Linter/formatter + debt | L566-L609 | **adopt**      | Same `scan debt` + new `scan tooling` (Finding #39).                                             |
| —          | Phase 4 (4.1-4.3) — Coupling analysis         | L744-L824 | **skip**       | Skill itself flags as approximations; promoting to binary would lock in low-fidelity heuristics. |
| —          | Phase 6 (Step 6.1) — Git log analysis         | L928-L970 | **defer**      | Single consumer today.                                                                           |

#### nightgauge-dep-modernize (1284 lines, 8 phases)

Counts: deterministic 6, probabilistic 2, borderline 3.

| #          | Phase                                               | Lines     | Recommendation | Proposed mechanism                                                                                                                                 |
| ---------- | --------------------------------------------------- | --------- | -------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| (37 cont.) | Phase 0 (Step 0.2)                                  | L188-L283 | **adopt**      | Same `scan ecosystem`.                                                                                                                             |
| 41         | Phases 1-2 — Inventory + outdated/vuln analysis     | L356-L632 | **adopt**      | `nightgauge scan deps --include-vulns` (consolidates health-check + security-audit + dep-modernize).                                               |
| 42         | Phase 6 (Step 6.2) — Apply updates with branch + PR | L835-L897 | **adopt**      | Replace inline `git checkout -b` / `gh pr create` with existing `nightgauge git branch-create` + `nightgauge pr create` verbs (`component:skill`). |

### Modernize / Integration (5)

#### nightgauge-modernize-plan (1023 lines, 9 phases)

Counts: deterministic 5, probabilistic 2, borderline 2.

| #   | Phase                                                     | Lines     | Recommendation | Proposed mechanism                                                                                                                                   |
| --- | --------------------------------------------------------- | --------- | -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| 43  | Phase 1 / 2.1-2.4 — Aggregate findings JSON               | L160-L320 | **adopt**      | `nightgauge modernize aggregate-findings` (reads health-report + security-audit + test-scaffold JSONs, applies severity-normalization + dedup rules) |
| —   | Phases 3 / 6 — Build dependency graph + estimate timeline | L373-L482 | **defer**      | Deterministic but worth bundling with aggregate-findings rather than shipping standalone.                                                            |

#### nightgauge-integration-audit (270 lines, 9 phases)

Counts: deterministic 1, probabilistic 5, borderline 2.

| #   | Phase                                | Lines     | Recommendation | Proposed mechanism                                                                                      |
| --- | ------------------------------------ | --------- | -------------- | ------------------------------------------------------------------------------------------------------- |
| 44  | Phase 2 — Platform API Reality Check | L64-L108  | **adopt**      | `nightgauge integration probe-platform` (reads endpoint manifest, issues curls, emits categorized JSON) |
| —   | Phase 3 — Client API Call Extraction | L110-L144 | **defer**      | Fixed regex per language, but call-site classification is judgment-heavy once paths are abstracted.     |

#### nightgauge-pattern-mining (240 lines, 6 phases) — _internal-utility_

Counts: deterministic 5, probabilistic 0, borderline 1.

| #   | Phase                                                       | Lines     | Recommendation | Proposed mechanism                                                                                                                                |
| --- | ----------------------------------------------------------- | --------- | -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| —   | Phase 3 — "Group by signature similarity" + idiom detection | L141-L158 | **skip**       | Borderline only because grouping is vaguer than the rest. Tighten prose (specify exact grouping keys: return type + arg arity) but don't extract. |

#### nightgauge-release-watch (960 lines, 9 phases)

Counts: deterministic 4, probabilistic 2, borderline 3.

| #   | Phase                                                  | Lines     | Recommendation | Proposed mechanism                                                                                    |
| --- | ------------------------------------------------------ | --------- | -------------- | ----------------------------------------------------------------------------------------------------- |
| 45  | Phase 2 / 3 — Load last-seen + fetch + filter releases | L146-L232 | **adopt**      | `nightgauge release fetch --since <ver> --source <repo>` (semver compare, fixed schema)               |
| 46  | Phase 4 — Parse and classify changes                   | L236-L326 | **adopt**      | `nightgauge release classify-changes` (consumes fetched releases JSON, emits typed change array)      |
| —   | Phase 5 — Score pipeline relevance                     | L330-L597 | **defer**      | Algorithmically deterministic, but Phase 5.2 explicitly delegates ≥50 scores to model interpretation. |

#### nightgauge-docs-watch (631 lines, 9 phases)

Counts: deterministic 4, probabilistic 2, borderline 3.

| #   | Phase                                        | Lines     | Recommendation | Proposed mechanism                                                                                                                                          |
| --- | -------------------------------------------- | --------- | -------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 47  | Phase 1 / 2 / 3 — Fetch index, snapshot diff | L103-L187 | **adopt**      | `nightgauge docs snapshot-diff --source <url> --snapshot <path>` (already partially extracted into `scripts/snapshot-diff.sh` per L197 — promote to binary) |
| —   | Phase 5 — Categorize findings by relevance   | L210-L243 | **defer**      | Tiny lookup; bundle with snapshot-diff verb.                                                                                                                |
| —   | Phase 5.5 — Release correlation              | L247-L392 | **defer**      | Coupled to release-watch fetch/classify verbs landing first.                                                                                                |

### Documentation Skills (4)

#### nightgauge-doc-gen (726 lines, 7 phases)

Counts: deterministic 2, probabilistic 9, borderline 4.

| #   | Phase                                   | Lines     | Recommendation | Proposed mechanism                                                                                 |
| --- | --------------------------------------- | --------- | -------------- | -------------------------------------------------------------------------------------------------- |
| —   | Phase 2 — Public-API detection          | L177-L302 | **defer**      | Per-language AST extraction would need ts-morph/ast-grep — not in current binary scope.            |
| —   | Phase 3 — Signature drift compare       | L332-L367 | **defer**      | Same parser dependency.                                                                            |
| —   | Phase 4.4 — Verify syntax               | L479-L492 | **skip**       | Wrapping `tsc`/`py_compile`/`go build` adds little value over the three-line shell snippet.        |
| —   | Phase 6.1 — Write pipeline context file | L576-L602 | **defer**      | Cross-cutting concern shared with every pipeline skill; address as separate context-emission verb. |

#### nightgauge-docs-write (771 lines, 10 phases)

Counts: deterministic 1, probabilistic 6, borderline 3.

| #   | Phase                                                  | Lines     | Recommendation | Proposed mechanism                                                                                          |
| --- | ------------------------------------------------------ | --------- | -------------- | ----------------------------------------------------------------------------------------------------------- |
| 48  | Phase 1.5 — Architecture-pattern detection             | L223-L262 | **adopt**      | `nightgauge docs detect-patterns --files <glob>` (closed-set keyword table → list of slugs + matched files) |
| 49  | Phase 7 — Markdown link verification                   | L621-L670 | **landed**     | ✅ `nightgauge docs check-links` (#3064 — proof consumer migrated; also serves `update-docs` Phase 4.5)     |
| —   | Phase 6 — Verify-references (extract backtick symbols) | L573-L617 | **defer**      | Symbol extraction from prose is heuristic.                                                                  |

#### update-docs (1151 lines, 10 phases)

Counts: deterministic 1, probabilistic 5, borderline 4.

| #          | Phase                                      | Lines     | Recommendation | Proposed mechanism                                                                                                                                   |
| ---------- | ------------------------------------------ | --------- | -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| (49 cont.) | Phase 4.5 — Markdown link validation       | L326-L469 | **deferred**   | Verb landed in #3064 under the B6 banner; migration of this consumer deferred to a follow-up PR (same staged-adoption pattern as #3059/#3061/#3062). |
| 50         | Phase 4.6 — Cross-file version consistency | L204-L256 | **adopt**      | `nightgauge docs version-consistency`                                                                                                                |
| 51         | Phase 4.8 — Updated-date staleness         | L264-L312 | **adopt**      | `nightgauge docs check-freshness`                                                                                                                    |
| —          | Phase 4.8 — CLAUDE.md quality audit        | L541-L649 | **skip**       | "Self-evident phrase" set is judgment that drifts.                                                                                                   |

#### smart-setup (1612 lines, 7 phases)

Counts: deterministic 2, probabilistic 4, borderline 5.

| #   | Phase                                                       | Lines       | Recommendation | Proposed mechanism                                                                                                                     |
| --- | ----------------------------------------------------------- | ----------- | -------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| 52  | Phase 4.5 — Greenfield tooling scaffold                     | L960-L1263  | **adopt**      | `nightgauge setup scaffold-tooling --select <comma-list>` (~303 lines of pure heredoc emission with brownfield-safe `[ ! -f ]` guards) |
| 53  | Phase 5 (5.4-5.6) — Project-board field validation          | L1341-L1418 | **adopt**      | `nightgauge project ensure-fields --number <N>` (consolidates with `repo-init` Phase 4)                                                |
| 54  | Phase 5 (5.8) — Generate `config.yaml`                      | L1458-L1542 | **adopt**      | `nightgauge config init --project <N>` (consolidates with `repo-init` Phase 6)                                                         |
| —   | Phase 0.1 — Auto-detection of VCS / AI config / KB presence | L204-L249   | **defer**      | Trivial; consumed only by next phase.                                                                                                  |
| —   | Phase 6 — TODO file generation                              | L1546-L1604 | **skip**       | One grep + group-by-file too small to justify a verb.                                                                                  |

### Setup / Test / Misc (5)

#### nightgauge-test-gen (855 lines, 11 phases)

Counts: deterministic 5, probabilistic 5, borderline 1.

| #   | Phase                     | Lines     | Recommendation | Proposed mechanism                                               |
| --- | ------------------------- | --------- | -------------- | ---------------------------------------------------------------- |
| —   | Phase 2 — Source analysis | L122-L144 | **defer**      | Trivially small; only consumed by AI generation step downstream. |

#### nightgauge-test-scaffold (1065 lines, 8 phases)

Counts: deterministic 6, probabilistic 2, borderline 3.

| #   | Phase                               | Lines     | Recommendation | Proposed mechanism                                                       |
| --- | ----------------------------------- | --------- | -------------- | ------------------------------------------------------------------------ |
| 55  | Phase 1 — Existing test inventory   | L290-L356 | **adopt**      | `nightgauge test inventory --json`                                       |
| 56  | Phase 3 — Risk-based prioritization | L478-L602 | **adopt**      | `nightgauge test risk-score --files <list>`                              |
| —   | Phase 0 — Setup & detection         | L160-L228 | **defer**      | Should ride on `scan ecosystem` (Finding #37) when designed cross-skill. |

#### pr-preflight (656 lines, 9 phases)

Counts: deterministic 9, probabilistic 0, borderline 4.

| #   | Phase                               | Lines     | Recommendation | Proposed mechanism                                                                                       |
| --- | ----------------------------------- | --------- | -------------- | -------------------------------------------------------------------------------------------------------- |
| 57  | Check 1 — Broken links              | L41-L93   | **adopt**      | `nightgauge preflight links` ✅ landed in #3098                                                          |
| 58  | Check 2 + 3 — JSON / YAML syntax    | L102-L145 | **adopt**      | `nightgauge preflight syntax` ✅ landed in #3098                                                         |
| 59  | Check 5 — Sensitive data detection  | L194-L229 | **adopt**      | `nightgauge preflight secrets` ✅ landed in #3098                                                        |
| 60  | Check 9 — Skill version consistency | L322-L435 | **adopt**      | `nightgauge preflight skill-versions [--fix]` ✅ landed in #3098 (eliminates macOS-specific `sed -i ''`) |

#### nightgauge-repo-init (1747 lines, 13 phases)

Counts: deterministic 11, probabilistic 2, borderline 4.

| #          | Phase                              | Lines       | Recommendation | Proposed mechanism                                                                              |
| ---------- | ---------------------------------- | ----------- | -------------- | ----------------------------------------------------------------------------------------------- |
| 61         | Phase 1 — Project selection        | L232-L361   | **adopt**      | `nightgauge project resolve --number N` (returns `{number, owner, owner_type, id, title, url}`) |
| (53 cont.) | Phase 4 — Project field validation | L609-L837   | **adopt**      | Same `project ensure-fields` (Finding #53).                                                     |
| (54 cont.) | Phase 6 — Generate `config.yaml`   | L1049-L1262 | **adopt**      | Same `config init` (Finding #54).                                                               |
| —          | Phase 5 — Repository link          | L841-L858   | **defer**      | `gh project link` kept on purpose for now per `.claude/rules/scripts.md`.                       |

#### nightgauge-config-show (385 lines, 4 phases)

Counts: deterministic 4, probabilistic 0, borderline 1.

| #   | Phase                                 | Lines    | Recommendation | Proposed mechanism                                                                                                    |
| --- | ------------------------------------- | -------- | -------------- | --------------------------------------------------------------------------------------------------------------------- |
| 62  | Phases 1-3 — Locate / merge / display | L82-L223 | **adopt**      | `nightgauge config show [--section X] [--source Y] [--paths] [--json]` (collapses entire skill to single binary call) |

---

## Appendix: Proposed Binary Work (Reach-Ranked)

> Reach = number of skills currently re-implementing the proposed verb's logic. Effort tier (S/M/L) and token-cost-savings tier (low/medium/high) are rough — measured savings come from outcome data after each follow-up lands.

### Reach 4

| ID     | Proposed verb / change                                                                                                                                                                                 | Skills benefiting                                                                                          | Effort | Savings |
| ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------- | ------ | ------- |
| **B1** | `nightgauge scan ecosystem` ✅ landed in #3059 (proof consumer: `health-check` Phase 0.2/0.3; security-audit + refactor-rewrite + dep-modernize migrations deferred to follow-up under the B1 banner)  | health-check, security-audit, refactor-rewrite, dep-modernize (also: smart-setup, test-scaffold via defer) | M      | high    |
| **B2** | `nightgauge pipeline aggregate` ✅ landed in #3060 (proof consumer: `pipeline-audit` Phase 2.1; pipeline-health + retro + continuous-improvement migrations deferred to follow-up under the B2 banner) | pipeline-audit, pipeline-health, retro, continuous-improvement                                             | M      | high    |

### Reach 3

| ID     | Proposed verb                                                                                                                                                                             | Skills benefiting                           | Effort | Savings |
| ------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------- | ------ | ------- |
| **B3** | `nightgauge scan deps --include-vulns` ✅ landed in #3061 (proof consumer: `health-check` Phase 1.1; security-audit + dep-modernize migrations deferred to follow-up under the B3 banner) | health-check, security-audit, dep-modernize | S      | medium  |

### Reach 2

| ID      | Proposed verb / change                                                                                                                                                                                                                   | Skills benefiting                                      | Effort | Savings |
| ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------ | ------ | ------- |
| **B4**  | `nightgauge issue route` (label/board-field → route/complexity) ✅ landed in #3062 (proof consumer: `issue-pickup` Step 3.2.5; `feature-planning` Phase 2 migration deferred to follow-up under the B4 banner)                           | issue-pickup, feature-planning                         | M      | high    |
| **B5**  | `nightgauge scan debt` + `scan tests` + `scan tooling` ✅ landed in #3063 (proof consumer: `health-check` Phase 2.2/3.1/3.2; `refactor-rewrite` Phase 2.1/2.2 migration deferred to follow-up under the B5 banner)                       | health-check, refactor-rewrite                         | S      | medium  |
| **B6**  | `nightgauge docs check-links` ✅ landed in #3064 (proof consumer: `docs-write` Phase 7; `update-docs` Phase 4.5 migration deferred to follow-up under the B6 banner)                                                                     | docs-write, update-docs                                | M      | medium  |
| **B7**  | `nightgauge build run` + `format run` + `ci-parity check` + `ci discover-commands`                                                                                                                                                       | feature-dev, feature-validate                          | M      | medium  |
| **B8**  | `nightgauge e2e detect` + `e2e run`                                                                                                                                                                                                      | feature-dev (also referenced by feature-validate)      | M      | medium  |
| **B9**  | `nightgauge project ensure-fields` ✅ landed in #3067 (proof consumer: `repo-init` Phase 4; `smart-setup` Phase 5 migration deferred to follow-up under the B9 banner)                                                                   | repo-init, smart-setup                                 | M      | medium  |
| **B10** | `nightgauge config init` ✅ landed in #3068 (proof consumer: `repo-init` Phase 6; `smart-setup` Step 5.8 migration deferred to follow-up under the B10 banner — flat-shape rewrite needs interactive UX work)                            | repo-init, smart-setup                                 | S      | low     |
| **B11** | `nightgauge config show` ✅ landed in #3069 (proof consumer: `backlog-preflight`; richer source-attributed view remains in the `config-show` user-invocable skill)                                                                       | config-show, continuous-improvement, backlog-preflight | S      | medium  |
| **B12** | `nightgauge issue infer-type` ✅ landed in #3070 (proof consumer: `backlog-preflight` Phase 4; `issue-refine` Phase 2.1 migration deferred to follow-up under the B12 banner)                                                            | backlog-preflight, issue-refine                        | S      | low     |
| **B13** | `nightgauge epic validate` + `add-blocked-by --guard-parent` ✅ landed in #3071 (proof consumers: `epic-validate` Phase 3 now calls binary for circular/stale blocker detection; `issue-create` Phase 3.5 Step 2 relies on binary guard) | issue-create, epic-validate                            | M      | medium  |

### Reach 1 — Pipeline-Stage Skills (highest priority within reach 1)

| ID      | Proposed verb / change                                                                              | Skill            | Effort | Savings |
| ------- | --------------------------------------------------------------------------------------------------- | ---------------- | ------ | ------- |
| **B14** | `nightgauge issue ac-check` ✅ landed in #3072 (proof consumer: `feature-validate` Phase 0.6.2)     | feature-validate | S      | medium  |
| **B15** | `nightgauge pr ruleset-precheck`                                                                    | pr-merge         | M      | medium  |
| **B16** | `nightgauge repo check-auto-merge` ✅ landed in #3074 (proof consumer: `pr-create` Phase 0.5)       | pr-create        | S      | low     |
| **B17** | `nightgauge knowledge render-pr-section` ✅ landed in #3075 (proof consumer: `pr-create` Phase 1.7) | pr-create        | S      | low     |
| **B18** | Extend `nightgauge git branch-create --issue` (prefix+slug from labels) ✅ landed in #3076          | issue-pickup     | S      | low     |
| **B19** | Extend `nightgauge knowledge scaffold` to honor config flags                                        | issue-pickup     | S      | low     |
| **B20** | Extend `nightgauge knowledge index --cross-repo --workspace`                                        | feature-planning | S      | medium  |

### Reach 1 — User-Invocable

| ID      | Proposed verb / change                                                                                                                                                                                                                                            | Skill                  | Effort | Savings |
| ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------- | ------ | ------- |
| **B21** | `nightgauge epic assess`                                                                                                                                                                                                                                          | assess-epic            | M      | medium  |
| **B22** | `nightgauge epic check-lifecycle`                                                                                                                                                                                                                                 | product-audit          | M      | medium  |
| **B23** | `nightgauge epic plan-waves` + `issue create-sub --wave --depends-on` + `size predict` ✅ landed in #3081; `plan-waves` now also deterministically serializes same-wave shared-target-file sub-issues (auto-injected `blockedBy`, populated `conflicts`) ✅ #4074 | issue-create           | M      | medium  |
| **B24** | `nightgauge queue add` epic auto-detect ✅ landed in #3082 (proof consumer: `queue` Phase 2.2 simplified to single binary call)                                                                                                                                   | queue                  | S      | low     |
| **B25** | `project add --bulk` + `project set-field --start-date --target-date` ✅ landed in #3083 (bulk add + date fields; project-sync SKILL.md updated to use binary)                                                                                                    | project-sync           | M      | medium  |
| **B26** | `nightgauge backlog preflight` ✅ landed in #3084 (implements Checks 2.1–2.5 from backlog-preflight Phase 2 as JSON output; skill amended to binary path with legacy shell fallback)                                                                              | backlog-preflight      | M      | low     |
| **B27** | `nightgauge health trends` + `gate metrics`                                                                                                                                                                                                                       | pipeline-health        | S      | low     |
| **B28** | `nightgauge intelligence loop-verdicts` + `focus rank` ✅ landed in #3086 (loop verdicts in `internal/intelligence/loopverdicts/`; focus rank in `internal/focus/rank.go`; continuous-improvement Phase 3 + Phase 4 amended with binary call + prose fallback)    | continuous-improvement | M      | medium  |
| **B29** | `nightgauge pipeline batch-failures` + `logs scan-failures` ✅ landed in #3087 (proof consumer: `retro` Phases 2.1–2.3 migrated in same PR)                                                                                                                       | retro                  | M      | medium  |
| **B30** | `nightgauge knowledge record-outcome`                                                                                                                                                                                                                             | retro                  | S      | low     |
| **B31** | `nightgauge modernize aggregate-findings`                                                                                                                                                                                                                         | modernize-plan         | M      | medium  |
| **B32** | `nightgauge integration probe-platform` ✅ landed in #3090 (probes platform API, emits 6-category JSON report; integration-audit Phase 2 amended to use binary with curl fallback)                                                                                | integration-audit      | M      | medium  |
| **B33** | `nightgauge release fetch` + `release classify-changes` ✅ landed in #3091 (proof consumer: `release-watch` Phases 2–4 migrated in same PR; classified output preserves byte-for-byte JSON shape)                                                                 | release-watch          | M      | medium  |
| **B34** | `nightgauge docs snapshot-diff`                                                                                                                                                                                                                                   | docs-watch             | S      | low     |
| **B35** | `nightgauge docs detect-patterns`                                                                                                                                                                                                                                 | docs-write             | S      | low     |
| **B36** | `nightgauge docs version-consistency` + `docs check-freshness`                                                                                                                                                                                                    | update-docs            | S      | medium  |
| **B37** | `nightgauge setup scaffold-tooling` ✅ landed in #3095 (proof consumer: `smart-setup` Phase 4.5 migrated in same PR; eliminates `node -e` devDep probes)                                                                                                          | smart-setup            | L      | medium  |
| **B38** | `nightgauge project resolve --number`                                                                                                                                                                                                                             | repo-init              | S      | low     |
| **B39** | `nightgauge test inventory` + `test risk-score` ✅ landed in [<!-- pr-number-placeholder -->](https://github.com/nightgauge/nightgauge/issues/<!-- pr-number-placeholder -->) (proof consumer: `test-scaffold` Phases 1 + 3 migrated in same PR)                  | test-scaffold          | M      | low     |
| **B40** | `nightgauge preflight links` + `preflight syntax` + `preflight secrets` + `preflight skill-versions` ✅ landed in #3098 (proof consumer: `pr-preflight` Checks 1, 2, 3, 5, 9 migrated in same PR; eliminates macOS-specific `sed -i ''`)                          | pr-preflight           | M      | medium  |
| **B41** | `nightgauge scan secrets` (six fixed regex passes + filters) ✅ landed in #3099 (proof consumer: `security-audit` Phase 2.2 migrated in the same PR)                                                                                                              | security-audit         | S      | medium  |

### Reach 1 — Skill-Drift Fixes (no new binary verbs)

These rows recommend amending the SKILL.md to call **existing** binary verbs instead of re-implementing the logic in prose. Component label: `component:skill`.

| ID      | Change                                                                                                                           | Skill                          |
| ------- | -------------------------------------------------------------------------------------------------------------------------------- | ------------------------------ |
| **B42** | Replace inline `git checkout -b` / `gh pr create` with existing `git branch-create` + `pr create` (Landed #3100)                 | dep-modernize Phase 6          |
| **B43** | Replace raw GraphQL mutations with existing `project move-status` / `issue remove-blocked-by` / `issue close` ✅ landed in #3101 | product-audit Phase 6          |
| **B44** | ✓ LANDED — Call existing `nightgauge failure classify` instead of describing the rules in prose ✅ landed in #3102               | retro Phase 4                  |
| **B45** | Replace yaml `grep+awk` mode auto-detect with `nightgauge config show` ✅ landed in #3103                                        | continuous-improvement Phase 1 |

---

## Risk Register

> Operations that look mechanical but require judgment in practice. Recorded so future audits don't flip them to `adopt` without arguing the judgment.

### Skip Findings (do not promote to binary)

| Skill            | Phase                                                     | Judgment that disqualifies                                                                                                                                                                     |
| ---------------- | --------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| feature-dev      | Phase 6.5 — Feedback signal evaluation                    | Triggers explicitly require agent judgment ("was a reasonable adaptation possible?", "did implementation require unplanned architectural changes?"). Output schema is fixed but inputs aren't. |
| feature-validate | Phase 2.5.3 — CI parity auto-fix loop                     | Failure-type classification is deterministic; the actual fix step requires reading error output and writing code.                                                                              |
| issue-create     | Phase 4 — Label→field mapping note                        | Mapping is already inside `project add`'s `syncLabelsToFields` step. Skill prose is descriptive, not a re-implementation. Trim if it drifts.                                                   |
| issue-create     | Phase 4.7 — Verification (mandatory)                      | True verification is encoded in command exit codes per the skill's own statement. The extra `jq` re-call is belt-and-suspenders.                                                               |
| epic-validate    | Phase 3 — Stale blockers / missing dependencies scan      | "Missing dependency from prose" check requires keyword judgment + cross-checking against `blockedBy`.                                                                                          |
| pattern-mining   | Phase 3 — Group by signature similarity                   | "Similarity grouping" has no fixed algorithm; tighten prose to specify exact grouping keys (return type + arg arity), but don't extract.                                                       |
| pipeline-audit   | Phase 4 — Severity classification                         | Threshold tables deterministic; the title/description/recommendation prose is genuinely probabilistic. If thresholds matter, expose as `severity_hint` in aggregate output.                    |
| pipeline-health  | Phase 5 — Severity classification                         | Same reasoning as audit Phase 4.                                                                                                                                                               |
| refactor-rewrite | Phase 4 — Coupling analysis                               | Skill itself flags the import-fan / cyclomatic checks as approximations (sample of 20-30, basename-substring). Promoting locks in low-fidelity heuristics.                                     |
| security-audit   | Phases 3-7 — OWASP/Crypto/Input/Auth/Config grep passes   | (defer, but watch) — externalizing requires versioned rule files; today's prose+regex is auditable in-place. Score genuinely needs judgment to filter false positives.                         |
| update-docs      | Phase 4.8 — CLAUDE.md quality audit                       | "Self-evident phrase" set is judgment that drifts.                                                                                                                                             |
| smart-setup      | Phase 6 — TODO file generation                            | One `grep` + group-by-file too small to justify a verb.                                                                                                                                        |
| doc-gen          | Phase 4.4 — Verify syntax (`tsc`/`py_compile`/`go build`) | Wrapping toolchain invocations adds little value over the three-line shell snippet.                                                                                                            |
| retro            | Phase 7 — Recommendation render                           | Templates fixed but action descriptions and impact prose still need AI to fill in.                                                                                                             |

### Watch List (deferred, revisit if reach grows)

- **`nightgauge integration extract-client-calls`** — fixed regex per language, but call-site classification is judgment-heavy once paths are abstracted into URL builders. Watch as more cross-repo skills emerge.
- **`nightgauge docs detect-relevance` (URL → bin)** — pure lookup table, but tiny. Bundle with `docs snapshot-diff` (B34) when scoping rather than promoting standalone.
- **`nightgauge setup detect-context`** — VCS / AI config / KB presence checks. Trivial today; revisit if a second skill needs the same flags.
- **`nightgauge docs scan-apis` / `docs signature-diff`** — would require multi-language AST parsers (ts-morph, ast-grep). Revisit when one language dominates the consuming skill mix.
- **`nightgauge modernize plan` (topo-sort + timeline math)** — bundle with `modernize aggregate-findings` (B31) before shipping.
- **`nightgauge release score`** — algorithm deterministic but Phase 5.2 explicitly delegates ≥50 scores to model interpretation. Revisit when the quick-pass / full-assessment split is firmer.
- **`nightgauge project link`** — `gh project link` kept on purpose per `.claude/rules/scripts.md`; track as future work.
- **Pipeline context-emission verb** — `dev-{N}.json` / `docgen-{N}.json` / similar are written from prose by every pipeline skill. Cross-cutting; deserves its own audit pass, not piecemeal additions.

---

## How To Refresh This Audit

1. Re-walk passes 1-4 in [Methodology](#methodology) at the new HEAD SHA.
2. Update the Audit SHA at the top, the counts table, and the per-skill `classification_counts`.
3. Re-rank the appendix by reach against the **current** Go binary surface — verbs that were proposed here may have landed in the meantime, in which case they become "skill drift to fix" rows (see B42-B45 pattern) rather than new-verb proposals.
4. Move appendix rows out of "watch list" when reach passes 2 or token-cost data demonstrates measurable savings.
