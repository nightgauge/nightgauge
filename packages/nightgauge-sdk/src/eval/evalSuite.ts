/**
 * Model-eval suite orchestration (Issue #4174).
 *
 * Ties the pieces together into "run a suite end to end": build the
 * {model × effort × reasoning} matrix, run every task through it (S4 runner),
 * optionally score each cell (S5), and serialize results to JSONL for local
 * baselines + platform emit. Pure orchestration — the executor/workspace/judge
 * are injected — so it is unit-testable with mocks; the CLI
 * (scripts/evaluate-models.ts) wires the real bindings.
 *
 * @see docs/decisions/011-model-eval-system.md
 */

import {
  BASELINE_PROMPT_VARIANT,
  MIN_HONEST_SCHEMA_VERSION,
  MODEL_EVAL_SCHEMA_VERSION,
  type EvalMatrixCell,
  type EvalRun,
  type EvalTask,
  type ModelDescriptor,
  type ModelEvalRecord,
} from "./modelEvalSchemas.js";
import {
  ModelEvalRunner,
  type EvalCellExecutor,
  type WorkspaceProvider,
} from "./modelEvalRunner.js";
import { type AutomatedBaseline } from "./qualityScorer.js";

/** Default local JSONL directory for model-eval run records. */
export const DEFAULT_MODEL_EVAL_RECORDS_DIR = ".nightgauge/model-evals";

/** Cartesian product of models × efforts × reasoning levels × prompt variants (#72). */
export function buildMatrix(
  modelIds: string[],
  efforts: EvalMatrixCell["effort"][],
  reasonings: EvalMatrixCell["reasoning"][],
  promptVariants: string[] = [BASELINE_PROMPT_VARIANT]
): EvalMatrixCell[] {
  const cells: EvalMatrixCell[] = [];
  for (const model_id of modelIds) {
    for (const effort of efforts) {
      for (const reasoning of reasonings) {
        for (const prompt_variant of promptVariants) {
          cells.push({ model_id, effort, reasoning, prompt_variant });
        }
      }
    }
  }
  return cells;
}

export interface RunEvalSuiteOptions {
  suite: string;
  runId: string;
  timestamp: string;
  mode: "mock" | "live";
  tasks: EvalTask[];
  matrix: EvalMatrixCell[];
  models: ModelDescriptor[];
  executor: EvalCellExecutor;
  workspaces: WorkspaceProvider;
  concurrency?: number;
  /**
   * When set, each cell is scored (S5) against this baseline, populating
   * `cell.score`. The score folds in the executor's judge verdict when a judge
   * ran (live `--judge`); otherwise it is deterministic-only. Scoring happens in
   * the runner (the judge must inspect the still-live workspace).
   */
  scoringBaseline?: AutomatedBaseline;
}

/** Run a full suite and return the assembled (optionally scored) EvalRun. */
export async function runEvalSuite(options: RunEvalSuiteOptions): Promise<EvalRun> {
  const runner = new ModelEvalRunner(options.executor, options.workspaces);
  return runner.run({
    suite: options.suite,
    runId: options.runId,
    timestamp: options.timestamp,
    mode: options.mode,
    tasks: options.tasks,
    matrix: options.matrix,
    models: options.models,
    concurrency: options.concurrency,
    scoringBaseline: options.scoringBaseline,
  });
}

/** Flatten an EvalRun into one JSONL record per cell, stamped with run fields. */
export function evalRunToRecords(run: EvalRun): ModelEvalRecord[] {
  return run.cells.map((cell) => ({
    ...cell,
    schema_version: MODEL_EVAL_SCHEMA_VERSION,
    run_id: run.run_id,
    suite: run.suite,
    timestamp: run.timestamp,
    mode: run.mode,
  }));
}

/** Serialize an EvalRun to newline-delimited JSON (one cell per line). */
export function serializeEvalRun(run: EvalRun): string {
  return evalRunToRecords(run)
    .map((r) => JSON.stringify(r))
    .join("\n");
}

