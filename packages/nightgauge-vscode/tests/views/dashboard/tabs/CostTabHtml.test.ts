/**
 * Tests for platform cost tab functions (Issue #3317, restructured #748)
 *
 * Covers:
 * 1. null data → empty state explaining why it is empty (never fetched)
 * 2. data.result present → renders total cost card
 * 3. data.result present → renders per-model breakdown
 * 4. data.result present → renders daily sparkline
 * 5. date range selector renders with active range
 * 6. XSS: model id strings are escaped
 * 7. getCostTabScript → returns script string with postMessage
 * 8. getPlatformCostTabStyles → returns non-empty CSS string
 * 9. empty byModel array → renders no-data message
 * 10. empty byDay array → renders no-data message
 * 11. (#748) data.failure set → renders the classified failure, distinct
 *     from both "never fetched" and a genuine empty/success result
 * 12. (#748) every PlatformFailureKind renders distinct copy
 * 13. (#785) isLoading renders an accessible loading state
 */

import { describe, it, expect } from "vitest";
import {
  getCostTabHtml,
  getCostTabScript,
  getPlatformCostTabStyles,
} from "../../../../src/views/dashboard/tabs/CostTabHtml";
import type { CostAnalyticsResult } from "../../../../src/services/IpcClientBase";
import type {
  PlatformCostTabData,
  PlatformFailure,
} from "../../../../src/views/dashboard/DashboardState";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeResult(overrides: Partial<CostAnalyticsResult> = {}): CostAnalyticsResult {
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

function makeData(overrides: Partial<CostAnalyticsResult> = {}): PlatformCostTabData {
  return { result: makeResult(overrides), isLoading: false };
}

function makeFailure(overrides: Partial<PlatformFailure> = {}): PlatformFailure {
  return {
    ok: false,
    kind: "server_error",
    endpoint: "platform.getCostAnalytics",
    message: "get cost analytics: server returned 500",
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("getCostTabHtml", () => {
  // The empty state must not read as an instruction to opt in: telemetry is
  // opt-out (#738), so "enable telemetry" would be wrong for most operators
  // seeing this panel. It explains the absence instead.
  it("null data (never fetched) → explains the absence without telling the operator to opt in", () => {
    const html = getCostTabHtml(null, "7d");
    expect(html).toContain("No server-aggregated cost data yet");
    expect(html).not.toContain("opt in");
    expect(html).not.toMatch(/Enable telemetry/i);
  });

  it("null data → renders date range selector", () => {
    const html = getCostTabHtml(null, "30d");
    expect(html).toContain('data-cost-range="7d"');
    expect(html).toContain('data-cost-range="30d"');
    expect(html).toContain('data-cost-range="90d"');
  });

  it("loading data → renders an announced loading state and preserves the date range", () => {
    const html = getCostTabHtml({ result: null, isLoading: true }, "30d");

    expect(html).toContain("Loading cost data…");
    expect(html).toContain('role="status"');
    expect(html).toContain('aria-live="polite"');
    expect(html).toContain('aria-atomic="true"');
    expect(html).toMatch(/class="toggle-btn active"[^>]*data-cost-range="30d"/);
    expect(html).not.toContain("No server-aggregated cost data yet");
  });

  it("data.result present → renders total cost card with formatted value", () => {
    const html = getCostTabHtml(makeData(), "7d");
    expect(html).toContain("$0.0123");
    expect(html).toContain("1,500");
  });

  it("data.result present → renders per-model breakdown", () => {
    const html = getCostTabHtml(makeData(), "7d");
    expect(html).toContain("claude-sonnet-4-6");
    expect(html).toContain("claude-haiku-4-5");
    expect(html).toContain("$0.0100");
  });

  it("data.result present → renders sparkline SVG for daily trend", () => {
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

// ---------------------------------------------------------------------------
// Failure-kind enumeration (#748) — previously ANY failure (401, 403, 500,
// offline) rendered as the identical "No server-aggregated cost data yet"
// copy as a genuinely empty account, indistinguishable from success. This is
// the regression detector for that whole class of bug.
// ---------------------------------------------------------------------------

describe("getCostTabHtml — failure kinds (#748)", () => {
  it("failure set → distinct from the never-fetched empty state", () => {
    const html = getCostTabHtml(
      { result: null, isLoading: false, failure: makeFailure({ kind: "offline" }) },
      "7d"
    );
    expect(html).not.toContain("No server-aggregated cost data yet");
    expect(html).toContain("Platform unreachable");
  });

  it("unauthorized → sign-in copy, no role/plan claim", () => {
    const html = getCostTabHtml(
      {
        result: null,
        isLoading: false,
        failure: makeFailure({ kind: "unauthorized", status: 401 }),
      },
      "7d"
    );
    expect(html).toContain("Sign-in required");
    expect(html).toContain("costSignInBtn");
    expect(html.toLowerCase()).not.toContain("role or plan");
    expect(html.toLowerCase()).not.toContain("upgrade");
  });

  it("forbidden → the only kind that may mention role/plan, quoting the real message", () => {
    const html = getCostTabHtml(
      {
        result: null,
        isLoading: false,
        failure: makeFailure({
          kind: "forbidden",
          status: 403,
          message: "get cost analytics: server returned 403",
        }),
      },
      "7d"
    );
    expect(html).toContain("Access denied");
    expect(html).toContain("server returned 403");
    expect(html).toContain("costRetryBtn");
  });

  it("server_error → retry copy naming the endpoint", () => {
    const html = getCostTabHtml(
      {
        result: null,
        isLoading: false,
        failure: makeFailure({ kind: "server_error", status: 500 }),
      },
      "7d"
    );
    expect(html).toContain("Platform error");
    expect(html).toContain("platform.getCostAnalytics");
  });

  it("not_configured → not-connected copy with sign-in", () => {
    const html = getCostTabHtml(
      { result: null, isLoading: false, failure: makeFailure({ kind: "not_configured" }) },
      "7d"
    );
    expect(html).toContain("Not connected");
    expect(html).toContain("costSignInBtn");
  });

  it("unrecognized kind → neutral message naming endpoint and status, never a guess", () => {
    const html = getCostTabHtml(
      {
        result: null,
        isLoading: false,
        failure: makeFailure({
          // @ts-expect-error — deliberately outside the known union to exercise the fallback
          kind: "something_new",
          status: 418,
          endpoint: "platform.getCostAnalytics",
          message: "unrecognized failure",
        }),
      },
      "7d"
    );
    expect(html).toContain("Unable to load data");
    expect(html).toContain("platform.getCostAnalytics");
    expect(html).toContain("418");
  });

  it("a genuine all-zero success result is not mistaken for a failure or the never-fetched state", () => {
    const zeroResult = makeResult({
      totalCostUsd: "0.0000",
      totalTokens: 0,
      breakdown: { byModel: [], byProject: [], byDay: [] },
    });
    const html = getCostTabHtml({ result: zeroResult, isLoading: false }, "7d");
    expect(html).toContain("$0.0000");
    expect(html).not.toContain("No server-aggregated cost data yet");
    expect(html).not.toContain("Platform error");
    expect(html).not.toContain("Sign-in required");
  });
});
