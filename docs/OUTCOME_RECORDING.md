# Outcome Recording and Complexity Model

> Canonical reference for the outcome recording subsystem — how PR-merge results
> and mid-pipeline signals feed back into the complexity model to improve future
> size predictions. For a high-level overview of where outcome recording fits in
> the pipeline, see [docs/ARCHITECTURE.md](ARCHITECTURE.md).

> **Not to be confused with the learning outcome corpus.** Two subsystems are
> both called "outcome recording". _This_ document covers the TypeScript/SDK
> **complexity model** (`OutcomeRecorder` → `.nightgauge/complexity-model.yaml`,
> success-only, driven by `PostPipelineAnalyzer`). The **learning/calibration
> corpus** is a separate, Go-owned system
> (`internal/intelligence/learning.Recorder` →
> `.nightgauge/pipeline/history/outcomes.jsonl`) that feeds the self-improvement
> loop verdicts and `nightgauge learn tune` — see
> [SELF_IMPROVEMENT_LOOP.md § Outcome Recording](SELF_IMPROVEMENT_LOOP.md#outcome-recording).
> Assuming one covers the other is exactly how the extension path went years
> writing no learning outcomes at all (#304).

## Overview

The outcome recording system closes the feedback loop between pipeline execution
results and future complexity estimation. Every time a pipeline run reaches PR
merge, the actual lines changed are recorded against the original predicted
size. That observation adjusts three things in the YAML complexity model: the
per-type modifiers, the matched-pattern confidence scores, and the running
prediction accuracy statistics.

A second, faster path exists for mid-pipeline signals. When `feature-dev` or
`feature-validate` emits `COMPLEXITY_UNDERESTIMATED`, `FeedbackLearningService`
records a partial outcome immediately — without waiting for PR merge — so the
model degrades matched-pattern confidence before the next issue is picked up.

Together these two paths mean the complexity model is a **living document**: it
improves with every issue the pipeline processes and degrades gracefully when
patterns prove unreliable.

### Where the prediction comes from, and when (#994)

`predictedModel` and `complexityScore` are read from the run's
`issue-{N}.json`, which the **issue-pickup stage writes into the run's
worktree**. Two facts follow, and getting either wrong empties the pair:

**There are two worktree layouts.** The Go manager writes
`<repoRoot>/.nightgauge/worktrees/{repoName}-issue-N`; the VSCode extension
writes `<repoRoot>/.worktrees/issue-N`. `execution.IssueContextCandidates` is
the single list both readers use — before it existed, the scheduler searched
neither layout and the IPC path searched only the extension's, so one corpus
field had two readers each knowing a different subset of where its source
lives.

**The read happens at RECORDING time, never at pickup.** At pickup the worktree
does not exist and issue-pickup has not run, so on a first run the value is
definitionally empty. `Scheduler.resolveOutcomePrediction` re-reads at the
recording boundary and only ever upgrades — a re-read that finds nothing keeps
whatever the caller had.

Until this was fixed, **every row in the corpus carried `predictedModel: ""`
and `complexityScore: 0` for the life of the product**, while the real context
files in the field carried both. Nothing reported it: `Calibrate` excludes a
pair with an empty half rather than booking a false miss, `ratio()` returns nil
rather than 0, and `analyzeCalibration` says "no data" — all correct, and
collectively indistinguishable from a corpus that is simply young. `nightgauge
doctor`'s `corpus_calibration` arm is the one check that tells them apart,
because it keys on the row count the polite consumers ignore.

### The model pair's vocabulary (#340)

The learning corpus (`.nightgauge/pipeline/history/outcomes.jsonl`) stores
`predictedModel` / `actualModel`, and every consumer compares them for
**equality**. Both halves are therefore registry **bands**
(`haiku|sonnet|opus|fable`) and nothing else — a reference the registry has no
band for records `""`, and consumers exclude a pair with an empty half from the
denominator rather than booking a miss.

`actualModel` is derived by `OutcomeActualBand`
(`internal/orchestrator/outcome_semantics.go`), which is a band question asked
about a concrete input. Go dispatches a band; the extension translates it at the
last mile (`opus` → codex `gpt-5.6-sol`; `opus` → Gemini
`gemini-2.5-pro` in every performance mode) and reports the launched id back.
The scheduler re-records that id as the stage's model so cost and history name
the model that actually ran. Those ids are **multi-band**: `gpt-5.6-sol` and
`gemini-2.5-pro` each serve both `opus` and `fable`. Collapsing one onto its
strongest band reads "fable" for a run the router predicted "opus" and the
adapter served exactly as asked, so every such run booked a routing MISS.

The mapping is therefore inverted through the registry rather than collapsed:

| Served (concrete)  | Predicted | `actualModel` | Why                                                         |
| ------------------ | --------- | ------------- | ----------------------------------------------------------- |
| `claude-opus-5`    | `opus`    | `opus`        | single-band id                                              |
| `gpt-5.6-sol`      | `opus`    | `opus`        | the model the opus band maps to — a HIT                     |
| `gpt-5.6-sol`      | `fable`   | `fable`       | same id, and the request says which band                    |
| `gpt-5.6-terra`    | `opus`    | `sonnet`      | genuinely weaker serve — a MISS                             |
| `gemini-2.5-pro`   | `opus`    | `opus`        | gemini honoring the opus band — a HIT                       |
| `gemini-2.5-flash` | `opus`    | `sonnet`      | serves [haiku, sonnet]: a real, correctly-booked MISS       |
| `gemini-2.0-flash` | `opus`    | `""`          | no registry band: excluded, never a miss                    |
| `gpt-5.5`          | `opus`    | `""`          | a configurable codex model the registry carries no band for |

The last four rows demonstrate why recording still measures the concrete model
instead of assuming translation succeeded. Both the **Go executor** path
(`nightgauge run`) and **extension-dispatched** paths translate recognized
bands for `codex`, `gemini`, `gemini-sdk` and `copilot` in every mode, so a
Gemini run asked for `opus` normally serves `gemini-2.5-pro` and books a HIT.
A provider refusal or fallback can still serve a weaker registered model and
book a genuine MISS. A model with no registry band records `""` and is excluded
from the accuracy denominator rather than fabricating one.

Local adapters remain different by design: `lm-studio` has no registry tier
hierarchy, so extension dispatch falls back to the configured loaded model for
every requested band. If that model has no registry band, its corpus pair is
excluded while the run record's per-stage `model_selection` still retains the
concrete id for attribution. The agentic-adapter gate currently prevents LM
Studio from running pipeline stages (#57); this rule applies on extension
surfaces where the adapter is eligible.

Attribution of what actually ran is kept where a concrete id belongs: the run
record's per-stage `model_selection`.

### A retry escalation is not a routing miss (#1002)

`Calibrate` excludes any row with `Retries > 0` from the **model-accuracy
denominator** (`ModelSamples`) — it does not touch the size pair, which has no
equivalent exclusion.

The retry engine's escalated tier becomes the dispatch override
(`resolveDispatchModel` applies `retryEngine.CurrentModel(stage)`), and that
override is re-recorded as `ActualModel`. A `haiku -> sonnet` escalation on a
failed stage — the retry ladder doing exactly its job — therefore produces a
pair that compares `PredictedModel` (the router's original recommendation)
against `ActualModel` (the retry-escalated dispatch), and nothing in that
comparison distinguishes "the router mispredicted" from "the retry ladder
intervened". Before this exclusion, a week with a normal number of retries
read as calibration degrading.

`Retries > 0` is a superset of "the tier actually changed" — a retry that
re-dispatched the same tier is excluded too — accepted deliberately because
erring toward fewer false misses is preferable to erring toward inflated ones.
Excluded rows are not silently dropped: `CalibrationReport.ModelSamplesExcludedRetry`
counts them, so the sample loss stays visible instead of reading as an
unexplained shrink in `ModelSamples`.

There is no equivalent `performance_mode` segmentation anywhere in this
component — nothing under `internal/intelligence/learning/` reads
`performance_mode`, and `Calibrate` does not segment the size pair on it. The
retry discriminator above is the first segmentation this component has ever
had.

**`modelAccuracy` measures divergence, not quality.** Even with the retry
exclusion in place, and with the override chain (floor/ceiling/sticky-downgrade
clamps) still fully in play, `modelAccuracy` measures whether what actually
dispatched matched the router's recommendation — it does not measure whether
the recommendation was any good. A test asserting `modelAccuracy == 1.0` on a
happy-path run cannot go red on a bad router and proves nothing about routing
quality.

### Stored telemetry across the band retirement is accept-void (#582)

Spike #568 §5 decided the migration policy for both history stores when the
band vocabulary was retired (epic #567), and the decision is **accept-void** —
no alias tables, no backfill, no migration code, ever (the pre-customer
no-compat rule in `AGENTS.md`):

- **Learning corpus** (`.nightgauge/pipeline/history/outcomes.jsonl`): rows
  written before the dispatch-envelope cutover carry no `schema_version`
  marker and no effort/thinking axes — those axes were never recorded and
  cannot be reconstructed, so a backfill would fabricate data and an alias
  would preserve almost nothing. Exclusion keys on the #340 empty-half guard:
  readers exclude a pair with an empty half from every denominator, and
  legacy rows join that excluded bucket because their empty halves are
  exactly the unrecorded ones. Post-cutover rows additionally carry
  `schema_version: "2"`, stamped at the corpus's one append point
  (`learning.Recorder.Record` — both writers flow through it), so row
  provenance is deterministic rather than vocabulary-sniffed; an unmarked
  row IS a pre-cutover row.
- **Run-record history** (per-stage `model_selection` in
  `.nightgauge/pipeline/history/`): pre-envelope records hold bare band
  strings in `model_selection.model`. They are operator forensics, not
  learning inputs — they stay readable as-is and are never rewritten. New
  records carry the full dispatch envelope `(model, effort, thinking)` plus
  selection-source attribution (#599). Consumers key off envelope fields
  resolved through the model registry — never band substrings, which fail
  open (stop matching) on concrete ids; the analyzers over this record
  stream (the model-routing health dimension and
  `ModelPerformanceAnalyzer`'s under/over-routing detectors, via the shared
  `bandStrength.ts` helper) were rewritten accordingly in #582. A handful of
  dispatch-side substring predicates survive as explicit keeps with inline
  reasons (see `scripts/check-band-vocabulary.py`'s header and PR #607).

### Effort is unverifiable on the `claude` adapter — read the empty field as "unknown"

`stageEfforts` and `stageServedEfforts` are **empty for every stage that runs
on the `claude` adapter, and that is correct, not a gap in the data**. Go
records effort only where it has first-party evidence of it, which today means
the grok-family adapters' `NIGHTGAUGE_GROK_EFFORT` dispatch env var
(`resolveDispatchEffort`) and any executor that reports a served effort on its
own stream. The claude CLI reports neither, so nothing in the system observes
what effort a claude stage actually ran at.

**Nobody can verify it today** — not the dashboard, not a retro, not an
operator asking "did that stage run at the effort I configured?" The honest
answer is that the configured value was requested and the result was never
observed.

The field is left empty rather than filled with the registry default on
purpose. A default written into an observation field is indistinguishable from
a measurement, and it would be consumed as one by the routing calibration
below — turning "we never looked" into "we looked and it was medium". Silence
that looks like data is the failure mode; an empty field that readers know
means _unknown_ is strictly better than a confident wrong number.

Contrast `stageThinking`, which IS populated on the claude adapter (#888):
that value is _derived_, from the resolved model's registry
`behavior.thinking_default` plus the `CLAUDE_CODE_DISABLE_THINKING` interlock,
and it is recorded because both inputs are facts Go's own process holds. The
line is not "requested vs served" — it is whether anything in the system can
actually answer the question.

Two recording paths feed `ComplexityModelService`:

```
  PR merge event
       │
       ▼
  OutcomeRecorder.recordOutcome()
       │
       ├─ 1. determine actual size bucket
       ├─ 2. modelService.recordOutcome()     ──► size_calibration running avg
       ├─ 3. updateAccuracy()                 ──► prediction_accuracy counters
       ├─ 4. adjustTypeModifiers()            ──► type_adjustments modifiers
       └─ 5. adjustPatternConfidence()        ──► patterns[*].confidence
                        │
                        ▼
              ComplexityModelService.save()
              (atomic write — temp file + verify)
                        │
                        ▼
              .nightgauge/complexity-model.yaml
                        │
                        ▼
              future feature-pickup estimation

  Mid-pipeline COMPLEXITY_UNDERESTIMATED signal
       │
       ▼
  FeedbackLearningService.recordUnderestimation()
       │
       ├─ idempotency check (isOutcomeRecorded)
       ├─ UNDERESTIMATION_CONFIDENCE_PENALTY on matched patterns
       ├─ prediction_accuracy: total++ (correct unchanged)
       └─ actual_size_bucket: 'UNDERESTIMATED' sentinel
                        │
                        ▼
              ComplexityModelService.save()
```

## Outcome Recording Lifecycle

### Entry Point: `OutcomeRecorder.recordOutcome(outcome: ExecutionOutcome)`

The `OutcomeRecorder` constructor takes a `ComplexityModelService`. Its primary
method, `recordOutcome()`, executes five sequential steps after loading the
current model.

#### Step 1 — Actual Size Bucket

`getActualSizeBucket(linesChanged, model)` maps the raw `lines_changed` count to
a size label (`XS` / `S` / `M` / `L` / `XL`) using the model's
`lines_changed_thresholds` field. Any count above the `XL` threshold maps to
`XL`.

#### Step 2 — Basic Outcome via `modelService.recordOutcome()`

`ComplexityModelService.recordOutcome()` updates `total_observations`, and
records a weighted running average for
`size_calibration[bucket].actual_average_lines` and `sample_count`. It also
updates `model_tracking.observations_by_model` for the model used.

#### Step 3 — Conditional Idempotency (Issue #1198)

Before writing, the recorder checks `isOutcomeRecorded()`. If an entry already
exists in `recent_outcomes` for the same issue number with
`actual_lines_changed === 0` (a garbage entry written during failure-path
recording), the existing entry is eligible for overwrite when the new outcome
carries real data. Non-zero existing entries are protected and the call returns
early without modification.

When overwriting a garbage entry,
`reverseOutcomeEffects(model, existing, newOutcome)` is called first. It removes
the stale entry from `recent_outcomes`, reverses the `prediction_accuracy`
counters it incremented, reverses the `size_calibration` running average it
affected, and reverses the `model_tracking` count — leaving the model in a clean
state before the new observation is applied.

#### Step 4 — Prediction Accuracy (`updateAccuracy()`)

`updateAccuracy(model, outcome, actualBucket, wasCorrect)` updates
`prediction_accuracy`:

- Increments `total_predictions`
- Increments `correct_predictions` when `wasCorrect` is true
- Increments `by_type[issueType].total` and `.correct`
- Increments `by_size[actualBucket].total` and `.correct`
- Appends to `recent_outcomes` (capped at `MAX_RECENT_OUTCOMES = 50`)

**Adjacent-size tolerance:** `isPredictionCorrect(predicted, actual)` uses
`SIZE_ORDER` (`['XS', 'S', 'M', 'L', 'XL']`) to compare index positions. A
prediction is considered correct when `|predictedIdx - actualIdx| <= 1`. This
means a prediction of `S` against an actual of `M` counts as correct, which
prevents over-penalizing predictions that are one size off.

#### Step 5 — Type Modifier Adjustment (`adjustTypeModifiers()`)

`adjustTypeModifiers(model, outcome, actualBucket, wasCorrect)` applies
directional error correction to `type_adjustments[issueType].modifier`:

```
shift = -(predictedIdx - actualIdx) * LEARNING_RATE
```

A negative `predictedIdx - actualIdx` means the prediction was too low (actual
was larger), so `shift` is positive, nudging the modifier upward to increase
future estimates for that type. The modifier is clamped to
`[-MAX_MODIFIER_MAGNITUDE, +MAX_MODIFIER_MAGNITUDE]`.

Adjustment only fires after `MIN_OBSERVATIONS_FOR_ADJUSTMENT = 5` total
observations for that issue type, preventing noise from too-small samples.

#### Step 6 — Pattern Confidence (`adjustPatternConfidence()`)

`adjustPatternConfidence(model, outcome, wasCorrect)` iterates over all matched
patterns referenced in the outcome:

- Correct prediction: `confidence += CONFIDENCE_BOOST` (`0.02`)
- Incorrect prediction: `confidence -= CONFIDENCE_PENALTY` (`0.05`)
- Result clamped to `[0.0, 1.0]`

The asymmetry (penalty 2.5× the boost) causes patterns to lose confidence
quickly when they correlate with mispredictions and regain it slowly after
sustained correct predictions.

### Survival Calibration (Issues #4152/#4153)

The steps above calibrate on a **merge-time proxy** (predicted size vs.
actual lines changed) — they cannot tell code that merged-and-held-up from
code that merged-and-broke-main-or-got-reverted. Spike #4134 closes that gap
with a separate, coarse-grained calibration dimension fed by **real
post-merge ground truth**: finalized `survival.Record` verdicts from the
post-merge survival outcome model. See
[GO_BINARY.md#survival-operations-4151](GO_BINARY.md#survival-operations-4151).

A survival record carries no issue-type or pattern attribution — only the
merge commit SHA — so unlike `adjustTypeModifiers`/`adjustPatternConfidence`
this is a single model-wide `prediction_accuracy.survival_calibration.confidence`
value (not per-type/per-pattern), starting at a **neutral prior of `0.5`**
(not a ceiling — a maxed-out start would permanently absorb the weak reward
into the clamp).

Bias-safe rule (mirrors the existing asymmetric `CONFIDENCE_BOOST` /
`CONFIDENCE_PENALTY` rule, per spike §1.2):

- **`reverted` / `broke`** (proven negative) → confidence **penalty**
  (`CONFIDENCE_PENALTY`, `-0.05`), gated behind `MIN_OBSERVATIONS_FOR_ADJUSTMENT`
  (5) cumulative negative observations (#4152). Proven ground truth — applied
  as soon as enough real data exists.
- **`survived`** (weak positive, terminal only) → confidence **boost**
  (`CONFIDENCE_BOOST`, `0.02`), gated behind `MIN_OBSERVATIONS_FOR_ADJUSTMENT`
  (5) cumulative **finalized** survived observations (#4153) — deliberately
  separate from and weaker than the penalty, and never applied to a lone or
  pending/unproven survival record (censored data must never read as proof).
- **`pending` / `unobserved`** → no signal, ignored entirely.

Implementation is parallel in both languages, sharing the same
`complexity-model.yaml`:

- **Go:** `internal/github/outcome_survival.go` —
  `OutcomeService.ApplySurvivalVerdicts(records []survival.Record)`. Invoked
  right after a reconcile/CLI sweep finalizes new verdicts
  (`internal/orchestrator/autonomous_survival_sweep.go` and
  `nightgauge survival sweep`), using
  `survival.SweepResult.FinalizedRecords` — not a full journal rescan.
- **TS:** `packages/nightgauge-sdk/src/services/OutcomeRecorder.ts` —
  `OutcomeRecorder.applySurvivalVerdicts(model, records)`, a pure model
  transform mirroring the Go logic field-for-field. The TS Zod schema
  (`SurvivalCalibrationSchema` in `context/schemas/survival.ts`) also matters
  even before a TS-side survival source exists: without it,
  `ComplexityModelSchema.safeParse` would silently strip an
  unrecognized `survival_calibration` key on every TS-side `load()`/`save()`
  round-trip, erasing Go's calibration state.

Both implementations deduplicate by `merge_commit_sha` against a persisted,
bounded ledger (`survival_calibration.processed_shas`), so calibration is
safe to re-apply with any subset of records — including a full store
reload — without double-counting an already-processed verdict.

## ComplexityModelService

**File:** `packages/nightgauge-sdk/src/services/ComplexityModelService.ts`

`ComplexityModelService` owns all I/O against the YAML model file. It is
injected into `OutcomeRecorder` and `FeedbackLearningService` and is the only
component that reads or writes the model on disk.

### YAML Model Structure

```yaml
schema_version: "1.0"
last_updated: "2026-02-28"
bootstrap_date: "2026-02-28" # present only if bootstrapped
total_observations: 347

decay:
  enabled: true
  half_life_days: 30

model_tracking:
  current_default: "claude-sonnet-4-6"
  observations_by_model:
    claude-sonnet-4-6: 280
    claude-opus-4-7: 67

patterns:
  high_complexity:
    - match: "refactor|redesign"
      modifier: 1.5
      confidence: 0.85
      rationale: "..."
      observations: 45
  medium_complexity: [...]
  low_complexity: [...]

size_calibration:
  XS: { expected_lines: 50, actual_average_lines: 59, sample_count: 87 }
  S: { expected_lines: 150, actual_average_lines: 213, sample_count: 94 }
  M: { expected_lines: 400, actual_average_lines: 471, sample_count: 82 }
  L: { expected_lines: 900, actual_average_lines: 987, sample_count: 61 }
  XL: { expected_lines: 2000, actual_average_lines: 2143, sample_count: 23 }

type_adjustments:
  feature: { modifier: -1.45, observations: 45, rationale: "..." }
  bug: { modifier: -0.6, observations: 30, rationale: "..." }

priority_adjustments:
  critical: { modifier: 0.2 }
  high: { modifier: 0.1 }

lines_changed_thresholds:
  XS: 100
  S: 325
  M: 850
  L: 1850
  XL: 2500

learnings:
  - "2026-01-15: ..."

prediction_accuracy:
  total_predictions: 280
  correct_predictions: 231
  by_type:
    feature: { total: 120, correct: 98 }
  by_size:
    S: { total: 94, correct: 82 }
  recent_outcomes:
    - issue_number: 1400
      predicted_size: "M"
      actual_size_bucket: "L"
      was_correct: true
      recorded_at: "2026-02-28T00:00:00Z"
      actual_lines_changed: 1200

critical_files:
  description: "Files whose modification significantly increases issue complexity"
  registry: ["src/config/schema.ts"]
  per_file_modifier: 0.5
  max_modifier: 1.5
```

### Bootstrap from Cross-Repo Data (Issue #1316)

`load()` auto-bootstraps when the model file does not exist. It calls the static
method `createBootstrapModel()`, which returns a baseline model pre-seeded with
universal calibration data derived from cross-repo observations. The bootstrap
sets `bootstrap_date` in the YAML so callers can identify freshly-initialized
models. Bootstrapping is silent — the pipeline never requires the model to be
manually initialized.

### Atomic Save

`save(model)` protects against partial writes:

1. Serialize `model` to YAML
2. Write to a temp file alongside the target path
3. Copy temp file to the target path
4. Perform post-write verification (read back and confirm non-empty)
5. On failure at any step, restore from the temp file and re-throw

### Exponential Decay (`applyDecay()`)

`applyDecay(model)` applies time-based decay to every pattern's `confidence`
score when `decay.enabled` is true. The decay factor is computed by
`calculateDecayFactor(daysSinceObservation, halfLife)`:

```
decayFactor = 0.5 ^ (daysSinceObservation / halfLife)
```

With the default `half_life_days: 30`, a pattern not observed for 30 days
retains 50% of its confidence; after 60 days it retains 25%. Decay is applied on
model load, so stale patterns naturally lose influence over time without any
explicit pruning.

### Threshold Recalibration (`recalibrateThresholds()`)

`recalibrateThresholds(model, minSamples=5)` recomputes
`lines_changed_thresholds` using a midpoint strategy: the threshold between two
adjacent size buckets is set to the midpoint between their
`actual_average_lines` values. Buckets with fewer than `minSamples` observations
are skipped. This is called periodically (not on every outcome) to let the
calibration accumulate enough observations before shifting thresholds.

## FeedbackLearningService

**File:** `packages/nightgauge-sdk/src/services/FeedbackLearningService.ts`

`FeedbackLearningService` handles the mid-pipeline recording path. It is
constructed with a `ComplexityModelService` and exposes one primary method:

### `recordUnderestimation()`

```
recordUnderestimation(
  issueNumber,
  predictedSizeLabel,
  issueType,
  issueTitle,
  issueDescription,
  signal,
  matchedPatterns?
)
```

Called when the orchestrator processes a `COMPLEXITY_UNDERESTIMATED` signal.
Unlike `OutcomeRecorder`, this method records immediately and does not wait for
PR merge. Its effects:

1. **Idempotency check** — calls `modelService.isOutcomeRecorded()` and returns
   early if the issue has already been recorded, preventing double penalties
   from duplicate signals.

2. **Confidence penalty** — applies `UNDERESTIMATION_CONFIDENCE_PENALTY`
   (`0.05`) to every pattern in `matchedPatterns`. This is the same magnitude as
   `OutcomeRecorder`'s `CONFIDENCE_PENALTY`, making mid-pipeline penalties
   equivalent to a post-merge misprediction.

3. **Accuracy recording** — appends to `prediction_accuracy`:
   - `total_predictions` incremented
   - `correct_predictions` is NOT incremented
   - `actual_size_bucket` is set to the sentinel string `'UNDERESTIMATED'`
   - `was_correct: false`

   The `'UNDERESTIMATED'` sentinel distinguishes mid-pipeline recordings from
   post-merge recordings in `recent_outcomes`, and is excluded from the
   `by_size` accuracy breakdown.

4. **Atomic save** — calls `modelService.save()` before returning.

## Recovery Files

**Related issue:** #1182

`ComplexityModelService` maintains a JSONL recovery log at
`.nightgauge/outcome-recovery.jsonl`. Its purpose is to preserve outcomes
that were computed but not yet written to the model — for example, if a process
crashes between computing an outcome and writing the YAML.

### Write Path: `appendToRecoveryFile(issueNumber, outcomeData)`

Each outcome is appended as a single JSON line. On POSIX systems the append is
atomic at the OS level (O_APPEND on a file descriptor), preventing interleaved
writes when multiple processes record concurrently.

### Recovery on Load: `replayRecoveryFile(model)`

Called inside `load()` after the model is read and validated. It reads the JSONL
file line by line, replays each pending outcome against the in-memory model
(calling `recordOutcome()` for each line), and truncates the recovery file to
zero bytes on success. If any line fails to parse, it is skipped and logged; the
recovery file is not truncated until all valid lines have been replayed.

```
load() call
  │
  ├─ read + validate complexity-model.yaml
  ├─ replayRecoveryFile(model)
  │     ├─ parse each JSONL line
  │     ├─ modelService.recordOutcome() per line
  │     └─ truncate on success
  └─ return model
```

### Recovery File Format

Each line is a standalone JSON object:

```json
{ "issueNumber": 1400, "outcomeData": { ... } }
```

The file path is returned by `getRecoveryFilePath()` and is always
`.nightgauge/outcome-recovery.jsonl` relative to the workspace root.

## Configuration

| YAML Field                    | Default | Description                                                         |
| ----------------------------- | ------- | ------------------------------------------------------------------- |
| `decay.enabled`               | `true`  | Enable exponential confidence decay on model load                   |
| `decay.half_life_days`        | `30`    | Half-life in days for pattern confidence decay                      |
| `lines_changed_thresholds.XS` | `100`   | Lines-changed upper bound for XS bucket                             |
| `lines_changed_thresholds.S`  | `325`   | Lines-changed upper bound for S bucket                              |
| `lines_changed_thresholds.M`  | `850`   | Lines-changed upper bound for M bucket                              |
| `lines_changed_thresholds.L`  | `1850`  | Lines-changed upper bound for L bucket                              |
| `lines_changed_thresholds.XL` | `2500`  | Lines-changed upper bound for XL bucket (anything above maps to XL) |

The model file path defaults to `.nightgauge/complexity-model.yaml`
relative to the workspace root. It can be overridden by passing an explicit
`modelPath` to the `ComplexityModelService` constructor.

## Key Constants

| Constant                             | Value  | Location                                  | Purpose                                                          |
| ------------------------------------ | ------ | ----------------------------------------- | ---------------------------------------------------------------- |
| `LEARNING_RATE`                      | `0.05` | `OutcomeRecorder`                         | Step size per outcome for type modifier adjustment               |
| `MAX_MODIFIER_MAGNITUDE`             | `3.0`  | `OutcomeRecorder`                         | Clamp bound for `type_adjustments[*].modifier`                   |
| `MIN_OBSERVATIONS_FOR_ADJUSTMENT`    | `5`    | `OutcomeRecorder`                         | Minimum per-type observations before modifier shifts             |
| `CONFIDENCE_BOOST`                   | `0.02` | `OutcomeRecorder`                         | Per-outcome confidence gain for correct predictions              |
| `CONFIDENCE_PENALTY`                 | `0.05` | `OutcomeRecorder`                         | Per-outcome confidence loss for incorrect predictions            |
| `MAX_RECENT_OUTCOMES`                | `50`   | `OutcomeRecorder`                         | Maximum entries retained in `recent_outcomes`                    |
| `UNDERESTIMATION_CONFIDENCE_PENALTY` | `0.05` | `FeedbackLearningService`                 | Confidence penalty for mid-pipeline underestimation              |
| `DEFAULT_SURVIVAL_CONFIDENCE`        | `0.5`  | `OutcomeRecorder` / `outcome_survival.go` | Neutral starting `survival_calibration.confidence` (#4152/#4153) |
| `MAX_PROCESSED_SURVIVAL_SHAS`        | `500`  | `OutcomeRecorder` / `outcome_survival.go` | Bound on the survival-calibration dedup ledger                   |

## Key File Map

| File                                                              | Purpose                                                                     |
| ----------------------------------------------------------------- | --------------------------------------------------------------------------- |
| `packages/nightgauge-sdk/src/services/OutcomeRecorder.ts`         | Main post-merge outcome recording: accuracy, modifiers, confidence          |
| `packages/nightgauge-sdk/src/services/FeedbackLearningService.ts` | Mid-pipeline `COMPLEXITY_UNDERESTIMATED` signal recording                   |
| `packages/nightgauge-sdk/src/services/ComplexityModelService.ts`  | YAML model I/O, atomic save, decay, recalibration, recovery file            |
| `packages/nightgauge-sdk/src/analysis/WorkflowOutcomeAnalyzer.ts` | V4 workflow-outcome fold: aggregate usage, judge-rejection, fan-out         |
| `internal/github/outcome_survival.go`                             | Go-side survival-verdict calibration (#4152/#4153)                          |
| `internal/intelligence/survival/`                                 | Survival record capture/detection/finalization (#4151, no calibration math) |

## V4 Workflow-Outcome Consumer (Issue #3915)

The multi-agent orchestration spine (epic #3899) emits a canonical
`schemaVersion-4` `WorkflowEvent` node tree — nested `SubAgentNode` `agents[]`
plus `JudgeVerdict`s. The outcome-recording + learning-loop consumers ingest
this tree **forward-only** (the old flat per-stage event shape is deleted):

- **`foldWorkflowOutcome(events)`** folds one run's append-only emission stream
  (by `(nodeId, max seq)`, mirroring the live tree sink) into a flat
  `WorkflowOutcome` — `usage` is the sum of every agent + judge leaf, plus
  `judgeRejectionRate` (fail / total verdicts), `fanoutEfficiency` (succeeded /
  total agents), and the executing `backend`.
- **`summarizeWorkflowOutcomes(outcomes)`** reduces many runs into a
  `WorkflowCalibrationSignal`: mean judge-rejection rate, mean fan-out
  efficiency, and the native-vs-fanout per-run cost delta (only once **both**
  backends have ≥1 run).
- **`PostPipelineAnalyzer`** (VSCode) reads the durable
  `.nightgauge/pipeline/workflow-*.jsonl` journals via
  `readWorkflowJournals()`, folds them, and surfaces the
  `WorkflowCalibrationSignal` on its analysis result + self-check output.
- The **learning-effectiveness health dimension** consumes the signal
  (`HealthAnalysisInput.workflowCalibration`): a high judge-rejection rate or a
  low fan-out efficiency lowers the dimension score and raises a finding.

---

> **Related Issues**: #1182 (recovery file replay on load), #1198 (conditional
> idempotency for garbage-entry overwrite), #1316 (bootstrap from cross-repo
> calibration data)

## Author

nightgauge
