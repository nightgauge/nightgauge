/**
 * Tests for `getProjectBoardWidgetHtml` lifecycle states.
 *
 * Disambiguates the four "no counts to show" cases that used to collapse
 * into a single visually identical 0/0/0/0 render:
 *  1. `loadingState === "loading"`   → "Loading project board…" copy
 *  2. `error` set                    → error message + retry button
 *  3. `loaded` + raw=0               → "No issues on this board yet"
 *  4. `loaded` + raw>0, filtered=0   → "Board has N items, none belong to <repo>"
 *
 * Plus the steady-state happy path: items present, no banners.
 */
import { describe, it, expect } from "vitest";
import { getProjectBoardWidgetHtml } from "../../../../src/views/dashboard/tabs/EpicsTabHtml";
import type { ProjectBoardData } from "../../../../src/views/dashboard/ProjectBoardTypes";

function baseData(overrides: Partial<ProjectBoardData> = {}): ProjectBoardData {
  return {
    statusCounts: { ready: 0, inProgress: 0, inReview: 0, done: 0, backlog: 0 },
    topReadyIssues: [],
    currentSprint: null,
    lastRefreshed: new Date("2026-05-17T12:00:00Z"),
    projectUrl: null,
    isConfigured: true,
    ...overrides,
  };
}

describe("getProjectBoardWidgetHtml — lifecycle states", () => {
  it("returns empty string when projectBoardData is null (panel just opened)", () => {
    expect(getProjectBoardWidgetHtml(null)).toBe("");
  });

  it("renders 'not configured' message when isConfigured=false", () => {
    const html = getProjectBoardWidgetHtml(baseData({ isConfigured: false }));
    expect(html).toContain("Project board not configured");
    expect(html).not.toContain("status-counts-grid");
  });

  it("renders 'Loading project board…' when loadingState is 'loading'", () => {
    const html = getProjectBoardWidgetHtml(baseData({ loadingState: "loading" }));
    expect(html).toContain("Loading project board");
    expect(html).toContain("loading-state");
    // Loading state intentionally does NOT render zero counts.
    expect(html).not.toContain("status-counts-grid");
  });

  it("renders the error message with a retry button when loadingState is 'error'", () => {
    const html = getProjectBoardWidgetHtml(
      baseData({
        loadingState: "error",
        error: "GraphQL: authentication required",
      })
    );
    expect(html).toContain("GraphQL: authentication required");
    expect(html).toContain("error-state");
    expect(html).toContain("refreshProjectBoard");
  });

  it("renders the error message even when only `error` is set (legacy callers)", () => {
    const html = getProjectBoardWidgetHtml(baseData({ error: "Network timeout" }));
    expect(html).toContain("Network timeout");
    expect(html).toContain("error-state");
  });

  it("renders the 'no items yet' banner when the board is genuinely empty", () => {
    const html = getProjectBoardWidgetHtml(
      baseData({
        loadingState: "loaded",
        diagnostics: { rawItemCount: 0, filteredItemCount: 0, expectedRepo: "nightgauge/foo" },
      })
    );
    expect(html).toContain("No issues on this project board");
    expect(html).toContain("status-counts-grid");
  });

  it("renders the repo-filter warning when the board has items but none match the workspace repo", () => {
    const html = getProjectBoardWidgetHtml(
      baseData({
        loadingState: "loaded",
        diagnostics: {
          rawItemCount: 42,
          filteredItemCount: 0,
          expectedRepo: "nightgauge/nightgauge",
        },
      })
    );
    expect(html).toContain("Board has 42 items");
    expect(html).toContain("none belong to");
    expect(html).toContain("nightgauge/nightgauge");
    expect(html).toContain("project.repo");
  });

  it("does not render any empty-state banner when the board has items to show", () => {
    const html = getProjectBoardWidgetHtml(
      baseData({
        loadingState: "loaded",
        statusCounts: { ready: 3, inProgress: 1, inReview: 2, done: 5, backlog: 4 },
        diagnostics: { rawItemCount: 15, filteredItemCount: 15, expectedRepo: "x/y" },
      })
    );
    expect(html).not.toContain("board-empty-banner");
    expect(html).not.toContain("No issues on this project board");
    expect(html).not.toContain("none belong to");
    expect(html).toContain('class="status-count-value">3</div>');
  });

  it("escapes the expectedRepo string into the warning banner", () => {
    const html = getProjectBoardWidgetHtml(
      baseData({
        loadingState: "loaded",
        diagnostics: {
          rawItemCount: 1,
          filteredItemCount: 0,
          expectedRepo: "<script>alert('xss')</script>",
        },
      })
    );
    expect(html).not.toContain("<script>alert");
    expect(html).toContain("&lt;script&gt;");
  });
});
