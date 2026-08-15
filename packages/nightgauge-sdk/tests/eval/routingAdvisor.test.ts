/**
 * Tests for the eval → routing advisor (Issue #4175). Pure: builds synthetic
 * eval records and asserts the recommendations + advisory overrides they drive.
 */

import { describe, it, expect } from "vitest";
import { EvalRoutingAdvisor } from "../../src/eval/routingAdvisor.js";
import {
  MODEL_EVAL_SCHEMA_VERSION,
  type JobClass,
  type ModelEvalRecord,
} from "../../src/eval/modelEvalSchemas.js";

/** Build `n` records for (jobClass, model) with a given composite quality + cost. */
function records(
  jobClass: JobClass,
  modelId: string,
  quality: number,
  costUsd: number,
  n: number,
  verdict: "pass" | "fail" = "pass"
): ModelEvalRecord[] {
  return Array.from({ length: n }, (_, i) => ({
    task_id: `${jobClass}-${i}`,
    job_class: jobClass,
    cell: { model_id: modelId, effort: "high", reasoning: "none", prompt_variant: "baseline" },
    model_id: modelId,
    model_version_label: modelId,
    verdict,
    tokens: { input: 1000, output: 1000, cache_read: 0, cache_creation: 0 },
    cost_usd: costUsd,
    latency_ms: 30_000,
    attempts_to_green: 1,
    gate_results: [{ name: "test", passed: verdict === "pass" }],
    score: {
      composite: quality,
      correctness: verdict === "pass" ? 100 : 0,
      dimensions: [],
      judge_used: false,
    },
    schema_version: MODEL_EVAL_SCHEMA_VERSION,
    run_id: "r",
    suite: "s",
    timestamp: "t",
    mode: "mock" as const,
  }));
}

describe("EvalRoutingAdvisor — per-mode recommendations", () => {
  // haiku: cheap, decent; opus: expensive, best; sonnet: mid/mid.
  const data = [
    ...records("ui-creation", "claude-haiku-4-5-20251001", 78, 0.04, 5),
    ...records("ui-creation", "claude-sonnet-5", 88, 0.17, 5),
    ...records("ui-creation", "claude-opus-4-8", 95, 0.35, 5),
  ];
  const advisor = new EvalRoutingAdvisor(data);

  it("efficiency picks the cheapest model above the quality floor", () => {
    expect(advisor.recommend("ui-creation", "efficiency")?.modelId).toBe(
      "claude-haiku-4-5-20251001"
    );
  });

  it("maximum picks the highest-quality model", () => {
    expect(advisor.recommend("ui-creation", "maximum")?.modelId).toBe("claude-opus-4-8");
  });

  it("balanced picks the best quality-per-dollar", () => {
    // haiku: 78/0.04 = 1950; sonnet: 88/0.17 ≈ 518; opus: 95/0.35 ≈ 271 → haiku wins.
    expect(advisor.recommend("ui-creation", "balanced")?.modelId).toBe("claude-haiku-4-5-20251001");
  });

  it("reports confidence from sample size", () => {
    const rec = advisor.recommend("ui-creation", "maximum")!;
    expect(rec.samples).toBe(5);
    expect(rec.confidence).toBe("medium"); // 5 samples: >= minSamples(3), < 9
  });

  it("returns undefined for a job class with no data", () => {
    expect(advisor.recommend("docs", "maximum")).toBeUndefined();
  });
});

describe("EvalRoutingAdvisor — eval data drives the recommendation", () => {
  it("changes the maximum-mode pick when the quality data changes", () => {
    const opusWins = new EvalRoutingAdvisor([
      ...records("backend-logic", "claude-opus-4-8", 96, 0.35, 5),
      ...records("backend-logic", "claude-haiku-4-5-20251001", 70, 0.04, 5),
    ]);
    expect(opusWins.recommend("backend-logic", "maximum")?.modelId).toBe("claude-opus-4-8");

    // Now Haiku is evaluated as the higher-quality model → the recommendation flips.
    const haikuWins = new EvalRoutingAdvisor([
      ...records("backend-logic", "claude-opus-4-8", 80, 0.35, 5),
      ...records("backend-logic", "claude-haiku-4-5-20251001", 93, 0.04, 5),
    ]);
    expect(haikuWins.recommend("backend-logic", "maximum")?.modelId).toBe(
      "claude-haiku-4-5-20251001"
    );
  });
});

