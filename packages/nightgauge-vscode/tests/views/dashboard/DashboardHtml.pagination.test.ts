/**
 * DashboardHtml.pagination.test.ts
 *
 * Tests for history pagination rendering (Issue #983).
 * Verifies "Showing X of Y", "Load More" button, and empty state.
 */

import { describe, it, expect } from "vitest";
import { getDashboardHtml } from "../../../src/views/dashboard/DashboardHtml";
import type {
  PipelineRunSummary,
  DashboardAggregates,
  TimeSavingsConfig,
} from "../../../src/views/dashboard/DashboardState";
import { DEFAULT_TIME_SAVINGS_CONFIG } from "../../../src/views/dashboard/DashboardState";

const mockWebview = { cspSource: "test-csp" } as any;

function createHistoryRun(issueNumber: number): PipelineRunSummary {
  return {
    issueNumber,
    title: `Test issue ${issueNumber}`,
    branch: `feat/${issueNumber}-test`,
    status: "complete" as any,
    stages: [],
    toolCalls: [],
    startedAt: new Date("2026-02-01T12:00:00Z"),
    usage: {
      inputTokens: 100,
      outputTokens: 50,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0.01,
      durationMs: 5000,
      stageCount: 6,
    },
  };
}

import { makeEmptyAggregates } from "./fixtures/aggregates";

const emptyAggregates: DashboardAggregates = makeEmptyAggregates();

describe("DashboardHtml - History Pagination (Issue #983)", () => {
  it('renders "Showing X of Y" when pagination info is provided', () => {
    const history = Array.from({ length: 10 }, (_, i) => createHistoryRun(100 + i));

    const html = getDashboardHtml(
      mockWebview,
      null,
      history,
      emptyAggregates,
      DEFAULT_TIME_SAVINGS_CONFIG,
      "all",
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      { totalCount: 30, hasMore: true }
    );

    expect(html).toContain("Showing 10 of 30");
  });

  it('renders "Load More" button when hasMore is true', () => {
    const history = Array.from({ length: 20 }, (_, i) => createHistoryRun(100 + i));

    const html = getDashboardHtml(
      mockWebview,
      null,
      history,
      emptyAggregates,
      DEFAULT_TIME_SAVINGS_CONFIG,
      "all",
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      { totalCount: 50, hasMore: true }
    );

    expect(html).toContain("Load More");
    expect(html).toContain('data-action="load-more-history"');
  });

  it('does NOT render "Load More" button when all items are shown', () => {
    const history = Array.from({ length: 5 }, (_, i) => createHistoryRun(100 + i));

    const html = getDashboardHtml(
      mockWebview,
      null,
      history,
      emptyAggregates,
      DEFAULT_TIME_SAVINGS_CONFIG,
      "all",
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      { totalCount: 5, hasMore: false }
    );

    expect(html).toContain("Showing 5 of 5");
    expect(html).not.toContain("Load More");
  });

  it("renders empty state when history is empty", () => {
    const html = getDashboardHtml(
      mockWebview,
      null,
      [],
      emptyAggregates,
      DEFAULT_TIME_SAVINGS_CONFIG,
      "all",
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      { totalCount: 0, hasMore: false }
    );

    expect(html).toContain("No pipeline runs recorded");
    expect(html).not.toContain("Load More");
    expect(html).not.toContain("Showing");
  });

  it("works without pagination info (backward compat)", () => {
    const history = Array.from({ length: 3 }, (_, i) => createHistoryRun(100 + i));

    const html = getDashboardHtml(
      mockWebview,
      null,
      history,
      emptyAggregates,
      DEFAULT_TIME_SAVINGS_CONFIG
    );

    // Should show count using history.length as total
    expect(html).toContain("Showing 3 of 3");
    expect(html).not.toContain("Load More");
  });
});
