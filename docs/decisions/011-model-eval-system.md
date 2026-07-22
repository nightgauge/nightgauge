# Model Evaluation & Benchmarking System — data model, scoring, isolation, storage

**Date:** 2026-06-30
**Author:** nightgauge
**Status:** Decided (epic #4167)
**Issue:** #4168 — Eval system ADR + core data contracts
**Builds on:** the cross-model **skill**-eval harness (`packages/nightgauge-sdk/src/eval/`, #3814) and `AutoModelSelector` / `AutoProviderRouter` routing.

---

## Executive Summary

We are adding a **model**-evaluation system: run realistic SDLC tasks (UI
creation, UX, backend, testing, bugfix, refactor, docs) through the real
pipeline across a matrix of **model × effort × reasoning**, measure **cost,
latency, attempts-to-green, correctness, and subjective quality**, and chart the
trade-offs so model-routing and new-release decisions are evidence-based.

This ADR fixes the contracts everything else in the epic depends on: the data
model, the scoring methodology (including the LLM-judge reliability guard), run
isolation, and where results are stored.

---

## Context

The existing harness (`src/eval/schemas.ts`) evaluates **skills** with a binary
pass/fail assertion engine on synthetic prompts, and a CI gate
(`.github/workflows/skill-eval.yml`) depends on its `baseline.jsonl`. It does
not measure cost, latency, or quality, and its scenarios are not realistic
tasks. Separately, `ModelPerformanceAnalyzer` aggregates passive live-run
telemetry but never runs a controlled "same task, many models" comparison.

We need the controlled comparison and a quality axis, without disturbing the
working skill-eval gate.

---

## Decisions

### 1. Two parallel eval lanes — do not mutate skill-eval

Skill-eval (`schemas.ts`, binary pass/fail) is a **live CI gate** and stays
untouched. Model-eval gets its own contracts in
`packages/nightgauge-sdk/src/eval/modelEvalSchemas.ts`, reusing shared
primitives (`ModelTierSchema`, `PIPELINE_SKILLS`, `EvalVerdictSchema`,
`EvalModeSchema`) from `schemas.ts`. Two lanes, one set of shared primitives.

### 2. Data model (the contracts this issue ships)

- **`ModelDescriptor`** — provider-neutral identity + economics: `provider`,
  optional tier alias, `concrete_version`, **token rates**
  (input/output/cache-read/cache-creation), supported **effort** and
  **reasoning** levels, context window. The single shape the S2 registry
  populates and from which cost is computed.
- **`EvalTask`** — a realistic task: instruction, `job_class`, target
  stage(s), difficulty, a **fixture reference** (how to materialize seed repo
  state), deterministic **check commands**, and a **rubric**.
- **`EvalMatrixCell`** — one `{model_id, effort, reasoning}` combination.
- **`ModelEvalCellResult`** — one task×cell outcome: token usage, `cost_usd`,
  `latency_ms`, `attempts_to_green`, deterministic `gate_results`, and an
  optional composite `EvalScore`.
- **`EvalScore`** — composite 0–100 + per-`QualityDimension` breakdown +
  deterministic correctness component + judge-confidence flag.
- **`EvalRun`** — a suite run: the matrix, a **snapshot of the
  `ModelDescriptor`s used** (so historical cost stays interpretable when the
  registry changes), all cells, and a summary including `total_cost_usd`.

The **wire-facing subset** (what the platform persists) is represented in
`@nightgauge/shared-types` by **S8 (#1158)**; these SDK schemas are the
source the shared-types mirror. Records are designed so the existing
`ModelPerformanceAnalyzer` aggregation patterns apply.

### 3. Scoring methodology

A cell's composite score (0–100) is a weighted blend of three components, with
**weights per job class** (UI tasks weight visual quality higher; refactors
weight correctness/tests higher):

1. **Correctness** — deterministic gate results (build/test/lint/typecheck),
   mapped to points. Reuses the pipeline's post-condition gates.
2. **Automated metrics** — penalties relative to a baseline for
   attempts-to-green, latency, and cost (efficiency, not just correctness).
3. **Judge quality** — an LLM judge scores subjective dimensions (UI/UX,
   idiomatic code, clarity) against the task rubric, emitting per-dimension
   scores + rationale (reusing the `JudgeVerdict` shape).

A **deterministic-only** mode (no judge) still produces a correctness score so
CI can run without LLM cost.

### 4. LLM-judge reliability guard (the genuinely uncertain part)

Subjective scoring is only trustworthy if it is **stable**. The judge is run
**N times** (default 3) on a sampled cell; if the score's standard deviation
exceeds a configured threshold (default **10** points), the cell's quality is
flagged `low_confidence` rather than trusted silently. S5 (#4173) implements the
guard and ships a test proving it fires on an intentionally inconsistent judge.
Threshold and N are configurable.

### 5. Run isolation — reuse, don't reinvent

Each matrix cell executes in an **isolated git worktree**
(`.nightgauge/worktrees/`, via `internal/execution/worktree.go`) reset to
the task's seed state, through the existing **`StageRunner` / adapter**
abstraction. No new isolation mechanism.

### 6. Storage — local JSONL + platform

Results are written **locally** as JSONL (reusing the `EvalRecorder`
write/baseline-diff pattern) so the system works offline and in CI, **and**
emitted to the **platform** (`POST /v1/analytics/evals`, S8) for the dashboard.
Cost is encoded as USD in the SDK; the platform converts to integer
micro-dollars per its existing convention.

### 7. Provider-neutral, Anthropic first

Execution goes through the adapter layer / `AutoProviderRouter`; model identity
and economics come from `ModelDescriptor`. The registry (S2) is seeded with
Claude Code models (Opus 4.8, Sonnet 5, Sonnet 4.6, Haiku 4.5, Fable 5) and at
least one non-Anthropic placeholder to prove a new provider is a data entry, not
a code change.

### 8. Effort and reasoning axes

`effort` reuses `ClaudeEffort` (`low|medium|high`). `reasoning` is a **new
provider-neutral budget axis** (`none|low|medium|high`) — today effort is
derived but not injected into the adapter spawn; S4 (#4171) wires both axes so
they actually change model behavior.

---

## Consequences

- S3/S4/S5/S6/S7 build against fixed shapes; S8 mirrors the wire subset into
  shared-types; S9/S10/S11 chart it; S12 feeds it back into routing.
- Adding a model is one `ModelDescriptor` entry (S2). Adding a task is one
  `EvalTask` (S3).
- Skill-eval remains a separate, untouched gate.

## Alternatives considered

- **Extend skill-eval's `EvalCellResult` in place** — rejected: risks the live
  CI gate and conflates binary contract checks with graded quality.
- **Store only on the platform** — rejected: breaks offline/CI use; local JSONL
  - platform mirror keeps both.
- **Score quality with deterministic heuristics only** — rejected: misses
  subjective UI/UX quality, which is a primary reason customers pick a model.
  The judge + reliability guard is the compromise.
