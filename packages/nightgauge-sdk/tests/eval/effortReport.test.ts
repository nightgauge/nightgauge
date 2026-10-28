/**
 * Tests for the effort report and effort_default recommendation (Issue #2119).
 * Pure: synthetic sweep records in, cells / recommendation / patch out.
 */

import { readFileSync } from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, it, expect } from "vitest";
import {
  buildEffortDefaultPatch,
  buildEffortReport,
  formatEffortReport,
  parseSweepRecords,
} from "../../src/eval/effortReport.js";
import {
  MODEL_EVAL_SCHEMA_VERSION,
  type EffortLevel,
  type ModelEvalRecord,
} from "../../src/eval/modelEvalSchemas.js";

const HASH = "sha-control";
const opts = { modelId: "m", tolerance: 5, minSamples: 3 };

interface Cell {
  effort: EffortLevel;
  quality: number;
  cost: number;
  n?: number;
  objective?: boolean;
  wall?: number;
  model?: string;
  hash?: string;
  suite?: string;
}

function recs(c: Cell): ModelEvalRecord[] {
  const model = c.model ?? "m";
  return Array.from({ length: c.n ?? 3 }, (_, i) => ({
    task_id: "control",
    job_class: "backend-logic" as const,
    cell: { model_id: model, effort: c.effort, reasoning: "none", prompt_variant: "baseline" },
    model_id: model,
    model_version_label: model,
    verdict: c.objective === false ? "fail" : "pass",
    tokens: { input: 0, output: 0, cache_read: 0, cache_creation: 0 },
    cost_usd: c.cost,
    latency_ms: 0,
    attempts_to_green: 1,
    gate_results: [],
    score: {
      composite: c.quality + i - ((c.n ?? 3) - 1) / 2,
      correctness: 100,
      dimensions: [],
      judge_used: false,
    },
    control_hash: c.hash ?? HASH,
    wall_clock_seconds: c.wall ?? 100,
    objective_passed: c.objective ?? true,
    schema_version: MODEL_EVAL_SCHEMA_VERSION,
    run_id: "r",
    suite: c.suite ?? "effort-sweep",
    timestamp: `2026-10-0${i + 1}T00:00:00Z`,
    mode: "live" as const,
  }));
}

const report = (...cells: Cell[]) => buildEffortReport(cells.flatMap(recs), opts);

