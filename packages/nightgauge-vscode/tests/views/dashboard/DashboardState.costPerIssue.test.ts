/**
 * DashboardState.costPerIssue.test.ts
 *
 * Tests for Issue #2546: wire type/size from HistoryIndexEntry through
 * PipelineRunSummary into getCostPerIssueAggregations().
 *
 * Covers:
 * - getCostPerIssueAggregations() uses most-recent-run's type/size
 * - Null/undefined handling for missing type/size
 * - Serialization round-trip preserves issueType and sizeLabel
 * - Backward compatibility with old data lacking these fields
 */

import { describe, it, expect, vi } from "vitest";
import { createMockMemento } from "../../mocks/memento";

vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn().mockReturnValue(undefined),
    })),
  },
  EventEmitter: class EventEmitter {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
}));

import { DashboardState } from "../../../src/views/dashboard/DashboardState";

/** Build a serialized PipelineRunSummary for storage */
function makeRun(overrides: {
  issueNumber: number;
  costUsd: number;
  startedAt?: string;
  is_recovery?: boolean;
  is_supercharge?: boolean;
  issueType?: string | null;
  sizeLabel?: string | null;
}) {
  return {
    issueNumber: overrides.issueNumber,
    title: `Issue #${overrides.issueNumber}`,
    branch: `feat/${overrides.issueNumber}`,
    startedAt: overrides.startedAt ?? "2026-03-01T00:00:00.000Z",
    completedAt: "2026-03-01T01:00:00.000Z",
    status: "complete" as const,
    stages: [],
    usage: {
      inputTokens: 10000,
      outputTokens: 5000,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: overrides.costUsd,
      durationMs: 3600000,
      stageCount: 6,
    },
    toolCalls: [],
    timeSavedMs: 7200000,
    is_recovery: overrides.is_recovery,
    is_supercharge: overrides.is_supercharge,
    issueType: overrides.issueType,
    sizeLabel: overrides.sizeLabel,
  };
}

function createState(runs: ReturnType<typeof makeRun>[]): DashboardState {
  const workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", runs]]));
  return new DashboardState(workspaceState);
}

// ============================================================================
// costPerIssue type/size mapping (Issue #2546)
// ============================================================================

