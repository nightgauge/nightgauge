/**
 * Tests for TrendsTabHtml (Issue #3320)
 *
 * Covers:
 * 1. undefined data → loading state
 * 2. null result in data → empty state
 * 3. data with < 7 current entries → sparse state message
 * 4. data with 10+ entries → renders charts (polyline, rect elements)
 * 5. data with showComparison=true → dashed comparison polyline present
 * 6. XSS: date strings with <script> are escaped
 * 7. getTrendsTabScript → returns non-empty script with expected message types
 * 8. getTrendsTabStyles → returns non-empty CSS string
 * 9. no access → no-access state
 * 10. isLoading=true → loading state
 */

import { describe, it, expect } from "vitest";
import {
  getTrendsTabHtml,
  getTrendsTabScript,
  getTrendsTabStyles,
} from "../../../../src/views/dashboard/tabs/TrendsTabHtml";
import type { TrendsData } from "../../../../src/views/dashboard/DashboardState";
import type { AnalyticsTrendsResult, TrendEntry } from "../../../../src/services/IpcClientBase";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeEntry(date: string, i: number): TrendEntry {
  return {
    date,
    successRate: 0.8 + i * 0.01,
    costPerRun: 0.05 + i * 0.001,
    totalRuns: 5 + i,
  };
}

function makeTrendsResult(count: number, withPrevious = false): AnalyticsTrendsResult {
  const current = Array.from({ length: count }, (_, i) =>
    makeEntry(`2026-04-${String(i + 1).padStart(2, "0")}`, i)
  );
  const previous = withPrevious
    ? Array.from({ length: count }, (_, i) =>
        makeEntry(`2026-03-${String(i + 1).padStart(2, "0")}`, i)
      )
    : [];
  return { current, previous, period: "30d" };
}

function makeTrendsData(overrides: Partial<TrendsData> = {}): TrendsData {
  return {
    result: makeTrendsResult(10),
    isLoading: false,
    hasAccess: true,
    showComparison: false,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("getTrendsTabHtml", () => {
  it("undefined → loading state", () => {
    const html = getTrendsTabHtml(undefined);
    expect(html).toContain("Loading trends");
  });

  it("null → loading state", () => {
    const html = getTrendsTabHtml(null);
    expect(html).toContain("Loading trends");
  });

  it("isLoading=true → loading state", () => {
    const html = getTrendsTabHtml(makeTrendsData({ isLoading: true }));
    expect(html).toContain("Loading trends");
  });

  it("no access → no-access message", () => {
    const html = getTrendsTabHtml(makeTrendsData({ hasAccess: false }));
    expect(html).toContain("Sign in");
  });

  it("null result → empty state", () => {
    const html = getTrendsTabHtml(makeTrendsData({ result: null }));
    expect(html).toContain("No trends data");
  });

  it("< 7 entries → sparse state with count", () => {
    const html = getTrendsTabHtml(makeTrendsData({ result: makeTrendsResult(4) }));
    expect(html).toContain("Not enough data");
    expect(html).toContain("4 so far");
  });

  it("10+ entries → renders date range selector", () => {
    const html = getTrendsTabHtml(makeTrendsData());
    expect(html).toContain('data-trends-range="30d"');
    expect(html).toContain('data-trends-range="90d"');
    expect(html).toContain('data-trends-range="180d"');
  });

  it("10+ entries → renders SVG polylines for success rate and cost", () => {
    const html = getTrendsTabHtml(makeTrendsData());
    expect(html).toContain("<polyline");
  });

  it("10+ entries → renders bar chart for total runs", () => {
    const html = getTrendsTabHtml(makeTrendsData());
    expect(html).toContain("trends-bar-current");
  });

  it("showComparison=true → dashed comparison polyline present", () => {
    const html = getTrendsTabHtml(
      makeTrendsData({ result: makeTrendsResult(10, true), showComparison: true })
    );
    expect(html).toContain("stroke-dasharray");
  });

  it("showComparison=false → no dashed line", () => {
    const html = getTrendsTabHtml(
      makeTrendsData({ result: makeTrendsResult(10, true), showComparison: false })
    );
    expect(html).not.toContain("stroke-dasharray");
  });

  it("XSS: date strings with <script> are escaped", () => {
    const xssEntry: TrendEntry = {
      date: "<script>alert(1)</script>",
      successRate: 0.9,
      costPerRun: 0.05,
      totalRuns: 5,
    };
    const result: AnalyticsTrendsResult = {
      current: Array.from({ length: 10 }, (_, i) =>
        i === 0 ? xssEntry : makeEntry(`2026-04-${String(i + 1).padStart(2, "0")}`, i)
      ),
      previous: [],
      period: "30d",
    };
    const html = getTrendsTabHtml(makeTrendsData({ result }));
    expect(html).not.toContain("<script>alert(1)</script>");
    expect(html).toContain("&lt;script&gt;");
  });

  it("error message is displayed when present", () => {
    const html = getTrendsTabHtml(makeTrendsData({ errorMessage: "Platform unavailable" }));
    expect(html).toContain("Platform unavailable");
  });
});

describe("getTrendsTabScript", () => {
  it("returns non-empty string", () => {
    expect(getTrendsTabScript().length).toBeGreaterThan(0);
  });

  it("contains trendsDateRangeChange message type", () => {
    expect(getTrendsTabScript()).toContain("trendsDateRangeChange");
  });

  it("contains trendsToggleComparison message type", () => {
    expect(getTrendsTabScript()).toContain("trendsToggleComparison");
  });

  it("contains trendsRefresh message type", () => {
    expect(getTrendsTabScript()).toContain("trendsRefresh");
  });
});

describe("getTrendsTabStyles", () => {
  it("returns non-empty CSS string", () => {
    expect(getTrendsTabStyles().length).toBeGreaterThan(0);
  });

  it("contains .trends-tab selector", () => {
    expect(getTrendsTabStyles()).toContain(".trends-tab");
  });

  it("contains .trends-charts-grid selector", () => {
    expect(getTrendsTabStyles()).toContain(".trends-charts-grid");
  });
});
