/**
 * Tests for EpicsTabHtml module (Issue #2549)
 *
 * Covers:
 * 1. getEpicEstimatesHtml() with empty aggregates → empty state
 * 2. getEpicEstimatesHtml() with estimated epic data → renders epic card
 * 3. getEpicEstimatesHtml() with failed estimation → renders warning
 * 4. getCrossRepoEpicProgressHtml() with empty data → returns empty string
 * 5. getCrossRepoEpicProgressHtml() with single-repo epic → returns empty string (not cross-repo)
 * 6. getCrossRepoEpicProgressHtml() with cross-repo epic → renders progress section
 * 7. "View" links contain non-empty href (not blank/malformed)
 * 8. XSS: user-derived strings are escaped
 */

import { describe, it, expect } from "vitest";
import {
  getEpicEstimatesHtml,
  getCrossRepoEpicProgressHtml,
} from "../../../../src/views/dashboard/tabs/EpicsTabHtml";
import type { DashboardAggregates } from "../../../../src/views/dashboard/DashboardState";
import type { CrossRepoEpicProgress } from "../../../../src/views/dashboard/EpicDashboard";
import type { EpicEstimate } from "@nightgauge/sdk";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

import { makeEmptyAggregates } from "../fixtures/aggregates";

function makeAggregates(
  overrides: Partial<Pick<DashboardAggregates, "epicEstimates" | "crossRepoEpicProgress">> = {}
): DashboardAggregates {
  return makeEmptyAggregates(overrides);
}

function makeEpicEstimate(overrides: Partial<EpicEstimate> = {}): EpicEstimate {
  return {
    epic_number: 101,
    epic_title: "Auth Revamp",
    sub_issues: [
      {
        number: 102,
        title: "Add OAuth support",
        size: "M",
        estimated_minutes: 600,
        status: "open",
      },
      {
        number: 103,
        title: "Remove legacy auth",
        size: "S",
        estimated_minutes: 120,
        status: "closed",
      },
    ],
    total_estimated_minutes: 720,
    total_remaining_minutes: 600,
    integration_buffer_minutes: 108,
    confidence: "high",
    confidence_detail: "Based on 10 historical runs",
    ...overrides,
  };
}

