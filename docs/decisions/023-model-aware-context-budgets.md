# Model-Aware Context Budgets — a fit check before dispatch, not after overflow

**Date:** 2026-09-21
**Author:** nightgauge
**Status:** Decided
**Issue:** #1645 (epic #1610, Wave 9, Path C)
**Amends:** [ADR-010](010-progressive-disclosure.md) (a second, window-triggered
disclosure axis alongside the existing model-tier axis) and
[ADR-016](016-model-aware-skill-overlays.md) (documents that overlay
composition happens before, not after, any future compact trim)
**Extends:** [ADR-022](022-opencode-multi-provider-adapter.md) (§ 5 provider
derivation, § 7-§8 `compaction.reserved`) and
[ADR-020](020-value-adding-features-default-on.md) (the fit check is default-on,
as an instance of that rule, not an exception to it)
**Observed against:** OpenCode 1.18.30 (harness baseline ~7.4k tokens, measured
input tokens on step 1 of a run)

---

## Executive Summary

Nothing checked whether a stage's rendered prompt fit the context window of
the model that would run it. `STAGE_EXPECTED_TOKENS` in the SDK's
`AutoProviderRouter` was a routing _weight_, not a budget; the Go renderer
never saw a window at all. A stage that outgrew its model's context either
overflowed at the provider (an `AI_APICallError`, parked under the
pre-existing `TerminalKindContextWindowExceeded`) or, worse, silently lost
instructions to truncation with no error at all.

This ADR adds:

1. `internal/skillrender/budget.go` — pure `Estimate`/`Share`/`Fit` functions
   over already-rendered content.
2. `nightgauge skill render --context-window N [--json]` — an opt-in fit
   verdict, byte-identical to today when the flag is unset.
3. A dispatch-time fit check in the Go scheduler: one bounded re-route hop,
   then a refusal classified into the terminal kind #1631 already declared and
   parked for this exact recovery.
4. An `AutoProviderRouter` fix: `opencode`'s context-window sub-score reads
   the resolved dispatch model's window (via a caller-supplied
   `opencode_context_window`, or a registry lookup of `opencode_model`)
   instead of a static 32k placeholder.

**Scope note.** This PR is the ADR plus one fit function and its two dispatch
consumers — it does not build a "compact render profile." Where the discovery
process below found a decision that implies future code (a compact profile,
bounded sub-sessions, non-USD budgets, capacity-aware sizing), this ADR
records the decision slot and defers the code, exactly like the issue's own
scope boundary for #1651/#1652/#1655.

## Context

### Prior art confirmed by reading the code, not assumed from the issue text

- `TerminalKindContextWindowExceeded = "context_window_exceeded"` already
  existed at `internal/orchestrator/failure_handler.go` (added by #1631,
  already in the parked set — no retry, no lifetime-failure increment, no
  cascade feed), with a comment naming this issue as its first caller. This
  ADR's implementation does not invent the terminal kind; it writes the first
  `refusePreDispatch` call site that classifies a refusal into it.
- `internal/orchestrator/scheduler_predispatch_refusal_test.go` already
  existed (617 lines, table-driven `TestXxxRefusal_RecordsTheReasonUnderTheCurrentStage`
  convention) — extended with new cases, not recreated.
- `refusePreDispatch`'s own return `kind` is unconditionally
  `TerminalKindValidationError` for every existing call site
  (`"prerequisite-preflight"`, `"skill-render"`). The context-budget call
  site is the first to override that return, into
  `TerminalKindContextWindowExceeded`, using its own `"context-budget"`
  source string so traces stay distinguishable across all three refusal
  sources.
- `resolveDispatchModel` (`internal/orchestrator/scheduler.go`) always returns
  a registry **band** (`"sonnet"`, …), never a concrete id or a window — every
  ladder in that package depends on that vocabulary. The fit check needed one
  more resolution step to reach a concrete `ModelDescriptor.ContextWindow`.
- `skillrender.Render`'s `OverlayKeys` already resolves that
  `ModelDescriptor` for overlay composition. Rather than re-deriving the same
  resolution a second time in the scheduler (risking the two disagreeing),
  `skillrender.Result` gained a `ContextWindow int` field, populated from the
  SAME descriptor `OverlayKeys` already resolved. The scheduler reads
  `skillData.ContextWindow` straight off the render result.
