/**
 * monitoringResolver.stallCalibration.test.ts (Issue #1657)
 *
 * History-calibrated stall thresholds, end to end through the REAL
 * StageDurationAnalyzer: only the history reader is stubbed. A local model's
 * stage samples get their own (adapter, model) bucket, so they neither
 * consume nor pollute the flagship (stage, mode) bucket, and whatever a local
 * execution's thresholds come out as, they are at least
 * LOCAL_PROVIDER_STALL_FLOOR.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }] },
}));

vi.mock("../../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(() => ({
    path: "/test/workspace/.nightgauge/config.yaml",
    isLegacy: false,
    exists: false,
  })),
  logDeprecationWarning: vi.fn(),
}));

vi.mock("../../../src/utils/executionHistoryReader", () => ({
  ExecutionHistoryReader: { readAll: vi.fn() },
}));

import {
  computeKillThreshold,
  DEFAULT_STALL_THRESHOLDS,
  getCalibratedStallData,
  getStallCalibrationMinRuns,
  LOCAL_PROVIDER_STALL_FLOOR,
  precomputeCalibratedStallThresholds,
  roundUpTo30s,
} from "../../../src/utils/resolvers/monitoringResolver";
import { ExecutionHistoryReader } from "../../../src/utils/executionHistoryReader";
import { StageDurationAnalyzer } from "../../../src/utils/StageDurationAnalyzer";

const ROOT = "/test/workspace";
const STAGE = "feature-dev";
const LOCAL_MODEL = "lmstudio/qwen/qwen3.8-27b";
const CLAUDE = { adapter: "claude", model: "claude-sonnet-5" };
const LOCAL = { adapter: "opencode", model: LOCAL_MODEL };

interface Sample {
  adapter: string;
  model: string;
  durationMs: number;
  /** Recorded as ADR-022 § 2 writes an opencode stage: served model, provider, `-m` value. */
  opencode?: boolean;
}

function record(sample: Sample, i: number): unknown {
  const selection = sample.opencode
    ? {
        model: `lm-studio/${sample.model.slice("lmstudio/".length)}`,
        adapter: sample.adapter,
        model_provider: "lm-studio",
        upstream_model: sample.model,
      }
    : { model: sample.model, adapter: sample.adapter };
  return {
    record_type: "run",
    outcome: "complete",
    recorded_at: new Date(Date.now() - i * 60_000).toISOString(),
    stages: {
      [STAGE]: {
        status: "complete",
        duration_ms: sample.durationMs,
        performance_mode: "elevated",
        model_selection: selection,
      },
    },
  };
}

/** `n` samples of one execution, durations `baseMs`, `baseMs + stepMs`, …. */
function samples(
  execution: { adapter: string; model: string },
  n: number,
  baseMs: number,
  stepMs: number,
  opencode = false
): Sample[] {
  return Array.from({ length: n }, (_, i) => ({
    ...execution,
    durationMs: baseMs + i * stepMs,
    opencode,
  }));
}

async function calibrate(history: Sample[]) {
  await StageDurationAnalyzer.invalidateCache();
  vi.mocked(ExecutionHistoryReader.readAll).mockResolvedValue(
    history.map(record) as Awaited<ReturnType<typeof ExecutionHistoryReader.readAll>>
  );
  await precomputeCalibratedStallThresholds(ROOT);
}

const MIN_RUNS = getStallCalibrationMinRuns(ROOT);
const CLAUDE_SAMPLES = samples(CLAUDE, MIN_RUNS + 2, 240_000, 15_000);

beforeEach(() => {
  vi.clearAllMocks();
});

