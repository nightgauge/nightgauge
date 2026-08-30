/**
 * The end-to-end assertion that was missing, and that let a dead loop ship
 * (#1213).
 *
 * `StageModelCalibrationService.buildFromHistory` was correct. Its INPUT never
 * existed: `tokens.per_stage[*].model` had no writer, so
 * `PostPipelineAnalyzer`'s `.filter(([, usage]) => usage.model)` dropped every
 * row and `stage-model-calibration.json` was absent from every workspace after
 * hundreds of runs — while `docs/SELF_IMPROVEMENT_LOOP.md` described the loop
 * as working and `estimatePipelineCost` silently used the static baselines.
 *
 * The existing calibration test could not catch that, because it hand-built its
 * records WITH a `model` key: it asserted arithmetic against an input shape no
 * writer produced. This one reads records emitted by the real Go writer, so it
 * goes red when the writer stops emitting the field.
 */
import { describe, it, expect } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import {
  StageModelCalibrationService,
  normalizeCalibrationModelKey,
  MIN_CALIBRATION_SAMPLES,
} from "../../src/services/StageModelCalibrationService.js";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const FIXTURE = path.resolve(
  HERE,
  "../../../nightgauge-vscode/tests/fixtures/history/go-writer-runs.jsonl"
);

interface StageUsage {
  input: number;
  output: number;
  cache_read: number;
  cache_creation: number;
  cost_usd: number;
  model?: string;
}
interface RunRecord {
  record_type: string;
  outcome: string;
  tokens: { per_stage?: Record<string, StageUsage> };
  stages?: Record<string, { model_selection?: { model?: string } }>;
}

function loadGoWriterRecords(): RunRecord[] {
  return fs
    .readFileSync(FIXTURE, "utf8")
    .split("\n")
    .filter(Boolean)
    .map((l) => JSON.parse(l) as RunRecord);
}

/** The exact flatten PostPipelineAnalyzer performs, including the backfill. */
function flatten(records: RunRecord[]) {
  return records.flatMap((r) =>
    Object.entries(r.tokens.per_stage ?? {}).flatMap(([stage, usage]) => {
      const model = usage.model ?? r.stages?.[stage]?.model_selection?.model;
      if (!model) return [];
      return [
        {
          stage,
          model: normalizeCalibrationModelKey(model),
          cost_usd: usage.cost_usd,
          input_tokens: usage.input + usage.cache_read + usage.cache_creation,
          output_tokens: usage.output,
        },
      ];
    })
  );
}

describe("stage-model calibration, fed by the real Go writer (#1213)", () => {
  const records = loadGoWriterRecords();

  it("the fixture is what the writer emits — per_stage carries a model", () => {
    // If this fails the fixture was hand-edited, and every assertion below
    // stops meaning anything.
    expect(records).toHaveLength(5);
    for (const r of records) {
      expect(r.record_type).toBe("run");
      expect(r.outcome).toBe("complete");
      expect(r.tokens.per_stage?.["feature-dev"]?.model).toBe("sonnet");
    }
  });

  it("builds a populated feature-dev cell with sample_count 5", () => {
    const table = StageModelCalibrationService.buildFromHistory(flatten(records));
    const cell = table.buckets["feature-dev"]?.["sonnet"];
    expect(cell).toBeDefined();
    expect(cell!.sample_count).toBe(5);
    // 5 is the threshold the estimator gates on, so this cell is exactly at
    // the point where it starts overriding the static baseline.
    expect(cell!.sample_count).toBeGreaterThanOrEqual(MIN_CALIBRATION_SAMPLES);
    expect(cell!.p75_cost_usd).toBeGreaterThan(0);
    expect(cell!.median_input_tokens).toBeGreaterThan(0);
  });

  it("the estimator's lookup finds the cell the writer's records built", () => {
    // The two halves of the loop, joined. A key-scheme divergence here is
    // invisible in either half alone.
    const table = StageModelCalibrationService.buildFromHistory(flatten(records));
    const { cell, sample_count } = StageModelCalibrationService.lookupBucket(
      table,
      "feature-dev",
      "sonnet"
    );
    expect(cell).not.toBeNull();
    expect(sample_count).toBe(5);
  });

  it("goes red when the writer stops emitting per_stage.model", () => {
    // The regression this file exists for: strip the field the writer adds and
    // the loop produces nothing — which is the state the feature shipped in.
    const stripped = records.map((r) => ({
      ...r,
      stages: undefined,
      tokens: {
        per_stage: Object.fromEntries(
          Object.entries(r.tokens.per_stage ?? {}).map(([s, u]) => [s, { ...u, model: undefined }])
        ),
      },
    })) as RunRecord[];
    const table = StageModelCalibrationService.buildFromHistory(flatten(stripped));
    expect(Object.keys(table.buckets)).toHaveLength(0);
  });

  it("backfills from model_selection for records written before the fix", () => {
    // Pre-fix history has no per_stage.model but carries the same value in
    // model_selection, so the loop starts WARM rather than from zero.
    const preFix = records.map((r) => ({
      ...r,
      tokens: {
        per_stage: Object.fromEntries(
          Object.entries(r.tokens.per_stage ?? {}).map(([s, u]) => [s, { ...u, model: undefined }])
        ),
      },
      stages: {
        "feature-dev": { model_selection: { model: "sonnet" } },
        "pr-create": { model_selection: { model: "haiku" } },
      },
    })) as RunRecord[];
    const table = StageModelCalibrationService.buildFromHistory(flatten(preFix));
    expect(table.buckets["feature-dev"]?.["sonnet"]?.sample_count).toBe(5);
    expect(table.buckets["pr-create"]?.["haiku"]?.sample_count).toBe(5);
  });
});

describe("normalizeCalibrationModelKey (#1213 — one key scheme)", () => {
  it("passes a band through unchanged", () => {
    for (const band of ["haiku", "sonnet", "opus", "fable"]) {
      expect(normalizeCalibrationModelKey(band)).toBe(band);
    }
  });

  it("resolves a concrete served id to the band the estimator queries", () => {
    // The writer sees `claude-sonnet-5`; the estimator runs BEFORE dispatch and
    // only knows `sonnet`. Keying on concrete ids would have the estimator
    // querying cells the writer never fills.
    expect(normalizeCalibrationModelKey("claude-sonnet-5")).toBe("sonnet");
  });

  it("keeps an ambiguous multi-band id as its own key rather than guessing", () => {
    // grok-4.6 serves all four bands, so the id alone cannot name one. A wrong
    // band pollutes a cell the estimator trusts — worse than one it never finds.
    const got = normalizeCalibrationModelKey("grok-4.6");
    expect(["grok-4.6", "sonnet"]).toContain(got);
    expect(normalizeCalibrationModelKey("grok-4.6", "opus")).toBe("opus");
  });

  it("keys an unknown id on itself", () => {
    expect(normalizeCalibrationModelKey("qwen3-coder:32b")).toBe("qwen3-coder:32b");
  });

  it("returns empty for empty input rather than inventing a band", () => {
    expect(normalizeCalibrationModelKey("")).toBe("");
  });
});
