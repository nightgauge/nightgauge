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
import type {
  AnalyticsHealthData,
  PlatformFailure,
} from "../../../../src/views/dashboard/DashboardState";

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

  it("isLoading = true → renders the loading state, not the error/empty state (#752)", () => {
    const html = getHealthTabHtml({ result: null, hasAccess: true, isLoading: true }, null);
    expect(html).toContain("Loading health data");
    expect(html).not.toContain("Health data unavailable");
  });

  it("isLoading = true with a prior result still present → renders loading, not the stale result (#752)", () => {
    // A retry starts from a populated tab (isLoading flips to true while the
    // old result/failure is still on the object) — the loading state must
    // win, or a retry on a tab that already has data renders no visible
    // change at all.
    const html = getHealthTabHtml({ ...makeData(), isLoading: true }, null);
    expect(html).toContain("Loading health data");
    expect(html).not.toContain("Overall Score");
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

// ---------------------------------------------------------------------------
// Failure-kind enumeration (#748) — the regression detector for this class
// of bug: every kind must render distinct, honest copy sourced from the
// classified failure, never a fixed guess.
// ---------------------------------------------------------------------------

function makeFailure(overrides: Partial<PlatformFailure> = {}): PlatformFailure {
  return {
    ok: false,
    kind: "server_error",
    endpoint: "platform.getAnalyticsHealth",
    message: "get analytics health: server returned 500",
    ...overrides,
  };
}

describe("getHealthTabHtml — failure kinds (#748)", () => {
  it("unauthorized → sign-in copy, no role/plan claim", () => {
    const html = getHealthTabHtml(
      {
        result: null,
        hasAccess: false,
        isLoading: false,
        failure: makeFailure({ kind: "unauthorized", status: 401 }),
      },
      null
    );
    expect(html).toContain("Sign-in required");
    expect(html).toContain("healthSignInBtn");
    expect(html).not.toContain("healthRefreshBtn");
    expect(html.toLowerCase()).not.toContain("role");
    expect(html.toLowerCase()).not.toContain("upgrade");
  });

  it("forbidden → the only kind that may mention role/plan, quoting the real message", () => {
    const html = getHealthTabHtml(
      {
        result: null,
        hasAccess: false,
        isLoading: false,
        failure: makeFailure({
          kind: "forbidden",
          status: 403,
          message: "get analytics health: server returned 403",
        }),
      },
      null
    );
    expect(html).toContain("Access denied");
    expect(html).toContain("server returned 403");
    expect(html).toContain("healthRefreshBtn");
  });

  it("server_error → retry copy naming a transient issue, not permanent", () => {
    const html = getHealthTabHtml(
      {
        result: null,
        hasAccess: false,
        isLoading: false,
        failure: makeFailure({ kind: "server_error", status: 500 }),
      },
      null
    );
    expect(html).toContain("Platform error");
    expect(html).toContain("healthRefreshBtn");
    expect(html.toLowerCase()).not.toContain("role");
  });

  it("offline → unreachable copy, not the old 'likely temporary' server_error framing", () => {
    const html = getHealthTabHtml(
      {
        result: null,
        hasAccess: false,
        isLoading: false,
        failure: makeFailure({ kind: "offline" }),
      },
      null
    );
    expect(html).toContain("Platform unreachable");
    expect(html).not.toContain("likely a temporary issue");
  });

  it("not_configured → not-connected copy with sign-in", () => {
    const html = getHealthTabHtml(
      {
        result: null,
        hasAccess: false,
        isLoading: false,
        failure: makeFailure({ kind: "not_configured" }),
      },
      null
    );
    expect(html).toContain("Not connected");
    expect(html).toContain("healthSignInBtn");
  });

  it("unrecognized kind → neutral message naming endpoint and status, never a guess", () => {
    const html = getHealthTabHtml(
      {
        result: null,
        hasAccess: false,
        isLoading: false,
        failure: makeFailure({
          // @ts-expect-error — deliberately outside the known union to exercise the fallback
          kind: "something_new",
          status: 418,
          endpoint: "platform.getAnalyticsHealth",
          message: "unrecognized failure",
        }),
      },
      null
    );
    expect(html).toContain("Unable to load data");
    expect(html).toContain("platform.getAnalyticsHealth");
    expect(html).toContain("418");
    expect(html.toLowerCase()).not.toContain("role");
    expect(html.toLowerCase()).not.toContain("upgrade");
  });

  it("no failure and no result (never fetched) → generic connect copy, distinct from every failure kind", () => {
    const html = getHealthTabHtml({ result: null, hasAccess: false, isLoading: false }, null);
    expect(html).toContain("connect to Nightgauge platform");
    expect(html).not.toContain("Sign-in required");
    expect(html).not.toContain("Access denied");
    expect(html).not.toContain("Platform error");
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