describe("recommendEffort via buildEffortReport", () => {
  it("recommends the cheapest effort within tolerance of the best", () => {
    const r = report(
      { effort: "low", quality: 88, cost: 1 },
      { effort: "medium", quality: 90, cost: 2 },
      { effort: "high", quality: 91, cost: 4 }
    );
    expect(r.recommendation).toMatchObject({ status: "recommended", effort: "low" });
  });

  it("skips an effort that failed the objective criteria", () => {
    const r = report(
      { effort: "low", quality: 95, cost: 1, objective: false },
      { effort: "medium", quality: 90, cost: 2 },
      { effort: "high", quality: 91, cost: 4 }
    );
    expect(r.recommendation).toMatchObject({ status: "recommended", effort: "medium" });
  });

  it("treats quality exactly at best - tolerance as within tolerance", () => {
    const r = report(
      { effort: "low", quality: 85, cost: 1 },
      { effort: "high", quality: 90, cost: 4 }
    );
    expect(r.recommendation).toMatchObject({ status: "recommended", effort: "low" });
    const out = report(
      { effort: "low", quality: 84.9, cost: 1 },
      { effort: "high", quality: 90, cost: 4 }
    );
    expect(out.recommendation).toMatchObject({ status: "recommended", effort: "high" });
  });

  it("marks n=1 low-confidence with no variance and refuses a recommendation it could change", () => {
    const r = report(
      { effort: "low", quality: 90, cost: 1, n: 1 },
      { effort: "high", quality: 90, cost: 4 }
    );
    const low = r.cells.find((c) => c.effort === "low")!;
    expect(low).toMatchObject({ n: 1, confidence: "low", quality_variance: null });
    expect(r.cells.find((c) => c.effort === "high")!.quality_variance).toBe(1);
    expect(r.recommendation).toMatchObject({
      status: "insufficient-data",
      effort: null,
      under_sampled: ["low"],
    });
    expect(formatEffortReport(r)).toContain("low-confidence");
  });

  it("reports insufficient-data when no cell has enough samples", () => {
    const r = report({ effort: "low", quality: 90, cost: 1, n: 1 });
    expect(r.recommendation.status).toBe("insufficient-data");
  });

  it("reports no-passing-effort when every sampled cell failed the objective", () => {
    const r = report({ effort: "low", quality: 90, cost: 1, objective: false });
    expect(r.recommendation.status).toBe("no-passing-effort");
  });

  it("reports a tie on equal cost and wall time instead of picking one", () => {
    const r = report(
      { effort: "low", quality: 90, cost: 1 },
      { effort: "medium", quality: 90, cost: 1 }
    );
    expect(r.recommendation).toMatchObject({
      status: "tie",
      effort: null,
      tied: ["low", "medium"],
    });
    const byWall = report(
      { effort: "low", quality: 90, cost: 1, wall: 200 },
      { effort: "medium", quality: 90, cost: 1, wall: 100 }
    );
    expect(byWall.recommendation).toMatchObject({ status: "recommended", effort: "medium" });
  });

  it("excludes other control hashes and suites, and compares other models on the hash", () => {
    const r = report(
      { effort: "low", quality: 90, cost: 1 },
      { effort: "high", quality: 10, cost: 0.1, hash: "old" },
      { effort: "high", quality: 10, cost: 0.1, suite: "matrix" },
      { effort: "medium", quality: 92, cost: 3, model: "other" }
    );
    expect(r.control_hash).toBe(HASH);
    expect(r.cells.map((c) => c.effort)).toEqual(["low"]);
    expect(r.excluded).toEqual({ not_sweep: 3, other_control_hash: 3 });
    expect(r.comparison).toHaveLength(1);
    expect(r.comparison[0]).toMatchObject({ model_id: "other", best: { effort: "medium" } });
  });

  it("parses JSONL and counts invalid lines", () => {
    const lines = recs({ effort: "low", quality: 90, cost: 1, n: 2 }).map((r) => JSON.stringify(r));
    const parsed = parseSweepRecords([...lines, "not json", "{}", ""].join("\n"));
    expect(parsed.records).toHaveLength(2);
    expect(parsed.invalid).toBe(2);
  });
});

describe("buildEffortDefaultPatch", () => {
  const here = path.dirname(fileURLToPath(import.meta.url));
  const sdkPath = path.resolve(here, "../../src/eval/model-registry.json");
  const goPath = path.resolve(here, "../../../../internal/models/model-registry.json");
  const text = readFileSync(sdkPath, "utf-8");
  const rel = "packages/nightgauge-sdk/src/eval/model-registry.json";

  it("changes exactly the target model's effort_default", () => {
    const patch = buildEffortDefaultPatch(text, rel, "claude-opus-4-8", "medium");
    expect(patch.split("\n").filter((l) => /^[-+]\s/.test(l))).toEqual([
      '-        "effort_default": "high",',
      '+        "effort_default": "medium",',
    ]);
    // Apply the hunk and confirm only that field moved.
    const lines = text.split("\n");
    const at = Number(/@@ -(\d+),/.exec(patch)![1]) - 1 + 3;
    lines[at] = lines[at].replace('"high"', '"medium"');
    const before = JSON.parse(text);
    const after = JSON.parse(lines.join("\n"));
    const idx = before.models.findIndex((m: { id: string }) => m.id === "claude-opus-4-8");
    expect(after.models[idx].behavior.effort_default).toBe("medium");
    after.models[idx].behavior.effort_default = "high";
    expect(after).toEqual(before);
  });

  it("produces the same hunk for the byte-identical Go copy", () => {
    const goRel = "internal/models/model-registry.json";
    const a = buildEffortDefaultPatch(text, rel, "claude-opus-4-8", "medium");
    const b = buildEffortDefaultPatch(
      readFileSync(goPath, "utf-8"),
      goRel,
      "claude-opus-4-8",
      "medium"
    );
    expect(b).toBe(a.split(rel).join(goRel));
  });

  it("is empty when the default already matches and throws on an unknown model", () => {
    expect(buildEffortDefaultPatch(text, rel, "claude-opus-4-8", "high")).toBe("");
    expect(() => buildEffortDefaultPatch(text, rel, "no-such-model", "low")).toThrow(/not found/);
  });
});
