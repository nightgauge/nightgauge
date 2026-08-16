/**
 * Routing-advice data file (#581 / spike #568 §4.2): build → write → read →
 * pick, the advisor→resolver handoff both resolvers consume.
 */

import { describe, it, expect } from "vitest";
import { mkdtempSync, rmSync, writeFileSync, mkdirSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import {
  buildRoutingAdvice,
  pickAdvice,
  readRoutingAdvice,
  writeRoutingAdvice,
  ROUTING_ADVICE_RELATIVE_PATH,
  ROUTING_ADVICE_SCHEMA_VERSION,
} from "../../src/eval/routingAdvice.js";
import {
  MODEL_EVAL_SCHEMA_VERSION,
  MIN_HONEST_SCHEMA_VERSION,
  type EffortLevel,
  type JobClass,
  type ModelEvalRecord,
} from "../../src/eval/modelEvalSchemas.js";

function records(
  jobClass: JobClass,
  modelId: string,
  quality: number,
  costUsd: number,
  n: number,
  effort: EffortLevel = "high"
): ModelEvalRecord[] {
  return Array.from({ length: n }, (_, i) => ({
    task_id: `${jobClass}-${i}`,
    job_class: jobClass,
    cell: { model_id: modelId, effort, reasoning: "none" as const, prompt_variant: "baseline" },
    model_id: modelId,
    model_version_label: modelId,
    verdict: "pass" as const,
    tokens: { input: 1000, output: 1000, cache_read: 0, cache_creation: 0 },
    cost_usd: costUsd,
    latency_ms: 30_000,
    attempts_to_green: 1,
    gate_results: [{ name: "test", passed: true }],
    score: { composite: quality, correctness: 100, dimensions: [], judge_used: false },
    schema_version: MODEL_EVAL_SCHEMA_VERSION,
    run_id: "r",
    suite: "s",
    timestamp: "t",
    mode: "mock" as const,
  }));
}

const DATA = [
  ...records("bugfix", "claude-sonnet-5", 85, 0.05, 6, "low"),
  ...records("bugfix", "claude-opus-4-8", 92, 0.4, 6, "high"),
  ...records("refactor", "claude-sonnet-5", 80, 0.05, 2, "low"), // sparse
];

describe("buildRoutingAdvice", () => {
  const advice = buildRoutingAdvice(DATA, "2026-08-16T00:00:00Z");

  it("stamps schema version, sample floor, quality floor, and the honesty floor", () => {
    expect(advice.schema_version).toBe(ROUTING_ADVICE_SCHEMA_VERSION);
    expect(advice.min_samples).toBe(5);
    expect(advice.quality_floor).toBe(70);
    expect(advice.min_honest_schema_version).toBe(MIN_HONEST_SCHEMA_VERSION);
    expect(advice.generated_at).toBe("2026-08-16T00:00:00Z");
  });

  it("stamps a customized quality floor so consumption cannot silently diverge", () => {
    const custom = buildRoutingAdvice(DATA, "2026-08-16T00:00:00Z", { qualityFloor: 90 });
    expect(custom.quality_floor).toBe(90);
  });

  it("emits every combination with envelope, backoff level, and advisable flag", () => {
    const bugfix = advice.entries.filter((e) => e.job_class === "bugfix");
    expect(bugfix).toHaveLength(2);
    for (const e of bugfix) {
      expect(e.backoff).toBe("exact");
      expect(e.advisable).toBe(true);
      expect(e.effort).toBeDefined();
      expect(e.thinking).toBe("off");
    }
    // The sparse refactor combination is emitted, visibly non-advisable.
    const sparse = advice.entries.find((e) => e.job_class === "refactor");
    expect(sparse?.advisable).toBe(false);
    expect(sparse?.samples).toBe(2);
  });
});

describe("write → read round trip", () => {
  it("reads back what was written, at the canonical path", () => {
    const root = mkdtempSync(join(tmpdir(), "ng-advice-"));
    try {
      const advice = buildRoutingAdvice(DATA, "2026-08-16T00:00:00Z");
      const path = writeRoutingAdvice(root, advice);
      expect(path).toBe(join(root, ROUTING_ADVICE_RELATIVE_PATH));
      expect(readRoutingAdvice(root)).toEqual(advice);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it("fails OPEN on a missing or invalid file — routing is never blocked by advice", () => {
    const root = mkdtempSync(join(tmpdir(), "ng-advice-"));
    try {
      expect(readRoutingAdvice(root)).toBeUndefined();
      const path = join(root, ROUTING_ADVICE_RELATIVE_PATH);
      mkdirSync(dirname(path), { recursive: true });
      writeFileSync(path, "{ not json");
      expect(readRoutingAdvice(root)).toBeUndefined();
      writeFileSync(path, JSON.stringify({ schema_version: 999, entries: [] }));
      expect(readRoutingAdvice(root)).toBeUndefined();
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});

describe("pickAdvice — the shared consumption-side selection", () => {
  const advice = buildRoutingAdvice(DATA, "2026-08-16T00:00:00Z");

  it("picks per mode among advisable entries only", () => {
    expect(pickAdvice(advice, "bugfix", "efficiency")?.model_id).toBe("claude-sonnet-5");
    expect(pickAdvice(advice, "bugfix", "maximum")?.model_id).toBe("claude-opus-4-8");
  });

  it("returns the full envelope of the winning entry", () => {
    const e = pickAdvice(advice, "bugfix", "efficiency");
    expect(e?.effort).toBe("low");
    expect(e?.thinking).toBe("off");
    expect(e?.backoff).toBe("exact");
  });

  it("returns undefined when nothing advisable exists — the axis query alone decides", () => {
    expect(pickAdvice(advice, "refactor", "efficiency")).toBeUndefined();
    expect(pickAdvice(advice, "docs", "efficiency")).toBeUndefined();
  });
});

describe("cross-language advice-file contract fixture", () => {
  // The advice file is a cross-language wire format: this WRITER (TS) and the
  // Go READER (internal/intelligence/routing/advice.go) must agree on field
  // spellings, or Go's json.Unmarshal zero-values a renamed field and advice
  // goes silently inert (fail-open, but undetected). The committed fixture is
  // TS-GENERATED — this test regenerates it in memory and fails on any drift,
  // and the Go reader test (TestRoutingAdviceCrossLanguageFixture) consumes
  // the SAME committed file, so an in-version rename breaks one suite or the
  // other instead of keeping both green.
  //
  // Regen after a DELIBERATE schema change (bump ROUTING_ADVICE_SCHEMA_VERSION):
  //   NIGHTGAUGE_REGEN_ADVICE_FIXTURE=1 npx vitest run tests/eval/routingAdvice.test.ts
  const fixturePath = join(
    dirname(fileURLToPath(import.meta.url)),
    "../../../..",
    "internal/intelligence/routing/testdata/routing-advice-crosslang.json"
  );

  it("the committed fixture is byte-exactly what buildRoutingAdvice writes", () => {
    const built = `${JSON.stringify(buildRoutingAdvice(DATA, "2026-08-16T00:00:00Z"), null, 2)}\n`;
    if (process.env.NIGHTGAUGE_REGEN_ADVICE_FIXTURE === "1") {
      writeFileSync(fixturePath, built);
    }
    expect(readFileSync(fixturePath, "utf-8")).toBe(built);
  });
});
