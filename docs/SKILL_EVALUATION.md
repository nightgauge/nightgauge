# Cross-Model Skill Evaluation Harness

> Run representative scenarios for a pipeline-stage skill against Haiku 4.5,
> Sonnet 4.6, and Opus 4.8 and report pass/fail per model, so regressions from a
> skill refactor or a model bump become detectable.

**Issue**: #3814

## Why this exists

Skills are portable `SKILL.md` instruction files the pipeline runs against a
model selected at spawn time — a **tier alias** (`haiku` / `sonnet` / `opus`)
passed to the `claude` CLI `--model` flag, which resolves the concrete version
itself (see the `model_version_resolution` note). Anthropic's guidance is that a
skill which works on Opus may need more explicit detail to work on Haiku. We
also actively change model routing (`AutoModelSelector`) and bump pinned model
versions. Before this harness there was no repeatable way to detect that a skill
which passed on Opus now fails on Haiku, nor to catch a regression introduced by
a skill refactor or a model bump.

The harness runs a small `scenario × model` matrix and reports a binary
pass/fail per cell. Results are persisted as JSONL so a later run can be diffed
against a baseline and regressions surfaced.

## Architecture

The harness lives in the SDK (`packages/nightgauge-sdk/src/eval/`) as a
pure, testable service, plus a thin standalone runner script
(`scripts/evaluate-skills.ts`) — mirroring the
`scripts/analyze-model-routing.ts` → SDK-service pattern. No Go binary or VSCode
changes.

```
evals/scenarios/<skill>/*.json   ─┐
                                  ├─► SkillEvalHarness.run()
evals/fixtures/<skill>/*.json    ─┘        │
                                           ▼
                              per-cell runScenario(scenario, model)
                              (MockModelRunner | LiveClaudeModelRunner)
                                           │
                                           ▼
                                 assertion engine (pure)
                                           │
                                           ▼
                            EvalRunReport ─► JSONL record + console matrix
                                           │
                                           ▼
                            EvalRecorder.diffAgainstBaseline(...)
```

| Component        | File                           | Responsibility                                          |
| ---------------- | ------------------------------ | ------------------------------------------------------- |
| Schemas          | `src/eval/schemas.ts`          | Zod schemas for scenarios, assertions, results, records |
| Assertion engine | `src/eval/assertions.ts`       | Pure `evaluateAssertions(output, assertions[])`         |
| Model runners    | `src/eval/modelRunner.ts`      | `MockModelRunner` + `LiveClaudeModelRunner`             |
| Orchestrator     | `src/eval/SkillEvalHarness.ts` | Iterates the matrix, aggregates an `EvalRunReport`      |
| Recorder + diff  | `src/eval/EvalRecorder.ts`     | JSONL write + `diffAgainstBaseline`                     |
| Loaders          | `src/eval/loader.ts`           | Load + validate scenarios and fixtures from disk        |
| Runner CLI       | `scripts/evaluate-skills.ts`   | `--skills/--models/--mode/--baseline`, prints matrix    |

The `ModelTier` type (`"haiku" | "sonnet" | "opus"`) is reused from
`src/analysis/AutoModelSelector.ts` — not redefined. A compile-time parity guard
in `schemas.ts` fails the build if the harness's runtime tier enum ever drifts
from that type.

## Scenario format

A scenario is a declarative JSON file at `evals/scenarios/<skill>/<name>.json`:

```json
{
  "id": "pc-body-flag",
  "skill": "pr-create",
  "description": "pr-create uses --body (with $(cat file)), not --body-file.",
  "failure_mode": "The Go binary `pr create` has no --body-file flag; using it aborts PR creation.",
  "prompt": "You are creating a PR with the Go binary and have the body in a temp file. Which flag do you pass the body with?",
  "assertions": [
    { "type": "contains", "value": "--body" },
    { "type": "not_contains", "value": "--body-file" }
  ],
  "models": ["haiku", "sonnet", "opus"]
}
```

| Field          | Required | Notes                                                                           |
| -------------- | -------- | ------------------------------------------------------------------------------- |
| `id`           | yes      | Kebab-case, **unique across all skills** (fixtures key off it).                 |
| `skill`        | yes      | One of the six pipeline skills; must match the directory it lives in.           |
| `description`  | yes      | One-line human description.                                                     |
| `failure_mode` | yes      | The known failure this scenario guards against — write this from a real gotcha. |
| `prompt`       | yes      | The scenario input given to the skill/model.                                    |
| `assertions`   | yes      | ≥1 deterministic check; **all** must pass for the cell to pass.                 |
| `models`       | no       | Restrict this scenario to a tier subset; otherwise the run's tiers apply.       |

