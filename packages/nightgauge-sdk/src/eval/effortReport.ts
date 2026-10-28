/**
 * Effort report and `effort_default` recommendation over control-sweep
 * records (Issue #2119).
 *
 * Reads the JSONL records `nightgauge eval sweep` (#2118) writes, aggregates
 * them per effort on one control hash, compares with other models swept on
 * the same hash, and applies one deterministic recommendation rule. Pure: the
 * runner (`scripts/eval-report.ts`) does the I/O.
 *
 * @see docs/MODEL_EVALUATION.md - "Effort report and registry recommendation"
 */

import {
  EFFORT_LEVELS,
  ModelEvalRecordSchema,
  type EffortLevel,
  type ModelEvalRecord,
} from "./modelEvalSchemas.js";

/** The suite `eval sweep` stamps on every record. */
export const EFFORT_SWEEP_SUITE = "effort-sweep";
/** Default quality tolerance, in composite points (0–100). */
export const DEFAULT_EFFORT_TOLERANCE = 5;
/** Default per-cell sample floor below which a cell is low-confidence. */
export const DEFAULT_EFFORT_MIN_SAMPLES = 3;

/** Aggregate of every run of one (model, effort) on one control hash. */
export interface EffortCell {
  effort: EffortLevel;
  n: number;
  /** Share of runs whose verdict was `pass`. */
  pass_rate: number;
  /** Share of runs that met the control's `pass_criteria`. */
  objective_pass_rate: number;
  /** Mean composite score over scored runs; null when none was scored. */
  quality_mean: number | null;
  /** Sample variance of the composite score; null below two scored runs. */
  quality_variance: number | null;
  cost_mean_usd: number;
  /** Mean wall time over runs that recorded one; null when none did. */
  wall_mean_seconds: number | null;
  attempts_mean: number;
  /** `low` when n is below the sample floor (always for n = 1). */
  confidence: "low" | "ok";
}

export type EffortRecommendationStatus =
  "recommended" | "tie" | "insufficient-data" | "no-passing-effort";

export interface EffortRecommendation {
  status: EffortRecommendationStatus;
  /** Set only when status is `recommended`. */
  effort: EffortLevel | null;
  /** The two efforts that tied, when status is `tie`. */
  tied: EffortLevel[];
  /** Efforts below the sample floor that blocked a recommendation. */
  under_sampled: EffortLevel[];
  reason: string;
}

export interface EffortRecommendationOptions {
  tolerance: number;
  minSamples: number;
}

/** Another model swept on the same control hash. */
export interface ModelComparison {
  model_id: string;
  /** Highest-quality cell that met the sample floor and the objective criteria. */
  best: EffortCell | null;
  /** That model's own recommendation under the same rule. */
  recommendation: EffortRecommendation;
}

export interface EffortReport {
  model_id: string;
  control_hash: string | null;
  tolerance: number;
  min_samples: number;
  /** Execution modes of the input records (`mock` means stub data). */
  modes: string[];
  cells: EffortCell[];
  recommendation: EffortRecommendation;
  comparison: ModelComparison[];
  excluded: {
    /** Records from another suite, without a control hash, or skipped. */
    not_sweep: number;
    /** Sweep records on a different control hash. */
    other_control_hash: number;
  };
}

/** Parse JSONL text; lines that fail the record schema are counted, not dropped silently. */
export function parseSweepRecords(text: string): { records: ModelEvalRecord[]; invalid: number } {
  const records: ModelEvalRecord[] = [];
  let invalid = 0;
  for (const line of text.split("\n")) {
    if (line.trim() === "") continue;
    let json: unknown;
    try {
      json = JSON.parse(line);
    } catch {
      invalid++;
      continue;
    }
    const parsed = ModelEvalRecordSchema.safeParse(json);
    if (parsed.success) records.push(parsed.data);
    else invalid++;
  }
  return { records, invalid };
}

const mean = (xs: number[]): number => xs.reduce((a, b) => a + b, 0) / xs.length;

function sampleVariance(xs: number[]): number | null {
  if (xs.length < 2) return null;
  const m = mean(xs);
  return xs.reduce((a, x) => a + (x - m) ** 2, 0) / (xs.length - 1);
}

/** Aggregate one model's records into cells, in effort-ladder order. */
export function aggregateEffortCells(records: ModelEvalRecord[], minSamples: number): EffortCell[] {
  const byEffort = new Map<EffortLevel, ModelEvalRecord[]>();
  for (const r of records) {
    const list = byEffort.get(r.cell.effort) ?? [];
    list.push(r);
    byEffort.set(r.cell.effort, list);
  }
  const cells: EffortCell[] = [];
  for (const effort of EFFORT_LEVELS) {
    const rs = byEffort.get(effort);
    if (!rs) continue;
    const quality = rs.flatMap((r) => (r.score ? [r.score.composite] : []));
    const walls = rs.flatMap((r) =>
      r.wall_clock_seconds !== undefined ? [r.wall_clock_seconds] : []
    );
    cells.push({
      effort,
      n: rs.length,
      pass_rate: rs.filter((r) => r.verdict === "pass").length / rs.length,
      objective_pass_rate: rs.filter((r) => r.objective_passed === true).length / rs.length,
      quality_mean: quality.length > 0 ? mean(quality) : null,
      quality_variance: sampleVariance(quality),
      // Failed and timed-out cells keep their real cost (#2118 ADR-003).
      cost_mean_usd: mean(rs.map((r) => r.cost_usd)),
      wall_mean_seconds: walls.length > 0 ? mean(walls) : null,
      attempts_mean: mean(rs.map((r) => r.attempts_to_green)),
      confidence: rs.length < minSamples ? "low" : "ok",
    });
  }
  return cells;
}

