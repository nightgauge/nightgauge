/**
 * OutcomeRecorder.performanceMode.test.ts (Issue #3009)
 *
 * Verifies the calibration-skip predicate after the pipeline_mode enum was
 * extended additively. The default-routing modes ("normal", "elevated") flow
 * into calibration; off-baseline modes ("efficiency", "maximum", legacy
 * "supercharge") are excluded.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import { OutcomeRecorder } from "../services/OutcomeRecorder.js";
import type { ComplexityModelService } from "../services/ComplexityModelService.js";
import type { ComplexityModel, ExecutionOutcome } from "../context/schemas/complexity-model.js";

function bootstrapModel(): ComplexityModel {
  return {
    lines_changed_thresholds: {
      XS: 50,
      S: 150,
      M: 400,
      L: 1000,
    },
    type_adjustments: {},
    patterns: { high_complexity: [], medium_complexity: [], low_complexity: [] },
    // Leave prediction_accuracy undefined so the recorder seeds the default.
  } as unknown as ComplexityModel;
}

function makeOutcome(pipelineMode: ExecutionOutcome["pipeline_mode"]): ExecutionOutcome {
  return {
    issue_number: 3009,
    issue_type: "feature",
    pr_number: 1,
    predicted_size: "M",
    actual_lines_changed: 200,
    stages_run: ["feature-dev"],
    stages_failed: [],
    model_used: "sonnet",
    completed_at: "2026-04-25T00:00:00Z",
    outcome: "success",
    pipeline_mode: pipelineMode,
  };
}

function buildRecorder(): {
  recorder: OutcomeRecorder;
  recordSpy: ReturnType<typeof vi.fn>;
} {
  const baseModel = bootstrapModel();
  const recordSpy = vi.fn((model: ComplexityModel) => model);
  const service = {
    load: vi.fn(async () => baseModel),
    save: vi.fn(async () => undefined),
    recordOutcome: recordSpy,
    findExistingOutcome: vi.fn(() => undefined),
  } as unknown as ComplexityModelService;
  const recorder = new OutcomeRecorder(service);
  return { recorder, recordSpy };
}

describe("OutcomeRecorder calibration-skip predicate (Issue #3009)", () => {
  let recorder: OutcomeRecorder;
  let recordSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    ({ recorder, recordSpy } = buildRecorder());
  });

  it("includes 'elevated' runs in calibration (default routing)", async () => {
    await recorder.recordOutcome(makeOutcome("elevated"));
    // elevated → full calibration path → recordOutcome called and follow-up updates run
    expect(recordSpy).toHaveBeenCalled();
  });

  it("includes 'normal' runs in calibration (legacy default)", async () => {
    await recorder.recordOutcome(makeOutcome("normal"));
    expect(recordSpy).toHaveBeenCalled();
  });

  it("excludes 'efficiency' runs from calibration", async () => {
    const result = await recorder.recordOutcome(makeOutcome("efficiency"));
    // skipped:false because the run was still appended for cost tracking,
    // but the recorder returned early before any calibration math.
    expect(result.skipped).toBe(false);
    expect(recordSpy).toHaveBeenCalledTimes(1);
  });

  it("excludes 'maximum' runs from calibration", async () => {
    const result = await recorder.recordOutcome(makeOutcome("maximum"));
    expect(result.skipped).toBe(false);
    expect(recordSpy).toHaveBeenCalledTimes(1);
  });

  it("excludes 'frontier' runs from calibration (Fable tier deviates from baseline)", async () => {
    const result = await recorder.recordOutcome(makeOutcome("frontier"));
    expect(result.skipped).toBe(false);
    expect(recordSpy).toHaveBeenCalledTimes(1);
  });

  it("excludes legacy 'supercharge' runs from calibration", async () => {
    const result = await recorder.recordOutcome(makeOutcome("supercharge"));
    expect(result.skipped).toBe(false);
    expect(recordSpy).toHaveBeenCalledTimes(1);
  });
});
