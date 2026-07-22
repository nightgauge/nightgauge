import { describe, it, expect } from "vitest";
import { analyzeModelRouting } from "../../../../analysis/health/dimensions/modelRouting.js";
import type {
  HealthAnalysisInput,
  HealthAnalysisConfig,
} from "../../../../analysis/health/types.js";
import { DEFAULT_HEALTH_CONFIG } from "../../../../analysis/health/types.js";
import type { ExecutionHistoryRecord } from "../../../../analysis/types.js";

function makeRecord(overrides: Partial<ExecutionHistoryRecord> = {}): ExecutionHistoryRecord {
  return {
    issueNumber: 100,
    stage: "feature-dev",
    success: true,
    retries: 0,
    inputTokens: 10000,
    outputTokens: 5000,
    costUsd: 0.1,
    durationMs: 60000,
    timestamp: "2025-01-15T10:00:00Z",
    ...overrides,
  };
}

function makeInput(
  records: ExecutionHistoryRecord[],
  extras?: Partial<HealthAnalysisInput>
): HealthAnalysisInput {
  return {
    executionHistory: records,
    healthScores: [],
    selfTuningLog: [],
    experimentResults: [],
    healthReports: [],
    ...extras,
  };
}

const config: HealthAnalysisConfig = DEFAULT_HEALTH_CONFIG;