describe("EvalRoutingAdvisor — honest-row exclusions (#571)", () => {
  it("excludes pre-v3 rows: effort was never applied, so they must not be averaged", () => {
    // Same (jobClass, model): 5 dishonest v2 rows claiming stellar quality and
    // 3 honest v3 rows. Only the v3 rows may count.
    const v2Rows = records("bugfix", "claude-opus-4-8", 99, 0.01, 5).map((r) => ({
      ...r,
      schema_version: "2",
    })) as unknown as ModelEvalRecord[];
    const v3Rows = records("bugfix", "claude-opus-4-8", 60, 0.35, 3);
    const advisor = new EvalRoutingAdvisor([...v2Rows, ...v3Rows]);

    const stats = advisor.statsFor("bugfix");
    expect(stats).toHaveLength(1);
    expect(stats[0].samples).toBe(3); // v2 rows never entered the aggregate
    expect(stats[0].meanQuality).toBeCloseTo(60, 5);
  });

  it("mutes the advisor entirely when only pre-v3 data exists", () => {
    const v2Only = records("bugfix", "claude-opus-4-8", 95, 0.3, 6).map((r) => ({
      ...r,
      schema_version: "2",
    })) as unknown as ModelEvalRecord[];
    const advisor = new EvalRoutingAdvisor(v2Only);
    expect(advisor.recommend("bugfix", "maximum")).toBeUndefined();
    expect(advisor.advise("claude-sonnet-5", "bugfix", "maximum").source).toBe("base");
  });

  it("fails closed on a missing or malformed schema_version — unknown provenance never aggregates", () => {
    const noVersion = records("bugfix", "claude-opus-4-8", 95, 0.3, 4).map((r) => {
      const { schema_version: _dropped, ...rest } = r;
      return rest;
    }) as unknown as ModelEvalRecord[];
    const garbageVersion = records("bugfix", "claude-opus-4-8", 95, 0.3, 4).map((r) => ({
      ...r,
      schema_version: "not-a-number",
    })) as unknown as ModelEvalRecord[];
    const advisor = new EvalRoutingAdvisor([...noVersion, ...garbageVersion]);
    expect(advisor.statsFor("bugfix")).toHaveLength(0);
  });

  it("excludes skipped cells — they carry no measurement", () => {
    const skipped = records("bugfix", "claude-opus-4-8", 0, 0, 4).map((r) => ({
      ...r,
      verdict: "skipped" as const,
      skip_reason: "effort 'max' is not supported by model 'claude-opus-4-8'",
      score: undefined,
    }));
    const executed = records("bugfix", "claude-opus-4-8", 80, 0.3, 3);
    const advisor = new EvalRoutingAdvisor([...skipped, ...executed]);

    const stats = advisor.statsFor("bugfix");
    expect(stats).toHaveLength(1);
    expect(stats[0].samples).toBe(3);
    // Skipped rows would have dragged passRate to 3/7 and quality toward 0.
    expect(stats[0].passRate).toBe(1);
    expect(stats[0].meanQuality).toBeCloseTo(80, 5);
  });
});

describe("EvalRoutingAdvisor — advisory override (opt-in)", () => {
  const advisor = new EvalRoutingAdvisor([
    ...records("bugfix", "claude-haiku-4-5-20251001", 90, 0.04, 6),
    ...records("bugfix", "claude-opus-4-8", 88, 0.35, 6),
  ]);

  it("overrides the base pick when eval recommends a different, confident model", () => {
    const out = advisor.advise("claude-opus-4-8", "bugfix", "efficiency");
    expect(out.modelId).toBe("claude-haiku-4-5-20251001");
    expect(out.source).toBe("eval-advisory");
  });

  it("keeps the base pick when eval agrees", () => {
    const out = advisor.advise("claude-haiku-4-5-20251001", "bugfix", "efficiency");
    expect(out.source).toBe("base");
  });

  it("does NOT override on low confidence (too few samples)", () => {
    const thin = new EvalRoutingAdvisor([
      ...records("refactor", "claude-haiku-4-5-20251001", 90, 0.04, 1),
      ...records("refactor", "claude-opus-4-8", 80, 0.35, 1),
    ]);
    const out = thin.advise("claude-opus-4-8", "refactor", "efficiency");
    expect(out.modelId).toBe("claude-opus-4-8");
    expect(out.source).toBe("base");
  });
});