/**
 * Render a human-readable comparison matrix aggregated by model — and by
 * `model@variant` when the run exercised more than the baseline prompt
 * variant (#72), so variant rows never blend into the model's baseline row.
 *
 * Skipped cells are NOT measurements (#571, ModelEvalVerdictSchema contract):
 * they never ran, so they must not be averaged with executed cells. Each row
 * aggregates executed cells only, reports the skip count in its own column,
 * and a row whose cells were ALL skipped renders `-` markers — never `0`s
 * that would be indistinguishable from a model that ran and failed
 * everything.
 */
export function formatComparisonMatrix(run: EvalRun): string {
  const multiVariant = run.cells.some((c) => c.cell.prompt_variant !== BASELINE_PROMPT_VARIANT);
  const byModel = new Map<string, EvalRun["cells"]>();
  for (const c of run.cells) {
    const key = multiVariant ? `${c.model_id}@${c.cell.prompt_variant}` : c.model_id;
    const list = byModel.get(key) ?? [];
    list.push(c);
    byModel.set(key, list);
  }
  const header = ["model", "n", "skip", "pass%", "quality", "cost$", "latency_s", "attempts"];
  const rows = [...byModel.entries()].map(([model, cells]) => {
    const executed = cells.filter((c) => c.verdict !== "skipped");
    const skipped = cells.length - executed.length;
    if (executed.length === 0) {
      return [model, "0", String(skipped), "-", "-", "-", "-", "-"];
    }
    const n = executed.length;
    const passPct = (executed.filter((c) => c.verdict === "pass").length / n) * 100;
    const quality = mean(executed.map((c) => c.score?.composite ?? 0));
    const cost = mean(executed.map((c) => c.cost_usd));
    const latency = mean(executed.map((c) => c.latency_ms)) / 1000;
    const attempts = mean(executed.map((c) => c.attempts_to_green));
    return [
      model,
      String(n),
      String(skipped),
      passPct.toFixed(0),
      quality.toFixed(1),
      cost.toFixed(4),
      latency.toFixed(1),
      attempts.toFixed(1),
    ];
  });
  return [header, ...rows].map((r) => r.join("\t")).join("\n");
}

function mean(xs: number[]): number {
  return xs.length ? xs.reduce((s, n) => s + n, 0) / xs.length : 0;
}

// ---------------------------------------------------------------------------
// Prompt-variant deltas (#72)
// ---------------------------------------------------------------------------

/** Per-(variant, model) quality delta against the baseline variant. */
export interface VariantDelta {
  model_id: string;
  prompt_variant: string;
  /** Scored cells backing this row (variant side). */
  n: number;
  /** Mean composite (0-100) across the variant's scored cells. */
  mean_composite: number;
  /** Mean composite of the same model's baseline cells. */
  baseline_composite: number;
  /**
   * `mean_composite - baseline_composite`. NEGATIVE means the variant scored
   * WORSE than baseline — a regression, which is exactly as detectable as an
   * improvement.
   */
  delta: number;
  /** Pass-rate delta (percentage points) for the same pairing. */
  pass_rate_delta: number;
}

/**
 * Compute per-(variant, model) composite-score deltas against the baseline
 * variant. Accepts any cell-result shape (an `EvalRun`'s cells or persisted
 * `ModelEvalRecord`s — records extend cells), so a variant can be compared
 * against a baseline captured on a LATER run by concatenating record sets.
 * Models with no scored baseline cells are skipped: a delta without a
 * baseline is not a measurement.
 *
 * Two row classes are excluded up front (#571):
 * - **Pre-honest persisted records** (`schema_version < 3`): their effort/
 *   thinking labels were never applied, so pooling them with honest rows —
 *   exactly what cross-run record concatenation invites — would corrupt the
 *   deltas. In-run cells (no run stamp) come from the current, honest runner.
 * - **Skipped cells**: never run, so they carry no measurement to average
 *   (ModelEvalVerdictSchema contract).
 */
