import { describe, it, expect } from "vitest";
import { mapHistoryRecordToV4 } from "../../../src/services/telemetry/pipelineRunV4Mapper";

function baseRecord(stages: Record<string, unknown>): Record<string, unknown> {
  return {
    record_type: "run",
    issue_number: 42,
    repo: "nightgauge/nightgauge",
    started_at: "2026-04-01T10:00:00-06:00",
    completed_at: "2026-04-01T10:05:00-06:00",
    outcome: "complete",
    stages,
    tokens: { per_stage: {} },
    routing: {},
  };
}

describe("mapStages executionPath/modelEffort/modelReasoning", () => {
  it("maps present fields through with no fallback", () => {
    const result = mapHistoryRecordToV4(
      baseRecord({
        "feature-dev": {
          status: "complete",
          execution_path: "llm",
          model_effort: "high",
          model_reasoning: "deep",
        },
      })
    );
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    const stage = result.record.stages[0];
    expect(stage.executionPath).toBe("llm");
    expect(stage.modelEffort).toBe("high");
    expect(stage.modelReasoning).toBe("deep");
  });

  it("is null when absent, and does not borrow model/adapter values", () => {
    const result = mapHistoryRecordToV4(
      baseRecord({
        "feature-dev": {
          status: "complete",
          model_selection: { model: "claude-sonnet-4-5" },
        },
      })
    );
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    const stage = result.record.stages[0];
    expect(stage.model).toBe("claude-sonnet-4-5");
    expect(stage.executionPath).toBeNull();
    expect(stage.modelEffort).toBeNull();
    expect(stage.modelReasoning).toBeNull();
  });

  it("truncates to 100 chars like model/stageId", () => {
    const long = "x".repeat(120);
    const result = mapHistoryRecordToV4(
      baseRecord({
        "feature-dev": {
          status: "complete",
          execution_path: long,
          model_effort: long,
          model_reasoning: long,
        },
      })
    );
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    const stage = result.record.stages[0];
    expect(stage.executionPath).toHaveLength(100);
    expect(stage.modelEffort).toHaveLength(100);
    expect(stage.modelReasoning).toHaveLength(100);
  });

  it("treats empty string as absent (null), not an empty value", () => {
    const result = mapHistoryRecordToV4(
      baseRecord({
        "feature-dev": {
          status: "complete",
          execution_path: "",
          model_effort: "",
          model_reasoning: "",
        },
      })
    );
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    const stage = result.record.stages[0];
    expect(stage.executionPath).toBeNull();
    expect(stage.modelEffort).toBeNull();
    expect(stage.modelReasoning).toBeNull();
  });
});
