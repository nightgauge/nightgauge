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

````json
{
  "id": "pc-body-flag",
  "skill": "pr-create",
  "description": "pr-create passes the body with --body (e.g. \"$(cat file)\"), not the unsupported --body-file.",
  "failure_mode": "The Go binary `pr create` has no --body-file flag; using it aborts PR creation.",
  "prompt": "… Which flag do you pass the body with? End your answer with the exact `pr create` command you would run, in a single ```bash fenced block.",
  "assertions": [
    {
      "type": "matches_regex",
      "pattern": "(?:^|\\s)--body(?:=|\\s)",
      "scope": "last_fenced_block",
      "lang": ["bash", "sh", "shell"],
      "strip_comments": true
    },
    {
      "type": "not_matches_regex",
      "pattern": "--body-file\\b",
      "scope": "last_fenced_block",
      "lang": ["bash", "sh", "shell"],
      "strip_comments": true
    }
  ],
  "models": ["haiku", "sonnet", "opus"]
}
````

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

| `type`              | Fields                         | Passes when                                                             |
| ------------------- | ------------------------------ | ----------------------------------------------------------------------- |
| `contains`          | `value`, `ignore_case?`, scope | The text contains the substring.                                        |
| `not_contains`      | `value`, `ignore_case?`, scope | The text does **not** contain the substring.                            |
| `matches_regex`     | `pattern`, `flags?`, scope     | The text matches the JS regex (invalid regex → fail, not throw).        |
| `not_matches_regex` | `pattern`, `flags?`, scope     | The text does **not** match the JS regex (invalid regex → fail).        |
| `json_path_exists`  | `path`, scope                  | The JSON resolves the dot/bracket path.                                 |
| `json_path_equals`  | `path`, `value`, scope         | The JSON path resolves to exactly `value` (a scalar; no type coercion). |
| `exit_code`         | `value`                        | Process exit code equals `value` (live mode / mock-supplied).           |

"scope" is the optional `scope`, `lang` and `strip_comments` fields:

- No `scope`: the text is the whole output, and the JSON is the first balanced
  JSON object/array in it (tolerating prose or code fences around it).