describe("getCostPerIssueAggregations — type/size from PipelineRunSummary (Issue #2546)", () => {
  it("should include issueType and sizeLabel from run data", () => {
    const runs = [
      makeRun({ issueNumber: 100, costUsd: 5.0, issueType: "feature", sizeLabel: "M" }),
    ];
    const state = createState(runs);
    const aggregates = state.getAggregates();
    const entry = aggregates.costPerIssue.find((a) => a.issueNumber === 100);

    expect(entry).toBeDefined();
    expect(entry!.issueType).toBe("feature");
    expect(entry!.sizeLabel).toBe("M");
  });

  it("should use most-recent run's type/size when issue has multiple runs", () => {
    const runs = [
      // Oldest run — no type/size
      makeRun({
        issueNumber: 100,
        costUsd: 2.0,
        startedAt: "2026-03-01T00:00:00.000Z",
        issueType: null,
        sizeLabel: null,
      }),
      // Middle run — partial
      makeRun({
        issueNumber: 100,
        costUsd: 3.0,
        startedAt: "2026-03-02T00:00:00.000Z",
        issueType: "bug",
        sizeLabel: null,
      }),
      // Most recent run — both populated
      makeRun({
        issueNumber: 100,
        costUsd: 1.5,
        startedAt: "2026-03-03T00:00:00.000Z",
        issueType: "bug",
        sizeLabel: "L",
      }),
    ];
    const state = createState(runs);
    const aggregates = state.getAggregates();
    const entry = aggregates.costPerIssue.find((a) => a.issueNumber === 100);

    expect(entry).toBeDefined();
    expect(entry!.issueType).toBe("bug");
    expect(entry!.sizeLabel).toBe("L");
    expect(entry!.runCount).toBe(3);
    expect(entry!.totalCostUsd).toBeCloseTo(6.5);
  });

  it("should return null issueType and sizeLabel when runs have no type/size", () => {
    const runs = [makeRun({ issueNumber: 200, costUsd: 4.0 })];
    const state = createState(runs);
    const aggregates = state.getAggregates();
    const entry = aggregates.costPerIssue.find((a) => a.issueNumber === 200);

    expect(entry).toBeDefined();
    expect(entry!.issueType).toBeNull();
    expect(entry!.sizeLabel).toBeNull();
  });

  it("should handle explicit null type/size gracefully", () => {
    const runs = [makeRun({ issueNumber: 300, costUsd: 1.0, issueType: null, sizeLabel: null })];
    const state = createState(runs);
    const aggregates = state.getAggregates();
    const entry = aggregates.costPerIssue.find((a) => a.issueNumber === 300);

    expect(entry).toBeDefined();
    expect(entry!.issueType).toBeNull();
    expect(entry!.sizeLabel).toBeNull();
  });

  it("should preserve type/size across different issues independently", () => {
    const runs = [
      makeRun({ issueNumber: 10, costUsd: 2.0, issueType: "feature", sizeLabel: "S" }),
      makeRun({ issueNumber: 20, costUsd: 3.0, issueType: "bug", sizeLabel: "XL" }),
      makeRun({ issueNumber: 30, costUsd: 1.0, issueType: null, sizeLabel: null }),
    ];
    const state = createState(runs);
    const aggregates = state.getAggregates();

    const e10 = aggregates.costPerIssue.find((a) => a.issueNumber === 10);
    const e20 = aggregates.costPerIssue.find((a) => a.issueNumber === 20);
    const e30 = aggregates.costPerIssue.find((a) => a.issueNumber === 30);

    expect(e10!.issueType).toBe("feature");
    expect(e10!.sizeLabel).toBe("S");
    expect(e20!.issueType).toBe("bug");
    expect(e20!.sizeLabel).toBe("XL");
    expect(e30!.issueType).toBeNull();
    expect(e30!.sizeLabel).toBeNull();
  });
});

// ============================================================================
// Serialization round-trip (Issue #2546)
// ============================================================================

describe("Serialization round-trip — issueType and sizeLabel (Issue #2546)", () => {
  it("should preserve issueType and sizeLabel through save/load cycle", () => {
    const runs = [makeRun({ issueNumber: 42, costUsd: 5.0, issueType: "feature", sizeLabel: "M" })];
    const state = createState(runs);

    // getAggregates triggers the aggregation; the real test is that
    // the deserialized history has issueType/sizeLabel intact
    const aggregates = state.getAggregates();
    const entry = aggregates.costPerIssue.find((a) => a.issueNumber === 42);

    expect(entry!.issueType).toBe("feature");
    expect(entry!.sizeLabel).toBe("M");
  });

  it("should handle runs with undefined issueType/sizeLabel (backward compat)", () => {
    // Simulates old serialized data without issueType/sizeLabel fields
    const oldRun = {
      issueNumber: 50,
      title: "Issue #50",
      branch: "feat/50",
      startedAt: "2026-03-01T00:00:00.000Z",
      completedAt: "2026-03-01T01:00:00.000Z",
      status: "complete" as const,
      stages: [],
      usage: {
        inputTokens: 10000,
        outputTokens: 5000,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 3.0,
        durationMs: 3600000,
        stageCount: 6,
      },
      toolCalls: [],
      timeSavedMs: 7200000,
      // No issueType or sizeLabel — old data
    };

    const workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", [oldRun]]]));
    const state = new DashboardState(workspaceState);
    const aggregates = state.getAggregates();
    const entry = aggregates.costPerIssue.find((a) => a.issueNumber === 50);

    expect(entry).toBeDefined();
    // undefined ?? null → null in getCostPerIssueAggregations
    expect(entry!.issueType).toBeNull();
    expect(entry!.sizeLabel).toBeNull();
  });
});
