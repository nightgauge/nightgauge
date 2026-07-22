/**
 * Tests for platform health tab functions (Issue #3318)
 *
 * Covers:
 * 1. null data → empty state with "Health data unavailable"
 * 2. data present → renders overall score
 * 3. data present → renders dimension cards for all dimensions
 * 4. data present with finding → renders finding title with severity badge
 * 5. finding with issue_number → renders "View #NNN" link
 * 6. getHealthTabScript → returns string with healthRefresh message type
 * 7. getHealthTabStyles → returns non-empty CSS string
 * 8. fetchedAt present → renders freshness timestamp
 * 9. XSS: finding titles are escaped
 * 10. Snapshot test for overall HTML structure
 */

import { describe, it, expect } from "vitest";
import {
  getHealthTabHtml,
  getHealthTabScript,
  getHealthTabStyles,
} from "../../../../src/views/dashboard/tabs/HealthTabHtml";
import type { AnalyticsHealthResult } from "../../../../src/services/IpcClientBase";
import type { AnalyticsHealthData } from "../../../../src/views/dashboard/DashboardState";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeDimension(
  name: string,
  score: number,
  findings: AnalyticsHealthResult["dimensions"][number]["findings"] = []
): AnalyticsHealthResult["dimensions"][number] {
  return { name, score, label: name.replace(/-/g, " "), findings };
}

function makeResult(overrides: Partial<AnalyticsHealthResult> = {}): AnalyticsHealthResult {
  return {
    overall_score: 72,
    dimensions: [
      makeDimension("token-economics", 80),
      makeDimension("cost-health", 65),
      makeDimension("stage-effectiveness", 90),
      makeDimension("model-routing", 55),
      makeDimension("reliability", 70),
      makeDimension("learning-effectiveness", 60),
      makeDimension("pipeline-velocity", 75),
    ],
    generated_at: "2026-05-13T12:00:00Z",
    period_days: 30,
    total_runs: 42,
    ...overrides,
  };
}

function makeData(overrides: Partial<AnalyticsHealthResult> = {}): AnalyticsHealthData {
  return { result: makeResult(overrides), hasAccess: true, isLoading: false };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("getHealthTabHtml", () => {
  it("null data → contains empty state message", () => {
    const html = getHealthTabHtml(null, null);
    expect(html).toContain("Health data unavailable");
    expect(html).toContain("healthRefreshBtn");
  });

  it("null data → contains Refresh button", () => {
    const html = getHealthTabHtml(null, null);
    expect(html).toContain("Refresh");
  });

  it("data present → renders overall score", () => {
    const html = getHealthTabHtml(makeData(), null);
    expect(html).toContain("72");
    expect(html).toContain("Overall Score");
  });

  it("data present → renders dimension cards for all 7 dimensions", () => {
    const data = makeData();
    const html = getHealthTabHtml(data, null);
    for (const dim of data.result!.dimensions) {
      expect(html).toContain(dim.name.replace(/-/g, " "));
    }
  });

  it("data present with finding → renders finding title with severity badge", () => {
    const data = makeData({
      dimensions: [
        makeDimension("reliability", 40, [
          {
            severity: "critical",
            title: "High failure rate",
            description: "Stage failures exceed threshold",
            recommendation: "Review stage configurations",
          },
        ]),
      ],
    });
    const html = getHealthTabHtml(data, null);
    expect(html).toContain("High failure rate");
    expect(html).toContain("Critical");
  });

  it("finding with issue_number → renders View #NNN link", () => {
    const data = makeData({
      dimensions: [
        makeDimension("cost-health", 50, [
          {
            severity: "high",
            title: "Cost spike",
            description: "Cost exceeded budget",
            recommendation: "Optimize token usage",
            issue_number: 1234,
          },
        ]),
      ],
    });
    const html = getHealthTabHtml(data, null);
    expect(html).toContain("View #1234");
    expect(html).toContain("/issues/1234");
  });

  it("fetchedAt present → renders freshness timestamp", () => {
    const fetchedAt = new Date(Date.now() - 5 * 60 * 1000); // 5 minutes ago
    const html = getHealthTabHtml(makeData(), fetchedAt);
    expect(html).toMatch(/Updated \d+ minutes? ago/);
  });

  it("fetchedAt just now → renders 'Updated just now'", () => {
    const fetchedAt = new Date(); // now
    const html = getHealthTabHtml(makeData(), fetchedAt);
    expect(html).toContain("Updated just now");
  });

  it("XSS: finding title with HTML special chars is escaped", () => {
    const data = makeData({
      dimensions: [
        makeDimension("reliability", 40, [
          {
            severity: "warning",
            title: "<script>alert('xss')</script>",
            description: "Bad input",
            recommendation: "Fix it",
          },
        ]),
      ],
    });
    const html = getHealthTabHtml(data, null);
    expect(html).not.toContain("<script>alert");
    expect(html).toContain("&lt;script&gt;");
  });

  it("no findings → renders no-findings message", () => {
    const data = makeData({
      dimensions: [makeDimension("reliability", 95)],
    });
    const html = getHealthTabHtml(data, null);
    expect(html).toContain("No findings");
  });

  it("overall score ≥80 → renders health-score-good class", () => {
    const html = getHealthTabHtml(makeData({ overall_score: 85 }), null);
    expect(html).toContain("health-score-good");
  });

  it("overall score 50-79 → renders health-score-fair class", () => {
    const html = getHealthTabHtml(makeData({ overall_score: 65 }), null);
    expect(html).toContain("health-score-fair");
  });

  it("overall score <50 → renders health-score-poor class", () => {
    const html = getHealthTabHtml(makeData({ overall_score: 30 }), null);
    expect(html).toContain("health-score-poor");
  });

  it("snapshot: overall HTML structure for null data", () => {
    const html = getHealthTabHtml(null, null);
    expect(html).toMatchSnapshot();
  });
});

describe("getHealthTabScript", () => {
  it("returns string containing healthRefresh message type", () => {
    const script = getHealthTabScript();
    expect(typeof script).toBe("string");
    expect(script).toContain("healthRefresh");
    expect(script).toContain("postMessage");
  });

  it("targets tab-panel-health element", () => {
    const script = getHealthTabScript();
    expect(script).toContain("tab-panel-health");
  });
});

describe("getHealthTabStyles", () => {
  it("returns non-empty CSS string", () => {
    const styles = getHealthTabStyles();
    expect(typeof styles).toBe("string");
    expect(styles.length).toBeGreaterThan(0);
    expect(styles).toContain(".health-tab");
  });

  it("contains dimension grid CSS", () => {
    const styles = getHealthTabStyles();
    expect(styles).toContain(".health-dim-grid");
  });

  it("contains severity badge CSS", () => {
    const styles = getHealthTabStyles();
    expect(styles).toContain(".health-badge-critical");
    expect(styles).toContain(".health-badge-high");
    expect(styles).toContain(".health-badge-warning");
  });
});
