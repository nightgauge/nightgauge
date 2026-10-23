/**
 * Tests for the 7-day delta chip rendered beneath each headline stat card.
 *
 * Polarity must match the metric's "good" direction:
 *  - +runs  / +time saved   → improving (green)
 *  - +cost                  → degrading (red)
 *  - +success-rate points   → improving (green)
 *
 * Scope semantics:
 *  - "all" scope shows the delta (chip is comparing to recent activity)
 *  - "session" scope hides it (session is its own bounded window)
 *
 * Suppression: when no runs land in the recent window (hasEnoughData=false)
 * the chip is suppressed entirely — "+0 vs prior 7d" on a brand-new install
 * is misleading.
 */
import { describe, it, expect } from "vitest";
import { getSummaryCardsHtml } from "../../../../src/views/dashboard/tabs/OverviewTabHtml";
import { makeEmptyAggregates } from "../fixtures/aggregates";

function aggregatesWithDelta(
  deltaOverrides: Partial<{
    runsDelta: number;
    costDeltaUsd: number;
    timeSavedDeltaMs: number;
    successRatePointsDelta: number;
    successRateRecent: number;
    hasEnoughData: boolean;
  }>
) {
  return makeEmptyAggregates({
    totalRuns: 944,
    totalCostUsd: 8124.2,
    totalTimeSavedMs: 9 * 60 * 60 * 1000,
    successRate: 0.75,
    recentDelta: {
      runsDelta: 0,
      runsPrior: 0,
      timeSavedDeltaMs: 0,
      timeSavedPriorMs: 0,
      costDeltaUsd: 0,
      costPriorUsd: 0,
      successRatePointsDelta: 0,
      successRateRecent: 0,
      successRatePrior: 0,
      hasEnoughData: true,
      windowDays: 7,
      ...deltaOverrides,
    },
  });
}

describe("getSummaryCardsHtml — 7-day delta chip", () => {
  it("does not render any delta chips when the scope is 'session'", () => {
    const html = getSummaryCardsHtml(aggregatesWithDelta({ runsDelta: 10 }), "session");
    expect(html).not.toContain("stat-delta");
  });

  it("does not render any delta chips when recentDelta.hasEnoughData is false", () => {
    const html = getSummaryCardsHtml(
      aggregatesWithDelta({ runsDelta: 5, hasEnoughData: false }),
      "all"
    );
    expect(html).not.toContain("stat-delta");
  });

  it("renders the runs chip green when runs ticked up", () => {
    const html = getSummaryCardsHtml(aggregatesWithDelta({ runsDelta: 12 }), "all");
    expect(html).toMatch(/stat-delta-improving[^>]*>\s*\+12 runs/);
  });

  it("renders the runs chip red when runs ticked down", () => {
    const html = getSummaryCardsHtml(aggregatesWithDelta({ runsDelta: -3 }), "all");
    expect(html).toMatch(/stat-delta-degrading[^>]*>\s*−3 runs/);
  });

  it("renders the cost chip green when cost decreased (lower is better)", () => {
    const html = getSummaryCardsHtml(aggregatesWithDelta({ costDeltaUsd: -12.34 }), "all");
    expect(html).toMatch(/stat-delta-improving[^>]*>\s*−\$12\.34/);
  });

  it("renders the cost chip red when cost increased (lower is better)", () => {
    const html = getSummaryCardsHtml(aggregatesWithDelta({ costDeltaUsd: 8.91 }), "all");
    expect(html).toMatch(/stat-delta-degrading[^>]*>\s*\+\$8\.91/);
  });

  it("renders the success-rate chip in points (pp), not relative percent", () => {
    const html = getSummaryCardsHtml(
      aggregatesWithDelta({ successRatePointsDelta: 5, successRateRecent: 0.8 }),
      "all"
    );
    expect(html).toMatch(/stat-delta-improving[^>]*>\s*\+5pp/);
    // Don't render a generic "+5%" — points and relative percent are different metrics.
    expect(html).not.toMatch(/\+5%/);
  });

  it("uses ± for an exactly-zero delta in the stable color", () => {
    const html = getSummaryCardsHtml(aggregatesWithDelta({ runsDelta: 0 }), "all");
    expect(html).toMatch(/stat-delta-stable[^>]*>\s*±0 runs/);
  });

  it("includes the window label so the comparison is unambiguous", () => {
    const html = getSummaryCardsHtml(aggregatesWithDelta({ runsDelta: 1 }), "all");
    expect(html).toContain("vs prior 7d");
  });
});
