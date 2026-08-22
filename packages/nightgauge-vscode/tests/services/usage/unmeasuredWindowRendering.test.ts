/**
 * An unmeasured window must never render as a zero (#808).
 *
 * `UsageWindow.used` was widened from `number` to `number | null` because a
 * declared-but-unobserved window was otherwise unrepresentable: `used: 0`
 * renders as "0% used", which is a fabricated utilization, and `limit: null`
 * alone only suppresses the BAR, not the figure beside it.
 *
 * Widening a core type is only safe if every renderer honours it, so these
 * tests drive the real renderers rather than asserting about the type.
 */

import { describe, expect, it } from "vitest";
import { formatUsageWindowText } from "../../../src/utils/statusBar";
import { toWindowView } from "../../../src/views/dashboard/usagePanel";
import { getUsagePanelSectionHtml } from "../../../src/views/dashboard/tabs/UsagePanelHtml";
import { declaredPlanWindows } from "../../../src/services/usage/claudePlanDeclaration";
import type { UsagePanelState } from "../../../src/views/dashboard/usagePanel";
import type { UsageWindow } from "../../../src/services/usage/types";

const SHELL = declaredPlanWindows("claude-rate-limit")[0];
const MEASURED: UsageWindow = { ...SHELL, used: 44, confidence: "estimated" };

describe("the status-bar meter", () => {
  it("says the window is awaiting a reading rather than printing a number", () => {
    const text = formatUsageWindowText("claude", SHELL);
    expect(text).toContain("awaiting first reading");
    expect(text).not.toMatch(/\b0\s*%/);
  });

  it("still renders a measured window normally", () => {
    expect(formatUsageWindowText("claude", MEASURED)).toContain("44%");
  });
});

describe("the Usage panel's window view", () => {
  it("derives no percentage and no bar fill from an unmeasured window", () => {
    const view = toWindowView(SHELL);
    expect(view.used).toBeNull();
    expect(view.pct).toBeNull();
    // A bar drawn at any width would be a fill nobody measured.
    expect(view.barPct).toBeNull();
  });

  it("derives both for a measured one", () => {
    const view = toWindowView(MEASURED);
    expect(view.pct).toBe(44);
    expect(view.barPct).toBe(44);
  });
});

describe("the Usage panel's HTML", () => {
  function stateWith(window: UsageWindow): UsagePanelState {
    return {
      adapter: "claude",
      planKind: "subscription-window",
      capturedAt: new Date("2026-08-22T12:00:00.000Z"),
      windows: [toWindowView(window)],
      familyGroups: [],
      burnRate: null,
      burnRateUnavailableReason: null,
      recentRuns: [],
      recentTotals: { costUsd: 0, tokens: 0, runCount: 0 },
      lookbackDays: 7,
    } as UsagePanelState;
  }

  it("prints an explicit absence, never a zero, for an unmeasured window", () => {
    const html = getUsagePanelSectionHtml(stateWith(SHELL));
    expect(html).toContain("awaiting first reading");
    expect(html).not.toContain("0% used");
    // No progress bar at all — not an empty one.
    expect(html).not.toContain("usage-progress-bar");
  });

  it("prints the figure for a measured window", () => {
    expect(getUsagePanelSectionHtml(stateWith(MEASURED))).toContain("44");
  });
});