function makeCrossRepoEpic(overrides: Partial<CrossRepoEpicProgress> = {}): CrossRepoEpicProgress {
  return {
    epicNumber: 200,
    epicTitle: "Cross-Repo Feature",
    repositories: [
      {
        name: "nightgauge/nightgauge",
        path: "/repo/a",
        subIssues: [
          { number: 201, title: "Part A", size: "M", estimated_minutes: 600, status: "open" },
        ],
        totalMinutes: 600,
        remainingMinutes: 600,
        completionPercent: 0,
        closedCount: 0,
        openCount: 1,
        status: "success",
      },
      {
        name: "acme/platform",
        path: "/repo/b",
        subIssues: [
          { number: 202, title: "Part B", size: "S", estimated_minutes: 120, status: "closed" },
        ],
        totalMinutes: 120,
        remainingMinutes: 0,
        completionPercent: 100,
        closedCount: 1,
        openCount: 0,
        status: "success",
      },
    ],
    overallCompletionPercent: 17,
    totalMinutes: 720,
    remainingMinutes: 600,
    integrationBufferMinutes: 108,
    confidence: "medium",
    confidenceDetail: "Based on 5 historical runs",
    isCrossRepo: true,
    fetchedAt: new Date("2026-04-08T12:00:00Z"),
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// getEpicEstimatesHtml
// ---------------------------------------------------------------------------

describe("getEpicEstimatesHtml", () => {
  it("empty epicEstimates → renders empty-state message", () => {
    const html = getEpicEstimatesHtml(makeAggregates({ epicEstimates: [] }));
    expect(html).toContain("empty-state");
    expect(html).toContain("No open epics found");
    expect(html).toContain("type:epic");
  });

  it("estimated epic → renders epic card with title and metrics", () => {
    const estimate = makeEpicEstimate();
    const html = getEpicEstimatesHtml(
      makeAggregates({
        epicEstimates: [{ epic_number: 101, epic_title: "Auth Revamp", estimate, warning: null }],
      })
    );
    expect(html).toContain("epic-card");
    expect(html).toContain("Auth Revamp");
    expect(html).toContain("Epic #101");
    expect(html).toContain("1/2 issues"); // 1 closed, 2 total
  });

  it("estimated epic with high confidence → renders high confidence badge", () => {
    const estimate = makeEpicEstimate({ confidence: "high" });
    const html = getEpicEstimatesHtml(
      makeAggregates({
        epicEstimates: [{ epic_number: 101, epic_title: "Auth Revamp", estimate, warning: null }],
      })
    );
    expect(html).toContain("confidence-high");
    expect(html).toContain("High");
  });

  it("estimated epic with low confidence → renders low confidence badge", () => {
    const estimate = makeEpicEstimate({
      confidence: "low",
      confidence_detail: "Insufficient data",
    });
    const html = getEpicEstimatesHtml(
      makeAggregates({
        epicEstimates: [{ epic_number: 101, epic_title: "Auth Revamp", estimate, warning: null }],
      })
    );
    expect(html).toContain("confidence-low");
  });

  it("failed estimation → renders failed epic card with warning", () => {
    const html = getEpicEstimatesHtml(
      makeAggregates({
        epicEstimates: [
          {
            epic_number: 105,
            epic_title: "Failing Epic",
            estimate: null,
            warning: "No sub-issues with size labels",
          },
        ],
      })
    );
    expect(html).toContain("epic-card-failed");
    expect(html).toContain("Failing Epic");
    expect(html).toContain("No sub-issues with size labels");
  });

  it("mix of estimated and failed → renders both sections", () => {
    const estimate = makeEpicEstimate({ epic_number: 101, epic_title: "Good Epic" });
    const html = getEpicEstimatesHtml(
      makeAggregates({
        epicEstimates: [
          { epic_number: 101, epic_title: "Good Epic", estimate, warning: null },
          { epic_number: 105, epic_title: "Bad Epic", estimate: null, warning: "No sub-issues" },
        ],
      })
    );
    expect(html).toContain("epic-card");
    expect(html).toContain("epic-failed-section");
    expect(html).toContain("1 epic found but cannot be estimated");
  });

  it("renders progress bar reflecting completion percentage", () => {
    const estimate = makeEpicEstimate({
      total_estimated_minutes: 720,
      total_remaining_minutes: 360, // 50% remaining → 50% complete
    });
    const html = getEpicEstimatesHtml(
      makeAggregates({
        epicEstimates: [{ epic_number: 101, epic_title: "Auth Revamp", estimate, warning: null }],
      })
    );
    expect(html).toContain("progress-bar");
    expect(html).toContain("width: 50%;");
  });

  it('"View" links have non-empty href attribute', () => {
    const estimate = makeEpicEstimate({ epic_number: 101, epic_title: "Auth Revamp" });
    const html = getEpicEstimatesHtml(
      makeAggregates({
        epicEstimates: [{ epic_number: 101, epic_title: "Auth Revamp", estimate, warning: null }],
      })
    );
    // Verify link is rendered and not blank
    const hrefMatch = html.match(/href="([^"]+)"/);
    expect(hrefMatch).not.toBeNull();
    expect(hrefMatch![1]).not.toBe("");
    expect(hrefMatch![1]).not.toBe("undefined");
  });

  it("XSS: epic title is HTML-escaped", () => {
    const xssTitle = '<script>alert("xss")</script>';
    const estimate = makeEpicEstimate({ epic_number: 101, epic_title: xssTitle });
    const html = getEpicEstimatesHtml(
      makeAggregates({
        epicEstimates: [{ epic_number: 101, epic_title: xssTitle, estimate, warning: null }],
      })
    );
    expect(html).not.toContain("<script>alert");
    expect(html).toContain("&lt;script&gt;");
  });

  it("XSS: sub-issue title is HTML-escaped", () => {
    const xssTitle = '"><img src=x onerror=alert(1)>';
    const estimate = makeEpicEstimate({
      sub_issues: [
        { number: 102, title: xssTitle, size: "M", estimated_minutes: 600, status: "open" },
      ],
    });
    const html = getEpicEstimatesHtml(
      makeAggregates({
        epicEstimates: [{ epic_number: 101, epic_title: "Safe Title", estimate, warning: null }],
      })
    );
    expect(html).not.toContain("<img src=x");
    expect(html).toContain("&gt;");
  });
});

// ---------------------------------------------------------------------------
// getCrossRepoEpicProgressHtml
// ---------------------------------------------------------------------------

describe("getCrossRepoEpicProgressHtml", () => {
  it("empty crossRepoEpicProgress → returns empty string", () => {
    const html = getCrossRepoEpicProgressHtml(makeAggregates({ crossRepoEpicProgress: [] }));
    expect(html).toBe("");
  });

  it("single-repo epic (isCrossRepo: false) → returns empty string", () => {
    const singleRepo = makeCrossRepoEpic({ isCrossRepo: false });
    const html = getCrossRepoEpicProgressHtml(
      makeAggregates({ crossRepoEpicProgress: [singleRepo] })
    );
    expect(html).toBe("");
  });

  it("cross-repo epic → renders progress section with epic title", () => {
    const crossRepoEpic = makeCrossRepoEpic();
    const html = getCrossRepoEpicProgressHtml(
      makeAggregates({ crossRepoEpicProgress: [crossRepoEpic] })
    );
    expect(html).toContain("cross-repo-section");
    expect(html).toContain("Cross-Repo Feature");
    expect(html).toContain("Epic #200");
  });

  it("cross-repo epic → shows cross-repo badge", () => {
    const html = getCrossRepoEpicProgressHtml(
      makeAggregates({ crossRepoEpicProgress: [makeCrossRepoEpic()] })
    );
    expect(html).toContain("cross-repo-badge");
    expect(html).toContain("Cross-Repo");
  });

  it("cross-repo epic → renders repository sections", () => {
    const html = getCrossRepoEpicProgressHtml(
      makeAggregates({ crossRepoEpicProgress: [makeCrossRepoEpic()] })
    );
    expect(html).toContain("repo-progress-section");
    expect(html).toContain("nightgauge/nightgauge");
  });

  it("cross-repo epic → displays total issues count", () => {
    const html = getCrossRepoEpicProgressHtml(
      makeAggregates({ crossRepoEpicProgress: [makeCrossRepoEpic()] })
    );
    // 1 closed + 1 open across 2 repos = 1/2
    expect(html).toContain("1/2 issues");
  });

  it("XSS: cross-repo epic title is HTML-escaped", () => {
    const xssTitle = '<script>alert("xss")</script>';
    const epic = makeCrossRepoEpic({ epicTitle: xssTitle });
    const html = getCrossRepoEpicProgressHtml(makeAggregates({ crossRepoEpicProgress: [epic] }));
    expect(html).not.toContain("<script>alert");
    expect(html).toContain("&lt;script&gt;");
  });
});