Scenarios should be **evaluation-driven**: each one targets a documented failure
mode (drawn from `SKILL.md` phase contracts and the project's recorded gotchas),
not generic "does it work" prompts.

## Assertion types

Assertions are intentionally **coarse** (contract-shape checks) so they tolerate
phrasing variation while still catching the documented failure mode. Prefer
key-presence / JSON-shape / substring checks over full-output equality.

| `type`             | Fields                  | Passes when                                                      |
| ------------------ | ----------------------- | ---------------------------------------------------------------- |
| `contains`         | `value`, `ignore_case?` | Output contains the substring.                                   |
| `not_contains`     | `value`, `ignore_case?` | Output does **not** contain the substring.                       |
| `matches_regex`    | `pattern`, `flags?`     | Output matches the JS regex (invalid regex → fail, not throw).   |
| `json_path_exists` | `path`                  | First balanced JSON in the output resolves the dot/bracket path. |
| `exit_code`        | `value`                 | Process exit code equals `value` (live mode / mock-supplied).    |

`json_path_exists` extracts the first balanced JSON object/array from the output
(tolerating prose or code fences around it) and supports `a.b.c` and
`a.b[0].c`. A resolved `null` counts as present.

## Mock vs. live mode

Two tiers, mirroring the `PLATFORM_TEST_URL` pattern from #2092:

- **mock** (default) — each cell's output comes from a fixture file
  `evals/fixtures/<skill>/<scenarioId>.json` with a `{ haiku, sonnet, opus }`
  shape (any subset). Deterministic, zero API cost. The only mode CI runs and
  what the harness's unit tests use. The shipped fixtures are authored so every
  scenario passes on every tier — the mock matrix is an all-green **baseline**;
  real cross-model divergence is observed only in live mode.

  ```json
  {
    "haiku": { "text": "...model output...", "exit_code": 0 },
    "sonnet": { "text": "...", "exit_code": 0 },
    "opus": { "text": "...", "exit_code": 0 }
  }
  ```

- **live** (`NIGHTGAUGE_SKILL_EVAL_LIVE=1`) — spawns
  `claude --print --model <tier>` and feeds the scenario prompt over stdin,
  matching `ClaudeHeadlessAdapter`'s invocation shape. Selecting `--mode live`
  without the env var is refused (guards against accidental API cost). Live mode
  uses ambient `claude` auth (`claude auth status`); **no API keys are read,
  stored, or logged**. Runs only in the optional, disabled-by-default
  `skill-eval-live` job (schedule/dispatch + repo var
  `ENABLE_SKILL_EVAL_LIVE=true`).

Because invocation is by **tier alias**, a concrete-version bump (Opus 4.8 →
4.9) is exactly the kind of change the harness is designed to catch as a
regression. The concrete version label (`MODEL_TIER_VERSION_LABELS`) is recorded
for interpretation only.

## Running the harness

```bash
# Mock mode (default): all skills, all tiers
npx tsx scripts/evaluate-skills.ts

# Restrict skills and/or tiers
npx tsx scripts/evaluate-skills.ts --skills feature-planning,pr-create --models haiku,opus

# Gate against a stored baseline (exit non-zero on any regression)
npx tsx scripts/evaluate-skills.ts --baseline .nightgauge/skill-evals/baseline.jsonl

# Live mode (opt-in)
NIGHTGAUGE_SKILL_EVAL_LIVE=1 npx tsx scripts/evaluate-skills.ts --mode live --skills pr-merge
```

The runner prints a `scenario × tier` matrix, writes a JSONL run record to
`.nightgauge/skill-evals/<skill-or-multi>-<timestamp>.jsonl` (gitignored),
and exits:

- `1` if any cell **regressed** versus `--baseline` (`pass → fail`/`error`);
- `1` if **no baseline** was supplied and any cell failed/errored;
- `0` otherwise.

## Regression detection

`EvalRecorder.diffAgainstBaseline(report, baseline)` compares two runs by
`(skill, scenario_id, model)`:

- **regression** — passed in the baseline, no longer passes;
- **fix** — failed/errored in the baseline, now passes;
- **added** — present in the report, absent from the baseline (never a
  regression — there is nothing to regress from).

A `fail → error` flip is **not** a regression (the cell was already failing).

To establish a baseline, run the harness and copy the JSONL record:

```bash
npx tsx scripts/evaluate-skills.ts
cp .nightgauge/skill-evals/multi-*.jsonl .nightgauge/skill-evals/baseline.jsonl
```

## Adding a scenario

1. Pick the failure mode — a concrete, documented way the skill can go wrong on
   a weaker model or after a refactor (e.g. "emits `--body-file`, which the Go
   binary rejects").
2. Write `evals/scenarios/<skill>/<name>.json` with a unique kebab-case `id`,
   the `failure_mode`, a `prompt`, and coarse `assertions` that catch the
   failure without over-fitting to phrasing.
3. Add the matching mock fixture `evals/fixtures/<skill>/<id>.json` with a
   good-output text per tier that satisfies the assertions (keep the mock
   baseline green).
4. Run `npx -w @nightgauge/sdk vitest run tests/eval/` —
   the end-to-end test asserts every shipped scenario passes in mock mode and
   that each pipeline skill has ≥3 scenarios.
5. Optionally validate with the runner: `npx tsx scripts/evaluate-skills.ts
--skills <skill>`.

## Scope (this PR)

- ≥3 scenarios for each of the six pipeline-stage skills (`issue-pickup`,
  `feature-planning`, `feature-dev`, `feature-validate`, `pr-create`,
  `pr-merge`).
- Mock mode + opt-in live mode; JSONL records + baseline regression diff.
- Mock-mode unit/integration tests; no live API calls in CI.

**Out of scope**: a VSCode UI; a Go binary subcommand; non-Claude adapters; and
statistical/quality scoring beyond binary pass/fail.

## CI gate (#4092)

`.github/workflows/skill-eval.yml` runs the harness as a **required status
check** on PRs that touch the eval surface (`evals/**`,
`packages/nightgauge-sdk/src/eval/**`, `scripts/evaluate-skills.ts`, the
committed baseline, or the workflow itself):

- **`skill-eval-baseline`** (PR/push, mock mode) — diffs against
  `.nightgauge/skill-evals/baseline.jsonl` and exits non-zero on any
  verdict regression. The runner **fails CLOSED** when the baseline is
  missing/empty, so the gate can never silently pass against a non-existent
  baseline. A negative self-test injects a wrong fixture answer and asserts the
  gate fires (mirrors the positive/negative discipline in `lint.yml`).
- **`skill-eval-live`** (schedule/dispatch) — optional cross-model LIVE run,
  shipped **disabled**; enable with repo variable `ENABLE_SKILL_EVAL_LIVE=true`.
  Never a required PR check (needs API budget + ambient auth).

**Regenerating the baseline** (intentionally, when scenarios/fixtures change):
run `npx tsx scripts/evaluate-skills.ts`, then copy the run record to
`.nightgauge/skill-evals/baseline.jsonl` (see "Establishing a baseline"
above) and commit it **in the same PR** as the scenario/fixture change — the
path filter re-runs the gate, and a fixture change without a matching baseline
update will regress the gate.

## Cross-adapter portability (#4029)

Skill **portability** across adapters is validated in two tiers, kept separate
because only one can run deterministically in CI:

1. **Deterministic gate (CI).** `nightgauge preflight skill-portability`
   (and its shell mirror `scripts/lint-skills/portability.sh`) fails the moment a
   skill embeds a hardcoded VSCode-extension binary path — the one mechanically-
   detectable portability regression. Wired into `.github/workflows/lint.yml`.
   This is the gate that guards the #4029 contract.
2. **Live multi-adapter eval (manual / opt-in).** This harness today spawns the
   Claude CLI only (`LiveClaudeModelRunner`). Validating behavioral parity of the
   six core skills against real Codex/Gemini/etc. binaries requires those CLIs
   installed **and authenticated**, so it cannot run in CI. The path when built:
   extend the live runner to accept `--adapter codex|gemini`, gate it behind
   `NIGHTGAUGE_SKILL_EVAL_LIVE=1`, and run
   `npx tsx scripts/evaluate-skills.ts --mode live --adapters claude,codex,gemini`.

See [SKILL_PORTABILITY.md](SKILL_PORTABILITY.md) for the full portability
contract (binary discovery, model tiers, phase markers, tool directives).