- `scope: "last_fenced_block"`: the text is the body of the **last** closed
  fenced code block (` ``` ` or `~~~`), or the last one whose info-string
  language is in `lang` (a string or list, case-insensitive). The JSON is that
  whole body, which must parse. `strip_comments: true` removes unquoted shell
  `#` comments first, so `gh pr merge --squash  # never --admin` is judged on
  what would run. `lang` and `strip_comments` require `scope`.
- **Fail closed:** when the scoped block is missing, unclosed, of the wrong
  language, or (for JSON assertions) not valid JSON, the assertion fails. That
  holds for `not_contains` and `not_matches_regex` too: a missing block never
  lets a negative assertion pass.

Paths support `a.b.c` and `a.b[0].c`; a resolved `null` counts as present.
Regex `flags` may use `dgimsuv`; sticky `y` is rejected by the schema, because
it would silently anchor a "matches anywhere" check at the start.

### Every scenario needs a positive assertion (#1267)

**A scenario built only from `not_contains` cannot go red**, and the suite
enforces that (see _The non-answer sentinel_ below). Beyond the mechanics, a
banned substring is usually the wrong instrument: it cannot tell a refusal from
a commitment. A `not_contains` on `"bisect"` written for the check-triage
never-green scenario passed the conforming answer only because that answer
happened to phrase its refusal as "I will **not** bisect" — the assertion could
not have distinguished that from "I will bisect".

What discriminates is the **correct statement being present**, not the wrong
word being absent. Assert that the run says the check is "not a regression";
keep `not_contains` for tokens that appear only when the model does the wrong
thing.

### Check the decision, not the prose

A prose `not_contains` also fails the opposite way: it cannot tell a
recommendation from a warning, so a **correct** answer that names the forbidden
thing in order to reject it goes red. Measured live on `pc-body-flag`, sonnet
answered "`--body "$PR_BODY"` — the Go binary has no `--body-file` flag …",
which is right, and failed `not_contains "--body-file"`. The mock fixtures had
been phrased around the defect ("an override flag", "the trunk") instead.
Detecting negation in free prose with a regex is not a fix: a guard of that kind
was tried and passed wrong answers such as "Instead of --body use --body-file."
and "No, add --admin to get past the pending check".

So a scenario that forbids a behaviour asks for the decision in a
machine-checkable form and checks only that:

- **Command scenarios** end the prompt with "End your answer with the exact
  command(s) you would run, in a single ` ```bash ` fenced block." Both the
  positive and the forbidden-behaviour assertions are scoped to that block with
  `strip_comments: true`. A positive check must not be satisfiable by the wrong
  answer: `(?:^|\s)--body(?:=|\s)`, not `contains "--body"`, which
  `--body-file` also satisfies.
- **Decision scenarios** end the prompt with "End your answer with a single
  ` ```json ` fenced block containing exactly this object …" and an
  enumerated object such as
  `{"action": "file_spike" | "propose_fix" | "increase_timeout" | "rerun_until_green", "proposes_code_change": true | false}`.
  `json_path_equals` on the block pins the correct value, which rejects every
  other choice.

The prose is then free to name the forbidden thing while rejecting it. No
shipped scenario runs a negative assertion over unscoped prose; a test in
`packages/nightgauge-sdk/tests/eval/structuredAnswerScenarios.test.ts` enforces
that. The same file holds, for every structured scenario, correct answers whose
prose names the forbidden thing negatively (must pass), wrong answers whose
block does the wrong thing whatever the prose says (must fail), and an answer
with no block (must fail). The mock fixtures are written in the structured
format, with prose that names the forbidden token negatively, so mock mode
exercises the same path.

Two limits are known and accepted:

- Assertions match literal text. A token split on purpose
  (`F="--adm""in"; gh pr merge $F`) evades a `not_matches_regex`. These are
  behavioural regression checks, not a security boundary.
- A block ends at the first bare closing fence. A heredoc inside the block
  whose body contains a ` ``` ` line cuts it short, so a correct answer can
  fail. No shipped scenario asks for fenced content inside its command.

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

  **The green baseline proves less than it looks like it does.** For each
  scenario the same author writes the fixture text _and_ the assertions that
  read it, so a green cell shows the assertions match that prose — not that they
  discriminate a conforming answer from a non-conforming one. See the sentinel
  below, which is what supplies the missing half.

- **live** (`NIGHTGAUGE_SKILL_EVAL_LIVE=1`) — spawns
  `claude --print --model <tier>` and feeds the scenario prompt over stdin,
  matching `ClaudeHeadlessAdapter`'s invocation shape. Selecting `--mode live`
  without the env var is refused (guards against accidental API cost). Live mode
  uses ambient `claude` auth (`claude auth status`); **no API keys are read,
  stored, or logged**. Invoked by hand only — nothing schedules it.

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
   the end-to-end tests assert every shipped scenario passes in mock mode, that
   each pipeline skill has ≥3 scenarios, and that every scenario **fails** the
   non-answer sentinel.
5. Optionally validate with the runner: `npx tsx scripts/evaluate-skills.ts
--skills <skill>`.

## The non-answer sentinel (#1267)

The mock matrix is green by construction, so on its own it cannot tell a working
assertion from one that matches anything. `SkillEvalHarness.test.ts` supplies
the other half: it runs **every** scenario loaded from disk against a fixed
non-answer — `"I don't know."` — and requires every cell to **fail**.

A scenario that passes on a non-answer has assertions that do not discriminate,
and the test names it. This is the `cannot-go-red` defect class
([docs/FAILURE_TAXONOMY.md](FAILURE_TAXONOMY.md#defect-classes-cross-cutting-engineering-patterns))
applied to the harness that exists to catch it elsewhere.

It is deliberately a **universal sentinel** rather than a per-scenario near-miss
answer. A near-miss corpus discriminates far more finely, and it also doubles the
authoring cost of every scenario and rots the moment someone adds a scenario and
forgets its counterpart — which is the same failure this test exists to prevent,
one level up. The sentinel needs no maintenance and covers every future scenario
the moment it lands.

What it does **not** catch: an assertion that is too loose but still stricter
than a bare non-answer — a regex matching any plausible on-topic response, say.
A near-miss corpus is the right follow-up if that turns out to bite.

## Scope (this PR)

- ≥3 scenarios for each of the six pipeline-stage skills (`issue-pickup`,
  `feature-planning`, `feature-dev`, `feature-validate`, `pr-create`,
  `pr-merge`).
- Mock mode + opt-in live mode; JSONL records + baseline regression diff.
- Mock-mode unit/integration tests; no live API calls in CI.

**Out of scope**: a VSCode UI; a Go binary subcommand; non-Claude adapters; and
statistical/quality scoring beyond binary pass/fail.

## CI gate — none exists (#881)

**Nothing runs this harness on a PR.** There is no skill-eval workflow, and the
baseline diff is not a required status check; earlier revisions of this document
and of [ADR 011](decisions/011-model-eval-system.md) said otherwise, which is the
#539/#545 defect class — an asserted enforcement that does not exist makes the
manual step it describes get skipped.

Running it is therefore a **manual step**, and it is on you when you touch the
eval surface (`evals/**`, `packages/nightgauge-sdk/src/eval/**`,
`scripts/evaluate-skills.ts`, or the committed baseline):

```bash
npx tsx scripts/evaluate-skills.ts --baseline .nightgauge/skill-evals/baseline.jsonl
```

The runner **fails CLOSED** when the baseline is missing or empty, so it cannot
silently pass against a non-existent baseline — but only if someone runs it.

**Regenerating the baseline** (intentionally, when scenarios/fixtures change):
run `npx tsx scripts/evaluate-skills.ts`, then copy the run record to
`.nightgauge/skill-evals/baseline.jsonl` (see "Establishing a baseline"
above) and commit it **in the same PR** as the scenario/fixture change.

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
