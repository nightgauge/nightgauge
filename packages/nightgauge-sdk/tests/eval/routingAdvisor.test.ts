/**
 * Tests for the eval → routing advisor (Issue #4175; envelope re-key #581).
 * Pure: builds synthetic eval records and asserts the recommendations +
 * advisory overrides they drive.
 *
 * #581 contract changes exercised here (deliberate, per the AC):
 * - aggregation keys on (job_class, model_id, effort, thinking) — different
 *   efforts of one model are separate combinations, never averaged;
 * - the per-combination sample floor is 5 (spike #568 §4.3) and sparse
 *   combinations surface as advisable:false instead of silently advising;
 * - hierarchical backoff: exact → (model, *, *) → no advice;
 * - judge-reliability low_confidence rows and registry-illegal envelopes
 *   (e.g. an effort against a model with no effort axis) never aggregate.
 */

import { describe, it, expect } from "vitest";
import { EvalRoutingAdvisor } from "../../src/eval/routingAdvisor.js";
import {
  MODEL_EVAL_SCHEMA_VERSION,
  type EffortLevel,
  type JobClass,
  type ModelEvalRecord,
  type ReasoningLevel,
} from "../../src/eval/modelEvalSchemas.js";

/** Build `n` records for one (jobClass, model, effort, reasoning) envelope. */
function records(
  jobClass: JobClass,
  modelId: string,
  quality: number,
  costUsd: number,
  n: number,
  verdict: "pass" | "fail" = "pass",
  effort: EffortLevel = "high",
  reasoning: ReasoningLevel = "none"
): ModelEvalRecord[] {
  return Array.from({ length: n }, (_, i) => ({
    task_id: `${jobClass}-${i}`,
    job_class: jobClass,
    cell: { model_id: modelId, effort, reasoning, prompt_variant: "baseline" },
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
  // sonnet@low: cheap, decent; opus-5@max: expensive, best; sonnet-5@high: mid/mid.
  // (Registry-legal envelopes only — haiku declares no effort axis, so no
  // haiku cell can exist in honest data; the interlock skips it before spawn.)
  const data = [
    ...records("ui-creation", "claude-sonnet-5", 78, 0.04, 5, "pass", "low"),
    ...records("ui-creation", "claude-sonnet-5", 88, 0.17, 5, "pass", "high"),
    ...records("ui-creation", "claude-opus-4-8", 95, 0.35, 5, "pass", "high"),
  ];
  const advisor = new EvalRoutingAdvisor(data);

  it("keys combinations on the envelope — one model at two efforts is two combinations", () => {
    const stats = advisor.statsFor("ui-creation");
    expect(stats).toHaveLength(3);
    const sonnetCombos = stats.filter((s) => s.modelId === "claude-sonnet-5");
    expect(sonnetCombos.map((s) => s.effort).sort()).toEqual(["high", "low"]);
    // The two efforts were NOT averaged together.
    expect(sonnetCombos.find((s) => s.effort === "low")?.meanQuality).toBeCloseTo(78, 5);
    expect(sonnetCombos.find((s) => s.effort === "high")?.meanQuality).toBeCloseTo(88, 5);
  });

  it("efficiency picks the cheapest envelope above the quality floor", () => {
    const rec = advisor.recommend("ui-creation", "efficiency")!;
    expect(rec.modelId).toBe("claude-sonnet-5");
    expect(rec.effort).toBe("low");
    expect(rec.thinking).toBe("off");
    expect(rec.backoff).toBe("exact");
    expect(rec.advisable).toBe(true);
  });

  it("maximum picks the highest-quality envelope", () => {
    const rec = advisor.recommend("ui-creation", "maximum")!;
    expect(rec.modelId).toBe("claude-opus-4-8");
    expect(rec.effort).toBe("high");
  });

  it("balanced picks the best quality-per-dollar", () => {
    // sonnet@low: 78/0.04 = 1950; sonnet@high: 88/0.17 ≈ 518; opus: 95/0.35 ≈ 271.
    const rec = advisor.recommend("ui-creation", "balanced")!;
    expect(rec.modelId).toBe("claude-sonnet-5");
    expect(rec.effort).toBe("low");
  });

  it("reports confidence from per-combination sample size (floor = 5, spike §4.3)", () => {
    const rec = advisor.recommend("ui-creation", "maximum")!;
    expect(rec.samples).toBe(5);
    expect(rec.confidence).toBe("medium"); // 5 samples: >= minSamples(5), < 15
  });

  it("returns undefined for a job class with no data", () => {
    expect(advisor.recommend("docs", "maximum")).toBeUndefined();
  });
});

describe("EvalRoutingAdvisor — eval data drives the recommendation", () => {
  it("changes the maximum-mode pick when the quality data changes", () => {
    const opusWins = new EvalRoutingAdvisor([
      ...records("backend-logic", "claude-opus-4-8", 96, 0.35, 5),
      ...records("backend-logic", "claude-sonnet-5", 70, 0.04, 5),
    ]);
    expect(opusWins.recommend("backend-logic", "maximum")?.modelId).toBe("claude-opus-4-8");

    // Now Sonnet is evaluated as the higher-quality model → the pick flips.
    const sonnetWins = new EvalRoutingAdvisor([
      ...records("backend-logic", "claude-opus-4-8", 80, 0.35, 5),
      ...records("backend-logic", "claude-sonnet-5", 93, 0.04, 5),
    ]);
    expect(sonnetWins.recommend("backend-logic", "maximum")?.modelId).toBe("claude-sonnet-5");
  });
});

describe("EvalRoutingAdvisor — honest-row exclusions (#571)", () => {
  it("excludes pre-v3 rows: effort was never applied, so they must not be averaged", () => {
    // Same envelope: 5 dishonest v2 rows claiming stellar quality and
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

  it("excludes judge-reliability low_confidence rows (ADR 011 §4, spike §4.3)", () => {
    const flagged = records("bugfix", "claude-opus-4-8", 99, 0.3, 4).map((r) => ({
      ...r,
      score: { ...r.score!, low_confidence: true },
    }));
    const trusted = records("bugfix", "claude-opus-4-8", 70, 0.3, 3);
    const advisor = new EvalRoutingAdvisor([...flagged, ...trusted]);

    const stats = advisor.statsFor("bugfix");
    expect(stats).toHaveLength(1);
    expect(stats[0].samples).toBe(3);
    expect(stats[0].meanQuality).toBeCloseTo(70, 5);
  });

  it("prunes registry-illegal envelopes — an effort against a model with no effort axis", () => {
    // claude-haiku-4-5-20251001 declares supported_efforts: [] (#336) — a row
    // claiming haiku@high measures an envelope that can never be dispatched.
    const illegal = records("bugfix", "claude-haiku-4-5-20251001", 99, 0.01, 6);
    const legal = records("bugfix", "claude-sonnet-5", 80, 0.1, 6);
    const advisor = new EvalRoutingAdvisor([...illegal, ...legal]);

    const stats = advisor.statsFor("bugfix");
    expect(stats).toHaveLength(1);
    expect(stats[0].modelId).toBe("claude-sonnet-5");
  });

  it("prunes thinking-off rows the disable interlock forbids (fable rejects disabled thinking)", () => {
    // claude-fable-5 declares thinking_disable_max_effort: never — a
    // reasoning:none (thinking off) fable row is an impossible envelope.
    const illegal = records("bugfix", "claude-fable-5", 99, 0.5, 6, "pass", "high", "none");
    const legal = records("bugfix", "claude-fable-5", 90, 0.5, 6, "pass", "high", "high");
    const advisor = new EvalRoutingAdvisor([...illegal, ...legal]);

    const stats = advisor.statsFor("bugfix");
    expect(stats).toHaveLength(1);
    expect(stats[0].thinking).toBe("on");
    expect(stats[0].meanQuality).toBeCloseTo(90, 5);
  });

  it("passes through models the registry does not know — no fact is invented", () => {
    const local = records("bugfix", "my-local-model", 75, 0, 6);
    const advisor = new EvalRoutingAdvisor(local);
    expect(advisor.statsFor("bugfix")).toHaveLength(1);
  });
});

describe("EvalRoutingAdvisor — sparsity and backoff (spike §4.3)", () => {
  it("marks a combination below the sample floor advisable:false and never applies it", () => {
    const thin = new EvalRoutingAdvisor([
      ...records("refactor", "claude-sonnet-5", 90, 0.04, 2, "pass", "low"),
    ]);
    const rec = thin.recommend("refactor", "efficiency")!;
    expect(rec.advisable).toBe(false);
    expect(rec.confidence).toBe("low");
    expect(thin.advise("claude-opus-4-8", "refactor", "efficiency").source).toBe("base");
  });

  it("backs off to the (model, *, *) aggregate when every exact combination is sparse", () => {
    // 3 + 3 samples across two efforts of one model: neither exact combo
    // passes the floor (5), but the model-level pool (6) does.
    const advisor = new EvalRoutingAdvisor([
      ...records("refactor", "claude-sonnet-5", 84, 0.1, 3, "pass", "low"),
      ...records("refactor", "claude-sonnet-5", 92, 0.2, 3, "pass", "high"),
    ]);
    const rec = advisor.recommend("refactor", "maximum")!;
    expect(rec.backoff).toBe("model");
    expect(rec.modelId).toBe("claude-sonnet-5");
    expect(rec.samples).toBe(6);
    // An aggregate is not a dispatchable envelope: no effort/thinking.
    expect(rec.effort).toBeUndefined();
    expect(rec.thinking).toBeUndefined();
    expect(rec.meanQuality).toBeCloseTo(88, 1); // sample-weighted mean
  });

  it("emits sparse combinations in adviceEntries with advisable:false — sparsity is visible, not silent", () => {
    const advisor = new EvalRoutingAdvisor([
      ...records("refactor", "claude-sonnet-5", 84, 0.1, 2, "pass", "low"),
      ...records("docs", "claude-sonnet-5", 84, 0.1, 6, "pass", "low"),
    ]);
    const entries = advisor.adviceEntries();
    const sparse = entries.find((e) => e.jobClass === "refactor" && e.backoff === "exact");
    const dense = entries.find((e) => e.jobClass === "docs" && e.backoff === "exact");
    expect(sparse?.advisable).toBe(false);
    expect(dense?.advisable).toBe(true);
  });
});

describe("EvalRoutingAdvisor — advisory override (opt-in)", () => {
  const advisor = new EvalRoutingAdvisor([
    ...records("bugfix", "claude-sonnet-5", 90, 0.04, 6, "pass", "low"),
    ...records("bugfix", "claude-opus-4-8", 88, 0.35, 6, "pass", "high"),
  ]);

  it("overrides the base pick with the full envelope when eval recommends a different, confident model", () => {
    const out = advisor.advise("claude-opus-4-8", "bugfix", "efficiency");
    expect(out.modelId).toBe("claude-sonnet-5");
    expect(out.effort).toBe("low");
    expect(out.thinking).toBe("off");
    expect(out.source).toBe("eval-advisory");
  });

  it("keeps the base pick when eval agrees", () => {
    const out = advisor.advise("claude-sonnet-5", "bugfix", "efficiency");
    expect(out.source).toBe("base");
  });

  it("does NOT override on low confidence (too few samples)", () => {
    const thin = new EvalRoutingAdvisor([
      ...records("refactor", "claude-sonnet-5", 90, 0.04, 1, "pass", "low"),
      ...records("refactor", "claude-opus-4-8", 80, 0.35, 1),
    ]);
    const out = thin.advise("claude-opus-4-8", "refactor", "efficiency");
    expect(out.modelId).toBe("claude-opus-4-8");
    expect(out.source).toBe("base");
  });
});