const wallKey = (c: EffortCell): number => c.wall_mean_seconds ?? Number.POSITIVE_INFINITY;

/**
 * The recommendation rule. Eligible cells have n >= minSamples, every run
 * meeting the objective criteria, and a quality score. Candidates are eligible
 * cells within `tolerance` (inclusive) of the best eligible quality, ordered
 * by mean cost then mean wall time. A cheaper effort that is under-sampled
 * could have won, so it blocks the recommendation; so does an exact tie.
 */
export function recommendEffort(
  cells: EffortCell[],
  opts: EffortRecommendationOptions
): EffortRecommendation {
  const none = { effort: null, tied: [], under_sampled: [] as EffortLevel[] };
  const underSampled = cells.filter((c) => c.n < opts.minSamples);
  const eligible = cells.filter(
    (c) => c.n >= opts.minSamples && c.objective_pass_rate === 1 && c.quality_mean !== null
  );
  if (eligible.length === 0) {
    const sampled = cells.filter((c) => c.n >= opts.minSamples);
    if (sampled.length > 0 && underSampled.length === 0) {
      return {
        ...none,
        status: "no-passing-effort",
        reason: `every effort with n >= ${opts.minSamples} failed the objective criteria`,
      };
    }
    return {
      ...none,
      status: "insufficient-data",
      under_sampled: underSampled.map((c) => c.effort),
      reason: `no effort has n >= ${opts.minSamples} runs that all met the objective criteria`,
    };
  }
  const best = Math.max(...eligible.map((c) => c.quality_mean as number));
  const candidates = eligible
    .filter((c) => (c.quality_mean as number) >= best - opts.tolerance)
    .sort((a, b) => a.cost_mean_usd - b.cost_mean_usd || wallKey(a) - wallKey(b));
  const pick = candidates[0];
  const blockers = underSampled.filter((c) => c.cost_mean_usd < pick.cost_mean_usd);
  if (blockers.length > 0) {
    return {
      ...none,
      status: "insufficient-data",
      under_sampled: blockers.map((c) => c.effort),
      reason:
        `${blockers.map((c) => c.effort).join(", ")} costs less than ${pick.effort} ` +
        `but has fewer than ${opts.minSamples} runs`,
    };
  }
  const second = candidates[1];
  if (second && second.cost_mean_usd === pick.cost_mean_usd && wallKey(second) === wallKey(pick)) {
    return {
      ...none,
      status: "tie",
      tied: [pick.effort, second.effort],
      reason: `${pick.effort} and ${second.effort} have equal mean cost and wall time`,
    };
  }
  return {
    ...none,
    status: "recommended",
    effort: pick.effort,
    reason:
      `cheapest effort within ${opts.tolerance} points of the best quality ` +
      `(${best.toFixed(1)}) that met the objective criteria in every run`,
  };
}

export interface BuildEffortReportOptions extends EffortRecommendationOptions {
  modelId: string;
  /** Control hash to report on; defaults to the newest record's for the model. */
  controlHash?: string;
}

/** Filter, aggregate and recommend. Records on other hashes or suites are counted, not used. */
export function buildEffortReport(
  records: ModelEvalRecord[],
  opts: BuildEffortReportOptions
): EffortReport {
  const sweep = records.filter(
    (r) => r.suite === EFFORT_SWEEP_SUITE && r.control_hash !== undefined && r.verdict !== "skipped"
  );
  let hash = opts.controlHash ?? null;
  if (hash === null) {
    const own = sweep
      .filter((r) => r.model_id === opts.modelId)
      .sort((a, b) => b.timestamp.localeCompare(a.timestamp));
    hash = own[0]?.control_hash ?? null;
  }
  const onHash = sweep.filter((r) => r.control_hash === hash);
  const ruleOpts = { tolerance: opts.tolerance, minSamples: opts.minSamples };
  const own = onHash.filter((r) => r.model_id === opts.modelId);
  const cells = aggregateEffortCells(own, opts.minSamples);

  const others = [...new Set(onHash.map((r) => r.model_id))]
    .filter((m) => m !== opts.modelId)
    .sort();
  const comparison = others.map((model_id): ModelComparison => {
    const theirs = aggregateEffortCells(
      onHash.filter((r) => r.model_id === model_id),
      opts.minSamples
    );
    const eligible = theirs.filter(
      (c) => c.n >= opts.minSamples && c.objective_pass_rate === 1 && c.quality_mean !== null
    );
    const best = eligible.reduce<EffortCell | null>(
      (b, c) => (b === null || (c.quality_mean as number) > (b.quality_mean as number) ? c : b),
      null
    );
    return { model_id, best, recommendation: recommendEffort(theirs, ruleOpts) };
  });

  return {
    model_id: opts.modelId,
    control_hash: hash,
    tolerance: opts.tolerance,
    min_samples: opts.minSamples,
    modes: [...new Set(own.map((r) => r.mode))].sort(),
    cells,
    recommendation: recommendEffort(cells, ruleOpts),
    comparison,
    excluded: {
      not_sweep: records.length - sweep.length,
      other_control_hash: sweep.length - onHash.length,
    },
  };
}

