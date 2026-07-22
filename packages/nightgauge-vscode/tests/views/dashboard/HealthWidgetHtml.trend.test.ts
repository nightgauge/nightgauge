/**
 * HealthWidgetHtml.trend.test.ts
 *
 * Unit tests for the health trend chart rendering with configurable ranges.
 *
 * @see Issue #789 - Persist Health Scores to Disk with 30-Day Trend
 */

import { describe, it, expect } from "vitest";
import {
  getTrendChartSectionHtml,
  getHealthWidgetHtml,
} from "../../../src/views/dashboard/HealthWidgetHtml";
import type {
  HealthWidgetData,
  TrendChartDay,
  TrendAnalysis,
} from "../../../src/views/dashboard/HealthWidgetTypes";

/** Local date string matching the source function's date calculation */
function localToday(): string {
  const now = new Date();
  const year = now.getFullYear();
  const month = String(now.getMonth() + 1).padStart(2, "0");
  const day = String(now.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

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

describe("getTrendChartSectionHtml", () => {
  it("returns empty string when trendChart is undefined", () => {
    const result = getTrendChartSectionHtml(undefined, null);
    expect(result).toBe("");
  });

  it("returns empty string when trendChart is empty", () => {
    const result = getTrendChartSectionHtml([], null);
    expect(result).toBe("");
  });

  it("renders 7 bars for default 7d range", () => {
    const today = localToday();
    const days: TrendChartDay[] = [{ date: today, avgScore: 75, count: 1 }];

    const html = getTrendChartSectionHtml(days, null, "7d");
    const barCount = (html.match(/class="trend-bar /g) || []).length;
    expect(barCount).toBe(7);
  });

  it("renders 30 bars for 30d range", () => {
    const days: TrendChartDay[] = [
      { date: "2026-02-15", avgScore: 75, count: 2 },
      { date: "2026-02-16", avgScore: 80, count: 1 },
    ];

    const html = getTrendChartSectionHtml(days, null, "30d");
    const barCount = (html.match(/class="trend-bar /g) || []).length;
    expect(barCount).toBe(30);
  });

  it("renders 24 bars for 24h range", () => {
    const now = new Date();
    const hourKey = now.toISOString().slice(0, 13);
    const days: TrendChartDay[] = [{ date: hourKey, avgScore: 80, count: 1 }];

    const html = getTrendChartSectionHtml(days, null, "24h");
    const barCount = (html.match(/class="trend-bar /g) || []).length;
    expect(barCount).toBe(24);
  });

  it("renders 90 bars for 90d range", () => {
    const today = localToday();
    const days: TrendChartDay[] = [{ date: today, avgScore: 75, count: 1 }];

    const html = getTrendChartSectionHtml(days, null, "90d");
    const barCount = (html.match(/class="trend-bar /g) || []).length;
    expect(barCount).toBe(90);
  });

  it("applies green color class for scores > 70", () => {
    const today = localToday();
    const days: TrendChartDay[] = [{ date: today, avgScore: 85, count: 1 }];

    const html = getTrendChartSectionHtml(days, null, "7d");
    expect(html).toContain("trend-bar-green");
  });

  it("applies yellow color class for scores 50-70", () => {
    const today = localToday();
    const days: TrendChartDay[] = [{ date: today, avgScore: 60, count: 1 }];

    const html = getTrendChartSectionHtml(days, null, "7d");
    expect(html).toContain("trend-bar-yellow");
  });

  it("applies red color class for scores < 50", () => {
    const today = localToday();
    const days: TrendChartDay[] = [{ date: today, avgScore: 35, count: 1 }];

    const html = getTrendChartSectionHtml(days, null, "7d");
    expect(html).toContain("trend-bar-red");
  });

  it("uses empty bar class for days with no data", () => {
    const today = localToday();
    const days: TrendChartDay[] = [{ date: today, avgScore: 75, count: 1 }];

    const html = getTrendChartSectionHtml(days, null, "7d");
    const emptyCount = (html.match(/trend-bar-empty/g) || []).length;
    expect(emptyCount).toBe(6);
  });

  it("renders section heading and range selector", () => {
    const today = localToday();
    const days: TrendChartDay[] = [{ date: today, avgScore: 75, count: 1 }];

    const html = getTrendChartSectionHtml(days, null, "7d");
    expect(html).toContain("Health Trend");
    expect(html).toContain("trend-range-select");
    expect(html).toContain("Last 7 Days");
  });

  it("marks the selected range in the dropdown", () => {
    const today = localToday();
    const days: TrendChartDay[] = [{ date: today, avgScore: 75, count: 1 }];

    const html = getTrendChartSectionHtml(days, null, "30d");
    expect(html).toContain('value="30d" selected');
    expect(html).not.toContain('value="7d" selected');
  });

  it("escapes HTML in date title attributes", () => {
    const today = localToday();
    const days: TrendChartDay[] = [{ date: today, avgScore: 75, count: 1 }];

    const html = getTrendChartSectionHtml(days, null, "7d");
    expect(html).toContain(`title="${today}: 75 (1 run)"`);
  });

  it("renders trend analysis when provided", () => {
    const today = localToday();
    const days: TrendChartDay[] = [{ date: today, avgScore: 75, count: 1 }];
    const analysis: TrendAnalysis = {
      direction: "improving",
      message: "Health improved 12% over last 3 days",
      periodDays: 3,
      percentChange: 12,
    };

    const html = getTrendChartSectionHtml(days, analysis, "7d");
    expect(html).toContain("trend-improving");
    expect(html).toContain("Health improved 12% over last 3 days");
  });

  it("renders declining trend arrow", () => {
    const today = localToday();
    const days: TrendChartDay[] = [{ date: today, avgScore: 40, count: 1 }];
    const analysis: TrendAnalysis = {
      direction: "declining",
      message: "Health declined 15% over last 3 days",
      periodDays: 3,
      percentChange: -15,
    };

    const html = getTrendChartSectionHtml(days, analysis, "7d");
    expect(html).toContain("trend-declining");
    expect(html).toContain("↓");
  });

  it("renders stable trend arrow", () => {
    const today = localToday();
    const days: TrendChartDay[] = [{ date: today, avgScore: 75, count: 1 }];
    const analysis: TrendAnalysis = {
      direction: "stable",
      message: "Health stable over last 3 days",
      periodDays: 3,
      percentChange: 0,
    };

    const html = getTrendChartSectionHtml(days, analysis, "7d");
    expect(html).toContain("trend-stable-dir");
    expect(html).toContain("→");
  });
});

describe("getHealthWidgetHtml with trend data", () => {
  it("includes trend chart section in full widget when data present", () => {
    const today = localToday();
    const data = createMockHealthData({
      trendChart: [{ date: today, avgScore: 80, count: 1 }],
      trendAnalysis: {
        direction: "stable",
        message: "Tracking started",
        periodDays: 3,
        percentChange: 0,
      },
      trendRange: "7d",
    });

    const html = getHealthWidgetHtml(data);
    expect(html).toContain("health-trend-section");
    expect(html).toContain("Health Trend");
    expect(html).toContain("trend-range-select");
  });

  it("does not include trend section when no trend data", () => {
    const data = createMockHealthData();
    const html = getHealthWidgetHtml(data);
    expect(html).not.toContain("health-trend-section");
  });
});
