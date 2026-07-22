# Realistic Eval Task Corpus

Realistic SDLC tasks the pipeline is run against during a **model evaluation**
(epic #4167, issue #4170). Unlike the skill-eval scenarios in `evals/scenarios/`
(synthetic, binary pass/fail), these tasks mirror what people actually build with
the pipeline — UI creation, UX/styling, backend logic, testing, bugfix, refactor,
docs — so the eval measures **cost / latency / attempts-to-green / quality** on
representative work.

Each task is one JSON file conforming to the **`EvalTask`** schema
(`packages/nightgauge-sdk/src/eval/modelEvalSchemas.ts`), loaded and
validated by `loadEvalTasks()` (`src/eval/taskLoader.ts`). The matrix runner
(S4, #4171) executes each task once per `{model × effort × reasoning}` cell in an
isolated worktree; the grading engine (S5, #4173) scores the result with the
task's deterministic `checks` plus an LLM judge against its `rubric`.

## Task format

```jsonc
{
  "id": "ui-pricing-card", // stable kebab-case, unique in the corpus
  "title": "Responsive three-tier pricing card component",
  "job_class": "ui-creation", // ui-creation | ux-styling | backend-logic
  //  | testing | bugfix | refactor | docs
  "target_stages": ["feature-dev", "feature-validate"], // pipeline stage(s) exercised
  "difficulty": "medium", // easy | medium | hard
  "instruction": "…the issue/prompt handed to the pipeline…",
  "fixture": {
    // how the seed repo state is materialized
    "kind": "scaffold-script", // scaffold-script | base-commit | snapshot-dir
    "ref": "evals/fixtures/ui-pricing-card/setup.sh",
  },
  "checks": [
    // deterministic correctness gates (scored by S5)
    { "name": "build", "command": "npm run build", "expect_exit_code": 0 },
    { "name": "test", "command": "npm test" }, // expect_exit_code defaults to 0
  ],
  "rubric": {
    // subjective dimensions the judge scores
    "criteria": [
      { "dimension": "ux_quality", "weight": 0.6, "guidance": "Polished, responsive, accessible?" },
      { "dimension": "correctness", "weight": 0.4, "guidance": "Tests cover all three tiers?" },
    ],
  },
}
```

### Fixture `kind`

| kind              | `ref` is…                                   | Use when                               |
| ----------------- | ------------------------------------------- | -------------------------------------- |
| `scaffold-script` | a shell script that seeds a fresh workspace | greenfield tasks (most of this corpus) |
| `base-commit`     | a commit SHA to check out                   | tasks that modify an existing codebase |
| `snapshot-dir`    | a directory to copy as the seed state       | tasks needing a fixed pre-built tree   |

Scaffold scripts live at `evals/fixtures/<task-id>/setup.sh` and must be
idempotent: given a fresh empty working directory, they create the seed files the
task starts from (and nothing model-specific). The runner resets to this state
before each cell so every model gets the identical starting point.

### `rubric` weights

`rubric.criteria[].weight` values are the judge's per-dimension weights; the
scorer (S5) blends them with the deterministic `checks` into a composite score,
applying per-`job_class` weighting (UI tasks weight `ux_quality` higher; refactors
weight `correctness`/tests higher).

## Current corpus

| Task                        | Job class     | Difficulty | What it exercises                                    |
| --------------------------- | ------------- | ---------- | ---------------------------------------------------- |
| `ui-pricing-card`           | ui-creation   | medium     | Responsive three-tier component + tests              |
| `frontend-debounced-search` | ui-creation   | **hard**   | Debounce + stale-response cancellation (race-safety) |
| `ux-form-accessibility`     | ux-styling    | medium     | Accessible signup form                               |
| `backend-rate-limiter`      | backend-logic | **hard**   | Time-based token-bucket middleware                   |
| `backend-lru-cache-ttl`     | backend-logic | **hard**   | O(1) LRU eviction + per-entry TTL (injected clock)   |
| `testing-cart-coverage`     | testing       | medium     | Raise coverage on a cart module                      |
| `bugfix-date-off-by-one`    | bugfix        | easy       | Root-cause a month-boundary off-by-one               |
| `refactor-extract-service`  | refactor      | medium     | Decompose a fat controller, behavior-preserving      |
| `docs-api-reference`        | docs          | medium     | Write an API reference (link-checked)                |

The `hard` tasks are where frontier models separate from cheaper ones — easy tasks
are aced across the board, so differentiation needs tasks weak models get subtly
wrong (a hack that passes tests, a race that only trips under interleaving).

## Adding a task

1. Add `evals/tasks/<id>.json` (validates against `EvalTaskSchema`).
2. Add `evals/fixtures/<id>/setup.sh` (idempotent seed).
3. `loadEvalTasks()` picks it up automatically; the corpus test asserts every
   job class is represented and every fixture script exists.
