/**
 * Tests for getPlatformQuotaSectionHtml()
 *
 * Covers rendering contracts:
 * 1. null data → returns empty string
 * 2. isCommunity = true → output contains "Unlimited community features", no progress bar
 * 3. isStale = true → output contains "Showing cached data"
 * 4. 50% pipeline runs → renders green bar
 * 5. 82% pipeline runs → renders warning bar + "Warning" badge
 * 6. 95% pipeline runs → renders critical bar + "Critical" badge
 * 7. limit = null on a metric → renders "Unlimited" text, no progress bar
 *
 * @see Issue #1479 - Add usage metering and quota display
 */

import { describe, it, expect } from "vitest";
import { getPlatformQuotaSectionHtml } from "../../../../src/views/dashboard/tabs/CostTabHtml";
import type { PlatformQuotaData } from "../../../../src/views/dashboard/DashboardState";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeQuotaData(overrides: Partial<PlatformQuotaData> = {}): PlatformQuotaData {
  return {
    pipelineRuns: { used: 50, limit: 100, pct: 50 },
    tokens: { used: 0, limit: null, pct: null },
    period: {
      start: "2026-03-01T00:00:00Z",
      end: "2026-03-31T23:59:59Z",
    },
    isCommunity: false,
    lastFetchedAt: "2026-03-11T12:00:00Z",
    isStale: false,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("getPlatformQuotaSectionHtml", () => {
  it("null data → returns empty string", () => {
    expect(getPlatformQuotaSectionHtml(null)).toBe("");
  });

  it('isCommunity = true → output contains "Unlimited community features", no progress bar', () => {
    const data = makeQuotaData({ isCommunity: true });
    const html = getPlatformQuotaSectionHtml(data);

    expect(html).toContain("Unlimited community features");
    expect(html).not.toContain("usage-progress-bar");
  });

  it('isStale = true → output contains "Showing cached data"', () => {
    const data = makeQuotaData({ isStale: true });
    const html = getPlatformQuotaSectionHtml(data);

    expect(html).toContain("Showing cached data");
  });

  it("50% pipeline runs → renders green bar (usage-bar-ok)", () => {
    const data = makeQuotaData({
      pipelineRuns: { used: 50, limit: 100, pct: 50 },
    });
    const html = getPlatformQuotaSectionHtml(data);

    expect(html).toContain("usage-bar-ok");
    expect(html).not.toContain("usage-bar-warning");
    expect(html).not.toContain("usage-bar-critical");
  });

  it('82% pipeline runs → renders warning bar + "Warning" badge', () => {
    const data = makeQuotaData({
      pipelineRuns: { used: 82, limit: 100, pct: 82 },
    });
    const html = getPlatformQuotaSectionHtml(data);

    expect(html).toContain("usage-bar-warning");
    expect(html).toContain("Warning");
    expect(html).not.toContain("usage-bar-critical");
  });

  it('95% pipeline runs → renders critical bar + "Critical" badge', () => {
    const data = makeQuotaData({
      pipelineRuns: { used: 95, limit: 100, pct: 95 },
    });
    const html = getPlatformQuotaSectionHtml(data);

    expect(html).toContain("usage-bar-critical");
    expect(html).toContain("Critical");
  });

  it('limit = null on a metric → renders "Unlimited" text, no progress bar for that metric', () => {
    const data = makeQuotaData({
      pipelineRuns: { used: 0, limit: null, pct: null },
    });
    const html = getPlatformQuotaSectionHtml(data);

    expect(html).toContain("Unlimited");
    // No progress bar for the unlimited metric (pipeline runs has no bar)
    expect(html).not.toContain("usage-bar-ok");
    expect(html).not.toContain("usage-bar-warning");
    expect(html).not.toContain("usage-bar-critical");
  });

  it('renders "Platform Quota" section heading', () => {
    const data = makeQuotaData();
    const html = getPlatformQuotaSectionHtml(data);

    expect(html).toContain("Platform Quota");
  });

  it("renders billing period dates when period is non-null", () => {
    const data = makeQuotaData({
      period: {
        start: "2026-03-01T00:00:00Z",
        end: "2026-03-31T23:59:59Z",
      },
    });
    const html = getPlatformQuotaSectionHtml(data);

    expect(html).toContain("Billing period");
  });

  it("no billing period section when period is null", () => {
    const data = makeQuotaData({ period: null });
    const html = getPlatformQuotaSectionHtml(data);

    expect(html).not.toContain("Billing period");
  });
});