describe("analyzeModelRouting", () => {
  it("returns hasEnoughData=false for empty records", () => {
    const result = analyzeModelRouting(makeInput([]), config);
    expect(result.hasEnoughData).toBe(false);
    expect(result.sampleSize).toBe(0);
    expect(result.dimension).toBe("model-routing");
    expect(result.score).toBe(50);
  });

  it("returns hasEnoughData=false when below minimum sample size", () => {
    const records = [1, 2, 3].map((i) =>
      makeRecord({
        issueNumber: i,
        model: "claude-sonnet",
        timestamp: `2025-01-${String(i).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeModelRouting(makeInput(records), config);
    expect(result.hasEnoughData).toBe(false);
  });

  it("applies a small deduction when only a single model is used", () => {
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 100 + i,
        model: "claude-sonnet",
        selectionSource: "config",
        timestamp: `2025-01-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeModelRouting(makeInput(records), config);
    expect(result.hasEnoughData).toBe(true);
    // Score should be deducted by 5 for single model
    expect(result.score).toBeLessThanOrEqual(95);
    expect(result.metrics["distinctModelCount"]).toBe(1);
  });

  it("generates an under-routing finding for haiku on XL tasks that fail", () => {
    // Haiku model + high complexity (XL) + auto-selected + failed → under-routing
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 200 + i,
        stage: "feature-dev",
        model: "claude-haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "XL",
        success: false,
        timestamp: `2025-02-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeModelRouting(makeInput(records), config);
    const underRoutingFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("under-routing")
    );
    expect(underRoutingFinding).toBeDefined();
    expect(result.metrics["underRoutingCount"]).toBeGreaterThan(0);
    expect(result.score).toBeLessThan(100);
  });

  it("generates an over-routing finding for opus on XS tasks that succeed first try", () => {
    // Opus model + low complexity (XS) + auto-selected + success + no retries → over-routing
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 300 + i,
        stage: "pr-create",
        model: "claude-opus",
        selectionSource: "auto",
        autoSelectorComplexity: "XS",
        success: true,
        retries: 0,
        timestamp: `2025-03-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeModelRouting(makeInput(records), config);
    const overRoutingFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("over-routing")
    );
    expect(overRoutingFinding).toBeDefined();
    expect(result.metrics["overRoutingCount"]).toBeGreaterThan(0);
  });

  it("scores well for good auto-selection with mixed models and high success", () => {
    // Auto-selected, alternating between sonnet and opus, all successful
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 400 + i,
        stage: "feature-dev",
        model: i % 2 === 0 ? "claude-sonnet" : "claude-opus",
        selectionSource: "auto",
        autoSelectorComplexity: i % 2 === 0 ? "S" : "L",
        success: true,
        retries: 0,
        timestamp: `2025-04-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeModelRouting(makeInput(records), config);
    expect(result.hasEnoughData).toBe(true);
    // No under/over routing, high success rate → higher score
    expect(result.score).toBeGreaterThanOrEqual(85);
    expect(result.metrics["underRoutingCount"]).toBe(0);
    expect(result.metrics["overRoutingCount"]).toBe(0);
  });

  it("returns a reasonable score when no records have auto-selection", () => {
    // All records with selectionSource='config' — no auto-selection data
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 500 + i,
        model: "claude-sonnet",
        selectionSource: "config",
        autoSelectorComplexity: undefined,
        timestamp: `2025-05-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeModelRouting(makeInput(records), config);
    expect(result.hasEnoughData).toBe(true);
    // autoSuccessRate defaults to 1 when no auto records; single model → -5
    expect(result.score).toBeGreaterThanOrEqual(70);
    expect(result.metrics["autoSelectionTotal"]).toBe(0);
  });

  it("includes period comparison when baseline is provided with sufficient auto records", () => {
    const makeAutoRecords = (offset: number, success: boolean) =>
      Array.from({ length: 10 }, (_, i) =>
        makeRecord({
          issueNumber: offset + i,
          model: "claude-sonnet",
          selectionSource: "auto",
          autoSelectorComplexity: "M",
          success,
          timestamp: `2025-06-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        })
      );

    const current = makeInput(makeAutoRecords(600, true));
    const baseline = makeInput(makeAutoRecords(700, false));

    const result = analyzeModelRouting(current, config, baseline);
    expect(result.periodComparison).toBeDefined();
    // Current has higher success rate → improving
    expect(result.periodComparison?.direction).toBe("improving");
  });

  it("populates all expected metric fields", () => {
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 800 + i,
        model: i % 2 === 0 ? "claude-haiku" : "claude-sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        timestamp: `2025-07-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeModelRouting(makeInput(records), config);
    expect(result.metrics).toHaveProperty("autoSelectionTotal");
    expect(result.metrics).toHaveProperty("autoSelectionSuccessRate");
    expect(result.metrics).toHaveProperty("distinctModelCount");
    expect(result.metrics).toHaveProperty("underRoutingCount");
    expect(result.metrics).toHaveProperty("overRoutingCount");
  });

  // ── Mixed-fleet scenarios (Issue #2055) ────────────────────────────────────

  it("mixed fleet: LM Studio (cost=0) not included in highCostModels", () => {
    // LM Studio model has effectiveCostPerSuccess=0; cloud model has nonzero cost.
    // Without the fix, the zero-cost model drags the fleet mean toward 0, making
    // the cloud model appear to exceed the 2x threshold.
    const records = [
      // 5 LM Studio records (cost=0, success)
      ...Array.from({ length: 5 }, (_, i) =>
        makeRecord({
          issueNumber: 3000 + i,
          model: "mistral",
          costUsd: 0,
          success: true,
          retries: 0,
          timestamp: `2025-11-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        })
      ),
      // 5 Anthropic records (cost=$0.10)
      ...Array.from({ length: 5 }, (_, i) =>
        makeRecord({
          issueNumber: 3100 + i,
          model: "claude-sonnet",
          costUsd: 0.1,
          success: true,
          retries: 0,
          timestamp: `2025-11-${String(i + 6).padStart(2, "0")}T10:00:00Z`,
        })
      ),
    ];
    const result = analyzeModelRouting(makeInput(records), config);
    // Cloud model should NOT be flagged as cost-inefficient
    const costFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("cost-ineffective")
    );
    expect(costFinding).toBeUndefined();
  });

  it("mixed fleet: meanEffectiveCostPerSuccess computed from cloud models only", () => {
    const records = [
      // 5 LM Studio records (cost=0)
      ...Array.from({ length: 5 }, (_, i) =>
        makeRecord({
          issueNumber: 3200 + i,
          model: "llama-3",
          costUsd: 0,
          success: true,
          retries: 0,
          timestamp: `2025-11-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        })
      ),
      // 5 cloud records (cost=$0.20)
      ...Array.from({ length: 5 }, (_, i) =>
        makeRecord({
          issueNumber: 3300 + i,
          model: "claude-opus",
          costUsd: 0.2,
          success: true,
          retries: 0,
          timestamp: `2025-11-${String(i + 6).padStart(2, "0")}T10:00:00Z`,
        })
      ),
    ];
    const result = analyzeModelRouting(makeInput(records), config);
    // meanEffectiveCostPerSuccess should reflect only cloud model ($0.20), not $0.10 average
    expect(result.metrics["meanEffectiveCostPerSuccess"]).toBeCloseTo(0.2);
    expect(result.metrics["localModelCount"]).toBe(1); // 1 distinct local model entry
  });

  it("all-local fleet: localModelCount reported in metrics, no cost-inefficiency finding", () => {
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 3400 + i,
        model: "mistral",
        costUsd: 0,
        success: true,
        retries: 0,
        timestamp: `2025-11-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeModelRouting(makeInput(records), config);
    expect(result.metrics["localModelCount"]).toBe(1);
    expect(result.metrics["meanEffectiveCostPerSuccess"]).toBe(0);
    const costFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("cost-ineffective")
    );
    expect(costFinding).toBeUndefined();
  });

  it("returns score in [0, 100] range under worst-case routing conditions", () => {
    // Under-routing + over-routing + high auto failure all at once
    const records = [
      // Under-routing: haiku on XL fails
      ...Array.from({ length: 5 }, (_, i) =>
        makeRecord({
          issueNumber: 900 + i,
          model: "claude-haiku",
          selectionSource: "auto",
          autoSelectorComplexity: "XL",
          success: false,
          timestamp: `2025-08-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        })
      ),
      // Over-routing: opus on XS succeeds
      ...Array.from({ length: 5 }, (_, i) =>
        makeRecord({
          issueNumber: 910 + i,
          model: "claude-opus",
          selectionSource: "auto",
          autoSelectorComplexity: "XS",
          success: true,
          retries: 0,
          timestamp: `2025-08-${String(i + 1).padStart(2, "0")}T11:00:00Z`,
        })
      ),
    ];
    const result = analyzeModelRouting(makeInput(records), config);
    expect(result.score).toBeGreaterThanOrEqual(0);
    expect(result.score).toBeLessThanOrEqual(100);
  });
});
