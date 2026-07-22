/**
 * Tests for platform cost tab functions (Issue #3317)
 *
 * Covers:
 * 1. null data → empty state with "Telemetry pending" message
 * 2. data present → renders total cost card
 * 3. data present → renders per-model breakdown
 * 4. data present → renders daily sparkline
 * 5. date range selector renders with active range
 * 6. XSS: model id strings are escaped
 * 7. getCostTabScript → returns script string with postMessage
 * 8. getPlatformCostTabStyles → returns non-empty CSS string
 * 9. empty byModel array → renders no-data message
 * 10. empty byDay array → renders no-data message
 */

import { describe, it, expect } from "vitest";
import {
  getCostTabHtml,
  getCostTabScript,
  getPlatformCostTabStyles,
} from "../../../../src/views/dashboard/tabs/CostTabHtml";
import type { CostAnalyticsResult } from "../../../../src/services/IpcClientBase";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeData(overrides: Partial<CostAnalyticsResult> = {}): CostAnalyticsResult {
  return {
    totalInputTokens: 1000,
    totalOutputTokens: 500,
    totalTokens: 1500,
    totalCostUsd: "0.0123",
    breakdown: {
      byModel: [
        { modelId: "claude-sonnet-4-6", costUsd: "0.0100", tokens: 1200 },
        { modelId: "claude-haiku-4-5", costUsd: "0.0023", tokens: 300 },
      ],
      byProject: [],
      byDay: [
        { date: "2026-05-06", costUsd: "0.0040" },
        { date: "2026-05-07", costUsd: "0.0083" },
      ],
    },
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("getCostTabHtml", () => {
  it("null data → contains empty state message", () => {
    const html = getCostTabHtml(null, "7d");
    expect(html).toContain("Telemetry pending");
    expect(html).toContain("opt in under settings");
  });

  it("null data → renders date range selector", () => {
    const html = getCostTabHtml(null, "30d");
    expect(html).toContain('data-cost-range="7d"');
    expect(html).toContain('data-cost-range="30d"');
    expect(html).toContain('data-cost-range="90d"');
  });

  it("data present → renders total cost card with formatted value", () => {
    const html = getCostTabHtml(makeData(), "7d");
    expect(html).toContain("$0.0123");
    expect(html).toContain("1,500");
  });

  it("data present → renders per-model breakdown", () => {
    const html = getCostTabHtml(makeData(), "7d");
    expect(html).toContain("claude-sonnet-4-6");
    expect(html).toContain("claude-haiku-4-5");
    expect(html).toContain("$0.0100");
  });

  it("data present → renders sparkline SVG for daily trend", () => {
    const html = getCostTabHtml(makeData(), "7d");
    expect(html).toContain("<polyline");
    expect(html).toContain("2026-05-06");
    expect(html).toContain("2026-05-07");
  });

  it("active date range has active class", () => {
    const html7 = getCostTabHtml(makeData(), "7d");
    expect(html7).toMatch(/class="toggle-btn active"[^>]*data-cost-range="7d"/);

    const html30 = getCostTabHtml(makeData(), "30d");
    expect(html30).toMatch(/class="toggle-btn active"[^>]*data-cost-range="30d"/);
  });

  it("XSS: model id is escaped in output", () => {
    const data = makeData({
      breakdown: {
        byModel: [{ modelId: "<script>alert(1)</script>", costUsd: "0.001", tokens: 10 }],
        byProject: [],
        byDay: [],
      },
    });
    const html = getCostTabHtml(data, "7d");
    expect(html).not.toContain("<script>alert(1)</script>");
    expect(html).toContain("&lt;script&gt;");
  });

  it("empty byModel → renders no-data placeholder", () => {
    const data = makeData({ breakdown: { byModel: [], byProject: [], byDay: [] } });
    const html = getCostTabHtml(data, "7d");
    expect(html).toContain("No model data available");
  });

  it("single byDay entry → renders no-trend placeholder", () => {
    const data = makeData({
      breakdown: {
        byModel: [],
        byProject: [],
        byDay: [{ date: "2026-05-07", costUsd: "0.001" }],
      },
    });
    const html = getCostTabHtml(data, "7d");
    expect(html).toContain("Not enough daily data");
  });
});

describe("getCostTabScript", () => {
  it("returns a script string with postMessage call", () => {
    const script = getCostTabScript();
    expect(script).toContain("costDateRangeChange");
    expect(script).toContain("vscode.postMessage");
    expect(script).toContain("data-cost-range");
  });
});

describe("getPlatformCostTabStyles", () => {
  it("returns non-empty CSS string", () => {
    const css = getPlatformCostTabStyles();
    expect(css.trim().length).toBeGreaterThan(0);
    expect(css).toContain(".platform-cost-tab");
    expect(css).toContain(".platform-cost-total-card");
  });
});