- `estimateTokens` (`internal/intelligence/routing/router.go`) is a _different_
  concern: it predicts a token count **before** a render exists, for
  routing/cost purposes. `budget.Estimate` measures the **actual rendered**
  content **after** render. The two are not merged.
- `compaction.reserved` is an OpenCode-native config key
  (`internal/execution/adapters/opencode_config.go`, ADR-022 §7-§8), not a
  Nightgauge-wide config surface. This ADR's reserve table
  (`internal/skillrender/budget.go`) is the general, adapter-agnostic answer;
  OpenCode's own `compaction.reserved` mechanics are referenced, not
  reimplemented.
- Local model descriptors (`ResolveLocal`, `internal/models/local.go`, #1633)
  are discovered by the Go process at dispatch time from the endpoint the
  machine-tier `opencode:` config declares, and are **never written to the
  registry** either language reads. This is why the SDK router cannot resolve
  a local Qwen fixture's window through `resolveModelForAdapter` — that
  function only ever sees the static registry file. See Q10 below.

## Decision

### Q1 — Budget share per stage

`internal/skillrender/budget.go`'s `Share(stage)` normalizes the issue's own
measured base-only render table (bytes/4) against the heaviest stage,
pr-merge:

| Stage            | Measured tokens (issue #1645) | Share |
| ---------------- | ----------------------------: | ----: |
| issue-pickup     |                        16,500 |  0.76 |
| feature-planning |                        12,300 |  0.57 |
| feature-dev      |                        11,600 |  0.53 |
| feature-validate |                        12,600 |  0.58 |
| pr-create        |                        11,200 |  0.52 |
| pr-merge         |                        21,700 |  1.00 |

A stage with no entry (`issue-refine`, or any future stage never added to the
table) gets a floor of **0.35** — no measurement is not zero share, but it is
also no basis for a bigger one.

`Share` is a fraction of the **usable window**, not the raw window. Fixed
reserves come off the top first, in this order, before a stage's own share is
computed: a harness baseline (7,400 tokens — OpenCode's own measured system
prompt plus tools; conservative and adapter-agnostic, since `Fit` takes no
adapter parameter and every dispatch path shares one reservation model), a
tool-schema reserve (3,000 tokens — a conservative constant standing in for
"measured per adapter at doctor time" until a dispatch consumer shows the
generic figure over- or under-reserves), and a compaction/output reserve
(4,096 tokens, the adapter's reply-budget convention generally, and directly
comparable to OpenCode's own `compaction.reserved` on that dispatch path).
What remains after those three is further reduced by a fixed 15%, held back
for tool output. `budget(stage, window) = usableWindow(window) × Share(stage)`.

This table lives in ONE file (`internal/skillrender/budget.go`) so #1654–#1664
never edit it directly, per the issue's own scope note.

### Q2 — Estimation

`Estimate(content)` is `len(content)/4` (bytes/4) — the same methodology
behind the issue's own measured-token table, continuing the ADR-020-era
convention. A **+15% safety margin** covers the stated ±15% tokenizer spread:
`Fit` compares `estimate × 1.15` against the budget, never the raw estimate,
and the margin scales the estimate up rather than the budget down so the
arithmetic stays in one place and one direction.

### Q3 — Dispatch outcome order

Implemented in this PR: **(1) one re-route hop → (2) refusal.** The hop is a
model swap, not a compact re-render (see Q5) — `nextContextBudgetReroute`
(`internal/orchestrator/context_budget.go`) looks for the largest-window,
non-deprecated model on the **same provider** that is not the model already
rejected. Provider-scoped rather than adapter-scoped: the fit check runs
after the model has already resolved to a provider, and staying within it
needs no new auth, no new adapter check, and no per-adapter pricing surprise
— only a bigger window on a model the dispatch can already reach.

- No larger-window alternative exists (the failing model is already the
  provider's biggest, the provider has one model, or the provider is local
  with no catalog to search) → refuse immediately, zero re-render attempts.
- A larger-window alternative exists → re-render against it and re-check
  `Fit`. Fits → dispatch on the re-routed model. Still does not fit → refuse.
  **Never a second hop, and never a retry of the original (stage, model)
  pair** — the AC5 bound.

Refusal calls `refusePreDispatch(item, runtime, workspaceRoot, stage, tracer,
"context-budget", reason)`, with `reason` naming the stage, estimated tokens,
budget, window and share, and the returned kind overridden to
`TerminalKindContextWindowExceeded` at this call site (§ Context above).

**Deferred to a later issue, decision slot only:** compact render profile (Q5
below), bounded sub-sessions (#1651), non-USD budgets (#1652), and
capacity-aware sizing (#1655) are none of them re-route mechanisms this PR
implements. A compact re-render is a real candidate for a future,
cheaper-than-model-swap first hop — recorded here as a slot in the ORDER
(before the model-swap hop), not as code: the current renderer has no density
axis to switch on (Q5), and building one is a separate, larger change than
"the ADR plus one fit function and its two dispatch consumers" this issue
scoped itself to.

**Amendment (#1651, #1662): the compact hop is wired.** The scheduler now
takes the order this section reserved: (1) the full render when it fits;
(2) otherwise, when the stage has a `_profiles/compact.md`, the compact
render when it fits (`skillrender.DecideProfile`), which is then the render
dispatched; (3) otherwise the model-swap hop above; (4) otherwise refusal,
whose reason says the compact render did not fit either. The profile
dispatched is logged and recorded as `skill_profile` on the stage-start
trace event. A model swapped in by (3) is dispatched on its full render.

### Q4 — Unknown window

Fail-open, consistent with `OverlayKeys`' own documented contract for an
unknown model or an unresolved local provider (render.go: "no registry
entries by design … documented fail-open behavior"): `Fit` short-circuits to
`{Fits: true}` whenever `window <= 0`, so a hosted model absent from the
registry or a local descriptor `models.Resolve` cannot produce dispatches
unchecked, exactly as it did before this issue. The branch taken is logged at
the scheduler's call site (`"context budget: unknown-window branch"`) so a
trace can tell "checked and passed" from "not checked" — the one thing that
actually changes for this population.

**Amendment (#1651).** A local model's window is no longer unknown when its
OpenCode endpoint is declared: the scheduler reads the `limit.context` the
dispatch's OpenCode config is built with (Q10), so local dispatches are
checked. The fail-open branch remains for a hosted model absent from the
registry and for a local endpoint whose limit does not resolve.

### Q5 — Compact render profile vs. ADR-010 and ADR-016

**Decision, not implemented in this PR.** A `compact` profile would be a
render axis **orthogonal to overlays**: ADR-016's overlay cascade
(`OverlayKeys`) is purely additive and keyed by provider→id, with no existing
axis for prompt density. Composition order, if built, is fixed: expand
includes → apply overlays → apply compact trim — never a parallel skill
source tree, preserving ADR-016's single-procedural-source-of-truth goal.
Elements that must always survive compaction: phase markers, artifact
contracts, gates, the Completion Checklist, and deny rules. Content
eligible for trimming: verbose rationale and comments in the base SKILL.md,
never `_includes` structural content or overlay fragments.

Building this amends ADR-010 (a second, window-triggered disclosure axis
alongside the existing model-tier axis) and ADR-016 (overlay composition
happens before compact trimming, not after) — recorded here so the amendment
is decided before the code exists, not improvised alongside it.

### Q6 — When compact is selected

**Decision, not implemented in this PR** (no code exists to select, per Q5).
When built: the threshold is the same one `Fit` already computes — estimated
tokens at the 1.15x margin exceeding the stage's normal-profile share — with
no operator override. ADR-020's default-on rule means a correctness gate that
prevents silent truncation has neither of the rule's two valid opt-out
reasons (repo footprint, per-run token cost), so a future compact fallback
would be default-ON, the same way the fit check itself is. This reconciles
with ADR-020 by being an instance of it, not an exception.

### Q7 — Sub-session policy (#1651)

Out of scope for implementation here, per the issue's own boundary. Decision
slot: windows below roughly 2x a stage's normal share are candidates for
per-plan-step sub-sessions once #1651 lands. Open questions #1651 owns: do
steps hand off through the working tree or through commits (feature-dev does
not commit today, AGENTS.md #1608), and how sub-sessions relate to in-session
compaction. No code ships for this in the current PR.

**Amendment (#1651): the policy as implemented.** The slot above named no
number, so #1651 fixed one. feature-dev runs as sub-sessions when the
dispatch model's resolved window (the same `skillData.ContextWindow` the fit
check reads, Q10) is known and below **200,000 tokens**. That covers the
131,072-token local models the compaction loop was observed on, and keeps
every 200k-and-larger hosted window, and every unknown window (Q4's
fail-open), on the single session. The hard cap is **12 sessions** per
feature-dev dispatch, whatever the plan's task count. The two open questions:
steps hand off through the **working tree**, not commits (feature-dev still
does not commit), with the scheduler writing a git-derived `dev-{N}.json`
(`handoff_source: derived`, `step`) after each step. Each step is a **fresh
session**, never a resume, so it does not rely on in-session compaction at
all. The session's prompt is the stable rendered-skill prefix first and the
step text last. The scheduler only splits the stage when its runner delivers
the scheduler's prompt: the IPC runner, where the extension composes the
prompt, keeps the single session. A step is a top-level, unfenced checkbox in
the plan's implementation section (or, with no such section, in any section
but acceptance criteria and checklists); the plan is re-read before every
step, so a task an earlier session already checked is never dispatched. The
bound is the unchecked-step count the stage started with, never more than
12; spending it with steps still unchecked fails the stage as
`dev_step_cap_reached`, and a retry resumes from the next unchecked step. The
sessions share the stage's timeout and cost ceiling. Operators opt out with
`pipeline.feature_dev_sub_sessions: false` or
`NIGHTGAUGE_FEATURE_DEV_SUB_SESSIONS=false`; the default is on, as ADR-020
requires of a correctness feature with no footprint or cost reason to be off.
Code: `internal/orchestrator/featuredev_steps.go`.

**Correction (#1651, AC7 run).** The registry lists no local model, so for
a model on a local OpenCode endpoint `skillData.ContextWindow` was 0 and the
policy never engaged. For an `opencode` dispatch whose model names an
endpoint the machine-tier `opencode:` block declares, the window is now the
`limit.context` the run's OpenCode config is built with: the declared value,
clamped to the loaded window, else the window discovered from the server
(`adapters.OpenCodeContextWindow`). The fit check reads the same value, so
at a small window feature-dev also gets the compact render (Q3 amendment),
and each sub-session's prompt starts with that compact render.

### Q8 — Non-USD budgets (#1652)

A context-window refusal is a ceiling this ADR enforces; turn, wall-clock and
token ceilings are a distinct mechanism, and this ADR originally shipped no
code for it and set no defaults.

**Amendment (#1652).** The mechanism is `pipeline.stage_budgets`, keyed by
`default` or a stage name, each entry holding `max_turns`, `max_wall_clock`
and `max_tokens`. The Go executor enforces it on the stage's stream while the
stage runs (`internal/execution/stage_budget.go`); the extension-hosted runner
does not yet. Decided:

- **Defaults.** 400 turns, 4h wall clock, 25,000,000 tokens; a zero-cost stage
  gets 200 turns. Each sits above what a normal hosted stage uses, so a
  default only ever stops a runaway: the largest recorded claude stage took
  287 turns; 4h is the largest stage timeout routing assigns (the OpenCode
  local cap), and the wall-clock deadline is min(stage timeout, the budget),
  so no stage gets less time than today; 25M is above any recorded hosted
  stage's input, output and cache-write tokens. 200 turns keeps the steps cap
  OpenCode stages already ran under (ADR-022). No per-stage default: stage
  timeouts already vary per stage, and the wall clock composes with them.
- **Tokens counted.** Input, output (reasoning included) and cache writes.
  Cache reads are left out: a hosted stage re-reads its cached context every
  turn, which its USD cap prices, and a local server reports its whole prompt
  as input.
- **Inheritance.** 0 or absent inherits (stage → `default` → built-in), and
  a refused or invalid value falls through the same way. Unlimited is only an
  explicit `-1`, logged on every dispatch; a lifted `max_turns` leaves the
  adapter's own default cap (200) in place.
- **Native cap.** The turn budget replaces the `--max-turns 200` the
  claude-CLI adapters and grok passed and OpenCode's 200 steps, so a hosted
  stage's native cap rises to 400.
- **Zero-cost rule.** A stage on a zero-cost provider, a model server the
  operator runs (including an OpenCode endpoint the machine tier declares as
  `lm-studio` or `ollama`) or a model the registry prices at $0, has no USD
  cap that can stop it, so it always gets non-zero ceilings: `-1` is refused
  there with a warning. A hosted model the registry cannot price is not
  zero-cost, which would give it the local turn default, but no USD cap binds
  it either, so its `-1` is refused as well (fail closed).
- **Turns.** Passed as the adapter's native cap where one exists and counted
  on the stream for every adapter with a turn boundary; the stage is stopped
  when its last allowed turn asks for another. For OpenCode the stream count
  is the enforcement, because its steps cap is not a hard stop (ADR-022,
  #1811).
- **Breach.** SIGTERM to the process group, SIGKILL after 10s, a check once
  the stage is reaped that no member is left, and the stamp
  `stage_budget_exceeded:<turns|wall_clock|tokens>` with the observed value
  and the limit. The existing budget-enforcer terminal rule classifies it as
  `budget_exceeded`, and the scheduler does not retry a `budget_exceeded`
  stage.

The operator reference is `docs/GUARDRAILS_AND_BUDGETS.md` § Per-stage non-USD
budgets.

### Q9 — Capacity-aware sizing (#1655)

Out of scope for implementation here. `internal/skillrender/budget.go` is
named as the file a future window → maximum-issue-size table would extend.
No code ships for this in the current PR.

**Amendment (#1655): the capacity table.** `capacityTable` in
`internal/skillrender/budget.go`, read through `MaxIssueSizeForWindow`
(and `sizeGate.MaxSizeForWindow`), maps a known window to the largest issue
size it admits: below 32,000 tokens XS; 32,000 S; 128,000 M; 200,000 L;
400,000 XL. Three points read it: `nightgauge size-gate check
--context-window | --adapter --model`, `nightgauge size-gate capacity`
(issue-create's Phase 2.85 scope gate), and the scheduler, which checks each
stage against the window of the model that stage resolved to (this ADR's
Q10 order) before the fit check, so a run is held to its smallest-window
stage model. An over-capacity dispatch is refused as
`context_window_exceeded` with a recovery of decompose, or re-routed under
`size_gate.routes.reject_action: soft-route` to the first
`capacity_fallback_models` entry that admits the size. Decomposition is one
level deep: an issue carrying `<!-- nightgauge:capacity-decomposed -->` that
is still over capacity requires human decomposition. An unknown window or
size applies no cap, as Q4.

### Q10 — Window source precedence

In order:

1. **Skill render CLI only:** an explicit `--context-window N` flag always
   wins — a caller-supplied test/operator value overrides everything else.
2. **The resolved `ModelDescriptor.ContextWindow`** from the registry
   (`models.Resolve`, via the SAME descriptor `OverlayKeys` already resolved
   for overlay composition — never re-derived a second time).
3. **Unknown → fail-open**, per Q4.

For the SDK router's `opencode` sub-score specifically (`AutoProviderRouter.ts`,
`scoreContextWindow`), the precedence is narrower because the SDK cannot
repeat Go-side local discovery (§ Context, `ResolveLocal` is Go-only and
writes nothing to either registry file):

1. `AutoRouterContext.opencode_context_window`, when the caller already
   queried the Go authority (`nightgauge opencode config --json`'s
   `limit.context`) — the only path that can see a local LM Studio/Ollama
   descriptor, including the local Qwen fixture's 131,072-token window
   (`internal/models/testdata/local-discovery/lmstudio-api-v0-models.json`).
2. A registry lookup of `AutoRouterContext.opencode_model` (an ADR-022
   `<provider>/<id>` dispatch string, split via `parseOpenCodeModel` before
   the lookup) — resolves a concrete hosted id, e.g. `anthropic/<id>`.
3. The static `ADAPTER_CONTEXT_WINDOW_TOKENS.opencode` placeholder (32,000) —
   unchanged fallback for "truly unknown."

`ADAPTER_CONTEXT_WINDOW_TOKENS` stays exactly as before for every other
adapter (AC4's "single-provider adapters keep the static table"): a router
choosing _which_ adapter to use cannot yet have resolved a per-model
descriptor for candidates it has not picked.

**Amendment (#1651): the local-endpoint rung.** In the scheduler, between 2
and 3: for an `opencode` dispatch whose model names an endpoint the
machine-tier `opencode:` block declares, the window is the `limit.context`
the run's OpenCode config is built with (`adapters.OpenCodeContextWindow`):
the declared value, clamped to the window the server has loaded, else the
window discovered from the server.

### Q11 — Relation to #80

Distinct, linked gates, neither a blocker of the other. #80
(`preflight skill-overlays`) bounds an individual overlay _fragment's_ byte
budget at compose time. This issue bounds the _whole rendered stage prompt_
against the _dispatch model's_ window, at render/dispatch time.

## Implementation

- `internal/skillrender/budget.go` (new) — `Estimate`, `Share`, `Fit`,
  `FitResult`, and the reserve constants and per-stage table from Q1.
- `internal/skillrender/budget_test.go` (new) — the two golden cases from the
  issue's Verification section (pr-merge fails at a 32,768-token window,
  passes at 262,144), plus unit coverage for the safety margin and the
  unknown-window fail-open branch.
- `internal/skillrender/render.go` — `Result` gained `ContextWindow int`,
  populated from the same `OverlayKeys` descriptor already resolved for
  overlay composition (Q10 § "no re-derivation").
- `cmd/nightgauge/skill.go` — `--context-window N` on `skill render`. `0`
  (default/unset) skips the `Fit` call path entirely, keeping the no-flag
  render byte-identical to before this issue. `> 0` prints the verdict
  (stderr on the plain path, a `budget` envelope field with `--json`) and
  exits non-zero when the render is over budget, via the existing
  `verdictExit` contract (the command has already written its output; only
  the exit code is carried).
- `internal/orchestrator/context_budget.go` (new) — `nextContextBudgetReroute`,
  the Q3 re-route hop.
- `internal/orchestrator/scheduler.go` — the fit check, re-route attempt, and
  refusal, wired into the existing dispatch sequence right after the skill
  composes (§ Context, Prior art).
- `internal/orchestrator/scheduler_predispatch_refusal_test.go` — extended
  with the context-budget refusal cases: no-larger-model immediate refusal,
  re-route-still-fails refusal after exactly one hop, a successful re-route
  that dispatches, and the unknown-window fail-open branch.
- `packages/nightgauge-sdk/src/analysis/AutoProviderRouter.ts` — the `opencode`
  window fix in `scoreContextWindow` / `resolveOpenCodeContextWindow`, and
  `AutoRouterContext` gained `opencode_model` and `opencode_context_window`
  (`auto-router-types.ts`).
- `packages/nightgauge-sdk/tests/analysis/AutoProviderRouter.test.ts` —
  extended with the resolved-window scoring cases from AC4.

## Consequences

- A stage that cannot fit is caught before spawn, with a named reason
  (stage, estimated tokens, budget, window, share), instead of failing
  minutes later on a provider overflow or silently truncating instructions.
- The re-route mechanism is a real, working code path (a same-provider
  model swap), not a documented-only decision — but it is deliberately
  narrower than the issue text's original "compact profile, then downgrade
  ladder" sketch, because no compact-render mechanism exists yet to be the
  first hop, and repurposing the sticky cost-downgrade ladder (designed to
  move to a CHEAPER model on unavailability, not a BIGGER-window one) would
  have been an unobserved, untested repurposing of a mechanism built for a
  different failure. A same-provider "pick the biggest available window"
  hop is a mechanism this PR built, tested, and can defend on its own terms.
- `docs/decisions/README.md` gains this ADR's index row.
- Follow-on issues (#1651, #1652, #1655) have a named landing spot
  (`internal/skillrender/budget.go`) instead of an ad hoc one.

## Implementation Tracking

- [x] `internal/skillrender/budget.go` + tests
- [x] `nightgauge skill render --context-window`
- [x] Scheduler dispatch-time fit check, re-route, refusal
- [x] `AutoProviderRouter` opencode window fix + tests
- [x] `docs/decisions/README.md` index row
- [x] #1651 bounded sub-sessions (policy recorded in the Q7 amendment)
- [ ] #1652 non-USD budgets (decision slot only, tracked separately)
- [x] #1655 capacity-aware sizing (the Q9 amendment)
- [x] A `compact` render profile (#1654: `internal/skillrender`'s Profile
      option, `--profile compact`, `DecideProfile`, and pr-merge's own
      `_profiles/compact.md` as the first consumer. `DecideProfile` is the
      compact half of this ADR's Q3/Q5 order — full fits, else compact if
      available and it fits, else refuse. Wiring it into the scheduler ahead
      of the existing model-swap hop is not part of #1654 and stays
      `internal/orchestrator`'s own, out of this issue's file ownership.)
