import { describe, it, expect } from "vitest";
import {
  buildPipelineExecutionEvent,
  PIPELINE_EXECUTION_EVENT_SCHEMA_VERSION,
  type PipelineExecutionInput,
} from "../../src/utils/telemetryEventBuilder";
import type { PipelineState } from "../../src/services/PipelineStateService";

function makeState(overrides?: Partial<PipelineState>): PipelineState {
  return {
    issue_number: 42,
    title: "Test issue",
    branch: "feat/42-test",
    stages: {
      "issue-pickup": { status: "complete" },
      "feature-planning": { status: "complete" },
      "feature-dev": { status: "complete" },
    },
    started_at: "2026-03-11T10:00:00.000Z",
    tokens: {
      input: 0,
      output: 0,
      total_input: 12000,
      total_output: 3000,
      total_cache_read: 500,
      total_cache_creation: 100,
      estimated_cost_usd: 0.42,
      per_stage: {
        "issue-pickup": { input: 1200, output: 300 },
        "feature-planning": { input: 3000, output: 800 },
        "feature-dev": { input: 7800, output: 1900 },
      },
    },
    outcome_type: "productive",
    ...overrides,
  };
}

function makeInput(
  stateOverrides?: Partial<PipelineState>,
  metaOverrides?: Partial<PipelineExecutionInput["issueMetadata"]>
): PipelineExecutionInput {
  return {
    state: makeState(stateOverrides),
    issueMetadata: {
      issueNumber: 42,
      sizeLabel: "L",
      typeLabel: "feature",
      ...metaOverrides,
    },
    startedAt: new Date("2026-03-11T10:00:00.000Z"),
    completedAt: new Date("2026-03-11T10:00:42.000Z"),
  };
}

describe("buildPipelineExecutionEvent", () => {
  it("builds correct AnalyticsEvent from complete input", () => {
    const event = buildPipelineExecutionEvent(makeInput());

    expect(event.eventType).toBe("pipeline_execution_completed");
    expect(event.timestamp).toBe("2026-03-11T10:00:42.000Z");

    const p = event.payload!;
    expect(p.schema_version).toBe(PIPELINE_EXECUTION_EVENT_SCHEMA_VERSION);
    expect(p.pipeline_duration_ms).toBe(42000);
    expect(p.total_input_tokens).toBe(12000);
    expect(p.total_output_tokens).toBe(3000);
    expect(p.total_cache_read_tokens).toBe(500);
    expect(p.total_cache_creation_tokens).toBe(100);
    expect(p.outcome).toBe("success");
    expect(p.stage_count).toBe(3);
    expect(p.issue_complexity).toBe("L");
    expect(p.backtracks).toBe(0);
    expect(p.model_escalations).toBe(0);

    const perStage = p.per_stage_tokens as Record<string, { input: number; output: number }>;
    expect(perStage["issue-pickup"]).toEqual({ input: 1200, output: 300 });
    expect(perStage["feature-planning"]).toEqual({ input: 3000, output: 800 });
  });

  it('maps "productive" outcome to "success"', () => {
    const event = buildPipelineExecutionEvent(makeInput({ outcome_type: "productive" }));
    expect(event.payload!.outcome).toBe("success");
  });

  it('maps "verify-and-close" outcome to "success"', () => {
    const event = buildPipelineExecutionEvent(makeInput({ outcome_type: "verify-and-close" }));
    expect(event.payload!.outcome).toBe("success");
  });

  it('maps "already-resolved" outcome to "success"', () => {
    const event = buildPipelineExecutionEvent(makeInput({ outcome_type: "already-resolved" }));
    expect(event.payload!.outcome).toBe("success");
  });

  it('maps "budget-ceiling" outcome to "failure"', () => {
    const event = buildPipelineExecutionEvent(makeInput({ outcome_type: "budget-ceiling" }));
    expect(event.payload!.outcome).toBe("failure");
  });

  it('maps "failure" outcome to "failure"', () => {
    const event = buildPipelineExecutionEvent(makeInput({ outcome_type: "failure" }));
    expect(event.payload!.outcome).toBe("failure");
  });

  it('maps undefined outcome to "failure"', () => {
    const event = buildPipelineExecutionEvent(makeInput({ outcome_type: undefined }));
    expect(event.payload!.outcome).toBe("failure");
  });

  it("handles zero stages gracefully", () => {
    const event = buildPipelineExecutionEvent(
      makeInput({
        stages: {},
        tokens: { input: 0, output: 0 },
      })
    );
    const p = event.payload!;
    expect(p.stage_count).toBe(0);
    expect(p.per_stage_tokens).toEqual({});
    expect(p.total_input_tokens).toBe(0);
  });

  it("handles missing size label without crashing", () => {
    const event = buildPipelineExecutionEvent(makeInput(undefined, { sizeLabel: null }));
    expect(event.payload!.issue_complexity).toBeNull();
  });

  it("stamps every pipeline event with schema_version for wire-contract evolution", () => {
    const event = buildPipelineExecutionEvent(makeInput());
    expect(event.payload!.schema_version).toBe(1);
  });

  it("outputs ISO 8601 timestamp", () => {
    const event = buildPipelineExecutionEvent(makeInput());
    expect(event.timestamp).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/);
  });

  it("includes backtrack and escalation counts", () => {
    const event = buildPipelineExecutionEvent(
      makeInput({
        backtracks: [
          {
            stage: "feature-dev",
            reason: "test",
            timestamp: "",
            from_stage: "feature-validate",
            to_stage: "feature-dev",
            signal_type: "PLAN_REVISION_NEEDED",
            rationale: "test",
            attempt_number: 1,
          },
        ],
        modelEscalations: [
          {
            stage: "feature-dev",
            fromModel: "sonnet",
            toModel: "opus",
            reason: "complexity",
          },
        ],
      })
    );
    expect(event.payload!.backtracks).toBe(1);
    expect(event.payload!.model_escalations).toBe(1);
  });

  it("does not include PII or file paths", () => {
    const event = buildPipelineExecutionEvent(makeInput());
    const json = JSON.stringify(event);
    // Should not contain issue title, branch name, or file paths
    expect(json).not.toContain("Test issue");
    expect(json).not.toContain("feat/42-test");
    expect(json).not.toContain(".ts");
  });
});
