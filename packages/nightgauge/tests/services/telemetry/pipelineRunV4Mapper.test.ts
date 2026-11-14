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

describe("mapHistoryRecordToV4 visibility (#2400)", () => {
  it("carries a private run's visibility onto the completion record", () => {
    const result = mapHistoryRecordToV4({ ...baseRecord({}), visibility: "private" });
    if (!result.ok) throw new Error(result.reason);
    expect(result.record.visibility).toBe("private");
    expect(JSON.stringify(result.record)).toContain('"visibility":"private"');
  });

  it("omits visibility for a team run and for any other value", () => {
    for (const visibility of [undefined, "team", "public"]) {
      const result = mapHistoryRecordToV4({ ...baseRecord({}), visibility });
      if (!result.ok) throw new Error(result.reason);
      expect(JSON.stringify(result.record)).not.toContain('"visibility"');
    }
  });
});

/** A run whose stages and per-stage tokens are given; tokens extras merge into the run tokens. */
function costRecord(
  perStage: Record<string, Record<string, unknown>>,
  runTokens: Record<string, unknown> = {},
  stageDetails: Record<string, Record<string, unknown>> = {}
): Record<string, unknown> {
  const stages: Record<string, unknown> = {};
  for (const name of Object.keys(perStage)) {
    stages[name] = { status: "complete", ...(stageDetails[name] ?? {}) };
  }
  return { ...baseRecord(stages), tokens: { per_stage: perStage, ...runTokens } };
}

function mapped(raw: Record<string, unknown>) {
  const result = mapHistoryRecordToV4(raw);
  if (!result.ok) throw new Error(`mapping failed: ${result.reason}`);
  return result.record;
}

describe("mapStages modelProvider and endpoint (#1750, mirrors the Go mapper)", () => {
  const opencode = { adapter: "opencode", cost_source: "computed", cost_usd: 0 };

  it("sends the recorded model_provider and endpoint for an opencode stage", () => {
    const stage = mapped(
      costRecord(
        { "feature-dev": opencode },
        {},
        {
          "feature-dev": {
            model_selection: {
              model: "lm-studio/qwen/qwen3.8-27b",
              model_provider: "lm-studio",
              endpoint: "lm-studio-4090",
            },
          },
        }
      )
    ).stages[0];
    expect(stage.modelProvider).toBe("lm-studio");
    expect(stage.endpoint).toBe("lm-studio-4090");
  });

  it("omits both keys when the stage recorded neither", () => {
    const stage = mapped(
      costRecord(
        { "feature-dev": opencode },
        {},
        {
          "feature-dev": { model_selection: { model: "claude-sonnet-5" } },
        }
      )
    ).stages[0];
    expect("modelProvider" in stage).toBe(false);
    expect("endpoint" in stage).toBe(false);
    expect(JSON.stringify(stage)).not.toMatch(/modelProvider|endpoint/u);
  });

  it("drops a label the platform's guard would reject instead of failing the upload", () => {
    const stage = mapped(
      costRecord(
        { "feature-dev": opencode },
        {},
        {
          "feature-dev": {
            model_selection: { model: "x", model_provider: "LM Studio", endpoint: "a".repeat(65) },
          },
        }
      )
    ).stages[0];
    expect("modelProvider" in stage).toBe(false);
    expect("endpoint" in stage).toBe(false);
  });
});

describe("mapStages costUsd: unknown is null, a stamped zero is 0 (#1750, #1630)", () => {
  it.each([
    ["unstamped zero", { cost_usd: 0, cost_source: "unknown", cost_unstamped: true }, null],
    [
      "unstamped despite a priced source",
      { cost_usd: 0, cost_source: "computed", cost_unstamped: true },
      null,
    ],
    ["zero with no cost_source", { cost_usd: 0 }, null],
    ["no cost_usd at all", {}, null],
    ["stamped local zero", { cost_usd: 0, cost_source: "computed" }, 0],
    ["deterministic zero", { cost_usd: 0, cost_source: "deterministic" }, 0],
    ["priced nonzero", { cost_usd: 0.25, cost_source: "native" }, 0.25],
    ["nonzero with no cost_source", { cost_usd: 0.1 }, 0.1],
  ])("%s → %s", (_name, tokens, want) => {
    const stage = mapped(costRecord({ "feature-dev": { adapter: "opencode", ...tokens } }))
      .stages[0];
    expect(stage.costUsd).toBe(want);
  });
});

describe("mapHistoryRecordToV4 totalCostUsd mirrors the Go switch (#1750)", () => {
  const local = { adapter: "opencode", cost_usd: 0, cost_source: "computed" };
  const bookend = { adapter: "claude", cost_usd: 0, cost_source: "deterministic" };
  const unstamped = {
    adapter: "opencode",
    cost_usd: 0,
    cost_source: "unknown",
    cost_unstamped: true,
  };

  it("uses a non-zero run estimate", () => {
    expect(
      mapped(costRecord({ "feature-dev": local }, { estimated_cost_usd: 0.42 })).totalCostUsd
    ).toBe(0.42);
  });

  it("backfills from the stage sum when the estimate is zero", () => {
    const rec = costRecord(
      {
        "feature-dev": { adapter: "claude", cost_usd: 0.3, cost_source: "native" },
        "feature-validate": { adapter: "claude", cost_usd: 0.2, cost_source: "native" },
      },
      { estimated_cost_usd: 0 }
    );
    expect(mapped(rec).totalCostUsd).toBeCloseTo(0.5);
  });

  it("sends 0 when every stage is a stamped zero", () => {
    const rec = costRecord(
      { "issue-pickup": bookend, "feature-dev": local },
      { estimated_cost_usd: 0 }
    );
    expect(mapped(rec).totalCostUsd).toBe(0);
  });

  it("sends null when any stage is unstamped", () => {
    const rec = costRecord(
      { "feature-dev": local, "feature-validate": unstamped },
      { estimated_cost_usd: 0, cost_unstamped: true }
    );
    expect(mapped(rec).totalCostUsd).toBeNull();
  });

  it("sends null when no stage names a cost source", () => {
    const rec = costRecord(
      { "feature-dev": { adapter: "codex", cost_usd: 0 } },
      { estimated_cost_usd: 0 }
    );
    expect(mapped(rec).totalCostUsd).toBeNull();
  });

  it("sends null when the run has no stage tokens", () => {
    expect(mapped(costRecord({})).totalCostUsd).toBeNull();
  });
});
