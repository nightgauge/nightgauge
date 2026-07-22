/**
 * HealthWidgetHtml.test.ts
 *
 * Unit tests for getHealthWidgetHtml() and rendering helpers covering:
 * - Empty state renders "Run your first pipeline" message
 * - Health summary card renders score and status badge
 * - Alert list renders correct count
 * - Recommendations limited to top 5
 * - Collapsed state adds collapsed class
 *
 * @see Issue #655 - Pipeline Health Dashboard Widget
 */

import { describe, it, expect } from "vitest";
import { getHealthWidgetHtml } from "../../../src/views/dashboard/HealthWidgetHtml";
import type { HealthWidgetData } from "../../../src/views/dashboard/HealthWidgetTypes";

// ---------------------------------------------------------------------------
// Mock factory
// ---------------------------------------------------------------------------

const createMockHealthData = (overrides?: Partial<HealthWidgetData>): HealthWidgetData => ({
  summary: { score: 75, status: "good", components: [] },
  sparklines: [],
  alerts: [],
  recommendations: [],
  predictionAccuracy: null,
  lastUpdated: "2026-01-01T00:00:00Z",
  isEmpty: false,
  ...overrides,
});

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("getHealthWidgetHtml", () => {
  it("renders empty state message when isEmpty is true", () => {
    // Arrange
    const data = createMockHealthData({ isEmpty: true });

    // Act
    const html = getHealthWidgetHtml(data);

    // Assert
    expect(html).toContain("Run your first pipeline");
    expect(html).toContain("health-empty-state");
  });

  it("renders health score and status badge in summary card", () => {
    // Arrange
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
        ],
      },
    });

    // Act
    const html = getHealthWidgetHtml(data);

    // Assert — score and status should both appear in output
    expect(html).toContain("82");
    expect(html).toContain("GOOD");
    expect(html).toContain("health-good");
    expect(html).toContain("Success Rate");
  });

  it("renders alerts with correct count in heading", () => {
    // Arrange
    const alerts = [
      {
        level: "warning" as const,
        stage: "plan",
        metric: "cost-spike",
        message: "Cost increased 50% in last 3 runs",
        timestamp: "2026-01-01T00:00:00Z",
      },
      {
        level: "critical" as const,
        stage: "implement",
        metric: "failure-rate",
        message: "Implementation failures above threshold",
        timestamp: "2026-01-01T00:00:00Z",
      },
      {
        level: "info" as const,
        stage: "review",
        metric: "token-drift",
        message: "Review token usage trending up",
        timestamp: "2026-01-01T00:00:00Z",
      },
    ];
    const data = createMockHealthData({ alerts });

    // Act
    const html = getHealthWidgetHtml(data);

    // Assert — heading should show total count, each alert message appears
    expect(html).toContain("Active Alerts (3)");
    expect(html).toContain("Cost increased 50% in last 3 runs");
    expect(html).toContain("Implementation failures above threshold");
    expect(html).toContain("Review token usage trending up");
    expect(html).toContain("alert-warning");
    expect(html).toContain("alert-critical");
    expect(html).toContain("alert-info");
  });

  it("renders at most 5 recommendations even when more are provided", () => {
    // Arrange — 10 recommendations
    const recommendations = Array.from({ length: 10 }, (_, i) => ({
      title: `Recommendation ${i + 1}`,
      description: `Description for recommendation ${i + 1}`,
      estimatedSavingsUsd: (i + 1) * 0.1,
      category: "efficiency",
      severity: "info",
    }));
    const data = createMockHealthData({ recommendations });

    // Act
    const html = getHealthWidgetHtml(data);

    // Assert — only the first 5 should appear
    expect(html).toContain("Recommendation 1");
    expect(html).toContain("Recommendation 5");
    expect(html).not.toContain("Recommendation 6");
    expect(html).not.toContain("Recommendation 10");
  });

  it("adds collapsed class when collapsed parameter is true", () => {
    // Arrange
    const data = createMockHealthData();

    // Act
    const html = getHealthWidgetHtml(data, true);

    // Assert
    expect(html).toContain("collapsed");
  });

  it("does not add collapsed class when collapsed parameter is false", () => {
    // Arrange
    const data = createMockHealthData();

    // Act
    const html = getHealthWidgetHtml(data, false);

    // Assert — the widget class should not include 'collapsed'
    expect(html).not.toMatch(/health-widget\s+collapsed/);
  });

  it("escapes HTML special characters in data fields to prevent XSS", () => {
    // Arrange — inject XSS payloads in string fields
    const data = createMockHealthData({
      summary: {
        score: 75,
        status: "good",
        components: [
          {
            name: "test",
            score: 50,
            weight: 0.5,
            trend: "stable",
            label: '<script>alert("xss")</script>',
          },
        ],
      },
      alerts: [
        {
          level: "warning",
          stage: "<img src=x onerror=alert(1)>",
          metric: "test",
          message: '"><script>alert("xss")</script>',
          timestamp: "2026-01-01T00:00:00Z",
        },
      ],
      recommendations: [
        {
          title: "<b onmouseover=alert(1)>hover</b>",
          description: 'Use &lt;script&gt; tags & "quotes"',
          estimatedSavingsUsd: 1.5,
          category: "test",
          severity: "info",
        },
      ],
      lastUpdated: '<script>alert("time")</script>',
    });

    // Act
    const html = getHealthWidgetHtml(data);

    // Assert — raw HTML tags must NOT appear unescaped
    expect(html).not.toContain("<script>");
    expect(html).not.toContain("<img src=x");
    expect(html).not.toContain("<b onmouseover");
    // Escaped versions should appear
    expect(html).toContain("&lt;script&gt;");
    expect(html).toContain("&lt;img src=x");
  });

  // --- Actionable Recommendations (Issue #787) ---

  it("renders Apply button for config-patch recommendations", () => {
    // Arrange
    const data = createMockHealthData({
      recommendations: [
        {
          title: "Cap context size",
          description: "Reduce oversized context",
          estimatedSavingsUsd: 0.5,
          category: "oversized-context",
          severity: "warning",
          action: {
            type: "config-patch",
            configPath: "pipeline.max_turns",
            suggestedValue: 5,
            label: "Cap context at 5000 tokens",
          },
        },
      ],
    });

    // Act
    const html = getHealthWidgetHtml(data);

    // Assert
    expect(html).toContain("recommendation-apply-btn");
    expect(html).toContain("Cap context at 5000 tokens");
    expect(html).toContain('data-action="apply-recommendation"');
  });

  it("does not render Apply button for info-only recommendations", () => {
    // Arrange
    const data = createMockHealthData({
      recommendations: [
        {
          title: "Review tool calls",
          description: "Check tool call patterns",
          estimatedSavingsUsd: 0.1,
          category: "tool-call-inefficiency",
          severity: "info",
          action: {
            type: "info-only",
            configPath: "",
            suggestedValue: null,
            label: "Review tool call patterns",
          },
        },
      ],
    });

    // Act
    const html = getHealthWidgetHtml(data);

    // Assert
    expect(html).not.toContain("recommendation-apply-btn");
    expect(html).not.toContain('data-action="apply-recommendation"');
  });

  it("renders Applied badge and Revert button for applied categories", () => {
    // Arrange
    const data = createMockHealthData({
      recommendations: [
        {
          title: "Enable caching",
          description: "Enable aggressive caching",
          estimatedSavingsUsd: 0.3,
          category: "cache-miss-patterns",
          severity: "warning",
          action: {
            type: "config-patch",
            configPath: "pipeline.auto_fix",
            suggestedValue: true,
            label: "Enable aggressive caching",
          },
        },
      ],
    });

    // Act
    const html = getHealthWidgetHtml(data, false, ["cache-miss-patterns"]);

    // Assert — should show Applied badge and Revert, not Apply
    expect(html).toContain("recommendation-applied-badge");
    expect(html).toContain("Applied");
    expect(html).toContain("recommendation-revert-btn");
    expect(html).toContain('data-action="revert-recommendation"');
    expect(html).not.toContain("recommendation-apply-btn");
  });

  // --- Sub-cent savings display (Issue #984) ---

  it('renders sub-cent savings as "< $0.01" instead of "$0.00"', () => {
    // Arrange
    const data = createMockHealthData({
      recommendations: [
        {
          title: "Small savings recommendation",
          description: "A recommendation with very small savings",
          estimatedSavingsUsd: 0.003,
          category: "efficiency",
          severity: "info",
        },
      ],
    });

    // Act
    const html = getHealthWidgetHtml(data);

    // Assert — should show "< $0.01", not "$0.00"
    expect(html).toContain("~< $0.01");
    expect(html).not.toContain("~$0.00");
  });

  it("renders normal savings with two decimal places", () => {
    // Arrange
    const data = createMockHealthData({
      recommendations: [
        {
          title: "Normal savings recommendation",
          description: "A recommendation with normal savings",
          estimatedSavingsUsd: 0.5,
          category: "efficiency",
          severity: "info",
        },
      ],
    });

    // Act
    const html = getHealthWidgetHtml(data);

    // Assert — should show "$0.50"
    expect(html).toContain("~$0.50");
  });

  it("renders exactly $0.01 savings with two decimal places", () => {
    // Arrange
    const data = createMockHealthData({
      recommendations: [
        {
          title: "Boundary savings",
          description: "Exactly one cent",
          estimatedSavingsUsd: 0.01,
          category: "efficiency",
          severity: "info",
        },
      ],
    });

    // Act
    const html = getHealthWidgetHtml(data);

    // Assert — boundary: >= 0.01 shows exact value
    expect(html).toContain("~$0.01");
    expect(html).not.toContain("< $0.01");
  });

  it("renders recommendation without button when no action provided", () => {
    // Arrange
    const data = createMockHealthData({
      recommendations: [
        {
          title: "Generic recommendation",
          description: "Some advice",
          estimatedSavingsUsd: 0.2,
          category: "general",
          severity: "info",
        },
      ],
    });

    // Act
    const html = getHealthWidgetHtml(data);

    // Assert
    expect(html).toContain("Generic recommendation");
    expect(html).not.toContain("recommendation-apply-btn");
    expect(html).not.toContain("recommendation-revert-btn");
  });

  // --- Sparkline averages (Issue #988) ---

  it("filters zero values from sparkline averages and shows correct run count", () => {
    const data = createMockHealthData({
      sparklines: [
        {
          metric: "cost",
          label: "Cost per Run",
          data: [0, 0, 1.5, 0, 2.5],
          trend: "stable",
          polarity: "lower-is-better",
          unit: "USD",
        },
      ],
    });

    const html = getHealthWidgetHtml(data);

    // Should average only 1.5 and 2.5 = 2.0, not (0+0+1.5+0+2.5)/5 = 0.8
    expect(html).toContain("$2.0000");
    expect(html).toContain("avg of 2 runs");
  });

  it('shows "no data" when all sparkline values are zero', () => {
    const data = createMockHealthData({
      sparklines: [
        {
          metric: "tokens",
          label: "Token Usage",
          data: [0, 0, 0],
          trend: "stable",
          polarity: "lower-is-better",
          unit: "tokens",
        },
      ],
    });

    const html = getHealthWidgetHtml(data);

    expect(html).toContain("no data");
    expect(html).toContain("0"); // avg is 0
  });

  it('shows singular "run" when only 1 data point has data', () => {
    const data = createMockHealthData({
      sparklines: [
        {
          metric: "cost",
          label: "Cost per Run",
          data: [0, 0, 0, 0, 3.0],
          trend: "stable",
          polarity: "lower-is-better",
          unit: "USD",
        },
      ],
    });

    const html = getHealthWidgetHtml(data);

    expect(html).toContain("avg of 1 run");
    expect(html).toContain("$3.0000");
  });

  // --- Polarity-driven color coding (cost-trend bug regression test) ---

  it("colors a falling cost sparkline green (improving) and a rising one red", () => {
    const falling = createMockHealthData({
      sparklines: [
        {
          metric: "cost",
          label: "Cost per Run",
          data: [2.0, 1.5, 1.0],
          trend: "down",
          polarity: "lower-is-better",
          unit: "USD",
        },
      ],
    });
    const rising = createMockHealthData({
      sparklines: [
        {
          metric: "cost",
          label: "Cost per Run",
          data: [1.0, 1.5, 2.0],
          trend: "up",
          polarity: "lower-is-better",
          unit: "USD",
        },
      ],
    });

    expect(getHealthWidgetHtml(falling)).toContain("trend-improving");
    expect(getHealthWidgetHtml(rising)).toContain("trend-degrading");
  });

  it("colors a rising success-rate sparkline green and a falling one red", () => {
    const rising = createMockHealthData({
      sparklines: [
        {
          metric: "successRate",
          label: "Success Rate",
          data: [50, 75, 100],
          trend: "up",
          polarity: "higher-is-better",
          unit: "%",
          treatZeroAsMissing: false,
        },
      ],
    });
    const falling = createMockHealthData({
      sparklines: [
        {
          metric: "successRate",
          label: "Success Rate",
          data: [100, 50, 0],
          trend: "down",
          polarity: "higher-is-better",
          unit: "%",
          treatZeroAsMissing: false,
        },
      ],
    });

    expect(getHealthWidgetHtml(rising)).toContain("trend-improving");
    expect(getHealthWidgetHtml(falling)).toContain("trend-degrading");
  });

  it("does not report 'no data' for success rate when all recent runs failed (0%)", () => {
    // Reproduces the bug from the Overview audit: 5 failed runs showed
    // "Success Rate ↓ 0% no data" because the renderer filtered v > 0.
    const data = createMockHealthData({
      sparklines: [
        {
          metric: "successRate",
          label: "Success Rate",
          data: [0, 0, 0, 0, 0],
          trend: "stable",
          polarity: "higher-is-better",
          unit: "%",
          treatZeroAsMissing: false,
        },
      ],
    });

    const html = getHealthWidgetHtml(data);

    expect(html).toContain("avg of 5 runs");
    expect(html).not.toContain("no data");
  });

  it("applies health-component-insufficient class when insufficientData is true (#991)", () => {
    const data = createMockHealthData({
      summary: {
        score: 50,
        status: "fair",
        components: [
          {
            name: "tokenEfficiencyTrend",
            score: 50,
            weight: 0.18,
            trend: "stable",
            label: "Token Efficiency",
            insufficientData: true,
            insufficientDataMessage: "Need 3 more runs for trend data",
          },
        ],
      },
    });

    const html = getHealthWidgetHtml(data);

    expect(html).toContain("health-component-insufficient");
    expect(html).toContain("Need 3 more runs for trend data");
    // Trend arrow replaced with dash
    expect(html).toContain("—");
  });

  it("does not apply insufficient class when insufficientData is falsy (#991)", () => {
    const data = createMockHealthData({
      summary: {
        score: 75,
        status: "good",
        components: [
          {
            name: "tokenEfficiencyTrend",
            score: 55,
            weight: 0.18,
            trend: "improving",
            label: "Token Efficiency",
          },
        ],
      },
    });

    const html = getHealthWidgetHtml(data);

    expect(html).not.toContain("health-component-insufficient");
    expect(html).toContain("↑"); // normal trend arrow
  });
});