const fmt = (v: number | null, digits: number): string => (v === null ? "n/a" : v.toFixed(digits));

/** Human-readable report: per-effort table, recommendation, comparison. */
export function formatEffortReport(report: EffortReport, currentDefault?: string): string {
  const out: string[] = [
    `Effort report: ${report.model_id} on control ${report.control_hash ?? "(none)"}`,
    `rule: tolerance ${report.tolerance} points, min samples ${report.min_samples}` +
      (report.modes.length > 0 ? ` | modes: ${report.modes.join(", ")}` : ""),
    "",
    "effort      n  pass  objective  quality  variance  cost_usd   wall_s  attempts  confidence",
  ];
  for (const c of report.cells) {
    out.push(
      [
        c.effort.padEnd(8),
        String(c.n).padStart(4),
        fmt(c.pass_rate, 2).padStart(5),
        fmt(c.objective_pass_rate, 2).padStart(10),
        fmt(c.quality_mean, 1).padStart(8),
        fmt(c.quality_variance, 1).padStart(9),
        c.cost_mean_usd.toFixed(4).padStart(9),
        fmt(c.wall_mean_seconds, 0).padStart(8),
        c.attempts_mean.toFixed(1).padStart(9),
        (c.confidence === "low" ? "low-confidence" : "ok").padStart(15),
      ].join(" ")
    );
  }
  if (report.cells.length === 0) out.push("(no sweep records for this model on this control hash)");
  const r = report.recommendation;
  out.push("", `recommendation: ${r.status}${r.effort ? ` → ${r.effort}` : ""} (${r.reason})`);
  if (currentDefault !== undefined) out.push(`current effort_default: ${currentDefault}`);
  if (report.comparison.length > 0) {
    out.push("", "comparison on the same control hash:");
    for (const m of report.comparison) {
      const b = m.best;
      out.push(
        `  ${m.model_id}: ` +
          (b
            ? `best ${b.effort} quality ${fmt(b.quality_mean, 1)} cost $${b.cost_mean_usd.toFixed(4)} ` +
              `wall ${fmt(b.wall_mean_seconds, 0)}s n=${b.n}`
            : "no eligible effort") +
          ` | ${m.recommendation.status}${m.recommendation.effort ? ` → ${m.recommendation.effort}` : ""}`
      );
    }
  }
  const { not_sweep, other_control_hash } = report.excluded;
  if (not_sweep + other_control_hash > 0) {
    out.push("", `excluded: ${not_sweep} non-sweep, ${other_control_hash} on another control hash`);
  }
  return out.join("\n");
}

const EFFORT_DEFAULT_LINE = /^(\s*"effort_default":\s*)"([^"]*)"(,?)$/;

/**
 * Build a `git apply`-compatible unified diff that changes only the
 * `behavior.effort_default` line of `modelId` in one registry file. Returns ""
 * when the value already equals `effort`. Throws when the model or its
 * `effort_default` line cannot be located.
 */
export function buildEffortDefaultPatch(
  registryText: string,
  relPath: string,
  modelId: string,
  effort: EffortLevel
): string {
  const lines = registryText.split("\n");
  const idLine = lines.findIndex((l) => l.trim() === `"id": ${JSON.stringify(modelId)},`);
  if (idLine < 0) throw new Error(`model '${modelId}' not found in ${relPath}`);
  let target = -1;
  for (let i = idLine + 1; i < lines.length; i++) {
    if (/^\s*"id":/.test(lines[i])) break;
    if (EFFORT_DEFAULT_LINE.test(lines[i])) {
      target = i;
      break;
    }
  }
  if (target < 0) throw new Error(`model '${modelId}' has no effort_default in ${relPath}`);
  const m = EFFORT_DEFAULT_LINE.exec(lines[target]) as RegExpExecArray;
  if (m[2] === effort) return "";
  const replaced = `${m[1]}"${effort}"${m[3]}`;

  const start = Math.max(0, target - 3);
  const end = Math.min(lines.length - 1, target + 3);
  const before = lines.slice(start, target).map((l) => ` ${l}`);
  const after = lines.slice(target + 1, end + 1).map((l) => ` ${l}`);
  const count = end - start + 1;
  return [
    `diff --git a/${relPath} b/${relPath}`,
    `--- a/${relPath}`,
    `+++ b/${relPath}`,
    `@@ -${start + 1},${count} +${start + 1},${count} @@`,
    ...before,
    `-${lines[target]}`,
    `+${replaced}`,
    ...after,
    "",
  ].join("\n");
}