describe("local-model stall calibration buckets (#1657)", () => {
  it("local samples leave the Claude thresholds for the same stage and mode unchanged", async () => {
    await calibrate(CLAUDE_SAMPLES);
    const baseline = getCalibratedStallData(ROOT, STAGE, "elevated", undefined, CLAUDE);
    expect(baseline?.source).toBe("calibrated");

    // Hours-long local stages: pooled with Claude's, they would raise its p95.
    await calibrate([
      ...CLAUDE_SAMPLES,
      ...samples(LOCAL, MIN_RUNS + 2, 3 * 3_600_000, 600_000, true),
    ]);
    expect(getCalibratedStallData(ROOT, STAGE, "elevated", undefined, CLAUDE)).toEqual(baseline);
    expect(getCalibratedStallData(ROOT, STAGE, "elevated")).toEqual(baseline);
  });

  it("local samples do not make a flagship bucket calibrated on their own", async () => {
    await calibrate(samples(LOCAL, MIN_RUNS + 2, 600_000, 60_000, true));
    const flagship = getCalibratedStallData(ROOT, STAGE, "elevated", undefined, CLAUDE);
    expect(flagship).toEqual({
      warnSec: DEFAULT_STALL_THRESHOLDS[STAGE],
      killSec: 0,
      source: "static",
      isColdStart: true,
    });
  });

  it("a local execution is calibrated from its own samples", async () => {
    const local = samples(LOCAL, MIN_RUNS + 2, 2 * 3_600_000, 300_000, true);
    await calibrate([...CLAUDE_SAMPLES, ...local]);
    const data = getCalibratedStallData(ROOT, STAGE, "elevated", undefined, LOCAL);
    expect(data?.source).toBe("calibrated");
    // Derived from the real analyzer's own stats for the local bucket.
    const stats = await StageDurationAnalyzer.getLocalStageStatsByMode(
      ROOT,
      `opencode/${LOCAL_MODEL}`,
      STAGE,
      "elevated"
    );
    const warnSec = roundUpTo30s((stats!.p95_ms / 1000) * 1.5);
    expect(data?.warnSec).toBe(warnSec);
    expect(data?.killSec).toBe(computeKillThreshold(stats!.max_ms / 1000, warnSec));
    expect(data!.warnSec).toBeGreaterThan(LOCAL_PROVIDER_STALL_FLOOR.warnSec);
  });

  it("a local execution's thresholds are never below the floor", async () => {
    // Short local stages calibrate below the floor.
    await calibrate([...CLAUDE_SAMPLES, ...samples(LOCAL, MIN_RUNS + 2, 30_000, 1_000, true)]);
    const calibrated = getCalibratedStallData(ROOT, STAGE, "elevated", undefined, LOCAL);
    expect(calibrated?.warnSec).toBe(LOCAL_PROVIDER_STALL_FLOOR.warnSec);
    expect(calibrated?.killSec).toBe(LOCAL_PROVIDER_STALL_FLOOR.killSec);

    // No samples of its own: cold start, floored; a disabled kill stays disabled.
    const cold = getCalibratedStallData(ROOT, "pr-create", "elevated", undefined, {
      adapter: "opencode",
      model: "ollama/qwen3-coder:30b",
    });
    expect(DEFAULT_STALL_THRESHOLDS["pr-create"]).toBeLessThan(LOCAL_PROVIDER_STALL_FLOOR.warnSec);
    expect(cold?.warnSec).toBe(LOCAL_PROVIDER_STALL_FLOOR.warnSec);
    expect(cold?.killSec).toBe(0);
  });

  it("a hosted opencode model is not local and reads the flagship bucket", async () => {
    await calibrate(CLAUDE_SAMPLES);
    const hosted = getCalibratedStallData(ROOT, STAGE, "elevated", undefined, {
      adapter: "opencode",
      model: "anthropic/claude-sonnet-5",
    });
    expect(hosted).toEqual(getCalibratedStallData(ROOT, STAGE, "elevated", undefined, CLAUDE));
  });

  it("the floor is derived from the observed 76 s prefill and ~8 tok/s decode", () => {
    // 76 s + 4096 tokens / 8 tok/s = 588 s, rounded up to 30 s; kill = 3 × warn.
    expect(LOCAL_PROVIDER_STALL_FLOOR.warnSec).toBe(roundUpTo30s(76 + 4096 / 8));
    expect(LOCAL_PROVIDER_STALL_FLOOR.killSec).toBe(
      computeKillThreshold(0, LOCAL_PROVIDER_STALL_FLOOR.warnSec)
    );
  });
});
