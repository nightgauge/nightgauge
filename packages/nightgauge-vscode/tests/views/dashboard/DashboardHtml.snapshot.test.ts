/**
 * DashboardHtml.snapshot.test.ts
 *
 * HTML snapshot regression tests for getDashboardHtml().
 * Captures structural HTML output to catch silent regressions
 * in the dashboard template generator.
 *
 * Snapshots are stored in __snapshots__/ and should be reviewed
 * in git diff when template changes are made intentionally.
 *
 * @see Issue #1242 - Add HTML snapshot regression tests for *Html.ts
 */

import { describe, it, expect } from "vitest";
import { getDashboardHtml } from "../../../src/views/dashboard/DashboardHtml";
import type {
  PipelineRunSummary,
  DashboardAggregates,
  TimeSavingsConfig,
  HistoryPaginationInfo,
} from "../../../src/views/dashboard/DashboardState";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const mockWebview = { cspSource: "test-csp" } as any;

/**
 * Normalize non-deterministic values in HTML output before snapshotting.
 * - Nonces change on every render
 * - renderTs is Date.now()
 */
function normalize(html: string): string {
  return (
    html
      .replace(/nonce-[A-Za-z0-9]{32}/g, "nonce-NONCE")
      .replace(/nonce="[A-Za-z0-9]{32}"/g, 'nonce="NONCE"')
      // renderTs appears as: renderTs=' + 1772804594263
      .replace(/renderTs=' \+ \d+/g, "renderTs=' + TIMESTAMP")
      // "Last updated" header timestamp changes on every render
      .replace(
        /Last updated: \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} UTC/g,
        "Last updated: TIMESTAMP UTC"
      )
      // Relative time strings change daily — normalize to fixed text
      .replace(/\d+ days? ago/g, "N days ago")
      .replace(/\d+ hours? ago/g, "N hours ago")
      .replace(/\d+ minutes? ago/g, "N minutes ago")
      .replace(/\d+ months? ago/g, "N months ago")
      // Elapsed-time in the Overview activity widget is computed from
      // (Date.now() - startedAt), so it drifts every minute the test runs.
      .replace(
        /(<div class="activity-metric-value">)[^<]+(<\/div>\s*<div class="activity-metric-label">Elapsed<\/div>)/g,
        "$1ELAPSED$2"
      )
  );
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

import { makeEmptyAggregates } from "./fixtures/aggregates";

const emptyAggregates: DashboardAggregates = makeEmptyAggregates();

const timeSavingsConfig: TimeSavingsConfig = {
  estimatedManualMinutes: {},
  hourlyRate: 50,
};

function createRun(overrides: Partial<PipelineRunSummary> = {}): PipelineRunSummary {
  return {
    issueNumber: 42,
    title: "Add feature X",
    branch: "feat/42-add-feature-x",
    status: "completed" as any,
    stages: [
      {
        stage: "issue-pickup",
        status: "completed",
        durationMs: 5000,
        startedAt: new Date("2026-01-01T10:00:00Z"),
        completedAt: new Date("2026-01-01T10:00:05Z"),
      },
      {
        stage: "feature-dev",
        status: "completed",
        durationMs: 120000,
        startedAt: new Date("2026-01-01T10:00:05Z"),
        completedAt: new Date("2026-01-01T10:02:05Z"),
      },
    ],
    toolCalls: [],
    startedAt: new Date("2026-01-01T10:00:00Z"),
    completedAt: new Date("2026-01-01T10:05:00Z"),
    usage: {
      inputTokens: 5000,
      outputTokens: 2000,
      cacheReadTokens: 1000,
      cacheCreationTokens: 500,
      costUsd: 0.15,
      durationMs: 300000,
      stageCount: 2,
    },
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Snapshot tests
// ---------------------------------------------------------------------------

describe("getDashboardHtml snapshots (Issue #1242)", () => {
  it("empty state — no current run, no history", () => {
    const html = getDashboardHtml(mockWebview, null, [], emptyAggregates, timeSavingsConfig);
    expect(normalize(html)).toMatchSnapshot();
  });

  it("active pipeline state — currentRun present, running status", () => {
    const activeRun = createRun({
      status: "running" as any,
      completedAt: undefined,
      stages: [
        {
          stage: "feature-dev",
          status: "running",
          startedAt: new Date("2026-01-01T10:00:00Z"),
        },
      ],
    });

    const html = getDashboardHtml(
      mockWebview,
      activeRun,
      [],
      { ...emptyAggregates, totalRuns: 1, sessionRuns: 1 },
      timeSavingsConfig
    );
    expect(normalize(html)).toMatchSnapshot();
  });

  it("completed run with tool calls in history", () => {
    const completedRun = createRun({
      toolCalls: [
        {
          tool: "Read",
          target: "src/index.ts",
          timestamp: new Date("2026-01-01T10:01:00Z"),
        },
        {
          tool: "Edit",
          target: "src/index.ts",
          timestamp: new Date("2026-01-01T10:01:30Z"),
        },
        {
          tool: "Bash",
          target: "npm test",
          timestamp: new Date("2026-01-01T10:02:00Z"),
        },
      ],
    });

    const aggregates: DashboardAggregates = {
      ...emptyAggregates,
      totalRuns: 1,
      sessionRuns: 1,
      totalCostUsd: 0.15,
      sessionCostUsd: 0.15,
      successRate: 100,
    };

    const html = getDashboardHtml(mockWebview, null, [completedRun], aggregates, timeSavingsConfig);
    expect(normalize(html)).toMatchSnapshot();
  });

  it("pagination controls rendered — second page of history", () => {
    const runs = Array.from({ length: 5 }, (_, i) =>
      createRun({ issueNumber: i + 1, title: `Issue ${i + 1}` })
    );

    const pagination: HistoryPaginationInfo = {
      currentPage: 2,
      totalPages: 3,
      pageSize: 5,
      totalItems: 15,
    };

    const html = getDashboardHtml(
      mockWebview,
      null,
      runs,
      { ...emptyAggregates, totalRuns: 15 },
      timeSavingsConfig,
      "all",
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      pagination
    );
    expect(normalize(html)).toMatchSnapshot();
  });
});
