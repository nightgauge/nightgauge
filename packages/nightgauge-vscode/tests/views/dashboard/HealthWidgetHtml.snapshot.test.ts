/**
 * HealthWidgetHtml.snapshot.test.ts
 *
 * HTML snapshot regression tests for getHealthWidgetHtml().
 * Supplements the existing behavioral tests with structural snapshots
 * to catch silent regressions in the health widget template generator.
 *
 * @see Issue #1242 - Add HTML snapshot regression tests for *Html.ts
 */

import { describe, it, expect } from "vitest";
import { getHealthWidgetHtml } from "../../../src/views/dashboard/HealthWidgetHtml";
import type { HealthWidgetData } from "../../../src/views/dashboard/HealthWidgetTypes";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function createMockHealthData(overrides?: Partial<HealthWidgetData>): HealthWidgetData {
  return {
    summary: { score: 75, status: "good", components: [] },
    sparklines: [],
    alerts: [],
    recommendations: [],
    predictionAccuracy: null,
    lastUpdated: "2026-01-01T00:00:00Z",
    isEmpty: false,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Snapshot tests
// ---------------------------------------------------------------------------

describe("getHealthWidgetHtml snapshots (Issue #1242)", () => {
  it("empty state — isEmpty true", () => {
    const data = createMockHealthData({ isEmpty: true });
    const html = getHealthWidgetHtml(data);
    expect(html).toMatchSnapshot();
  });

  it("healthy state — score 82, good status, with components", () => {
    const data = createMockHealthData({
      summary: {
        score: 82,
        status: "good",
        components: [
          {
            name: "successRate",
            score: 90,
            weight: 0.25,
            trend: "improving",
            label: "Success Rate",
          },
          {
            name: "tokenEfficiency",
            score: 78,
            weight: 0.18,
            trend: "stable",
            label: "Token Efficiency",
          },
        ],
      },
      sparklines: [
        {
          metric: "cost",
          label: "Cost per Run",
          data: [0.1, 0.12, 0.15, 0.14, 0.13],
          trend: "stable",
          polarity: "lower-is-better",
          unit: "USD",
        },
      ],
      lastUpdated: "2026-01-01T12:00:00Z",
    });
    const html = getHealthWidgetHtml(data);
    expect(html).toMatchSnapshot();
  });

  it("with alerts — warning and critical", () => {
    const data = createMockHealthData({
      summary: { score: 45, status: "poor", components: [] },
      alerts: [
        {
          level: "critical",
          stage: "feature-dev",
          metric: "failure-rate",
          message: "Implementation failures above threshold",
          timestamp: "2026-01-01T10:00:00Z",
        },
        {
          level: "warning",
          stage: "pr-create",
          metric: "cost-spike",
          message: "Cost increased significantly in last 3 runs",
          timestamp: "2026-01-01T10:30:00Z",
        },
      ],
    });
    const html = getHealthWidgetHtml(data);
    expect(html).toMatchSnapshot();
  });

  it("with recommendations including actionable config-patch", () => {
    const data = createMockHealthData({
      recommendations: [
        {
          title: "Cap context size",
          description: "Reduce oversized context to lower token usage",
          estimatedSavingsUsd: 0.25,
          category: "oversized-context",
          severity: "warning",
          action: {
            type: "config-patch",
            configPath: "pipeline.max_turns",
            suggestedValue: 5,
            label: "Cap context at 5000 tokens",
          },
        },
        {
          title: "Review tool calls",
          description: "Check tool call patterns for inefficiency",
          estimatedSavingsUsd: 0.08,
          category: "tool-call-inefficiency",
          severity: "info",
        },
      ],
    });
    const html = getHealthWidgetHtml(data);
    expect(html).toMatchSnapshot();
  });

  it("collapsed state", () => {
    const data = createMockHealthData({
      summary: { score: 60, status: "fair", components: [] },
    });
    const html = getHealthWidgetHtml(data, true);
    expect(html).toMatchSnapshot();
  });
});
