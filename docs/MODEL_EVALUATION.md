# Model Evaluation

Nightgauge can compare configured model routes using repeatable tasks and
structured outcomes. The goal is to support evidence-based routing without
turning one benchmark into a universal model ranking.

## What is measured

- Completion and deterministic gate results
- Attempts required to reach a valid result
- Latency and token usage
- Provider-reported or locally calculated resource cost
- Task-specific quality signals

Evaluations should be interpreted per job class and pipeline stage. A model that
performs well on documentation work may not be the best route for debugging or
UI implementation.

## Execution modes

Mock execution validates evaluation wiring without invoking a paid model. Live
execution invokes the locally configured provider and may incur provider
charges. Nightgauge never starts a live evaluation merely because mock
evaluation succeeded; the operator must choose the live path.

## Whole-pipeline effort sweep

`nightgauge eval sweep --model <id> --control` runs the pinned control task
(`evals/tasks/README.md` § Control task) once for each effort the model
supports, through the whole pipeline. Each cell records cost, wall time,
attempts, the effort each stage was dispatched at, an objective pass/fail from
the control's `pass_criteria`, and the quality score. Records go to the
model-eval records store as JSONL.

```bash
nightgauge eval sweep --model claude-opus-4-8 --control --efforts low,high --max-usd 40
```

- **Cells run one at a time.** They share one GitHub control issue.
- **`--max-usd` is checked before each cell.** The projected spend is what has
  been spent plus the larger of `--estimate-usd` and the costliest completed
  cell. A cell that would exceed the cap does not start; the sweep exits 1 and
  names how many cells ran.
- **Failed and timed-out cells keep their cost.** They are recorded with the
  spend read from the run's history, never as zero.
- **Each cell is a separate clone.** It has its own `.git`, so pipeline state,
  branches and caches are per cell. It works on `eval/sweep/<run-id>/<effort>`,
  and a `pre-push` hook refuses pushes to `main`. The model and effort pin is an
  uncommitted `.nightgauge/config.local.yaml`, so the base commit and control
  hash do not change.
- **The pin is verified, not assumed.** A cell fails when any stage that
  dispatched a model recorded a different effort or none.

`--mode stub` (the default) exercises the loop, cap and record format without a
model. `--mode live` spends real money and is not yet available:

- `nightgauge run` has no way to stop before pr-merge, so a live cell would
  merge into the fixture repository. Live cells refuse to start until it does.
- Effort is not recorded on the `claude` adapter (see Safe interpretation), so
  a Claude cell fails its pin check. Sweep an adapter that reports effort.

## Effort report and registry recommendation

`nightgauge eval report --model <id>` reads the sweep records for one model on
one control hash and prints a per-effort table: `n`, pass rate, objective pass
rate, mean quality (composite score), quality variance, mean cost, mean wall
time, mean attempts and confidence. It then compares other models swept on the
same hash and states a recommendation for `behavior.effort_default`.

```bash
nightgauge eval report --model claude-opus-4-8 --emit-patch effort-default.patch
```

- **One control hash.** `--control-hash` selects it; by default it is the hash
  of the model's newest record. Records on other hashes or from other suites
  are counted and excluded, never mixed in. Skipped cells are ignored.
- **`n` and variance are shown per cell.** Variance is the sample variance of
  the composite score and reads `n/a` below two runs. A cell with fewer than
  `--min-samples` runs (default 3) is `low-confidence`; a single run always is.

The recommendation rule is deterministic:

1. An effort is **eligible** when it has at least `--min-samples` runs and
   every run met the control's `pass_criteria`.
2. The **best** quality is the highest mean quality among eligible efforts.
3. **Candidates** are eligible efforts whose mean quality is at least
   `best - --tolerance` (default 5 points, inclusive).
4. Candidates are ordered by mean cost, then mean wall time. The first is
   recommended.

A recommendation is never made silently on thin or ambiguous data. The status
is one of:

| Status              | Meaning                                                                                          |
| ------------------- | ------------------------------------------------------------------------------------------------ |
| `recommended`       | One effort won the rule.                                                                         |
| `tie`               | The top two candidates have equal mean cost and wall time. Both are named; neither is picked.    |
| `insufficient-data` | No effort is eligible, or an effort cheaper than the winner has too few runs and could have won. |
| `no-passing-effort` | Every effort with enough runs failed the objective criteria.                                     |

`--json` prints the same report as JSON for other renderers.

`--emit-patch <path>` writes a `git apply` diff that changes only the model's
`behavior.effort_default` in both registry copies
(`packages/nightgauge-sdk/src/eval/model-registry.json` and
`internal/models/model-registry.json`). The command never edits the registry.
Apply the patch in a pull request. It writes no patch, and exits 1, unless the
status is `recommended`, or when any input record came from a stub sweep. When
the recommendation already equals the current default it writes nothing.

On Anthropic models the report repeats the caveat that effort is unverifiable
on the `claude` adapter (see Safe interpretation).

## Evidence and privacy

Tracked evaluation evidence may contain task identifiers, route identifiers,
usage totals, timing, and gate outcomes over synthetic tasks. It must not contain
prompts, generated source, diffs, credentials, customer content, or private
repository paths.

Telemetry upload is optional and governed by
[TELEMETRY_PRIVACY.md](TELEMETRY_PRIVACY.md). Local evaluation and local routing
remain available when telemetry is disabled.

## Safe interpretation

1. Compare like-for-like tasks and configuration.
2. Require enough samples to distinguish a trend from noise.
3. Treat provider prices and capabilities as versioned inputs, not permanent
   facts in documentation.
4. Keep deterministic quality gates constant across routes.
5. Require human review before evaluation results change repository policy.
6. **Do not read an empty effort as a default.** Effort is not observable on
   the `claude` adapter — nothing in the system reports what effort a claude
   stage actually ran at, so `stageEfforts` / `stageServedEfforts` are empty
   there by design rather than by omission. A comparison across efforts is
   only meaningful on adapters that report one; elsewhere the configured value
   is a request nobody confirmed. See
   [OUTCOME_RECORDING.md](OUTCOME_RECORDING.md#effort-is-unverifiable-on-the-claude-adapter--read-the-empty-field-as-unknown).

7. **Per-run cost is not cost per closed issue.** Routing advice compares pass
   rate, quality and cost per evaluation run. It carries no term for turns per
   stage or for the rework rounds a weaker result causes, and those dominate
   what an implementation stage costs. Read a cheaper-per-run result for
   implementation work with that in mind. See
   [CONFIGURATION.md § Routing by cost per closed issue](CONFIGURATION.md#routing-by-cost-per-closed-issue).

Use the CLI help and [CONFIGURATION.md](CONFIGURATION.md) for current commands
and schema fields.