export function computeVariantDeltas(cells: EvalRun["cells"] | ModelEvalRecord[]): VariantDelta[] {
  type Cell = EvalRun["cells"][number];
  const byModel = new Map<string, Map<string, Cell[]>>();
  for (const c of cells) {
    if (!isHonestExecutedRow(c)) continue;
    const variants = byModel.get(c.model_id) ?? new Map<string, Cell[]>();
    const list = variants.get(c.cell.prompt_variant) ?? [];
    list.push(c);
    variants.set(c.cell.prompt_variant, list);
    byModel.set(c.model_id, variants);
  }

  const deltas: VariantDelta[] = [];
  for (const [model_id, variants] of byModel) {
    const baselineScored = (variants.get(BASELINE_PROMPT_VARIANT) ?? []).filter((c) => c.score);
    if (baselineScored.length === 0) continue;
    const baselineComposite = mean(baselineScored.map((c) => c.score!.composite));
    const baselinePassRate = passRate(variants.get(BASELINE_PROMPT_VARIANT) ?? []);

    for (const [prompt_variant, list] of variants) {
      if (prompt_variant === BASELINE_PROMPT_VARIANT) continue;
      const scored = list.filter((c) => c.score);
      if (scored.length === 0) continue;
      const meanComposite = mean(scored.map((c) => c.score!.composite));
      deltas.push({
        model_id,
        prompt_variant,
        n: scored.length,
        mean_composite: round2(meanComposite),
        baseline_composite: round2(baselineComposite),
        delta: round2(meanComposite - baselineComposite),
        pass_rate_delta: round2(passRate(list) - baselinePassRate),
      });
    }
  }
  // Stable, comparison-friendly order: model, then variant.
  return deltas.sort(
    (a, b) =>
      a.model_id.localeCompare(b.model_id) || a.prompt_variant.localeCompare(b.prompt_variant)
  );
}

/** Render variant deltas as a human-readable table (positive = better than baseline). */
export function formatVariantDeltas(deltas: VariantDelta[]): string {
  if (deltas.length === 0) {
    return "no variant deltas (run includes no scored non-baseline cells)";
  }
  const header = ["model", "variant", "n", "quality", "baseline", "Δquality", "Δpass%"];
  const rows = deltas.map((d) => [
    d.model_id,
    d.prompt_variant,
    String(d.n),
    d.mean_composite.toFixed(1),
    d.baseline_composite.toFixed(1),
    signed(d.delta),
    signed(d.pass_rate_delta),
  ]);
  return [header, ...rows].map((r) => r.join("\t")).join("\n");
}

/**
 * Whether a row may enter an aggregation (#571): it must be an EXECUTED cell
 * (skipped cells carry no measurement), and — when it is a persisted record
 * rather than an in-run cell — it must be an honest (v3+) row. A record whose
 * version is missing or malformed fails CLOSED (`Number(...)` → NaN). In-run
 * cells carry no run stamp and are honest by construction: they were produced
 * by this very runner, which only emits the current schema version.
 */
function isHonestExecutedRow(c: EvalRun["cells"][number] | ModelEvalRecord): boolean {
  if (c.verdict === "skipped") return false;
  if ("schema_version" in c || "run_id" in c) {
    return Number((c as ModelEvalRecord).schema_version) >= MIN_HONEST_SCHEMA_VERSION;
  }
  return true;
}

/** Pass rate over EXECUTED cells only — a skipped cell is not a failed one (#571). */
function passRate(cells: EvalRun["cells"]): number {
  const executed = cells.filter((c) => c.verdict !== "skipped");
  if (executed.length === 0) return 0;
  return (executed.filter((c) => c.verdict === "pass").length / executed.length) * 100;
}

function round2(n: number): number {
  return Math.round(n * 100) / 100;
}

function signed(n: number): string {
  return `${n > 0 ? "+" : ""}${n.toFixed(1)}`;
}
