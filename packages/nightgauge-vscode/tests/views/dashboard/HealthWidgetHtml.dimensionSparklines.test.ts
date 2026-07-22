/**
 * HealthWidgetHtml.dimensionSparklines.test.ts
 *
 * Tests that the HTML renderer includes/omits the dimension sparklines section
 * based on the presence of dimensionSparklines in HealthWidgetData.
 *
 * @see Issue #1411 - Health trend persistence and dashboard sparklines
 */

import { describe, it, expect } from "vitest";
import {
  getHealthWidgetHtml,
  getDimensionSparklinesSectionHtml,
} from "../../../src/views/dashboard/HealthWidgetHtml";
import type {
  HealthWidgetData,
  DimensionSparkline,
} from "../../../src/views/dashboard/HealthWidgetTypes";

// ── Fixtures ────────────────────────────────────────────────────────────────

function makeDimensionSparklines(): DimensionSparkline[] {
  return [
    {
      dimension: "token-economics",
      label: "Token Economics",
      data: [60, 65, 70, 75, 80],
      trend: "improving",
    },
    {
      dimension: "cost-health",
      label: "Cost Health",
      data: [70, 68, 72, 71, 73],
      trend: "stable",
    },
    {
      dimension: "reliability",
      label: "Reliability",
      data: [85, 80, 75, 70, 65],
      trend: "declining",
    },
  ];
}

function makeFullWidgetData(overrides: Partial<HealthWidgetData> = {}): HealthWidgetData {
  return {
    summary: {
      score: 75,
      status: "good",
      components: [],
    },
    sparklines: [],
    alerts: [],
    recommendations: [],
    predictionAccuracy: null,
    lastUpdated: "2026-01-15T10:00:00Z",
    isEmpty: false,
    ...overrides,
  };
}

// ── getDimensionSparklinesSectionHtml ─────────────────────────────────────────

describe("getDimensionSparklinesSectionHtml", () => {
  it("returns empty string when sparklines is undefined", () => {
    expect(getDimensionSparklinesSectionHtml(undefined)).toBe("");
  });

  it("returns empty string when sparklines is empty array", () => {
    expect(getDimensionSparklinesSectionHtml([])).toBe("");
  });

  it("includes dimension sparklines section when data is present", () => {
    const html = getDimensionSparklinesSectionHtml(makeDimensionSparklines());
    expect(html).toContain("Dimension Trends");
    expect(html).toContain("dimension-sparklines");
    expect(html).toContain("sparklines-grid");
  });

  it("renders a card for each dimension", () => {
    const sparklines = makeDimensionSparklines();
    const html = getDimensionSparklinesSectionHtml(sparklines);
    expect(html).toContain("Token Economics");
    expect(html).toContain("Cost Health");
    expect(html).toContain("Reliability");
  });

  it("renders improving trend as ↑", () => {
    const html = getDimensionSparklinesSectionHtml([
      {
        dimension: "token-economics",
        label: "Token Economics",
        data: [60, 70, 80],
        trend: "improving",
      },
    ]);
    expect(html).toContain("↑");
    expect(html).toContain("dim-trend-improving");
  });

  it("renders declining trend as ↓", () => {
    const html = getDimensionSparklinesSectionHtml([
      {
        dimension: "reliability",
        label: "Reliability",
        data: [80, 70, 60],
        trend: "declining",
      },
    ]);
    expect(html).toContain("↓");
    expect(html).toContain("dim-trend-declining");
  });

  it("renders stable trend as →", () => {
    const html = getDimensionSparklinesSectionHtml([
      {
        dimension: "cost-health",
        label: "Cost Health",
        data: [70, 71, 70],
        trend: "stable",
      },
    ]);
    expect(html).toContain("→");
    expect(html).toContain("dim-trend-stable");
  });

  it("renders individual score bars for each data point", () => {
    const html = getDimensionSparklinesSectionHtml([
      {
        dimension: "reliability",
        label: "Reliability",
        data: [40, 60, 80],
        trend: "improving",
      },
    ]);
    // Three bars should be rendered
    expect((html.match(/class="dim-bar/g) ?? []).length).toBe(3);
  });

  it("uses green bar for score > 70", () => {
    const html = getDimensionSparklinesSectionHtml([
      {
        dimension: "reliability",
        label: "Reliability",
        data: [80],
        trend: "stable",
      },
    ]);
    expect(html).toContain("dim-bar-green");
  });

  it("uses yellow bar for score 50-70", () => {
    const html = getDimensionSparklinesSectionHtml([
      {
        dimension: "reliability",
        label: "Reliability",
        data: [60],
        trend: "stable",
      },
    ]);
    expect(html).toContain("dim-bar-yellow");
  });

  it("uses red bar for score < 50", () => {
    const html = getDimensionSparklinesSectionHtml([
      {
        dimension: "reliability",
        label: "Reliability",
        data: [30],
        trend: "declining",
      },
    ]);
    expect(html).toContain("dim-bar-red");
  });

  it("displays the last score as a number", () => {
    const html = getDimensionSparklinesSectionHtml([
      {
        dimension: "reliability",
        label: "Reliability",
        data: [60, 70, 83],
        trend: "improving",
      },
    ]);
    expect(html).toContain("83");
  });

  it("escapes HTML in label", () => {
    const html = getDimensionSparklinesSectionHtml([
      {
        dimension: "test",
        label: "<script>xss</script>",
        data: [75],
        trend: "stable",
      },
    ]);
    expect(html).not.toContain("<script>");
    expect(html).toContain("&lt;script&gt;");
  });
});

// ── getHealthWidgetHtml ────────────────────────────────────────────────────

describe("getHealthWidgetHtml — dimension sparklines integration", () => {
  it("includes dimension sparklines section in full widget when data present", () => {
    const html = getHealthWidgetHtml(
      makeFullWidgetData({ dimensionSparklines: makeDimensionSparklines() })
    );
    expect(html).toContain("Dimension Trends");
    expect(html).toContain("Token Economics");
  });

  it("omits dimension sparklines section when dimensionSparklines is undefined", () => {
    const html = getHealthWidgetHtml(makeFullWidgetData());
    expect(html).not.toContain("Dimension Trends");
    expect(html).not.toContain("sparklines-grid");
  });

  it("omits dimension sparklines section when dimensionSparklines is empty", () => {
    const html = getHealthWidgetHtml(makeFullWidgetData({ dimensionSparklines: [] }));
    expect(html).not.toContain("Dimension Trends");
  });
});
