/**
 * DashboardHtml.pipelineTabBlankFix.test.ts
 *
 * Tests for Issue #1842 — Pipeline tab blank fix.
 * Verifies that the pipeline tab shows historical run data when no pipeline is
 * actively running, instead of rendering blank.
 */

import { describe, it, expect } from "vitest";
import { getDashboardHtml } from "../../../src/views/dashboard/DashboardHtml";
import { getToolCallsHtml } from "../../../src/views/dashboard/tabs/PipelineTabHtml";
import type {
  PipelineRunSummary,
  DashboardAggregates,
  TimeSavingsConfig,
} from "../../../src/views/dashboard/DashboardState";

const mockWebview = { cspSource: "test-csp" } as any;

import { makeEmptyAggregates } from "./fixtures/aggregates";

const emptyAggregates: DashboardAggregates = makeEmptyAggregates();

const timeSavingsConfig: TimeSavingsConfig = {
  estimatedManualMinutes: {},
  hourlyRate: 50,
};

function createRun(overrides: Partial<PipelineRunSummary> = {}): PipelineRunSummary {
  return {
    issueNumber: 42,
    title: "Test pipeline run",
    branch: "feat/42-test",
    status: "complete" as any,
    stages: [
      { stage: "issue-pickup", status: "complete" },
      { stage: "feature-planning", status: "complete" },
    ] as any,
    toolCalls: [],
    startedAt: new Date("2025-06-01T12:00:00Z"),
    usage: {
      inputTokens: 1000,
      outputTokens: 200,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0.05,
      durationMs: 60000,
      stageCount: 2,
    },
    ...overrides,
  };
}

describe("Pipeline tab blank fix (Issue #1842)", () => {
  it('shows "Most Recent Pipeline Run" header when currentRun is null and history has items', () => {
    const historyRun = createRun();
    const html = getDashboardHtml(
      mockWebview,
      null,
      [historyRun],
      emptyAggregates,
      timeSavingsConfig
    );

    expect(html).toContain("Most Recent Pipeline Run");
    expect(html).not.toContain("Current Pipeline Run");
  });

  it("shows explicit empty state when currentRun is null and history is empty", () => {
    const html = getDashboardHtml(mockWebview, null, [], emptyAggregates, timeSavingsConfig);

    expect(html).toContain("No pipeline runs yet");
    expect(html).not.toContain('id="section-pipeline-progress"');
  });

  it('shows "Current Pipeline Run" and cost estimate when currentRun is not null', () => {
    const activeRun = createRun({ status: "running" as any });
    const html = getDashboardHtml(mockWebview, activeRun, [], emptyAggregates, timeSavingsConfig);

    expect(html).toContain("Current Pipeline Run");
    expect(html).not.toContain("Most Recent Pipeline Run");
    // Cost estimate widget placeholder should be present (getCostEstimateWidgetHtml called)
    expect(html).toContain('id="section-pipeline-progress"');
  });

  it("does not render Pre-Run Cost Estimate section for historical runs", () => {
    const historyRun = createRun();
    const html = getDashboardHtml(
      mockWebview,
      null,
      [historyRun],
      emptyAggregates,
      timeSavingsConfig
    );

    // getCostEstimateWidgetHtml is only called when currentRun is set
    // For historical runs it's suppressed — "Pre-Run Cost Estimate" header never appears
    expect(html).not.toContain("Pre-Run Cost Estimate");
  });

  it("renders data-auto-load-issue attribute for most recent historical run with no tool calls", () => {
    const historyRun = createRun({ toolCalls: [] });
    const html = getDashboardHtml(
      mockWebview,
      null,
      [historyRun],
      emptyAggregates,
      timeSavingsConfig
    );

    expect(html).toContain(`data-auto-load-issue="${historyRun.issueNumber}"`);
    expect(html).not.toContain("Load Tool Calls");
  });

  it("getToolCallsHtml with autoLoad=true renders data-auto-load-issue instead of button", () => {
    const html = getToolCallsHtml([], 42, true);

    expect(html).toContain('data-auto-load-issue="42"');
    expect(html).not.toContain("Load Tool Calls");
    expect(html).toContain("tool-calls-load-container");
  });

  it("getToolCallsHtml with autoLoad=false still renders manual load button", () => {
    const html = getToolCallsHtml([], 42, false);

    expect(html).toContain("Load Tool Calls");
    expect(html).toContain('data-action="load-tool-calls"');
    expect(html).not.toContain("data-auto-load-issue");
  });

  it("includes auto-load JS in the pipeline panel script (Issue #1842)", () => {
    const html = getDashboardHtml(
      mockWebview,
      null,
      [createRun()],
      emptyAggregates,
      timeSavingsConfig
    );

    expect(html).toContain("data-auto-load-issue");
    expect(html).toContain("autoLoadIssue");
    expect(html).toContain("type: 'loadRunDetails'");
  });

  it("shows section-pipeline-progress for historical run", () => {
    const historyRun = createRun();
    const html = getDashboardHtml(
      mockWebview,
      null,
      [historyRun],
      emptyAggregates,
      timeSavingsConfig
    );

    expect(html).toContain('id="section-pipeline-progress"');
  });
});
