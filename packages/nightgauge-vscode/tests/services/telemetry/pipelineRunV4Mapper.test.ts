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

describe("mapStages excludes model_selection's dispatch-envelope fields (#580)", () => {
  // The platform's `.strict()` V4 schema rejects the WHOLE record on any
  // unknown key (the same failure mode #588 documents for cost_unstamped).
  // model_selection.adapter/served_model/effort/thinking/mode must therefore
  // never reach the mapped stage object — only `model` may.
  it("reads only model_selection.model, nothing else, into the mapped stage", () => {
    const result = mapHistoryRecordToV4(
      baseRecord({
        "feature-dev": {
          status: "complete",
          model_selection: {
            model: "sonnet",
            source: "scheduler",
            adapter: "grok",
            served_model: "grok-4.6",
            effort: "high",
            thinking: "on",
            mode: "automatic",
          },
        },
      })
    );
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    const stage = result.record.stages[0];
    expect(stage.model).toBe("sonnet");
    // The mapped stage carries exactly V4StageMetric's declared keys — none
    // of adapter/servedModel/effort/thinking/mode/source leaked through.
    expect(Object.keys(stage).sort()).toEqual(
      [
        "attempt",
        "costUsd",
        "durationMs",
        "executionPath",
        "inputTokens",
        "model",
        "modelEffort",
        "modelReasoning",
        "outputTokens",
        "stageId",
        "stageName",
        "success",
        "totalTokens",
      ].sort()
    );
  });
});
